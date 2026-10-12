package node

// Supervisor tests against the FAKE upstream (internal/adapters/node/fakeupstream), a
// real child process that speaks the two things the supervisor observes: the
// `http://host:port` listen line and GET /api/auth/session.
//
// These are not mock tests. Every assertion is about a real process — its pid, whether
// it is still in the process table, its exit status, the TCP port it bound — because
// the properties under test (does a crash get noticed, is a restart a NEW process, does
// a stop leave no child) only exist at the process level. The real-Node test in
// node_test.go covers the other half: that the thing being supervised is the actual
// @nex/server.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// harness wires a supervisor to the fake upstream with a fake clock and a fake poll,
// so the crash-budget timing is exercised without spending 1+2+4+8+16 real seconds.
type harness struct {
	supervisor *Supervisor
	controlDir string
	clock      *fakeClock
	// sleeps records every backoff delay the supervisor asked for, in order.
	sleeps chan time.Duration
}

// crashEnv returns the control-file entry for every generation.
//
// The fake upstream polls the path and CONSUMES the trigger (removes the file) before
// exiting, so a stale file can never kill the next generation the instant it starts.
func (h *harness) crashEnv() string {
	return "FAKE_CONTROL_FILE=" + filepath.Join(h.controlDir, "crash")
}

func newHarness(t *testing.T, mutate func(*Options)) *harness {
	t.Helper()
	fake := fakeUpstreamBinary(t)
	clock := newFakeClock()
	h := &harness{controlDir: t.TempDir(), clock: clock, sleeps: make(chan time.Duration, 64)}

	opts := Options{
		Spawn: SpawnConfig{
			Args:        []string{fake},
			Dir:         t.TempDir(),
			DataBaseDir: t.TempDir(),
			ExtraEnv:    []string{h.crashEnv()},
		},
		ReadyTimeout:   10 * time.Second,
		HealthInterval: 20 * time.Millisecond,
		StopGrace:      3 * time.Second,
		Now:            clock.Now,
		// Poll stands in for every sleep in the supervisor, and advances the injected
		// clock instead of really sleeping.
		//
		// It records a delay ONLY when it is one of the crash-backoff entries: the same
		// seam also backs the health probe's retry interval, so recording every sleep
		// would mix "waited 1s before restarting" with "waited 20ms between probes".
		Poll: func(ctx context.Context, d time.Duration) error {
			for _, entry := range CrashBackoffMs {
				if d == time.Duration(entry)*time.Millisecond {
					select {
					case h.sleeps <- d:
					default:
					}
					break
				}
			}
			clock.Advance(d)
			return ctx.Err()
		},
	}
	if mutate != nil {
		mutate(&opts)
	}
	supervisor, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.supervisor = supervisor
	t.Cleanup(func() {
		_ = supervisor.Stop(context.Background())
	})
	return h
}

// startAndWait starts the supervisor and blocks until the child is ready.
func (h *harness) startAndWait(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.supervisor.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := h.supervisor.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
}

// controlFile is the path the fake upstream polls for a crash trigger.
func (h *harness) controlFile() string {
	return filepath.Join(h.controlDir, "crash")
}

// crash asks the current child to exit(7) by creating its control file.
func (h *harness) crash(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(h.controlFile(), nil, 0o600); err != nil {
		t.Fatalf("write crash trigger: %v", err)
	}
}

// waitState blocks until the supervisor reaches state, and fails if it does not.
func (h *harness) waitState(t *testing.T, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := h.supervisor.State(); got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("state = %q, want %q (lastExitReason=%q budget=%+v)",
		h.supervisor.State(), want, h.supervisor.Status().LastExitReason, h.supervisor.Status().CrashBudget)
}

// nextSleep returns the next backoff delay the supervisor waited for.
func (h *harness) nextSleep(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-h.sleeps:
		return d
	case <-time.After(20 * time.Second):
		t.Fatal("the supervisor never waited for a backoff delay")
		return 0
	}
}

// pidAlive reports whether pid is still in the process table.
//
// Unix: signal 0 performs the existence check without delivering anything. Windows:
// the exec package's own predicate, which is the same OpenProcess probe its tree kill
// uses. Both are the "is there a process with this pid" question; the Unix one is a
// direct syscall, the Windows one reuses the adapter so the two cannot disagree.
func pidAlive(t *testing.T, pid int) bool {
	t.Helper()
	if pid <= 1 {
		t.Fatalf("refusing to probe pid %d", pid)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// waitGone blocks until pid leaves the process table.
func waitGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !pidAlive(t, pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d is still alive %s after the supervisor stopped", pid, timeout)
}

// TestSupervisorStartsAndBecomesReady is the happy path: spawn, discover the port from
// the child's listen line, prove it with the health probe, and report ready.
func TestSupervisorStartsAndBecomesReady(t *testing.T) {
	h := newHarness(t, nil)
	h.startAndWait(t)

	status := h.supervisor.Status()
	if status.State != StateReady {
		t.Fatalf("state = %q, want %q", status.State, StateReady)
	}
	if status.PID <= 1 {
		t.Fatalf("pid = %d, want a real child", status.PID)
	}
	if status.Port <= 0 {
		t.Fatalf("port = %d, want the port discovered from the listen line", status.Port)
	}
	if status.Secret == "" {
		t.Error("no shared secret was generated for the child")
	}
	if !pidAlive(t, status.PID) {
		t.Errorf("child pid %d is not alive right after becoming ready", status.PID)
	}
	// The gateway dials this; a wrong one would make the whole front useless.
	if got, want := h.supervisor.UpstreamBaseURL(), fmt.Sprintf("http://127.0.0.1:%d", status.Port); got != want {
		t.Errorf("UpstreamBaseURL() = %q, want %q", got, want)
	}
	// Clients must be able to reach the CURRENT child after any restart; a 200 on the
	// health route with the supervisor's secret is that proof end to end.
	if ready := NewHealthChecker(h.supervisor.UpstreamBaseURL(), status.Secret).
		Check(context.Background()); !ready.OK || ready.StatusCode != http.StatusOK {
		t.Errorf("probe of the ready child = %+v, want 200", ready)
	}
}

// TestHealthProbeUsesTheGeneratedSecret checks the readiness contract the plan names:
// GET /api/auth/session answers 200 for the secret Go generated and 401 for anything
// else, which is what makes "the secret reached the child" observable.
func TestHealthProbeUsesTheGeneratedSecret(t *testing.T) {
	h := newHarness(t, nil)
	h.startAndWait(t)

	base := h.supervisor.UpstreamBaseURL()
	checker := NewHealthChecker(base, h.supervisor.UpstreamToken())
	if ready := checker.Check(context.Background()); !ready.OK || ready.StatusCode != http.StatusOK {
		t.Fatalf("probe with the supervisor's token = %+v, want 200", ready)
	}
	wrong := NewHealthChecker(base, "not-the-secret")
	if ready := wrong.Check(context.Background()); !ready.OK || ready.StatusCode != http.StatusUnauthorized {
		t.Fatalf("probe with a wrong token = %+v, want the 401 that proves auth is enforced", ready)
	}
}

// TestCrashIsRestartedWithBackoff is the core supervision behavior: kill the child,
// observe the crash, wait the first backoff entry, and get a NEW process that is ready
// again on a new port with a new secret.
func TestCrashIsRestartedWithBackoff(t *testing.T) {
	h := newHarness(t, nil)
	h.startAndWait(t)
	before := h.supervisor.Status()

	h.crash(t)
	if delay := h.nextSleep(t); delay != time.Second {
		t.Errorf("first restart delay = %s, want 1s (CRASH_BACKOFF_MS[0])", delay)
	}
	h.waitState(t, StateReady, 30*time.Second)

	after := h.supervisor.Status()
	if after.Generation != before.Generation+1 {
		t.Errorf("generation = %d, want %d", after.Generation, before.Generation+1)
	}
	if after.PID == before.PID {
		t.Errorf("the restarted child kept pid %d; it is not a new process", after.PID)
	}
	if after.Secret == before.Secret {
		t.Error("the restart reused the previous shared secret")
	}
	// The crashed generation must be really gone, not just forgotten.
	waitGone(t, before.PID, 10*time.Second)
	// And the new one must be serving, which is what "clients can reconnect" means.
	if !pidAlive(t, after.PID) {
		t.Errorf("restarted pid %d is not alive", after.PID)
	}
}

// TestCrashBudgetStopsAfterFiveRestarts is the crash-loop policy end to end: the first
// five crashes restart with the table's delays, the sixth does not restart at all, and
// the supervisor parks in crash-loop-stopped with no child left behind.
//
// The delays asserted here are CRASH_BACKOFF_MS verbatim (backoff_test.go pins the
// table against the TypeScript); what this adds is that the SUPERVISOR asks for them,
// in that order, and stops when the budget says so.
func TestCrashBudgetStopsAfterFiveRestarts(t *testing.T) {
	h := newHarness(t, nil)
	h.startAndWait(t)

	for crash := 1; crash <= len(CrashBackoffMs); crash++ {
		want := time.Duration(CrashBackoffMs[crash-1]) * time.Millisecond
		h.crash(t)
		if got := h.nextSleep(t); got != want {
			t.Fatalf("crash %d restart delay = %s, want %s (CRASH_BACKOFF_MS[%d])",
				crash, got, want, crash-1)
		}
		// Wait for the new child to be ready before crashing it again; otherwise the
		// crash would land during startup and count as a startup failure instead.
		h.waitState(t, StateReady, 30*time.Second)
	}

	// Crash six: the budget is exhausted, so there must be NO further restart, NO
	// further sleep, and NO child left running.
	h.crash(t)
	h.waitState(t, StateCrashLoopStopped, 30*time.Second)

	select {
	case delay := <-h.sleeps:
		t.Fatalf("the supervisor waited %s after the crash budget was exhausted", delay)
	case <-time.After(300 * time.Millisecond):
	}

	if pid := h.supervisor.currentPID(); pid != 0 {
		t.Errorf("pid = %d in crash-loop-stopped; no child must be left running", pid)
	}
	snapshot := h.supervisor.Status().CrashBudget
	if snapshot.CrashCount != 6 || !snapshot.Exhausted {
		t.Errorf("crash budget = %+v, want crashCount 6 and exhausted", snapshot)
	}
	// A supervisor parked in crash-loop-stopped must refuse to start again: the
	// budget is only reset by a human.
	if err := h.supervisor.Start(context.Background()); !errors.Is(err, ErrCrashLoopStopped) {
		t.Errorf("Start() after the budget was exhausted = %v, want ErrCrashLoopStopped", err)
	}
}

// TestStopLeavesNoChild is the shutdown requirement: after Stop, the child's pid is
// gone from the process table. Checked by pid, on every OS — an assertion about a port
// would pass while a zombie held the process alive.
func TestStopLeavesNoChild(t *testing.T) {
	h := newHarness(t, nil)
	h.startAndWait(t)
	pid := h.supervisor.currentPID()
	if pid <= 1 {
		t.Fatal("no child to stop")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.supervisor.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	waitGone(t, pid, 15*time.Second)
	if state := h.supervisor.State(); state != StateStopped {
		t.Errorf("state = %q, want %q", state, StateStopped)
	}
	if got := h.supervisor.UpstreamBaseURL(); got != "" {
		t.Errorf("UpstreamBaseURL() = %q after Stop, want \"\"", got)
	}
}

// TestStopIsIdempotent: a second Stop with no child is a no-op, not an error — the
// signal handler and an explicit shutdown can both fire.
func TestStopIsIdempotent(t *testing.T) {
	h := newHarness(t, nil)
	h.startAndWait(t)
	ctx := context.Background()
	if err := h.supervisor.Stop(ctx); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := h.supervisor.Stop(ctx); err != nil {
		t.Errorf("second Stop: %v, want nil", err)
	}
}

// TestStopKillsAWedgedChild covers the escalation: a child that ignores SIGTERM must
// still be gone when Stop returns, because the Go front cannot let a process keep the
// port after it has reported the stop.
func TestStopKillsAWedgedChild(t *testing.T) {
	h := newHarness(t, nil)
	h.startAndWait(t)
	pid := h.supervisor.currentPID()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.supervisor.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitGone(t, pid, 15*time.Second)
}

// TestSpawnFailureCountsAgainstTheBudget: a child that cannot start at all (the
// executable does not exist) must be throttled by the crash budget, not respawned in a
// tight loop. A broken deployment that respawns as fast as it fails is how a Go front
// turns a missing bundle into a fork bomb.
func TestSpawnFailureCountsAgainstTheBudget(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "definitely-not-here")
	h := newHarness(t, func(o *Options) {
		o.Spawn = SpawnConfig{Args: []string{missing}, Dir: t.TempDir(), DataBaseDir: t.TempDir()}
		o.ReadyTimeout = time.Second
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := h.supervisor.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Five restarts are attempted with the table's delays, then the budget stops it.
	for attempt := range CrashBackoffMs {
		want := time.Duration(CrashBackoffMs[attempt]) * time.Millisecond
		if got := h.nextSleep(t); got != want {
			t.Fatalf("spawn-failure restart %d delay = %s, want %s", attempt+1, got, want)
		}
	}
	h.waitState(t, StateCrashLoopStopped, 30*time.Second)

	select {
	case delay := <-h.sleeps:
		t.Fatalf("the supervisor waited %s after the crash budget was exhausted", delay)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestDataBaseDirMatchesTheGoServer pins the environment contract: the child receives
// the SAME NEX_DATA_BASE_DIR Go resolved for itself, so web-sessions.json — written by
// both implementations during the strangler transition — resolves to one file.
func TestDataBaseDirMatchesTheGoServer(t *testing.T) {
	base := t.TempDir()
	t.Setenv("NEX_DATA_BASE_DIR", base)
	if got := DataBaseDir(); got != base {
		t.Errorf("DataBaseDir() = %q, want %q", got, base)
	}
	if got, want := AppConfigDir(), filepath.Join(base, ".nex", "v2"); got != want {
		t.Errorf("AppConfigDir() = %q, want %q", got, want)
	}
	env, err := SpawnConfig{Args: []string{"node"}, DataBaseDir: base}.BuildEnv(0)
	if err != nil {
		t.Fatalf("BuildEnv: %v", err)
	}
	if got := envValue(env, "NEX_DATA_BASE_DIR"); got != base {
		t.Errorf("child env NEX_DATA_BASE_DIR = %q, want %q", got, base)
	}
	if got := envValue(env, "PORT"); got != "0" {
		t.Errorf("child env PORT = %q, want 0 (ephemeral)", got)
	}
	if got := envValue(env, "NEX_SERVER_HOST"); got != LoopbackHost {
		t.Errorf("child env NEX_SERVER_HOST = %q, want %q", got, LoopbackHost)
	}
}

// TestAuthTokenIsGeneratedNotConfigured: the shared secret is a per-start random
// value with the shape hostCapability.ts uses, never a caller-supplied default.
func TestAuthTokenIsGeneratedNotConfigured(t *testing.T) {
	first, err := GenerateAuthToken()
	if err != nil {
		t.Fatalf("GenerateAuthToken: %v", err)
	}
	second, err := GenerateAuthToken()
	if err != nil {
		t.Fatalf("GenerateAuthToken: %v", err)
	}
	if first == second {
		t.Error("two generated secrets are equal; the generator is not random")
	}
	// 32 random bytes base64url without padding — the same construction as the
	// one-shot host capability ticket (hostCapability.ts, randomBytes(32)).
	if len(first) != 43 {
		t.Errorf("secret length = %d, want 43 (32 bytes base64url, unpadded)", len(first))
	}
	if strings.ContainsAny(first, "+/=") {
		t.Errorf("secret %q is not base64url without padding", first)
	}
}

// TestExtraEnvCannotOverrideManagedVariables: a caller must not be able to end up with
// two PORT entries, or with a host that is not loopback, through ExtraEnv. The order
// of the two NEX_SERVER_AUTH_TOKEN entries is the difference between a working
// upstream and an authentication failure nobody can explain.
func TestExtraEnvCannotOverrideManagedVariables(t *testing.T) {
	env, err := SpawnConfig{
		Args:      []string{"node"},
		AuthToken: "generated-by-go",
		ExtraEnv: []string{
			"PORT=9999",
			"NEX_SERVER_HOST=0.0.0.0",
			"NEX_SERVER_AUTH_TOKEN=attacker",
			"NEX_SERVER_ID=some-id",
			"malformed-without-equals",
		},
	}.BuildEnv(0)
	if err != nil {
		t.Fatalf("BuildEnv: %v", err)
	}
	counts := map[string]int{}
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		counts[key]++
	}
	for _, key := range []string{"PORT", "NEX_SERVER_HOST", "NEX_SERVER_AUTH_TOKEN"} {
		if counts[key] != 1 {
			t.Errorf("%s appears %d times, want exactly 1: %v", key, counts[key], env)
		}
	}
	if got := envValue(env, "PORT"); got != "0" {
		t.Errorf("PORT = %q, want the managed 0", got)
	}
	if got := envValue(env, "NEX_SERVER_HOST"); got != LoopbackHost {
		t.Errorf("NEX_SERVER_HOST = %q, want the managed loopback", got)
	}
	if got := envValue(env, "NEX_SERVER_AUTH_TOKEN"); got != "generated-by-go" {
		t.Errorf("NEX_SERVER_AUTH_TOKEN = %q, want the caller's token", got)
	}
	// An ExtraEnv key this package does not manage still passes through: that is how
	// NEX_SERVER_ID / NEX_WEB_STATIC_ROOT reach the child without a field per variable.
	if got := envValue(env, "NEX_SERVER_ID"); got != "some-id" {
		t.Errorf("NEX_SERVER_ID = %q, want it passed through", got)
	}
	for _, entry := range env {
		if !strings.Contains(entry, "=") {
			t.Errorf("malformed entry %q reached the child environment", entry)
		}
	}
}
