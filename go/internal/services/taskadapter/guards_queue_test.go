package taskadapter

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// guards_queue_test.go — rows D1–D23 of phase-3-guards.md: the host command queue and
// the drain.

// D1: a task with an active prompt input does not drain. Sending mid-turn would
// interleave two turns on one session.
func TestDrain_ActiveInputBlocksDrain(t *testing.T) {
	adapter, agent, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	adapter.Owner().ClaimRun(target, "run-A")
	if _, err := adapter.EnqueueCommand(target, "run-A", newCommand("c1", "t1", "hi")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	adapter.Drain(context.Background(), target)
	if got := agent.sentCommands(); len(got) != 0 {
		t.Fatalf("D1: an active run must block the drain, sent %d", len(got))
	}
	// The command is still queued, not lost.
	if got := adapter.Queue().list(target.TaskKey()); len(got) != 1 {
		t.Fatalf("the blocked command must stay queued, got %d", len(got))
	}
}

// D2: a command already `running` blocks a second drain — one turn at a time.
func TestDrain_RunningCommandBlocksDrain(t *testing.T) {
	adapter, agent, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	// The first command is already in flight.
	adapter.Queue().Append(target, newCommand("c1", "t1", "one"))
	if _, refused := adapter.Queue().TakeDrainable(target, false, 1); refused != "" {
		t.Fatalf("fixture must start with one drainable command, refused: %s", refused)
	}
	if _, err := adapter.EnqueueCommand(target, "", newCommand("c2", "t2", "two")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	adapter.Drain(context.Background(), target)
	if got := agent.sentCommands(); len(got) != 0 {
		t.Fatalf("D2: a running command must block the next send, sent %d", len(got))
	}
}

// D3: nothing drainable is a no-op, not an error and not a spin.
func TestDrain_NoAcceptedCommandIsNoop(t *testing.T) {
	adapter, agent, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)

	// Only a failed command: nothing accepted, so nothing to send.
	failed := newCommand("c1", "t1", "hi")
	adapter.Queue().Append(target, failed)
	adapter.Queue().MarkFailed(target, "c1", 1, "boom")

	adapter.Drain(context.Background(), target)
	if got := agent.sentCommands(); len(got) != 0 {
		t.Fatalf("D3: a failed command must not drain, sent %d", len(got))
	}
	if got := adapter.Queue().list(target.TaskKey()); len(got) != 1 {
		t.Fatalf("the failed command must stay in the queue for the phone, got %d", len(got))
	}
}

// D4: drains serialize per task. A command enqueued DURING a send must still be sent —
// by the same drain pass, after the first one settles — and never concurrently.
func TestDrain_SerializesPerTask(t *testing.T) {
	adapter, agent, scheduler := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)

	var inFlight int
	var maxConcurrent int
	var mu sync.Mutex
	var enqueuedSecond bool
	agent.onSend = func(Command) {
		mu.Lock()
		inFlight++
		if inFlight > maxConcurrent {
			maxConcurrent = inFlight
		}
		// A second input arrives while the FIRST is sending — once only, so the loop
		// terminates after two sends.
		if !enqueuedSecond {
			enqueuedSecond = true
			if _, err := adapter.EnqueueCommand(target, "", newCommand("c2", "t2", "second")); err != nil {
				t.Errorf("mid-send enqueue: %v", err)
			}
		}
		mu.Unlock()
		// The send releases the active run at its terminal, which is what lets the
		// next command drain.
		adapter.HandleTerminalEvent(target, "turn.completed")
		mu.Lock()
		inFlight--
		mu.Unlock()
	}
	if _, err := adapter.EnqueueCommand(target, "", newCommand("c1", "t1", "first")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if scheduler.count() != 1 {
		t.Fatalf("want 1 scheduled drain, got %d", scheduler.count())
	}

	scheduler.runAll()

	if maxConcurrent != 1 {
		t.Fatalf("D4: drains must not overlap, saw %d concurrent", maxConcurrent)
	}
	sent := agent.sentCommands()
	if len(sent) != 2 {
		t.Fatalf("D4: the mid-send command must still be sent, sent %d", len(sent))
	}
	if sent[0].CommandID != "c1" || sent[1].CommandID != "c2" {
		t.Fatalf("FIFO broken: sent %s then %s", sent[0].CommandID, sent[1].CommandID)
	}
}

// D5: a failed send marks the command failed and keeps it in the queue. It must not
// vanish (the phone's pending list shows it) and must not retry forever.
func TestDrain_FailureMarksCommandFailed(t *testing.T) {
	adapter, agent, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	agent.failWith = errors.New("agent refused")

	if _, err := adapter.EnqueueCommand(target, "", newCommand("c1", "t1", "hi")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	adapter.Drain(context.Background(), target)

	queued := adapter.Queue().list(target.TaskKey())
	if len(queued) != 1 {
		t.Fatalf("the failed command must stay queued, got %d", len(queued))
	}
	if queued[0].Status != CommandFailed {
		t.Fatalf("want status %s, got %s", CommandFailed, queued[0].Status)
	}
	if queued[0].Error != "agent refused" {
		t.Fatalf("the failure reason must be recorded, got %q", queued[0].Error)
	}
}

// D6: MarkRunning flips only the matching command. Two queued commands, one drains.
func TestQueue_MarkRunningIsPerCommand(t *testing.T) {
	q := newQueue()
	target := testTarget()
	q.Append(target, newCommand("c1", "t1", "one"))
	q.Append(target, newCommand("c2", "t2", "two"))

	if _, ok := q.MarkRunning(target, "c1", 5); !ok {
		t.Fatal("MarkRunning must find c1")
	}
	commands := q.list(target.TaskKey())
	if commands[0].Status != CommandRunning || commands[1].Status != CommandAccepted {
		t.Fatalf("only c1 may flip: %s / %s", commands[0].Status, commands[1].Status)
	}
}

// D7: promote moves the command to the HEAD and re-arms it, keeping its id.
func TestQueue_PromoteMovesToHead(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	adapter.Queue().Append(target, newCommand("c1", "t1", "one"))
	adapter.Queue().Append(target, newCommand("c2", "t2", "two"))

	promoted, err := adapter.PromoteCommand(target, "", "c2")
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if promoted.Status != CommandAccepted {
		t.Fatalf("a promoted command must be re-armed, got %s", promoted.Status)
	}
	commands := adapter.Queue().list(target.TaskKey())
	if len(commands) != 2 || commands[0].CommandID != "c2" || commands[1].CommandID != "c1" {
		t.Fatalf("promote must move c2 to the head, got %s", commands[0].CommandID)
	}
}

// D8: promoting a `running` command is a no-op success. Resetting it would let a retried
// promote start the same input twice.
func TestQueue_PromoteRunningIsNoop(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	adapter.Queue().Append(target, newCommand("c1", "t1", "one"))
	adapter.Queue().TakeDrainable(target, false, 1) // -> running

	promoted, err := adapter.PromoteCommand(target, "", "c1")
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if promoted.Status != CommandRunning {
		t.Fatalf("D8: a running command must stay running, got %s", promoted.Status)
	}
	commands := adapter.Queue().list(target.TaskKey())
	if len(commands) != 1 {
		t.Fatalf("promote must not duplicate the command, got %d", len(commands))
	}
}

// D9: promoting an unknown commandId is OWNER_COMMAND_FAILED.
func TestQueue_PromoteUnknownThrows(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)

	_, err := adapter.PromoteCommand(target, "", "nope")
	if got := OwnerErrorCode(err); got != CodeOwnerCommandFailed {
		t.Fatalf("want %s, got %q (err=%v)", CodeOwnerCommandFailed, got, err)
	}
}

// D10: cancelling an unknown commandId is an idempotent success with reason not_found.
func TestQueue_CancelUnknownIsNotFound(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)

	outcome, err := adapter.CancelCommand(target, "", "nope")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if !outcome.Canceled || outcome.Reason != "not_found" {
		t.Fatalf("D10: want canceled/not_found, got %+v", outcome)
	}
}

// D11: cancelling a `running` command is refused — it must not be pulled back out of the
// send path.
func TestQueue_CancelRunningRefused(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	adapter.Queue().Append(target, newCommand("c1", "t1", "one"))
	adapter.Queue().TakeDrainable(target, false, 1) // -> running

	outcome, err := adapter.CancelCommand(target, "", "c1")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if outcome.Canceled || outcome.Reason != "already_running" || outcome.Status != CommandRunning {
		t.Fatalf("D11: want canceled=false/already_running/running, got %+v", outcome)
	}
	if got := adapter.Queue().list(target.TaskKey()); len(got) != 1 {
		t.Fatalf("a refused cancel must keep the command, got %d", len(got))
	}
}

// D12: a successful cancel deletes from the HOST queue. A renderer-only delete would be
// undone by the next snapshot.
func TestQueue_CancelRemovesFromHostQueue(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	adapter.Queue().Append(target, newCommand("c1", "t1", "one"))
	adapter.Queue().Append(target, newCommand("c2", "t2", "two"))

	outcome, err := adapter.CancelCommand(target, "", "c1")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if !outcome.Canceled {
		t.Fatalf("cancel must succeed, got %+v", outcome)
	}
	commands := adapter.Queue().list(target.TaskKey())
	if len(commands) != 1 || commands[0].CommandID != "c2" {
		t.Fatalf("the host queue must lose c1, got %+v", commands)
	}
}

// D13: enqueue appends — FIFO within a client.
func TestQueue_FIFOWithinClient(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)

	for _, id := range []string{"c1", "c2", "c3"} {
		if _, err := adapter.EnqueueCommand(target, "", newCommand(id, "t-"+id, id)); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	commands := adapter.Queue().list(target.TaskKey())
	want := []string{"c1", "c2", "c3"}
	if len(commands) != 3 {
		t.Fatalf("want 3 commands, got %d", len(commands))
	}
	for index, expected := range want {
		if commands[index].CommandID != expected {
			t.Fatalf("position %d holds %s, want %s", index, commands[index].CommandID, expected)
		}
	}
}

// D14: a retried commandId does not duplicate. The phone retries after a timeout; the
// retry must not become a second queue entry that drains as a second turn.
func TestQueue_RetriedCommandIDIsIdempotent(t *testing.T) {
	adapter, agent, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)

	if _, err := adapter.EnqueueCommand(target, "", newCommand("c1", "t1", "hello")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := adapter.EnqueueCommand(target, "", newCommand("c2", "t2", "second")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// The phone retries c1.
	if _, err := adapter.EnqueueCommand(target, "", newCommand("c1", "t1", "hello")); err != nil {
		t.Fatalf("retry: %v", err)
	}

	commands := adapter.Queue().list(target.TaskKey())
	if len(commands) != 2 {
		t.Fatalf("D14: a retried commandId must not duplicate, got %d entries: %+v", len(commands), commands)
	}
	if commands[0].CommandID != "c1" || commands[1].CommandID != "c2" {
		t.Fatalf("the retry must keep c1's FIFO position, got %s then %s", commands[0].CommandID, commands[1].CommandID)
	}
	// And exactly one c1 turn reaches the agent.
	adapter.Drain(context.Background(), target)
	sent := agent.sentCommands()
	count := 0
	for _, command := range sent {
		if command.CommandID == "c1" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("D14: c1 must be sent exactly once, sent %d", count)
	}
}

// D15: a terminal event only closes the command whose traceId matches AND that is
// `running`.
func TestQueue_TerminalOnlyCompletesMatchingInput(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	adapter.Queue().Append(target, newCommand("c1", "t1", "one"))
	adapter.Queue().Append(target, newCommand("c2", "t2", "two"))
	adapter.Queue().TakeDrainable(target, false, 1) // c1 -> running

	// The queue's own completion rule: only a `running` command whose traceId equals the
	// terminal input may be closed.
	if _, closed := adapter.Queue().CompleteByInputID(target, "t2"); closed {
		t.Fatal("D15: a terminal naming the wrong input must not close a command")
	}
	if got := adapter.Queue().list(target.TaskKey()); len(got) != 2 {
		t.Fatalf("D15: the queue must be untouched, got %d", len(got))
	}
	if _, closed := adapter.Queue().CompleteByInputID(target, "t1"); !closed {
		t.Fatal("D15: the matching terminal input must close the running command")
	}
	commands := adapter.Queue().list(target.TaskKey())
	if len(commands) != 1 || commands[0].CommandID != "c2" {
		t.Fatalf("only c1 may be closed, got %+v", commands)
	}
	// A still-`accepted` entry is never closed by a terminal: only the running one was.
	if _, closed := adapter.Queue().CompleteByInputID(target, "t1"); closed {
		t.Fatal("D15: an accepted command must not be closed by a repeated terminal")
	}
}

// D16: a terminal with no recorded input id is a no-op. The v4 sessions-index terminal
// event carries no inputId, so only the LOCAL record may close the queue.
func TestQueue_NoTerminalInputIsNoop(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	adapter.Queue().Append(target, newCommand("c1", "t1", "one"))
	adapter.Queue().TakeDrainable(target, false, 1)

	if _, closed := adapter.Queue().CompleteByInputID(target, ""); closed {
		t.Fatal("D16: an empty terminal input id must not close anything")
	}
	if got := adapter.Queue().list(target.TaskKey()); len(got) != 1 {
		t.Fatalf("the command must stay queued, got %d", len(got))
	}
}

// D17: a terminal event for a task this host does not run must not CREATE state.
func TestTerminal_NoLocalStateIsNoop(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)

	adapter.HandleTerminalEvent(target, "turn.completed")
	if adapter.Owner().HasActiveRun(target) {
		t.Fatal("D17: a terminal must not create an owner run")
	}
	if adapter.Queue().present(target.TaskKey()) {
		t.Fatal("D17: a terminal must not create a queue entry")
	}
}

// D18: the same for the ready boundary.
func TestReady_NoLocalStateIsNoop(t *testing.T) {
	adapter, _, scheduler := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)

	adapter.HandleReadyEvent(target, "prompt_completed")
	if adapter.Queue().present(target.TaskKey()) {
		t.Fatal("D18: ready must not create a queue entry")
	}
	if scheduler.count() != 0 {
		t.Fatalf("D18: ready with no local state must not schedule a drain, got %d", scheduler.count())
	}
}

// D19: the ready boundary schedules the next drain. It is the only signal that licenses
// the next send — a fixed delay would either stall the queue or push a second input into
// a still-locked turn.
func TestReady_SchedulesNextDrain(t *testing.T) {
	adapter, agent, scheduler := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)

	// A command is queued and a run is active (the turn is finishing).
	adapter.Queue().Append(target, newCommand("c1", "t2", "queued"))
	adapter.Owner().ClaimRun(target, "t1")

	adapter.HandleReadyEvent(target, "prompt_completed")
	if scheduler.count() != 1 {
		t.Fatalf("D19: the ready boundary must schedule a drain, got %d", scheduler.count())
	}
	scheduler.runAll()
	if got := agent.sentCommands(); len(got) != 1 {
		t.Fatalf("the queued command must be sent after ready, sent %d", len(got))
	}
}

// D20: a turn terminal does NOT drain. A turn terminal is not the agent's active lock
// being released; sending here would race the CLI's lock.
func TestTerminal_DoesNotDrain(t *testing.T) {
	adapter, agent, scheduler := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)

	adapter.Queue().Append(target, newCommand("c1", "t2", "queued"))
	adapter.Owner().ClaimRun(target, "t1")

	adapter.HandleTerminalEvent(target, "turn.completed")
	if scheduler.count() != 0 {
		t.Fatalf("D20: a terminal must not schedule a drain, got %d", scheduler.count())
	}
	if got := agent.sentCommands(); len(got) != 0 {
		t.Fatalf("D20: nothing may be sent on a terminal, sent %d", len(got))
	}
	// The queued command survives the terminal.
	if got := adapter.Queue().list(target.TaskKey()); len(got) != 1 {
		t.Fatalf("the queued command must survive the terminal, got %d", len(got))
	}
	// And the ready boundary then sends it.
	adapter.HandleReadyEvent(target, "prompt_completed")
	scheduler.runAll()
	if got := agent.sentCommands(); len(got) != 1 {
		t.Fatalf("the ready boundary must send it, sent %d", len(got))
	}
}

// D21: turn.started's payload.inputId is the protocol fact that claims ownership for a
// subscription the adapter never sent a prompt on.
func TestOwnership_TurnStartedClaimsInput(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)

	adapter.HandleTurnStarted(target, "input-42")
	active, ok := adapter.Owner().ActiveInput(target)
	if !ok || active != "input-42" {
		t.Fatalf("D21: turn.started must claim the run, got %q (ok=%v)", active, ok)
	}
	// An empty inputId establishes nothing.
	adapter.HandleTurnStarted(target, "")
	if active, _ := adapter.Owner().ActiveInput(target); active != "input-42" {
		t.Fatalf("an empty inputId must not disturb the claim, got %q", active)
	}
}

// D22: turn.completed / turn.failed release the claim. Otherwise the task stays "owned"
// forever and refuses every later phone command.
func TestOwnership_TurnTerminalReleasesInput(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	adapter.HandleTurnStarted(target, "input-42")

	adapter.HandleTurnTerminal(target)
	if adapter.Owner().HasActiveRun(target) {
		t.Fatal("D22: the turn terminal must release the run")
	}
}

// D23: a failed send releases the claim. A phantom owner run would refuse every later
// phone command for the task until the process restarted.
func TestOwnership_SendFailureReleasesInput(t *testing.T) {
	adapter, agent, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)
	agent.failWith = errors.New("transport died")

	adapter.Owner().ClaimRun(target, "run-A")
	if err := adapter.SendPrompt(context.Background(), "task-1", "", newCommand("c1", "run-A", "hi")); err == nil {
		t.Fatal("the send must propagate its failure")
	}
	if adapter.Owner().HasActiveRun(target) {
		t.Fatal("D23: a failed send must release the claim")
	}
}
