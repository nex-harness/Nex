package taskadapter

import (
	"context"
	"testing"
)

// guards_owner_test.go — rows A1–A7 of phase-3-guards.md: the LOCAL ownership half.

// A1: a command with no ownerRunId is the desktop-continuous leg and is never
// owner-checked. Refusing it would break the desktop's own UI, which has no run id.
func TestOwnerGuard_NoOwnerRunIDPasses(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)

	// The task is mid-turn under a DIFFERENT run; a desktop command with no run id
	// still has to be accepted, because the desktop is the host's own UI.
	adapter.Owner().ClaimRun(target, "run-desktop")

	if _, err := adapter.EnqueueCommand(target, "", newCommand("c1", "t1", "hi")); err != nil {
		t.Fatalf("desktop-continuous enqueue must not be owner-checked: %v", err)
	}
}

// A2: a phone owner command that arrives after the run already closed is refused with
// NO_ACTIVE_TASK_OWNER — and refused BEFORE it can touch the queue.
func TestOwnerGuard_NoActiveOwnerRefused(t *testing.T) {
	adapter, _, scheduler := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	// The turn finished, so nothing owns the task any more.
	if _, ok := adapter.Owner().ActiveInput(target); ok {
		t.Fatal("fixture must start with no active run")
	}

	_, err := adapter.EnqueueCommand(target, "run-phone", newCommand("c1", "t1", "hi"))
	if got := OwnerErrorCode(err); got != CodeNoActiveTaskOwner {
		t.Fatalf("want %s, got %q (err=%v)", CodeNoActiveTaskOwner, got, err)
	}
	if got := adapter.Queue().list(target.TaskKey()); len(got) != 0 {
		t.Fatalf("a refused command must not enter the queue, got %d entries", len(got))
	}
	if scheduler.count() != 0 {
		t.Fatalf("a refused command must not schedule a drain, got %d", scheduler.count())
	}
}

// A3: the owner's staleness boundary. An enqueue naming an OLD run must not mutate the
// CURRENT run's queue.
func TestOwnerGuard_StaleRunRefused(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	adapter.Owner().ClaimRun(target, "run-B")

	for _, mutate := range []struct {
		name string
		call func() error
	}{
		{"enqueue", func() error {
			_, err := adapter.EnqueueCommand(target, "run-A", newCommand("c1", "t1", "stale"))
			return err
		}},
		{"promote", func() error {
			_, err := adapter.PromoteCommand(target, "run-A", "c1")
			return err
		}},
		{"cancel", func() error {
			_, err := adapter.CancelCommand(target, "run-A", "c1")
			return err
		}},
	} {
		err := mutate.call()
		if got := OwnerErrorCode(err); got != CodeStaleTaskOwnerCommand {
			t.Fatalf("%s: want %s, got %q (err=%v)", mutate.name, CodeStaleTaskOwnerCommand, got, err)
		}
	}
	if got := adapter.Queue().list(target.TaskKey()); len(got) != 0 {
		t.Fatalf("a stale command must not reach the queue, got %d entries", len(got))
	}
	// The current run is untouched.
	if active, _ := adapter.Owner().ActiveInput(target); active != "run-B" {
		t.Fatalf("the current run must keep ownership, got %q", active)
	}
}

// A3b: the SAME run is accepted — the guard is a staleness check, not a lockout.
func TestOwnerGuard_CurrentRunAccepted(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	adapter.Owner().ClaimRun(target, "run-B")

	if _, err := adapter.EnqueueCommand(target, "run-B", newCommand("c1", "t1", "hi")); err != nil {
		t.Fatalf("the current run must be accepted: %v", err)
	}
}

// A4: a task this host never loaded is a refusal with its own code, not an empty
// target. The caller must not fall through and drive a foreign session.
func TestOwnerGuard_UnknownTaskRefused(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)

	err := adapter.SendPrompt(context.Background(), "never-loaded", "", newCommand("c1", "t1", "hi"))
	if got := OwnerErrorCode(err); got != CodeSessionTargetNotFound {
		t.Fatalf("want %s, got %q (err=%v)", CodeSessionTargetNotFound, got, err)
	}
}

// A5: cronAutomationId is sticky. resumeTask / onDynamicTaskEvent carry no attribution,
// so a plain overwrite would strip a cron task's identity on the first resume.
func TestOwnerGuard_CronAutomationIDIsSticky(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)

	adapter.RememberTarget(Target{TaskID: "task-1", WorkspacePath: "/repo", CronAutomationID: "cron-7"})
	// A resume that carries no cronAutomationId must not clear it.
	adapter.RememberTarget(Target{TaskID: "task-1", WorkspacePath: "/repo"})

	stored, err := adapter.TargetOf("task-1")
	if err != nil {
		t.Fatalf("target lookup: %v", err)
	}
	if stored.CronAutomationID != "cron-7" {
		t.Fatalf("cronAutomationId must stay sticky, got %q", stored.CronAutomationID)
	}

	// An EXPLICIT value does replace it.
	adapter.RememberTarget(Target{TaskID: "task-1", WorkspacePath: "/repo", CronAutomationID: "cron-9"})
	stored, _ = adapter.TargetOf("task-1")
	if stored.CronAutomationID != "cron-9" {
		t.Fatalf("an explicit cronAutomationId must win, got %q", stored.CronAutomationID)
	}
}

// A6: an event queued as a microtask must be inert after dispose — it must not touch
// released state.
func TestDispose_QueuedEventsAreInert(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	adapter.Owner().ClaimRun(target, "run-A")
	if _, err := adapter.EnqueueCommand(target, "run-A", newCommand("c1", "t1", "hi")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	adapter.Drain(context.Background(), target)

	adapter.Dispose()
	if !adapter.IsDisposed() {
		t.Fatal("adapter must report disposed")
	}
	// The terminal/ready edges must be no-ops once disposed: no panic, no state.
	adapter.HandleTerminalEvent(target, "turn.completed")
	adapter.HandleReadyEvent(target, "prompt_completed")
	// A drain scheduled after dispose must do nothing.
	adapter.drains.Schedule(target)
	adapter.drains.Drain(context.Background(), target)
	if got := adapter.Queue().list(target.TaskKey()); len(got) != 1 {
		t.Fatalf("post-dispose drains must not touch the queue, got %d", len(got))
	}
}

// A7: dispose is idempotent — the host-exit path and the window-teardown path both reach
// it, and the second call must not repeat the teardown.
func TestDispose_Idempotent(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 1 })
	adapter, _, _ := newTestAdapter(t)
	adapter.leases = leases
	adapter.SetHostID("host-a")
	leases.Acquire("host-a", LeaseTarget{WorkspaceKey: "/repo", TaskID: "task-1", RunID: "run-A"})

	adapter.Dispose()
	adapter.Dispose()

	if _, held := leases.Lookup("/repo", "task-1"); held {
		t.Fatal("B5: dispose must release this host's leases")
	}
}
