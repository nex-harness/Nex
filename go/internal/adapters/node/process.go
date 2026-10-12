// Package node supervises the Node upstream of the Go server.
//
// Step 2.9 of docs/specs/go-rewrite/phase-2-go-server.md. The Go server is the
// process the browser talks to; everything it has not ported yet is forwarded to
// a real @nex/server child process (the strangler front of step 2.8). This
// package owns that child: where its binary is, what environment it gets, when it
// counts as ready, when it crashed, and how long the tree is given to die.
//
// What is ported, and from where:
//
//   - crash budget and lifecycle states: supervisor/crashBudget.ts + contracts.ts
//     (see backoff.go);
//   - the two-phase stop with a bounded wait for a terminal state: Supervisor's
//     stopCore() in supervisor/supervisor.ts — the reason it does not simply wait
//     five seconds and return is spelled out in the comment there and preserved
//     here in stopChild;
//   - parent-disconnect handling: server-core/parentDisconnect.ts — a Core that
//     outlives its supervisor must still close its own HTTP/WebSocket/agent
//     resources, or it becomes an orphan holding the port (see supervisor.go);
//   - process spawning and tree termination: internal/adapters/exec, Phase 1's
//     adapter. This package does NOT spawn or signal anything itself: the Node
//     child needs the same setpgid / Job Object boundary every other child needs,
//     and a second implementation of "kill the tree" is how orphans are born.
//
// What is deliberately NOT ported: the release manager, the update transaction,
// the data-root lock, the control socket and the status snapshot. Those belong to
// the nex-server-cli supervisor, which still owns installation and updates. This
// supervisor supervises one child inside one already-running Go server process and
// writes no persistent state of its own (the Phase 2 rollback story depends on
// that: "delete gateway-routes.json to restore default routing" stays true).
//
// Environment (D7: no new environment variables). Every variable below already
// exists in the TypeScript server and is read there:
//
//	PORT=0                     ephemeral loopback port (createHttpServer with port 0)
//	NEX_SERVER_HOST=127.0.0.1  loopback bind; the TS refuses a non-loopback host
//	                            without auth, and this child is not a public listener
//	NEX_SERVER_AUTH_TOKEN      the shared secret Go generated at start
//	NEX_DATA_BASE_DIR          the same data base Go itself uses, so web-sessions.json
//	                            (written by BOTH implementations during the strangler
//	                            transition) resolves to one file
//
// The token is not a user-facing setting: it is generated per start and passed
// through the child's environment, which is why passing it this way does not make
// it a new configuration surface.
package node

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/exec"
	"github.com/tylinux/nex/go/internal/adapters/serverenv"
)

// Environment variable names, quoted from the TypeScript readers:
// entry-http.ts reads PORT, NEX_SERVER_HOST and NEX_SERVER_AUTH_TOKEN;
// services/paths.ts reads NEX_DATA_BASE_DIR.
const (
	envPort           = "PORT"
	envHost           = "NEX_SERVER_HOST"
	envAuthToken      = "NEX_SERVER_AUTH_TOKEN"
	envDataBaseDir    = "NEX_DATA_BASE_DIR"
	envServerID       = "NEX_SERVER_ID"
	envServerName     = "NEX_SERVER_NAME"
	envServerRoot     = "NEX_SERVER_ROOT"
	envStaticRoot     = "NEX_WEB_STATIC_ROOT"
	envBuiltinProvCfg = "NEX_BUILTIN_PROVIDER_CONFIG_FILE"
)

// LoopbackHost is the address the child binds. It is never 0.0.0.0: the Go front is
// the only public listener, and the upstream carries a per-process secret that must
// not be reachable from the network.
const LoopbackHost = "127.0.0.1"

// authTokenBytes is the entropy of the generated secret. webAuth's capability store
// uses 32 random bytes base64url for the same "unpredictable ticket" job
// (hostCapability.ts, `randomBytes(32)`), and the token is compared with
// subtle.ConstantTimeCompare, so length alone must not help a guesser.
const authTokenBytes = 32

// SpawnConfig is one launch of the Node upstream.
//
// Command is the argv, not a shell string: Args[0] is the executable (node, or a
// SEA binary that is its own entry) and Args[1:] its arguments. The supervisor never
// interpolates a path into a shell — internal/adapters/exec documents the same rule
// and a bundled path may legitimately contain spaces.
type SpawnConfig struct {
	// Args is the argv of the Node upstream, e.g. ["node", ".../dist/entry-http.js"].
	Args []string
	// Dir is the child's working directory. Empty means the Go server's own.
	Dir string
	// DataBaseDir is NEX_DATA_BASE_DIR: the data base Go resolves for itself. Empty
	// omits the variable, which leaves the child on the same default ($HOME) Go
	// would resolve — the same fallback chain getDataBaseDir() has.
	DataBaseDir string
	// AuthToken is the shared secret. Generated by the supervisor when empty.
	AuthToken string
	// ExtraEnv are additional KEY=VALUE entries appended after the managed ones, so a
	// caller can pass NEX_SERVER_ID / NEX_WEB_STATIC_ROOT without this file growing a
	// field per variable. A key that this package also sets is ignored.
	ExtraEnv []string
	// InheritEnv keeps the Go process's own environment in the child. Default (false)
	// is the safer choice and what the tests want: a stray NEX_* in the developer's
	// shell must not silently change which deployment is being exercised. Set it when
	// the child needs something from the environment Go cannot enumerate (PATH on a
	// system where the executable is not absolute, LD_LIBRARY_PATH, ...).
	InheritEnv bool
	// TermGrace overrides exec.DefaultTermGrace for the escalation SIGTERM -> SIGKILL.
	// The TypeScript supervisor uses 5 s graceful / 2 s to see the terminal state;
	// that is StopGrace below, and this is only the second phase of the kill.
	TermGrace int64
	// MaxOutputBytes caps the retained stdout/stderr per stream. Zero uses the exec
	// adapter default.
	MaxOutputBytes int64
}

// Validate rejects a config that cannot produce a running child.
func (c SpawnConfig) Validate() error {
	if len(c.Args) == 0 || strings.TrimSpace(c.Args[0]) == "" {
		return errors.New("node: SpawnConfig.Args must name an executable")
	}
	return nil
}

// managedEnvKeys are the variables this package owns. An ExtraEnv entry with one of
// these names is dropped rather than appended, so a caller cannot end up with two
// PORT entries and an OS that picks the wrong one.
var managedEnvKeys = map[string]bool{
	envPort: true, envHost: true, envAuthToken: true, envDataBaseDir: true,
	envServerRoot: true,
}

// BuildEnv assembles the child's environment.
//
// PORT is 0 by default: an ephemeral port the OS assigns to the CHILD. Reserving a
// port here and handing it over would reintroduce the classic TOCTOU — something
// else could take it between our close and the child's listen — and createHttpServer
// already accepts port 0 and prints the real one from its listen callback. The
// supervisor reads that line out of the child's stdout (see health.go), which is why
// the port is a parameter and not a lookup.
func (c SpawnConfig) BuildEnv(port int) ([]string, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	token := strings.TrimSpace(c.AuthToken)
	if token == "" {
		generated, err := GenerateAuthToken()
		if err != nil {
			return nil, err
		}
		token = generated
	}
	env := []string{
		envPort + "=" + strconv.Itoa(port),
		envHost + "=" + LoopbackHost,
		envAuthToken + "=" + token,
	}
	if base := strings.TrimSpace(c.DataBaseDir); base != "" {
		env = append(env, envDataBaseDir+"="+base)
	}
	for _, entry := range c.ExtraEnv {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || managedEnvKeys[strings.TrimSpace(key)] {
			continue
		}
		env = append(env, entry)
	}
	if c.InheritEnv {
		inherited := os.Environ()
		kept := make([]string, 0, len(inherited))
		for _, entry := range inherited {
			key, _, _ := strings.Cut(entry, "=")
			if managedEnvKeys[key] {
				continue
			}
			kept = append(kept, entry)
		}
		env = append(kept, env...)
	}
	return env, nil
}

// GenerateAuthToken returns a fresh shared secret: 32 random bytes, base64url, no
// padding — the exact construction hostCapability.ts uses for its one-shot ticket,
// so the value has the same shape and entropy class as the other per-process
// secrets in this codebase.
func GenerateAuthToken() (string, error) {
	buf := make([]byte, authTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("node: generate auth token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// child is one running Node process plus everything the supervisor needs to talk
// to it.
type child struct {
	proc *exec.Process
	// token is the secret THIS child was started with. It is kept per generation:
	// the supervisor rotates it on every restart (see supervisor.go), so a child that
	// outlived its generation must never be addressed with a newer secret.
	token string
	port  int
	// lines carries every complete line the child wrote to stdout, installed before
	// the process started so no line can be missed. It is what reveals the listen
	// port for a child bound to PORT=0.
	lines chan string
}

// stdoutLineBuffer is the depth of the stdout line tap. A burst is dropped rather
// than blocking the child: the port line is the first thing the server prints, and
// the retained Output keeps the rest for diagnostics.
const stdoutLineBuffer = 64

// PID is the child's process id.
func (c *child) PID() int {
	if c == nil || c.proc == nil {
		return 0
	}
	return c.proc.PID()
}

// BaseURL is the child's http origin.
func (c *child) BaseURL() string {
	return "http://" + LoopbackHost + ":" + strconv.Itoa(c.port)
}

// Token is the secret the child was started with.
func (c *child) Token() string {
	if c == nil {
		return ""
	}
	return c.token
}

// start launches the child. It is the ONLY place in this package that calls
// os/exec, and it does so through internal/adapters/exec so the child inherits the
// process-tree boundary (Setpgid on Unix, Job Object on Windows) that
// KillProcessTree knows how to address.
func (s *Supervisor) start(ctx context.Context, cfg SpawnConfig, token string, port int) (*child, error) {
	env, err := cfg.BuildEnv(port)
	if err != nil {
		return nil, err
	}
	// Replace the generated token with the caller's: BuildEnv generates one only when
	// AuthToken was empty, and the supervisor keeps exactly one token per generation so
	// the gateway's ?token= and the child's NEX_SERVER_AUTH_TOKEN cannot drift apart.
	env = overrideEnv(env, envAuthToken, token)

	lines := make(chan string, stdoutLineBuffer)
	opts := exec.Options{
		Args:           cfg.Args,
		Dir:            cfg.Dir,
		Env:            env,
		TermGrace:      time.Duration(cfg.TermGrace),
		MaxOutputBytes: cfg.MaxOutputBytes,
		StdoutLines:    lines,
	}
	proc, err := exec.Start(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("node: start upstream %q: %w", cfg.Args[0], err)
	}
	return &child{proc: proc, token: token, port: port, lines: lines}, nil
}

// overrideEnv replaces one KEY=VALUE entry in place, appending when it is absent.
func overrideEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// stopChild asks the child to stop and then proves it is gone.
//
// This is the port of Supervisor.stopCore(): send the stop, wait a bounded grace for
// the terminal state, escalate, and — the part the TypeScript comment insists on —
// keep waiting for an observed terminal state after SIGKILL instead of returning on
// a timer. Returning without one would release the Go front's handle on a process
// that is still alive, and the next generation would fight it for the port.
//
// The child here is the HTTP entry (packages/server/dist/entry-http.js), not the
// CLI's forked Core, so there is no IPC channel to send a shutdown command on. The
// signal is the stop request; the escalation is KillProcessTree, which on Unix is
// the whole process group and on Windows the Job Object — the same guarantee the
// forked Core got from `core.send({command:"shutdown"})` plus its own SIGKILL.
//
// The stdout line tap is deliberately not drained: SetLineSink sends without
// blocking and drops on a full buffer, so a late line can never wedge a stop, and the
// channel is garbage collected with the child.
func stopChild(ctx context.Context, c *child, grace time.Duration) error {
	if c == nil || c.proc == nil {
		return nil
	}
	pid := c.proc.PID()
	// KillTree is SIGTERM to the whole process group first and SIGKILL after TermGrace,
	// and both phases address the group (Unix) or the Job Object (Windows) rather than
	// the bare pid — otherwise a grandchild, e.g. the per-workspace Agent the server
	// spawns, would survive the stop and keep the port.
	if err := c.proc.KillTree(ctx); err != nil {
		return fmt.Errorf("node: stop upstream pid %d: %w", pid, err)
	}
	return waitTerminal(c.proc, grace)
}

// waitTerminal blocks until the child has been reaped or the deadline passes.
//
// This is the check supervisor.ts insists on after SIGKILL: "发出 SIGKILL 后不能
// 最多等两秒便无条件返回：未观察到子进程终态也会释放…". Returning on the timer without
// an observed terminal state would let the Go front believe the upstream is gone while
// a process still holds the port, and the next generation would fight it.
func waitTerminal(p *exec.Process, grace time.Duration) error {
	done := make(chan exec.Result, 1)
	go func() { done <- p.Wait() }()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return fmt.Errorf("node: upstream pid %d did not terminate after SIGKILL", p.PID())
	}
}

// DataBaseDir is the data base the Go server resolved for itself: NEX_DATA_BASE_DIR
// or $HOME, exactly getDataBaseDir() (packages/services/src/paths.ts) and
// DefaultRoutesFilePath()'s resolution. The child is given the same value so the two
// implementations agree on where web-sessions.json lives.
func DataBaseDir() string {
	if base := serverenv.Trimmed(envDataBaseDir); base != "" {
		return base
	}
	return serverenv.HomeDir()
}

// AppConfigDir is getAppConfigDir(): <data base>/.nex/v2. Exported because a caller
// that has to place a file next to Node's own state needs the same answer.
func AppConfigDir() string {
	return filepath.Join(DataBaseDir(), ".nex", "v2")
}

// envValue reads one entry out of an environment slice. Test and diagnostic helper.
func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}
