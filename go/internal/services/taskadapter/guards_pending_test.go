package taskadapter

import (
	"context"
	"testing"
)

// guards_pending_test.go — rows E1–E8 of phase-3-guards.md: the pending optimistic
// state boundaries.

// E1: a new input must not inherit the previous round's live-only buffers. Left in
// place, the next run's snapshot would append the previous run's tools to the new
// reply's tail, or resume a tool input built from the old turn's bytes.
func TestPending_NewInputClearsLiveProjection(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	pending := adapter.Pending()

	// The previous run left a live tool and a half-streamed input behind.
	pending.Remember(target, "tool-A", "", map[string]any{"name": "Read"})
	pending.AppendStreamingInput(target, "tool-A", "partial")
	if got := pending.Snapshot(target); len(got) != 1 {
		t.Fatalf("fixture must start with one live tool, got %d", len(got))
	}

	// The next run starts: sendPrompt clears before claiming the run.
	if err := adapter.SendPrompt(context.Background(), "task-1", "", newCommand("c1", "run-B", "hi")); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := pending.Snapshot(target); len(got) != 0 {
		t.Fatalf("E1: the live projection must be cleared, got %d", len(got))
	}
	// The streaming cache is cleared too: re-streaming tool-A starts from empty.
	if got := pending.AppendStreamingInput(target, "tool-A", "fresh"); got != "fresh" {
		t.Fatalf("E1: the streaming cache must be cleared, got %q", got)
	}
}

// E2: the snapshot's pendingCommands is the HOST queue. A phone refresh reads the host,
// and a renderer-only pending list would be lost or contradicted on the next snapshot.
func TestPending_SnapshotReadsHostQueue(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	adapter.Queue().Append(target, newCommand("c1", "t1", "one"))

	pending := PendingCommands(adapter.Queue(), target)
	if len(pending) != 1 || pending[0].CommandID != "c1" {
		t.Fatalf("the snapshot must read the host queue, got %+v", pending)
	}

	// A cancel that only cleared a renderer copy would leave this intact — which is
	// exactly the bug the host-side cancel prevents.
	if _, err := adapter.CancelCommand(target, "", "c1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if pending := PendingCommands(adapter.Queue(), target); len(pending) != 0 {
		t.Fatalf("a cancelled command must leave the host queue, got %+v", pending)
	}
	// An empty queue projects as an EMPTY ARRAY, never nil: the phone needs an explicit
	// "cleared" signal on restore.
	if pending := PendingCommands(adapter.Queue(), target); pending == nil || len(pending) != 0 {
		t.Fatalf("an empty queue must project as an empty array, got %#v", pending)
	}
}

// E3: pendingElicitations is included when non-empty, or when the caller is replayable.
// desktop-continuous must not receive an empty set — there it would clear prompts that
// are legitimately in flight.
func TestPending_EmptyElicitationsOnlyForReplayable(t *testing.T) {
	if IncludeEmptyPendingElicitations("desktop-continuous", 0) {
		t.Fatal("E3: desktop-continuous must not receive an empty elicitation set")
	}
	if !IncludeEmptyPendingElicitations("web-remote-replayable", 0) {
		t.Fatal("E3: the replayable path needs an explicit empty set to clear on restore")
	}
	if !IncludeEmptyPendingElicitations("desktop-continuous", 2) {
		t.Fatal("E3: a non-empty set is always included")
	}
}

// E4: every event fires BOTH the task-keyed and the taskId-global emitter. The task view
// and the workspace list subscribe through different keys, so delivering to one and not
// the other loses events for half the UI.
func TestPending_TaskEventFansOutToBothEmitters(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)

	var taskScoped, globalScoped int
	adapter.OnTaskEvent(target, func(TaskEvent) { taskScoped++ })
	adapter.OnGlobalTaskEvent(target.TaskID, func(TaskEvent) { globalScoped++ })

	adapter.EmitTaskEvent(target, TaskEvent{Type: "task_snapshot_updated"})

	if taskScoped != 1 || globalScoped != 1 {
		t.Fatalf("E4: both emitters must fire once, got task=%d global=%d", taskScoped, globalScoped)
	}

	// A different taskId gets the global event on ITS OWN global stream, and reaches
	// only its own scoped emitter — never the other task's.
	other := Target{TaskID: "task-2", WorkspacePath: "/repo"}
	var otherGlobal int
	adapter.OnTaskEvent(other, func(TaskEvent) { taskScoped++ })
	adapter.OnGlobalTaskEvent(other.TaskID, func(TaskEvent) { otherGlobal++ })

	adapter.EmitTaskEvent(other, TaskEvent{Type: "task_snapshot_updated"})
	if taskScoped != 2 {
		t.Fatalf("E4: the second task's scoped emitter must fire, got %d", taskScoped)
	}
	if otherGlobal != 1 {
		t.Fatalf("E4: the second task's global emitter must fire, got %d", otherGlobal)
	}
	if globalScoped != 1 {
		t.Fatalf("E4: task-1's global emitter must NOT see task-2's event, got %d", globalScoped)
	}
}

// E5: the live projection table is bounded per task budget. Unbounded growth on a
// long-lived host is a memory leak.
func TestPending_LiveProjectionIsBounded(t *testing.T) {
	pending := newPendingState()
	for index := 0; index < MaxLiveToolProjectionTasks+10; index++ {
		pending.Remember(Target{TaskID: "t", WorkspacePath: "/repo"}, "tool", "", nil)
	}
	// One task, so the TASK budget is not what bounds it; assert the table exists.
	if got := pending.Diagnostics()["liveProjectionTasks"]; got != 1 {
		t.Fatalf("want 1 task entry, got %d", got)
	}

	// Distinct tasks, each with one tool: the TASK budget must evict the oldest.
	for index := 0; index < MaxLiveToolProjectionTasks+10; index++ {
		pending.Remember(Target{TaskID: "t", WorkspacePath: "/repo/" + itoa(index)}, "tool", "", nil)
	}
	if got := pending.Diagnostics()["liveProjectionTasks"]; got > MaxLiveToolProjectionTasks {
		t.Fatalf("E5: the task budget must bound the table, got %d", got)
	}
}

// E6: per task, the lowest-order tool is evicted first — a live projection must never
// drop the tool currently streaming.
func TestPending_PerTaskTrimKeepsNewest(t *testing.T) {
	pending := newPendingState()
	target := testTarget()
	for index := 0; index < MaxLiveToolProjectionToolsPerTask+5; index++ {
		pending.Remember(target, "tool-"+itoa(index), "", nil)
	}
	snapshot := pending.Snapshot(target)
	if len(snapshot) != MaxLiveToolProjectionToolsPerTask {
		t.Fatalf("E6: want the per-task budget, got %d", len(snapshot))
	}
	if snapshot[0].ToolID != "tool-5" {
		t.Fatalf("E6: the five oldest tools must be evicted, oldest left is %s", snapshot[0].ToolID)
	}
	newest := snapshot[len(snapshot)-1].ToolID
	if newest != "tool-"+itoa(MaxLiveToolProjectionToolsPerTask+4) {
		t.Fatalf("E6: the newest tool must survive, got %s", newest)
	}
}

// E7: a cancel that actually removed something emits a snapshot update, because other
// observers of the task must see the removal.
func TestPending_CancelEmitsSnapshotUpdate(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	adapter.Queue().Append(target, newCommand("c1", "t1", "one"))

	var events []TaskEvent
	adapter.OnTaskEvent(target, func(event TaskEvent) { events = append(events, event) })

	if _, err := adapter.CancelCommand(target, "", "c1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if len(events) != 1 || events[0].Type != "task_snapshot_updated" {
		t.Fatalf("E7: a real cancel must emit exactly one snapshot update, got %+v", events)
	}
	if events[0].Reason != "task_status_changed" {
		t.Fatalf("the emission must declare its change category, got %q", events[0].Reason)
	}
}

// E8: a cancel that changed nothing emits nothing. An already_running or not_found cancel
// would make a snapshot update a lie the UI has to reconcile away.
func TestPending_CancelRunningNoSnapshotUpdate(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		commandID string
		prepare   func(adapter *Adapter, target Target)
		// canceled is the outcome the caller sees. A not_found cancel is a SUCCESS
		// (D10: cancel is idempotent) but still emits nothing (E8) — "the command is
		// gone" is what the phone already knew.
		canceled bool
	}{
		{
			name:      "already_running",
			commandID: "c1",
			prepare: func(adapter *Adapter, target Target) {
				adapter.Queue().Append(target, newCommand("c1", "t1", "one"))
				adapter.Queue().TakeDrainable(target, false, 1)
			},
			canceled: false,
		},
		{name: "not_found", commandID: "ghost", canceled: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			adapter, _, _ := newTestAdapter(t)
			target := testTarget()
			adapter.RememberTarget(target)
			if scenario.prepare != nil {
				scenario.prepare(adapter, target)
			}

			var events []TaskEvent
			adapter.OnTaskEvent(target, func(event TaskEvent) { events = append(events, event) })

			outcome, err := adapter.CancelCommand(target, "", scenario.commandID)
			if err != nil {
				t.Fatalf("cancel: %v", err)
			}
			if outcome.Canceled != scenario.canceled {
				t.Fatalf("want canceled=%v, got %+v", scenario.canceled, outcome)
			}
			if len(events) != 0 {
				t.Fatalf("E8: a cancel that removed nothing must emit nothing, emitted %d", len(events))
			}
		})
	}
}

// itoa avoids pulling strconv into the test file for two call sites.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// TestMemoryDiagnostics: the per-table sizes a host reports. Diagnostic only — it must
// not change a decision, and it must read every table the TS provider reports.
func TestMemoryDiagnostics(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 1 })
	leases.Acquire("host-a", leaseTarget("run-A"))
	adapter, _, _ := newTestAdapter(t)
	adapter.leases = leases
	target := testTarget()
	adapter.RememberTarget(target)
	adapter.Queue().Append(target, newCommand("c1", "t1", "one"))
	adapter.Pending().Remember(target, "tool-A", "", nil)

	stats := adapter.MemoryDiagnostics()
	for key, want := range map[string]int{
		"runtimeCommands":     1,
		"leases":              1,
		"liveProjectionTasks": 1,
	} {
		if got := stats[key]; got != want {
			t.Fatalf("%s: want %d, got %d", key, want, got)
		}
	}
}
