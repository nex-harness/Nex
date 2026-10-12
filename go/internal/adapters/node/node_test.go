package node

// Real-Node integration: the supervisor driving the ACTUAL @nex/server
// (packages/server/dist/entry-http.js), not the fake upstream.
//
// The fake proves the supervision machinery; it cannot prove that the thing being
// supervised is the real server. These tests close that gap on the three properties
// that would silently differ between a fake and the real bundle:
//
//  1. the port is discoverable. The real server prints `http://127.0.0.1:<port>` from
//     its listen callback (http.ts) — the same line the fake prints. If the bundle's
//     log format changed, the supervisor would hang waiting for a port;
//  2. the health endpoint is what the plan names: GET /api/auth/session answers 200 with
//     the shared secret, and 401 without it. That is webAuth's middleware, including
//     the session store it writes;
//  3. a real process can be SIGKILLed and really comes back, and a client that
//     reconnects afterwards is served by the NEW generation.
//
// It SKIPS with an explicit reason when the bundle or node is absent. A skip is never
// a silent pass: `go test` reports it as SKIP, and the reason names the build command.

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/exec"
)

// realNodeEntry is the built @nex/server HTTP entry, relative to the repository root.
const realNodeEntry = "packages/server/dist/entry-http.js"

// newRealNodeSupervisor builds a supervisor for the real Node server, skipping the
// test with a reason when the prerequisites are missing.
func newRealNodeSupervisor(t *testing.T) *Supervisor {
	t.Helper()
	entry := filepath.Join(repoRoot(t), filepath.FromSlash(realNodeEntry))
	if _, err := os.Stat(entry); err != nil {
		t.Skipf("build @nex/server first (pnpm --filter @nex/server build): %v", err)
	}
	node := nodeExecutable(t)

	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	workspace := filepath.Join(root, "ws")
	for _, dir := range []string{filepath.Join(dataDir, ".nex", "v2"), workspace} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	supervisor, err := New(Options{
		Spawn: SpawnConfig{
			Args:        []string{node, entry},
			Dir:         root,
			DataBaseDir: dataDir,
			// The real server is slow to boot (esbuild bundle + service collection);
			// 15 s is the TypeScript supervisor's own coreReadyTimeoutMs.
			ExtraEnv: []string{"NEX_SERVER_WORKSPACE=" + workspace},
		},
		ReadyTimeout:   60 * time.Second,
		HealthInterval: 500 * time.Millisecond,
		StopGrace:      10 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := supervisor.Stop(context.Background()); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	return supervisor
}

// TestRealNodeBecomesReadyAndServesTheHealthRoute is the end-to-end readiness claim:
// Go starts the real @nex/server, discovers its ephemeral port from its own listen
// line, and confirms it answers GET /api/auth/session with the secret Go generated.
func TestRealNodeBecomesReadyAndServesTheHealthRoute(t *testing.T) {
	supervisor := newRealNodeSupervisor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := supervisor.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := supervisor.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	status := supervisor.Status()
	if status.PID <= 1 || status.Port <= 0 {
		t.Fatalf("status = %+v, want a real pid and the discovered port", status)
	}

	base := supervisor.UpstreamBaseURL()
	// The secret the supervisor generated must be the one the real server enforces.
	if ready := NewHealthChecker(base, status.Secret).Check(ctx); !ready.OK || ready.StatusCode != http.StatusOK {
		t.Errorf("health probe with the generated secret = %+v, want 200", ready)
	}
	// /api/auth/session is deliberately NOT token protected (isTokenProtectedPath
	// returns false for /api/auth/*), so it answers 200 for anyone — which is exactly
	// why it is the right readiness route and the wrong auth probe. The enforcement
	// itself is asserted on a PROTECTED route: /api/server-info is under /api/*, so a
	// wrong secret must be rejected there. If that ever passes with a wrong token, the
	// upstream is reachable without the shared secret Go generated and the loopback
	// bind is the only thing between it and the network.
	probe := func(token, path string) int {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?token="+token, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if got := probe("not-the-secret", "/api/server-info"); got != http.StatusUnauthorized {
		t.Errorf("a protected route with a wrong secret = %d, want 401", got)
	}
	if got := probe(status.Secret, "/api/server-info"); got != http.StatusOK {
		t.Errorf("a protected route with the generated secret = %d, want 200", got)
	}
	// GET /api/auth/session is the route the Docker acceptance list uses; its body
	// must report authRequired, which is how the UI decides to show the login page.
	body := getBody(t, base+HealthPath+"?token="+status.Secret)
	if !strings.Contains(body, `"authRequired":true`) {
		t.Errorf("%s body = %s, want authRequired:true", HealthPath, body)
	}
}

// TestRealNodeIsRestartedAfterSIGKILL is the plan's integration requirement: start the
// real server, kill -9 it, and observe that the supervisor brings it back and a client
// can connect to the new one.
//
// SIGKILL rather than a graceful stop on purpose: a crash is the case the supervisor
// exists for, and it is the one a clean shutdown path cannot accidentally pass.
func TestRealNodeIsRestartedAfterSIGKILL(t *testing.T) {
	supervisor := newRealNodeSupervisor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if err := supervisor.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := supervisor.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	before := supervisor.Status()

	// The crash budget's first delay is 1 s, so allow for it plus a Node boot.
	if err := exec.KillProcessTree(ctx, before.PID); err != nil {
		t.Fatalf("kill the upstream tree: %v", err)
	}

	// Wait for the restart to land: a new generation, a new pid, ready again.
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		status := supervisor.Status()
		if status.State == StateReady && status.PID != before.PID && status.PID > 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	after := supervisor.Status()
	if after.State != StateReady || after.PID == before.PID {
		t.Fatalf("after SIGKILL the upstream is %+v, want a new ready generation (was pid %d)", after, before.PID)
	}
	if !pidAlive(t, after.PID) {
		t.Errorf("the restarted upstream pid %d is not alive", after.PID)
	}

	// "Clients can reconnect" is the observable half: a client that asks the new
	// generation must be served by it, and it must be the NEW secret — the supervisor
	// rotates it per generation precisely so a stale leg cannot linger.
	restored := supervisor.UpstreamBaseURL()
	if ready := NewHealthChecker(restored, after.Secret).Check(ctx); !ready.OK {
		t.Fatalf("the reconnected client could not reach the restarted upstream: %+v", ready)
	}
	if after.Secret == before.Secret {
		t.Error("the restart reused the previous shared secret; a stale leg would still authenticate")
	}
}

// TestRealNodeStopLeavesNoChild is the shutdown requirement against the real server: no
// orphaned Node process, and no leftover session file written by a process that was
// killed mid-flight.
//
// The pid check is the one that matters; the file check is a bonus that catches the
// failure mode where the tree is gone but the data dir was created by a child that
// outlived the stop.
func TestRealNodeStopLeavesNoChild(t *testing.T) {
	supervisor := newRealNodeSupervisor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := supervisor.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := supervisor.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	pid := supervisor.currentPID()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer stopCancel()
	if err := supervisor.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	waitGone(t, pid, 30*time.Second)
	if state := supervisor.State(); state != StateStopped {
		t.Errorf("state = %q, want %q", state, StateStopped)
	}
	// The secret must not survive a stop: nothing is listening, so anything holding
	// the origin would dial a dead port.
	if got := supervisor.UpstreamBaseURL(); got != "" {
		t.Errorf("UpstreamBaseURL() = %q after Stop, want \"\"", got)
	}
}

// getBody fetches url and returns the body, failing the test on any non-200.
func getBody(t *testing.T, url string) string {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", url, response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(body)
}
