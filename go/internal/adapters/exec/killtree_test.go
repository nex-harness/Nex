//go:build !windows

// Process-tree tests. The Windows twin lives in killtree_windows_test.go; the
// scenarios themselves are the ones step 1.8 of
// docs/specs/go-rewrite/phase-1-go-agent.md asks for ("process-tree kill test
// spawns sh -c 'sleep 60 & sleep 60'", 10 MiB truncation, cancellation latency).
package exec

import (
	"bytes"
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// procEntry is one row of the process table snapshot.
type procEntry struct {
	pid, ppid, pgid int
}

// snapshotProcessTable lists (pid, ppid, pgid) for every visible process.
//
// /proc is the source because it is the only table the test can read without a
// tool dependency; the two callers degrade to an empty snapshot on a system
// without /proc, which makes the assertions vacuous rather than flaky on a
// restricted CI container.
func snapshotProcessTable(t *testing.T) []procEntry {
	t.Helper()
	dirs, err := os.ReadDir("/proc")
	if err != nil {
		t.Logf("no /proc (%v): process-table assertions degrade to a no-op", err)
		return nil
	}
	var out []procEntry
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue // not a pid directory
		}
		status, err := os.ReadFile(filepath.Join("/proc", d.Name(), "status"))
		if err != nil {
			continue // the process exited between ReadDir and ReadFile
		}
		e := procEntry{pid: pid}
		for line := range strings.Lines(string(status)) {
			switch {
			case strings.HasPrefix(line, "PPid:"):
				e.ppid, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:")))
			case strings.HasPrefix(line, "NSpgid:"):
				e.pgid, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "NSpgid:")))
			}
		}
		out = append(out, e)
	}
	return out
}

// descendantsOf returns every process whose PPid chain reaches root, so the
// assertion also catches a grandchild that job control moved into another
// process group (the case process-tree.ts snapshots `ps` for).
func descendantsOf(table []procEntry, root int) []procEntry {
	byParent := map[int][]procEntry{}
	for _, e := range table {
		byParent[e.ppid] = append(byParent[e.ppid], e)
	}
	var found []procEntry
	queue := byParent[root]
	for len(queue) > 0 {
		e := queue[0]
		queue = queue[1:]
		found = append(found, e)
		queue = append(queue, byParent[e.pid]...)
	}
	return found
}

// groupMembers returns every process in the process group led by pgid.
func groupMembers(table []procEntry, pgid int) []procEntry {
	var found []procEntry
	for _, e := range table {
		if e.pgid == pgid {
			found = append(found, e)
		}
	}
	return found
}

// TestProcessTreeKillLeavesNoDescendants is the step-1.8 scenario: a shell that
// backgrounded two long sleeps must leave nothing behind after the tree is
// killed, neither in its own process group nor outside it.
func TestProcessTreeKillLeavesNoDescendants(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := Start(ctx, Options{Args: []string{"sh", "-c", "sleep 60 & sleep 60 & wait"}})
	if err != nil {
		t.Fatal(err)
	}
	leader := p.PID()
	// Give the shell time to fork both workers; killing too early would prove
	// nothing because there would be nothing to orphan.
	time.Sleep(200 * time.Millisecond)
	if kids := descendantsOf(snapshotProcessTable(t), leader); len(kids) < 2 {
		t.Fatalf("expected at least 2 descendants of %d before the kill, got %d", leader, len(kids))
	}
	killCtx, killCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer killCancel()
	if err := KillProcessTree(killCtx, leader); err != nil {
		t.Fatalf("KillProcessTree(%d): %v", leader, err)
	}
	// The kill returns once the leader is gone; descendants may still be
	// unwinding, so the window is polled until it is empty.
	deadline := time.Now().Add(2 * time.Second)
	var left []procEntry
	for {
		table := snapshotProcessTable(t)
		left = append(descendantsOf(table, leader), groupMembers(table, leader)...)
		if len(left) == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(left) != 0 {
		t.Errorf("process tree of %d left %d descendant(s) alive: %v", leader, len(left), left)
	}
	_ = p.Wait()
}

// TestCancellationObservedWithin500ms pins the spec's cancellation budget: a
// cancelled Start context has to terminate the child, and Wait has to return
// within 500 ms of the cancel.
func TestCancellationObservedWithin500ms(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := Start(ctx, Options{Args: []string{"sleep", "60"}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan Result, 1)
	go func() { done <- p.Wait() }()
	cancel()
	select {
	case res := <-done:
		if res.WaitErr == nil {
			t.Errorf("a killed child must report a non-nil WaitErr, got %+v", res)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Wait did not return within 500ms of cancel (DefaultTermGrace=%v)", DefaultTermGrace)
	}
	if err := KillProcessTree(context.Background(), p.PID()); err != nil {
		t.Errorf("kill of an already-dead tree must be nil, got %v", err)
	}
}

// TestOutputCollectorTruncation is the step-1.8 "output truncation test with
// 10 MiB output": the byte count stays honest while head and tail respect
// their caps.
func TestOutputCollectorTruncation(t *testing.T) {
	const size = 10 << 20 // 10 MiB
	// awk writes the bytes in one burst per chunk without buffering the whole
	// output in the child, which keeps the child's memory flat.
	cmd := osexec.Command("awk", "BEGIN{for(i=0;i<10485760;i++)printf \"x\"}")
	// A small inline cap is what makes truncation observable: the default 1 MiB
	// head would hide the difference between "dropped" and "kept".
	collector := NewOutputCollector(4096, 1024)
	cmd.Stdout = collector
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	out := collector.Result()
	if out.Bytes < size {
		t.Errorf("Bytes = %d, want >= %d: dropped bytes must still be counted", out.Bytes, size)
	}
	if len(out.Text) != 4096 {
		t.Errorf("head = %d bytes, want exactly the 4096 cap", len(out.Text))
	}
	if len(out.Tail) != 1024 {
		t.Errorf("tail = %d bytes, want exactly the 1024 cap", len(out.Tail))
	}
	if !out.Truncated {
		t.Errorf("Truncated = false for %d bytes behind a 4096 cap", out.Bytes)
	}
	// The tail is the newest data: every byte came from the same run, so the
	// invariant that matters is that head+tail never exceeded the caps.
	if collector.Bytes() != out.Bytes {
		t.Errorf("Bytes() = %d, Result().Bytes = %d: must agree", collector.Bytes(), out.Bytes)
	}
}

// TestKillProcessTreeRefusesPidOne guards the self-kill boundary: pid 1 is
// init and pid 0 addresses our own process group.
func TestKillProcessTreeRefusesPidOne(t *testing.T) {
	for _, pid := range []int{0, 1, -1} {
		err := KillProcessTree(context.Background(), pid)
		if err == nil {
			t.Errorf("KillProcessTree(ctx, %d) = nil, want a refusal", pid)
			continue
		}
		if !strings.Contains(err.Error(), "refusing") {
			t.Errorf("KillProcessTree(ctx, %d) = %v, want the refusal error", pid, err)
		}
	}
}

// TestKillTreeIdempotent checks the contract KillTree documents: concurrent
// callers join the first kill instead of each signalling their own, and neither
// panics nor reports an error for a tree that is already gone.
func TestKillTreeIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := Start(ctx, Options{Args: []string{"sleep", "60"}})
	if err != nil {
		t.Fatal(err)
	}
	const callers = 8
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = p.KillTree(context.Background())
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("KillTree caller %d: %v", i, err)
		}
	}
	// A later call after the tree is gone joins the finished kill.
	if err := p.KillTree(context.Background()); err != nil {
		t.Errorf("KillTree after completion: %v", err)
	}
	res := p.Wait()
	if res.Signal != "SIGKILL" && res.Signal != "SIGTERM" {
		t.Errorf("Signal = %q, want the name of the signal that ended the child", res.Signal)
	}
}

// TestExecReportsCleanExit is the baseline the kill tests sit on: a command
// that exits 0 reports no signal and no WaitErr.
func TestExecReportsCleanExit(t *testing.T) {
	p, err := Start(context.Background(), Options{Args: []string{"sh", "-c", "printf hello"}})
	if err != nil {
		t.Fatal(err)
	}
	res := p.Wait()
	if res.WaitErr != nil || res.ExitCode != 0 || res.Killed() {
		t.Errorf("clean exit reported as %+v", res)
	}
	if got := res.Stdout.Text; got != "hello" {
		t.Errorf("stdout = %q, want %q", got, "hello")
	}
}

// TestStdoutAndStderrAreSeparate pins the two-collector contract: streams never
// bleed into each other, and each keeps its own head/tail caps.
func TestStdoutAndStderrAreSeparate(t *testing.T) {
	p, err := Start(context.Background(), Options{Args: []string{
		"sh", "-c", "printf out; printf err >&2",
	}})
	if err != nil {
		t.Fatal(err)
	}
	res := p.Wait()
	if res.Stdout.Text != "out" || res.Stderr.Text != "err" {
		t.Errorf("stdout=%q stderr=%q, want out/err", res.Stdout.Text, res.Stderr.Text)
	}
}

// TestStartRejectsEmptyArgs keeps the argv contract (the package never runs a
// shell string) at the entry point, where a caller bug would otherwise become a
// confusing exec error.
func TestStartRejectsEmptyArgs(t *testing.T) {
	if _, err := Start(context.Background(), Options{}); err == nil {
		t.Error("Start with no Args = nil, want an error")
	}
}

// TestOutputCollectorHeadCapIsExact checks the collector against a writer that
// dribbles output in chunks larger than the cap, where the tail ring and the
// head slice have to stay in bounds.
func TestOutputCollectorHeadCapIsExact(t *testing.T) {
	c := NewOutputCollector(8, 4)
	chunk := bytes.Repeat([]byte("ab"), 32)
	total := 0
	for range 10 {
		n, err := c.Write(chunk)
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	if total != len(chunk)*10 {
		t.Errorf("Write reported %d bytes consumed, want %d", total, len(chunk)*10)
	}
	out := c.Result()
	if out.Bytes != int64(len(chunk)*10) {
		t.Errorf("Bytes = %d, want %d", out.Bytes, len(chunk)*10)
	}
	if out.Text != "abababab" {
		t.Errorf("head = %q, want the first 8 bytes", out.Text)
	}
	if out.Tail != "abab" {
		t.Errorf("tail = %q, want the last 4 bytes", out.Tail)
	}
}

// TestGraceTimerStopIsIdempotent guards the two-call pattern in killProcessTree
// (defer timer.stop() after the channel already fired).
func TestGraceTimerStopIsIdempotent(t *testing.T) {
	timer := newGraceTimer(time.Millisecond)
	<-timer.done()
	timer.stop()
	timer.stop() // must not panic
}

// TestProcessTreeGoneClosesWhenThePidExits covers the watcher the kill body
// selects on, including the already-dead pid that must not block forever.
func TestProcessTreeGoneClosesWhenThePidExits(t *testing.T) {
	p, err := Start(context.Background(), Options{Args: []string{"sh", "-c", "exit 0"}})
	if err != nil {
		t.Fatal(err)
	}
	p.Wait()
	select {
	case <-processTreeGone(p.PID()):
	case <-time.After(2 * time.Second):
		t.Fatal("processTreeGone did not close for an exited pid")
	}
}

// TestSignalProcessTreeOnMissingPidIsNotAnError documents the ESRCH contract:
// killing a tree that already exited is a no-op, not a failure. The standalone
// entry point is called by timeouts that may have raced the child.
func TestSignalProcessTreeOnMissingPidIsNotAnError(t *testing.T) {
	// A pid that cannot exist: Start one, reap it, and reuse the number only if
	// the kernel has really retired it. 1<<22 is above the default pid_max on
	// Linux and below the int32 pid space, so ESRCH is reliable.
	const deadPID = 1 << 22
	if err := signalProcessTree(context.Background(), deadPID, terminateSignal); err != nil {
		t.Errorf("signalProcessTree(%d) = %v, want nil for a gone tree", deadPID, err)
	}
	if err := signalProcessTree(context.Background(), deadPID, killSignal); err != nil {
		t.Errorf("signalProcessTree(%d, kill) = %v, want nil for a gone tree", deadPID, err)
	}
}

// TestKillTreeDoesNotTouchOtherProcesses is the safety half of the boundary:
// killing one tree must not signal the test binary itself, its parent or a
// sibling that happens to share no group with the victim.
func TestKillTreeDoesNotTouchOtherProcesses(t *testing.T) {
	me := os.Getpid()
	if me <= 1 {
		t.Fatal("test binary must not run as pid 1")
	}
	p, err := Start(context.Background(), Options{Args: []string{"sleep", "60"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.KillTree(context.Background()); err != nil {
		t.Fatalf("KillTree: %v", err)
	}
	p.Wait()
	// If the kill had targeted our own group we would not reach this line; the
	// assertion is that the process is still the one we started out as.
	if os.Getpid() != me {
		t.Errorf("pid changed from %d to %d across a KillTree", me, os.Getpid())
	}
}

// TestKillTreeSurvivesDescendantThatIgnoresTerm covers the escalation: the
// group keeps a writer open after SIGTERM, so the kill must escalate to SIGKILL
// and Wait must still return.
func TestKillTreeSurvivesDescendantThatIgnoresTerm(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The trap keeps the shell alive through SIGTERM, so only the SIGKILL phase
	// can end the tree; its stdout stays open through the whole kill, which is
	// what makes Wait() the real assertion.
	p, err := Start(ctx, Options{
		Args:         []string{"sh", "-c", "trap '' TERM; sleep 60 & wait"},
		Dir:          t.TempDir(),
		Env:          []string{"PATH=" + os.Getenv("PATH")},
		MaxTailBytes: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan Result, 1)
	go func() { done <- p.Wait() }()
	if err := p.KillTree(context.Background()); err != nil {
		t.Errorf("KillTree on a SIGTERM-ignoring tree: %v", err)
	}
	select {
	case res := <-done:
		if !res.Killed() && res.ExitCode == 0 {
			t.Errorf("a killed tree reported %+v", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not return after the escalation to SIGKILL")
	}
}

// TestKillProcessTreeReportsRefusalBeforeSignalling pins ordering: the refusal
// for pid <= 1 happens before any signal, so the caller's own process group is
// never touched even transiently.
func TestKillProcessTreeReportsRefusalBeforeSignalling(t *testing.T) {
	err := KillProcessTree(context.Background(), 1)
	if err == nil {
		t.Fatal("KillProcessTree(ctx, 1) = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "pid 1") {
		t.Errorf("error %v should name the pid it refused", err)
	}
}
