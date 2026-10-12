package main

// upstream.go is the Node upstream's half of the entry point: where it is, what environment it
// gets, how its port is chosen, and how it is stopped. serve.go owns the HTTP side and the order
// the two are assembled in; this file owns the child's whole lifetime.
//
// Everything here exists because of one constraint: the upstream is the SAME TypeScript process the
// deployment ran before this rewrite, unchanged. So the Go front has to speak to it the way its
// launcher does — argv, environment, a port it can bind, and a data base it can write — and cannot
// ask it to behave differently. Three of the decisions below are direct consequences of that, and
// each is spelled out at its definition because each looks wrong without the reason:
//
//   - the data base is NOT this server's (upstreamDataDir) — sharing it makes login
//     non-deterministic, because both processes would write one web-sessions.json under two
//     different tokens;
//   - the port is PINNED, not asked for with PORT=0 (reserveUpstreamPort) — entry-http.ts computes
//     `Number(PORT) || 3030`, and 0 is falsy in JavaScript, so PORT=0 silently means 3030;
//   - the environment is INHERITED (startUpstream) — `node` has to be findable on PATH when the
//     entry is a .js bundle, and there is no other way to give a child a PATH without inheriting.

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/node"
	"github.com/tylinux/nex/go/internal/adapters/serverenv"
)

// upstreamProcess is the supervised Node child plus the bits of its state the entry point needs.
type upstreamProcess struct {
	supervisor *node.Supervisor
	// routerSource is filled in after the gateway is built, so the startup log can say where the
	// routing table came from (a file, or "defaults" when none exists).
	routerSource string
}

// startUpstream builds the supervisor, starts the first child and blocks until it is ready.
//
// WaitReady is bounded by node.DefaultReadyTimeout (the supervisor's ReadyTimeout option), and a
// timeout is a STARTUP FAILURE, not a degraded start: serving without an upstream would answer 1013
// to every WebSocket and 502 to every unowned route, which is indistinguishable from the server
// being broken. Failing loudly with the child's real exit reason is more useful than half-serving.
func startUpstream(ctx context.Context, cfg config, log *slog.Logger) (*upstreamProcess, error) {
	entry, err := resolveNodeEntryChecked(cfg.nodeEntry)
	if err != nil {
		return nil, err
	}
	port, err := reserveUpstreamPort()
	if err != nil {
		return nil, err
	}
	dataDir, err := upstreamDataDir()
	if err != nil {
		return nil, err
	}
	supervisor, err := node.New(node.Options{
		// PinPort rather than the supervisor's default of 0; see reserveUpstreamPort.
		PinPort: port,
		Spawn: node.SpawnConfig{
			// Args, not a shell string: a bundled path may legitimately contain spaces, and
			// internal/adapters/exec never interpolates into a shell.
			Args: nodeCommand(entry),
			// The child's cwd is the Go process's. entry-http.ts resolves NEX_WEB_STATIC_ROOT and
			// every other path absolutely, so the cwd only affects what a RELATIVE path means —
			// and leaving it as the operator's cwd is what makes a relative
			// NEX_SERVER_WORKSPACE mean the same thing here as it does in Node.
			DataBaseDir: dataDir,
			// The static root and the identity variables are passed down so Node's own /api routes
			// describe the same deployment the Go front does. The gateway does not proxy static
			// requests (the Go shell owns them), but the upstream still serves them if asked
			// directly, and a server-info it printed with a different id would be confusing when
			// diagnosing a deployment.
			ExtraEnv: []string{
				"NEX_WEB_STATIC_ROOT=" + cfg.staticRoot,
				"NEX_SERVER_WORKSPACE=" + serverenv.Trimmed("NEX_SERVER_WORKSPACE"),
				"NEX_SERVER_ID=" + serverenv.Trimmed("NEX_SERVER_ID"),
				"NEX_SERVER_NAME=" + serverenv.Trimmed("NEX_SERVER_NAME"),
			},
			// InheritEnv: the managed variables (PORT, NEX_SERVER_HOST, NEX_SERVER_AUTH_TOKEN,
			// NEX_DATA_BASE_DIR) are set by this package and win; everything else — PATH above all —
			// is needed for `node` to be found at all. The risk of inheriting is a stray NEX_* in
			// the operator's shell, and the variables that would matter are exactly the ones this
			// package overrides.
			InheritEnv: true,
		},
		WatchHealth: true,
		Log:         log,
	})
	if err != nil {
		return nil, err
	}
	process := &upstreamProcess{supervisor: supervisor}
	if err := supervisor.Start(ctx); err != nil {
		return nil, err
	}
	readyCtx, cancel := context.WithTimeout(ctx, node.DefaultReadyTimeout)
	defer cancel()
	if err := supervisor.WaitReady(readyCtx); err != nil {
		return nil, err
	}
	log.Info("node upstream ready",
		"entry", entry,
		"baseURL", supervisor.UpstreamBaseURL(),
		"state", supervisor.State(),
	)
	return process, nil
}

// stopUpstream stops the Node tree on the way out.
//
// It runs on a context detached from the cancelled signal one, because the reason we are here is
// that the context IS cancelled: a stop bounded by an already-dead context would skip the SIGTERM
// entirely and leave the child running, which is the orphan the process-tree logic exists to
// prevent. node.Stop escalates SIGTERM to KillTree/SIGKILL and waits for an observed terminal
// state, so returning from here means the tree is gone.
func stopUpstream(process *upstreamProcess, log *slog.Logger) {
	if process == nil || process.supervisor == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), node.DefaultStopGrace+2*time.Second)
	defer cancel()
	if err := process.supervisor.Stop(ctx); err != nil {
		log.Error("node upstream did not stop cleanly", "err", err)
		return
	}
	log.Info("node upstream stopped")
}

// upstreamDataDirName is the subdirectory under the app config dir. It is a constant rather than a
// flag because the Go front is its only possible writer: a second process that chose the same name
// would be the collision this arrangement exists to prevent.
const upstreamDataDirName = "node-upstream"

// upstreamDataDir returns the data base the Node upstream runs with: a `node-upstream`
// subdirectory of this server's own app config dir.
//
// WHY NOT THE SAME DATA BASE. This is the single most important line in the file. entry-http.ts
// always passes `sessionsFilePath: join(getAppConfigDir(), "web-sessions.json")` whenever a token
// is set, so an upstream sharing the data base would open the very file this front just wrote a
// browser's session into — under a DIFFERENT token, because internal/adapters/node generates one
// per start. webAuth's store deliberately discards every session whose fingerprint does not match
// its own ("token 变更（指纹不同）时丢弃全部会话"): correct for a token rotation, and here it would
// mean the upstream's next write logs every user out, at a moment unrelated to anything they did.
// Sharing the data base was therefore not a tidy option, it was non-deterministic.
//
// WHY IT LIVES UNDER THE FRONT'S CONFIG DIR RATHER THAN IN TMP. An operator debugging a deployment
// has to be able to find it, and "the state the upstream owns" should not require knowing the
// process's PID or start time. It is created if absent and never wiped, so the provider config
// materialised on the first run survives a restart and a crash-loop does not thrash the filesystem.
func upstreamDataDir() (string, error) {
	dir := filepath.Join(node.AppConfigDir(), upstreamDataDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create the node upstream data dir %s: %w", dir, err)
	}
	return dir, nil
}

// reserveUpstreamPort asks the OS for a free loopback port and gives it up again, so the child can
// bind it.
//
// It is a PROBE, not a reservation, and the difference matters: between closing this socket and the
// child binding the same port, something else could take it. That window is microseconds on
// loopback, and the failure mode is a visible EADDRINUSE in the startup log followed by the
// crash-budget restart — not a silent misconfiguration.
//
// WHY NOT PORT=0, which would have no window at all. The supervisor supports it (the child announces
// its real port on stdout and the supervisor reads it), but it does not work against THIS child:
// entry-http.ts computes `Number(process.env["PORT"]) || 3030`, and 0 is falsy in JavaScript, so
// PORT=0 silently becomes 3030 — a fixed port that two servers on one machine fight over
// deterministically. Pinning a port we chose ourselves is strictly better than that, and the
// race-free alternative is not available without changing the TypeScript.
func reserveUpstreamPort() (int, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(node.LoopbackHost, "0"))
	if err != nil {
		return 0, fmt.Errorf("reserve an upstream port on %s: %w", node.LoopbackHost, err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("release the probed upstream port %d: %w", port, err)
	}
	return port, nil
}

// nodeCommand builds the argv for the Node upstream from the resolved entry.
//
// A SEA binary is its own entry point, so it is executed directly. A `.js` bundle needs the Node
// executable in front of it; process.execPath is wrong here (this IS the Go binary), so `node` is
// resolved from PATH, which is why SpawnConfig.InheritEnv is set.
func nodeCommand(entry string) []string {
	if filepath.Ext(entry) == ".js" {
		return []string{nodeExecutable(), entry}
	}
	return []string{entry}
}

// nodeExecutable is the `node` on PATH, or the platform's fallback name. A build that ships a Node
// runtime next to the server is expected to put it on PATH or to point the entry at the SEA binary
// directly, which is the layout scripts/build-nex.mjs and the Docker image both use.
func nodeExecutable() string {
	if path, err := execLookPath("node"); err == nil {
		return path
	}
	if runtimeIsWindows() {
		return "node.exe"
	}
	return "node"
}
