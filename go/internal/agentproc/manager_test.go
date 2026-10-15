// The test harness for the agent process manager.
//
// Everything here builds a REAL child process: the fake agent binary is compiled
// once per test run and the manager spawns it through the same exec adapter the
// production path uses. A stub process object would prove nothing about the two
// things step 3.10.1 is responsible for — that a crashed tree leaves no zombie
// and that a spawn is reaped exactly once — because both live in the OS.
package agentproc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/logx"
)

// fakeAgentBinary compiles ./fakeagent once per test binary and returns its path.
// Building it (rather than shelling out to a script) keeps the fake a real Go
// process with real signal handling on every platform the suite runs on.
var fakeAgentBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "agentproc-fakeagent-*")
	if err != nil {
		return "", err
	}
	name := "fakeagent"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", binary, "./fakeagent")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return binary, nil
})

// mustFakeAgent returns the fake agent path or fails the test.
func mustFakeAgent(t *testing.T) string {
	t.Helper()
	binary, err := fakeAgentBinary()
	if err != nil {
		t.Fatalf("build fakeagent: %v", err)
	}
	return binary
}

// fakeAgentCommand returns a resolver that spawns the fake agent in the given
// mode with the given extra environment. The command's cwd is the workspace path
// so the preflight's "workspace" cwd source is exercised.
func fakeAgentCommand(t *testing.T, mode string, extraEnv map[string]string) CommandResolver {
	t.Helper()
	binary := mustFakeAgent(t)
	return func(ctx CommandResolverContext) (*AgentCommand, error) {
		return &AgentCommand{
			Command: binary,
			Args:    []string{"app-server", "--stdio"},
			Cwd:     ctx.WorkspacePath,
			Env:     envForMode(mode, extraEnv),
		}, nil
	}
}

func envForMode(mode string, extra map[string]string) map[string]string {
	env := map[string]string{"FAKE_MODE": mode}
	for key, value := range extra {
		env[key] = value
	}
	return env
}

// quietLogger keeps the manager's service log out of the test output; a test that
// wants to assert on a log line installs its own buffer (see capturingLogger).
func quietLogger() logx.ServiceLogger {
	return logx.NewServiceLogger("nex-agent-test", &logx.Options{Sink: discardWriter{}, IsDebugEnabled: func() bool { return false }})
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// capturingLogger keeps the last lines so a test can assert the lifecycle marker
// reached the log.
type capturingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *capturingLogger) Debug(traceID, msg string, args ...any) { l.record("debug", msg) }
func (l *capturingLogger) Info(traceID, msg string, args ...any)  { l.record("info", msg) }
func (l *capturingLogger) Warn(traceID, msg string, args ...any)  { l.record("warn", msg) }
func (l *capturingLogger) Error(traceID, msg string, args ...any) { l.record("error", msg) }

func (l *capturingLogger) record(level, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, level+" "+msg)
}

func (l *capturingLogger) contains(needle string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

// newTestManager builds a manager wired to the fake agent with a quiet log.
func newTestManager(t *testing.T, opts Options) *Manager {
	t.Helper()
	if opts.Log == nil {
		opts.Log = quietLogger()
	}
	manager := NewManager(opts)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		manager.DisposeAllAndWait(ctx)
	})
	return manager
}

// tempWorkspace creates a workspace directory and returns a ref for it.
func tempWorkspace(t *testing.T, name string) (WorkspaceRef, string) {
	t.Helper()
	dir := t.TempDir()
	workspace := WorkspaceRef{WorkspacePath: filepath.Join(dir, name)}
	if err := os.MkdirAll(workspace.WorkspacePath, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	return workspace, dir
}

// processExists reports whether a pid is still in the process table. On Linux it
// reads /proc; elsewhere the zombie test is skipped rather than faked.
func processExists(pid int) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return err == nil
}

// isZombie reports whether a pid is a zombie on Linux: the entry exists but its
// state is Z, i.e. the process died and nobody has reaped it yet.
func isZombie(pid int) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	text := string(data)
	// The state field follows the parenthesised comm, which may contain spaces.
	close := strings.LastIndex(text, ")")
	if close < 0 || close+2 >= len(text) {
		return false
	}
	return text[close+2] == 'Z'
}

// waitFor polls until condition holds or the deadline passes. It returns the last
// observed value so the caller can report what it saw.
func waitFor(t *testing.T, timeout time.Duration, describe string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, describe)
}
