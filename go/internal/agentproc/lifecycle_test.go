// lifecycle_test.go covers the process lifecycle rules of step 3.10.1 against
// real child processes: spawn, reuse, per-workspace isolation, crash, hang, slow
// start, the bounded stdin-EOF wait, idle reclaim, restart epochs and tree kill.
package agentproc

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSpawnAndResidencyReusesOneProcessPerWorkspace: the residency rule. The same
// workspace key must get the SAME process (a second GetClient is a reuse, not a
// second spawn), and the epoch must not move.
func TestSpawnAndResidencyReusesOneProcessPerWorkspace(t *testing.T) {
	manager := newTestManager(t, Options{CommandResolver: fakeAgentCommand(t, "", nil)})
	workspace, _ := tempWorkspace(t, "ws")

	first, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("first GetClient: %v", err)
	}
	if first.PID() == 0 {
		t.Fatal("first GetClient returned a client with no pid")
	}

	second, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("second GetClient: %v", err)
	}
	if first.PID() != second.PID() {
		t.Fatalf("pid changed across GetClient for the same workspace: %d then %d", first.PID(), second.PID())
	}
	if first.Generation() != 1 {
		t.Errorf("first generation = %d, want 1", first.Generation())
	}
	if got := manager.ManagedCount(); got != 1 {
		t.Errorf("ManagedCount = %d, want 1 (one resident process)", got)
	}
}

// TestTwoWorkspacesGetTwoProcesses is the other half of the residency rule: two
// different keys must never share one agent process.
func TestTwoWorkspacesGetTwoProcesses(t *testing.T) {
	manager := newTestManager(t, Options{CommandResolver: fakeAgentCommand(t, "", nil)})
	first, _ := tempWorkspace(t, "ws-a")
	second, _ := tempWorkspace(t, "ws-b")

	clientA, err := manager.GetClient(context.Background(), first)
	if err != nil {
		t.Fatalf("GetClient(a): %v", err)
	}
	clientB, err := manager.GetClient(context.Background(), second)
	if err != nil {
		t.Fatalf("GetClient(b): %v", err)
	}
	if clientA.PID() == clientB.PID() {
		t.Fatalf("two workspaces shared pid %d", clientA.PID())
	}
	if got := manager.ManagedCount(); got != 2 {
		t.Errorf("ManagedCount = %d, want 2", got)
	}
}

// TestSamePathDifferentIdentityDoesNotShare: the AGENTS.md identity rule. Two
// workspaces with the SAME path but DIFFERENT remote identities are two agents —
// this is the case that a path-only key gets wrong.
func TestSamePathDifferentIdentityDoesNotShare(t *testing.T) {
	manager := newTestManager(t, Options{CommandResolver: fakeAgentCommand(t, "", nil)})
	dir := t.TempDir()
	remoteA := WorkspaceRef{WorkspacePath: dir, WorkspaceIdentity: "ssh://host-a" + dir}
	remoteB := WorkspaceRef{WorkspacePath: dir, WorkspaceIdentity: "ssh://host-b" + dir}

	clientA, err := manager.GetClient(context.Background(), remoteA)
	if err != nil {
		t.Fatalf("GetClient(a): %v", err)
	}
	clientB, err := manager.GetClient(context.Background(), remoteB)
	if err != nil {
		t.Fatalf("GetClient(b): %v", err)
	}
	if clientA.PID() == clientB.PID() {
		t.Fatalf("two identities on one path shared pid %d", clientA.PID())
	}
	if clientA.Workspace().Key() == clientB.Workspace().Key() {
		t.Fatal("the two identities resolved to the same residency key")
	}
}

// TestConcurrentGetClientSpawnsOnce: host warmup and the first UI read can ask
// for the same workspace at the same time. Exactly one process may result.
func TestConcurrentGetClientSpawnsOnce(t *testing.T) {
	manager := newTestManager(t, Options{CommandResolver: fakeAgentCommand(t, "", nil)})
	workspace, _ := tempWorkspace(t, "ws")

	const callers = 12
	pids := make(chan int, callers)
	errs := make(chan error, callers)
	var start sync.WaitGroup
	start.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer start.Done()
			client, err := manager.GetClient(context.Background(), workspace)
			if err != nil {
				errs <- err
				return
			}
			pids <- client.PID()
		}()
	}
	start.Wait()
	close(pids)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent GetClient: %v", err)
	}
	unique := map[int]bool{}
	for pid := range pids {
		unique[pid] = true
	}
	if len(unique) != 1 {
		t.Fatalf("concurrent GetClient produced %d distinct processes, want 1", len(unique))
	}
	if got := manager.ManagedCount(); got != 1 {
		t.Errorf("ManagedCount = %d, want 1", got)
	}
}

// TestCrashIsReportedAsUnexpectedAndRespawns covers the crash path: the exit is
// classified as unexpected, the lifecycle log carries the marker, and the next
// GetClient transparently starts a new process in the next epoch.
func TestCrashIsReportedAsUnexpectedAndRespawns(t *testing.T) {
	logger := &capturingLogger{}
	recorder := &recordingReporter{}
	manager := newTestManager(t, Options{
		CommandResolver:          fakeAgentCommand(t, "crash-plain", map[string]string{"FAKE_STDERR_LINES": "4"}),
		Log:                      logger,
		ProcessLifecycleReporter: recorder,
	})
	workspace, _ := tempWorkspace(t, "ws")

	client, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	firstPID := client.PID()

	waitFor(t, 10*time.Second, "the crash to be observed", func() bool {
		return recorder.exitCount() > 0
	})

	exit := recorder.lastExit()
	if exit.TerminationKind != terminationUnexpected {
		t.Errorf("terminationKind = %q, want %q — a crash must not look expected",
			exit.TerminationKind, terminationUnexpected)
	}
	if exit.ExitCode == 0 {
		t.Error("exit code = 0, want the fake agent's non-zero crash code")
	}
	// The stderr tail is the only explanation a production log has for a crash.
	if exit.StderrLineCount == 0 {
		t.Error("no stderr lines were captured for the crash")
	}
	if len(exit.StderrTail) == 0 {
		t.Error("an unexpected exit carried no stderr tail")
	}
	if !logger.contains(LifecycleLogMarker) {
		t.Errorf("the lifecycle log marker %q never reached the log", LifecycleLogMarker)
	}

	// The crashed process is no longer the resident one; the next call respawns in
	// a new epoch.
	waitFor(t, 5*time.Second, "the crashed process to be released", func() bool {
		return manager.ManagedCount() == 0
	})
	second, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("GetClient after crash: %v", err)
	}
	if second.PID() == firstPID {
		t.Fatal("the respawn reused the crashed pid")
	}
	if second.Generation() <= client.Generation() {
		t.Errorf("respawn generation = %d, want greater than %d", second.Generation(), client.Generation())
	}
}

// TestDiagnosticIsReportedAndNotTailed covers the stderr side channel: a
// well-formed diagnostic line is reported as a structured exception (and redacted
// on the way out) instead of being buried in the log tail.
func TestDiagnosticIsReportedAndNotTailed(t *testing.T) {
	recorder := &recordingReporter{}
	manager := newTestManager(t, Options{
		CommandResolver:          fakeAgentCommand(t, "crash", nil),
		ProcessLifecycleReporter: recorder,
	})
	workspace, _ := tempWorkspace(t, "ws")

	if _, err := manager.GetClient(context.Background(), workspace); err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	waitFor(t, 10*time.Second, "the diagnostic to be reported", func() bool {
		return recorder.exceptionCount() > 0
	})

	exception := recorder.lastException()
	if exception.Diagnostic.Name != "FakeAgentCrash" {
		t.Errorf("diagnostic name = %q, want FakeAgentCrash", exception.Diagnostic.Name)
	}
	if exception.Diagnostic.Kind != KindUncaughtException {
		t.Errorf("diagnostic kind = %q, want %q", exception.Diagnostic.Kind, KindUncaughtException)
	}
	// The diagnostic is a reported fact, not log tail: the exit tail must NOT carry
	// the diagnostic line itself.
	exit := recorder.lastExit()
	for _, line := range exit.StderrTail {
		if len(line) > 0 && line[0] == '[' {
			t.Errorf("the diagnostic line leaked into the stderr tail: %q", line)
		}
	}
}

// TestRedactionHappensBeforeTheRecordLeavesTheHost is the secret-boundary test:
// the secret-shaped value the fake agent prints must be redacted in BOTH the
// exception record and the stderr tail.
func TestRedactionHappensBeforeTheRecordLeavesTheHost(t *testing.T) {
	recorder := &recordingReporter{}
	const secret = "sk-abcdefghijklmnopqrstuvwx"
	manager := newTestManager(t, Options{
		CommandResolver:          fakeAgentCommand(t, "crash", nil),
		ProcessLifecycleReporter: recorder,
	})
	workspace, _ := tempWorkspace(t, "ws")

	if _, err := manager.GetClient(context.Background(), workspace); err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	waitFor(t, 10*time.Second, "the diagnostic to be reported", func() bool {
		return recorder.exceptionCount() > 0
	})
	exception := recorder.lastException()
	if contains(exception.Diagnostic.Message, secret) {
		t.Errorf("the exception message still carries the raw key: %q", exception.Diagnostic.Message)
	}
	if !contains(exception.Diagnostic.Message, "<redacted>") {
		t.Errorf("the exception message was not redacted at all: %q", exception.Diagnostic.Message)
	}

	// The same rule applies to the plain stderr tail.
	plainRecorder := &recordingReporter{}
	const plainSecret = "sk-zzzzzzzzzzzzzzzzzzzzzzzz"
	plain := newTestManager(t, Options{
		CommandResolver: fakeAgentCommand(t, "crash-plain", map[string]string{
			"FAKE_STDERR_LINES": "3",
			"FAKE_STDERR_TEXT":  "authorization: Bearer " + plainSecret,
		}),
		ProcessLifecycleReporter: plainRecorder,
	})
	other, _ := tempWorkspace(t, "ws2")
	if _, err := plain.GetClient(context.Background(), other); err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	waitFor(t, 10*time.Second, "the plain crash to be reported", func() bool {
		return plainRecorder.exitCount() > 0
	})
	tail := plainRecorder.lastExit().StderrTail
	if len(tail) == 0 {
		t.Fatal("the plain crash produced no stderr tail")
	}
	for _, line := range tail {
		if contains(line, plainSecret) {
			t.Errorf("the stderr tail still carries the raw key: %q", line)
		}
		if !contains(line, "<redacted>") {
			t.Errorf("the stderr tail line was not redacted: %q", line)
		}
	}
}

// TestHangingProcessIsKilledAfterTheEOFWait is the 1.8 s stdin-EOF rule: an agent
// that ignores EOF must still be gone, and gone via the tree kill.
func TestHangingProcessIsKilledAfterTheEOFWait(t *testing.T) {
	manager := newTestManager(t, Options{CommandResolver: fakeAgentCommand(t, "ignore-eof", nil)})
	workspace, _ := tempWorkspace(t, "ws")

	client, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	pid := client.PID()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := manager.DisposeWorkspace(ctx, workspace); err != nil {
		t.Fatalf("DisposeWorkspace: %v", err)
	}
	elapsed := time.Since(start)

	// The graceful EOF window is 1.8 s; the kill must land shortly after, not hang
	// and not return instantly (which would mean EOF was mistaken for a stop).
	if elapsed < StdioEOFWait {
		t.Errorf("dispose returned after %s, before the %s EOF window — the bounded wait was skipped", elapsed, StdioEOFWait)
	}
	if elapsed > 15*time.Second {
		t.Errorf("dispose took %s, far beyond the EOF window plus the tree kill", elapsed)
	}
	assertGone(t, pid)
}

// TestGracefulStdinEOFStopsTheAgentWithoutTheKillPath is the positive half: an
// agent that honors stdin EOF exits on its own, and the dispose returns quickly
// with the process already gone.
func TestGracefulStdinEOFStopsTheAgentWithoutTheKillPath(t *testing.T) {
	manager := newTestManager(t, Options{CommandResolver: fakeAgentCommand(t, "", nil)})
	workspace, _ := tempWorkspace(t, "ws")

	client, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	pid := client.PID()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	if err := manager.DisposeWorkspace(ctx, workspace); err != nil {
		t.Fatalf("DisposeWorkspace: %v", err)
	}
	elapsed := time.Since(start)
	// A well-behaved agent exits on EOF, so the dispose must not burn the whole
	// 1.8 s window before escalating.
	if elapsed >= StdioEOFWait {
		t.Errorf("dispose of an EOF-honoring agent took %s; the EOF window should have been enough", elapsed)
	}
	assertGone(t, pid)
}

// TestSlowStartStillSpawnsAndIsReaped: an agent that takes its time to come up must
// still be spawned, tracked and reaped — the manager has no separate "starting"
// timeout that could leave it behind.
func TestSlowStartStillSpawnsAndIsReaped(t *testing.T) {
	readyFile := filepath.Join(t.TempDir(), "ready")
	manager := newTestManager(t, Options{
		CommandResolver: fakeAgentCommand(t, "slow-start", map[string]string{
			"FAKE_SLOW_START_MS": "300",
			"FAKE_READY_FILE":    readyFile,
		}),
	})
	workspace, _ := tempWorkspace(t, "ws")

	client, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	waitFor(t, 10*time.Second, "the slow agent to announce readiness", func() bool {
		_, err := os.Stat(readyFile)
		return err == nil
	})
	if !client.IsAlive() {
		t.Fatal("the slow-starting agent was reported dead after announcing ready")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := manager.DisposeWorkspace(ctx, workspace); err != nil {
		t.Fatalf("DisposeWorkspace: %v", err)
	}
	assertGone(t, client.PID())
}

// TestStdoutEOFDoesNotMeanTheProcessExited pins the boundary between the two
// halves of the transport: an agent that closes stdout while staying alive has
// ended the PROTOCOL leg but is still an OS process the manager owns. Getting
// this backwards is how a host loses track of a live tree, so the test asserts
// the process is still there and still reapable.
func TestStdoutEOFDoesNotMeanTheProcessExited(t *testing.T) {
	manager := newTestManager(t, Options{CommandResolver: fakeAgentCommand(t, "stdout-eof", nil)})
	workspace, _ := tempWorkspace(t, "ws")

	client, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	// The fake agent closes stdout immediately and then hangs; the process must
	// still be alive even though its output stream is finished.
	time.Sleep(300 * time.Millisecond)
	if !client.IsAlive() {
		t.Fatal("closing stdout was treated as the process exiting")
	}
	if manager.ManagedCount() != 1 {
		t.Errorf("ManagedCount = %d, want 1 — a live process must stay owned", manager.ManagedCount())
	}

	// And it is still reapable: ownership of a half-dead process is not lost.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := manager.DisposeWorkspace(ctx, workspace); err != nil {
		t.Fatalf("DisposeWorkspace: %v", err)
	}
	assertGone(t, client.PID())
}

// TestIdleReclaimStopsTheTreeAndRespawnsOnDemand covers the idle window of a
// control-plane lane (mcp-status style): no work for the configured time reclaims
// the tree, and the next GetClient transparently starts a new one.
func TestIdleReclaimStopsTheTreeAndRespawnsOnDemand(t *testing.T) {
	manager := newTestManager(t, Options{
		CommandResolver: fakeAgentCommand(t, "", nil),
		IdleTimeoutMs:   250,
	})
	workspace, _ := tempWorkspace(t, "ws")

	first, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	firstPID := first.PID()

	waitFor(t, 10*time.Second, "the idle reclaim to take the tree", func() bool {
		return !processExists(firstPID)
	})
	if got := manager.ManagedCount(); got != 0 {
		t.Errorf("ManagedCount after idle reclaim = %d, want 0", got)
	}

	// The next use transparently respawns, in the next epoch.
	second, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("GetClient after idle reclaim: %v", err)
	}
	if !second.IsAlive() {
		t.Error("the respawned agent is not alive")
	}
	if second.Generation() <= first.Generation() {
		t.Errorf("respawn generation = %d, want greater than %d", second.Generation(), first.Generation())
	}
}

// TestNoIdleReclaimWithoutATimeout: a chat lane has no idle timeout, so its agent
// must survive indefinitely. A manager that reclaimed by default would kill the
// agent out from under an open task.
func TestNoIdleReclaimWithoutATimeout(t *testing.T) {
	manager := newTestManager(t, Options{CommandResolver: fakeAgentCommand(t, "", nil)})
	workspace, _ := tempWorkspace(t, "ws")

	client, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	// Well past any plausible idle window.
	time.Sleep(700 * time.Millisecond)
	if !client.IsAlive() {
		t.Fatal("an agent on a manager with no idle timeout was reclaimed anyway")
	}
	if manager.ManagedCount() != 1 {
		t.Errorf("ManagedCount = %d, want 1", manager.ManagedCount())
	}
}

// TestRestartBumpsTheRuntimeEpoch is the restartWorkspaceProcess rule: disposing a
// workspace and asking again must produce a NEW epoch, so a subscriber can tell
// the old runtime's frames from the new one's.
func TestRestartBumpsTheRuntimeEpoch(t *testing.T) {
	recorder := &recordingReporter{}
	manager := newTestManager(t, Options{
		CommandResolver:          fakeAgentCommand(t, "", nil),
		ProcessLifecycleReporter: recorder,
	})
	workspace, _ := tempWorkspace(t, "ws")

	first, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	firstGeneration := first.Generation()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := manager.DisposeWorkspace(ctx, workspace); err != nil {
		t.Fatalf("DisposeWorkspace: %v", err)
	}

	second, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("GetClient after restart: %v", err)
	}
	if second.Generation() <= firstGeneration {
		t.Fatalf("generation after restart = %d, want greater than %d", second.Generation(), firstGeneration)
	}
	if second.Identity().Identity == first.Identity().Identity {
		t.Error("the restart reused the previous runtime identity string")
	}
	// The restart is announced so v4 subscribers know to re-subscribe.
	restarts := manager.DrainRestarted()
	if len(restarts) != 1 {
		t.Fatalf("restarted events = %d, want 1", len(restarts))
	}
	if restarts[0].RuntimeIdentity.Generation != second.Generation() {
		t.Errorf("the restarted event carried generation %d, want %d",
			restarts[0].RuntimeIdentity.Generation, second.Generation())
	}
}

// TestDisposeWorkspaceDoesNotCloseTheManager: restartWorkspaceProcess must reclaim
// ONE workspace's agent. If it closed the manager, no agent could ever be started
// again for that host.
func TestDisposeWorkspaceDoesNotCloseTheManager(t *testing.T) {
	manager := newTestManager(t, Options{CommandResolver: fakeAgentCommand(t, "", nil)})
	first, _ := tempWorkspace(t, "ws-a")
	second, _ := tempWorkspace(t, "ws-b")

	if _, err := manager.GetClient(context.Background(), first); err != nil {
		t.Fatalf("GetClient(a): %v", err)
	}
	if _, err := manager.GetClient(context.Background(), second); err != nil {
		t.Fatalf("GetClient(b): %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := manager.DisposeWorkspace(ctx, first); err != nil {
		t.Fatalf("DisposeWorkspace: %v", err)
	}

	// The OTHER workspace must be untouched, and the disposed one must be able to
	// start again: a workspace dispose is not a manager dispose.
	if client := manager.GetExistingClient(second); client == nil {
		t.Fatal("disposing one workspace evicted the other")
	}
	client, err := manager.GetClient(context.Background(), first)
	if err != nil {
		t.Fatalf("GetClient after workspace dispose: %v", err)
	}
	if !client.IsAlive() {
		t.Error("the respawned agent for the disposed workspace is not alive")
	}
}

// TestDisposeAllAndWaitReapsEverything is the app-quit path: after it returns, no
// agent process may remain and the manager must hold nothing.
func TestDisposeAllAndWaitReapsEverything(t *testing.T) {
	manager := newTestManager(t, Options{CommandResolver: fakeAgentCommand(t, "", nil)})
	workspaces := make([]WorkspaceRef, 0, 3)
	pids := make([]int, 0, 3)
	for i := 0; i < 3; i++ {
		workspace, _ := tempWorkspace(t, "ws-"+strconv.Itoa(i))
		client, err := manager.GetClient(context.Background(), workspace)
		if err != nil {
			t.Fatalf("GetClient(%d): %v", i, err)
		}
		workspaces = append(workspaces, workspace)
		pids = append(pids, client.PID())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	manager.DisposeAllAndWait(ctx)

	if got := manager.ManagedCount(); got != 0 {
		t.Errorf("ManagedCount after DisposeAllAndWait = %d, want 0", got)
	}
	for _, pid := range pids {
		assertGone(t, pid)
	}
	// After disposal a new start is refused, which is what "disposed" means.
	if _, err := manager.GetClient(context.Background(), workspaces[0]); err == nil {
		t.Error("GetClient succeeded on a disposed manager")
	}
}

// TestTreeKillReachesTheGrandchild is the point of spawning the agent in its own
// process group: killing only the root would leave its MCP-like descendants
// alive, holding the workspace and its runtime cache open.
func TestTreeKillReachesTheGrandchild(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the grandchild check reads /proc and is Linux-only")
	}
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	manager := newTestManager(t, Options{CommandResolver: fakeAgentCommand(t, "", map[string]string{
		"FAKE_CHILD":          "1",
		"FAKE_CHILD_PID_FILE": pidFile,
	})})
	workspace, _ := tempWorkspace(t, "ws")

	client, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	rootPID := client.PID()
	// Wait until the grandchild's pid is published and the process is really there,
	// so the assertion is about a process that demonstrably existed.
	var descendant int
	waitFor(t, 10*time.Second, "the grandchild pid to be published", func() bool {
		raw, readErr := os.ReadFile(pidFile)
		if readErr != nil {
			return false
		}
		descendant, readErr = strconv.Atoi(strings.TrimSpace(string(raw)))
		return readErr == nil && descendant > 0 && processExists(descendant)
	})
	if descendant == rootPID {
		t.Fatalf("the grandchild pid %d equals the root pid", descendant)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := manager.DisposeWorkspace(ctx, workspace); err != nil {
		t.Fatalf("DisposeWorkspace: %v", err)
	}

	assertGone(t, rootPID)
	// The tree kill is the whole point: a descendant that outlived the root must
	// not survive the reap.
	waitFor(t, 5*time.Second, "the grandchild to be reaped with the tree", func() bool {
		return !processExists(descendant)
	})
}

// TestNoZombiesAfterManySpawnKillCycles is the soak the gate asks for: 100 cycles
// of spawn + kill must leave no unreaped process behind. A manager that forgot
// Wait() (or that reaped twice) shows up here as a growing set of zombies.
func TestNoZombiesAfterManySpawnKillCycles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the zombie check reads /proc and is Linux-only")
	}
	const cycles = 100
	manager := newTestManager(t, Options{CommandResolver: fakeAgentCommand(t, "", nil)})
	workspace, _ := tempWorkspace(t, "ws")

	pids := make([]int, 0, cycles)
	for i := 0; i < cycles; i++ {
		client, err := manager.GetClient(context.Background(), workspace)
		if err != nil {
			t.Fatalf("cycle %d GetClient: %v", i, err)
		}
		pids = append(pids, client.PID())
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err = manager.DisposeWorkspace(ctx, workspace)
		cancel()
		if err != nil {
			t.Fatalf("cycle %d DisposeWorkspace: %v", i, err)
		}
		if got := manager.ManagedCount(); got != 0 {
			t.Fatalf("cycle %d left %d owned processes; ownership must be released per cycle", i, got)
		}
	}

	// No zombie and no survivor among the pids we spawned.
	for _, pid := range pids {
		if isZombie(pid) {
			t.Errorf("pid %d is a zombie after the spawn/kill cycles", pid)
		}
		if processExists(pid) {
			t.Errorf("pid %d still exists after the spawn/kill cycles", pid)
		}
	}
}

// TestGoroutinesDoNotLeakAcrossRestarts is the goroutine-leak check: every spawn
// starts a stderr pump and an exit observer, and both must return when the
// process is reaped. A leak here is what turns a long-lived host into a slow one.
func TestGoroutinesDoNotLeakAcrossRestarts(t *testing.T) {
	manager := newTestManager(t, Options{CommandResolver: fakeAgentCommand(t, "", nil)})
	workspace, _ := tempWorkspace(t, "ws")

	// Warm up first: the first spawn starts package-level goroutines (the race
	// detector, the runtime) that would otherwise be counted as leaks.
	if _, err := manager.GetClient(context.Background(), workspace); err != nil {
		t.Fatalf("warm-up GetClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	if err := manager.DisposeWorkspace(ctx, workspace); err != nil {
		t.Fatalf("warm-up dispose: %v", err)
	}
	cancel()
	settle()
	baseline := runtime.NumGoroutine()

	for i := 0; i < 20; i++ {
		if _, err := manager.GetClient(context.Background(), workspace); err != nil {
			t.Fatalf("iteration %d GetClient: %v", i, err)
		}
		iterCtx, iterCancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := manager.DisposeWorkspace(iterCtx, workspace); err != nil {
			t.Fatalf("iteration %d dispose: %v", i, err)
		}
		iterCancel()
	}
	settle()
	after := runtime.NumGoroutine()
	// The exec adapter keeps a small, bounded amount of state per Process; a leak
	// would show as ~2 goroutines per cycle (pump + observer), i.e. 40 here.
	if after > baseline+5 {
		t.Errorf("goroutines grew from %d to %d across 20 restart cycles — a pump or an observer is leaking", baseline, after)
	}
}

// settle gives the reaper goroutines a moment to unwind before a goroutine count.
func settle() {
	for i := 0; i < 20; i++ {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
}

// assertGone fails when a pid is still present after a reap.
func assertGone(t *testing.T, pid int) {
	t.Helper()
	if runtime.GOOS != "linux" {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("pid %d is still in the process table after the reap", pid)
}

// waitForDescendant finds a live child of the given pid, polling /proc.
func waitForDescendant(t *testing.T, rootPID int) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir("/proc")
		if err != nil {
			t.Fatalf("read /proc: %v", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			pid, err := strconv.Atoi(entry.Name())
			if err != nil {
				continue
			}
			if parentPIDOf(t, pid) == rootPID && pid != rootPID {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no descendant of %d appeared", rootPID)
	return 0
}

// parentPIDOf reads a pid's parent from /proc/<pid>/stat, tolerating a process that
// exits between the listing and the read.
func parentPIDOf(t *testing.T, pid int) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return -1
	}
	text := string(data)
	close := -1
	for i := len(text) - 1; i >= 0; i-- {
		if text[i] == ')' {
			close = i
			break
		}
	}
	if close < 0 || close+4 >= len(text) {
		return -1
	}
	// After ")" comes " S ppid ..."; ppid starts two fields later.
	fields := 0
	index := close + 2
	for index < len(text) && fields < 2 {
		if text[index] == ' ' {
			fields++
			if fields == 2 {
				break
			}
		}
		index++
	}
	start := index
	for index < len(text) && text[index] != ' ' {
		index++
	}
	ppid, err := strconv.Atoi(text[start:index])
	if err != nil {
		return -1
	}
	return ppid
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
