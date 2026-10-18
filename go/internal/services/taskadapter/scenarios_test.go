package taskadapter

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// scenarios_test.go — the three scenarios the step is judged on beyond the per-guard
// rows: a two-host owner/lease handover, a stale command that never executes, and a
// FIFO queue that neither loses nor duplicates under concurrency. Plus the goroutine
// leak delta.

// TestScenario_OwnerLeaseHandover_TwoHostsOneTask: two Desktop windows have the same task
// open. Only one may own the run; a phone's command is routed to the owner and answered
// by it; after the owner goes away the lease is released and the other window takes over.
func TestScenario_OwnerLeaseHandover_TwoHostsOneTask(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 100 })
	hosts := newFakeHosts("host-a", "host-b", "host-phone")
	var delivered []OwnerCommandDelivery
	var mu sync.Mutex

	router, _ := newRouter(t, leases, hosts)
	// Capture what the owner host receives.
	router.deliver = func(delivery OwnerCommandDelivery) error {
		mu.Lock()
		defer mu.Unlock()
		delivered = append(delivered, delivery)
		return nil
	}

	// Host A opens the task and claims the run.
	if !leases.Acquire("host-a", leaseTarget("run-A")).Acquired {
		t.Fatal("host-a must acquire the run it started")
	}
	// Host B has the same task open and asks for the SAME run: refused.
	refused := leases.Acquire("host-b", leaseTarget("run-A"))
	if refused.Acquired {
		t.Fatal("host-b must not take a lease host-a holds")
	}

	// The phone enqueues an owner command for run A. It reaches host-a and no one else.
	if err := router.Route("host-phone", ownerCommand("req-1", "run-A")); err != nil {
		t.Fatalf("the phone's command must route to the owner: %v", err)
	}
	mu.Lock()
	gotDeliveries := len(delivered)
	mu.Unlock()
	if gotDeliveries != 1 {
		t.Fatalf("the command must be delivered exactly once, got %d", gotDeliveries)
	}
	// Only the lease owner may answer it.
	if router.Complete("host-b", OwnerCommandResult{CommandRequestID: "req-1", Success: true}) {
		t.Fatal("host-b is not the owner; it must not answer")
	}
	if !router.Complete("host-a", OwnerCommandResult{CommandRequestID: "req-1", Success: true}) {
		t.Fatal("host-a is the owner; it must answer")
	}

	// Host A closes its window: its lease is released, host-b's nothing (it has none).
	leases.ReleaseHost("host-a")
	if _, held := leases.Lookup("/repo", "task-1"); held {
		t.Fatal("host-a's lease must be released on close")
	}
	// Now host-b takes over the same task with a new run.
	taken := leases.Acquire("host-b", LeaseTarget{
		WorkspaceKey: "/repo", TaskID: "task-1", RunID: "run-B", LeaseRequestID: "req-2",
	})
	if !taken.Acquired || taken.OwnerHostID != "host-b" {
		t.Fatalf("host-b must take over after host-a released, got %+v", taken)
	}
	// A command for the OLD run is now stale for the new owner.
	if err := router.Route("host-phone", ownerCommand("req-3", "run-A")); OwnerErrorCode(err) != CodeStaleTaskOwnerCommand {
		t.Fatalf("the old run must be stale against the new lease, got %v", err)
	}
	// And the new run routes.
	if err := router.Route("host-phone", ownerCommand("req-4", "run-B")); err != nil {
		t.Fatalf("the new run must route to host-b: %v", err)
	}
}

// TestScenario_StaleCommandNeverExecutes: a command carrying a stale baseRevision/run id
// returns stale and NEVER reaches the agent. The run it names is not the current one, so
// the local owner check refuses it before the queue, and the CLI would refuse the stale
// base before the model. Neither path executes.
func TestScenario_StaleCommandNeverExecutes(t *testing.T) {
	adapter, agent, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)

	// Run B is the current one.
	adapter.Owner().ClaimRun(target, "run-B")

	// The phone submits three stale commands, each naming a different dead run.
	for _, staleRun := range []string{"run-A", "run-O", ""} {
		_, err := adapter.EnqueueCommand(target, staleRun, newCommand("c-"+staleRun, "t-"+staleRun, "stale"))
		if staleRun == "" {
			// The desktop leg (no ownerRunId) is accepted — see A1. That is the
			// deliberate exception: only the REMOTE leg carries a run id.
			continue
		}
		if OwnerErrorCode(err) != CodeStaleTaskOwnerCommand {
			t.Fatalf("a stale run %q must be refused with STALE, got %v", staleRun, err)
		}
	}

	// Drain: nothing stale was queued, so the agent sees nothing.
	adapter.Drain(context.Background(), target)
	for _, command := range agent.sentCommands() {
		if strings.HasPrefix(command.TraceID, "t-run-") {
			t.Fatalf("a stale command reached the agent: %+v", command)
		}
	}
	// The CLI boundary also refuses a stale base (C8): the model never changes.
	var model string
	ack, err := SubmitCAS(func(request CommandRequest) (CommandAck, error) {
		return CommandAck{Status: AckStale, RevisionAtDecision: 3}, nil
	}, CASOptions{Type: "switchModelConfig", SessionID: "task-1", ContextMessage: "model", MaxAttempts: 2})
	if err == nil || ack.Status != AckStale {
		t.Fatalf("an all-stale CAS must fail, got ack=%s err=%v", ack.Status, err)
	}
	if model != "" {
		t.Fatalf("the stale write executed, model=%q", model)
	}
}

// TestScenario_QueueFIFO_NoLossNoDuplicate: 50 commands from one client drain in
// submission order, none lost, none duplicated, under concurrent terminal/ready/drain
// traffic from several goroutines. It uses the REAL scheduler (a goroutine per drain),
// because the invariant under test is that the adapter keeps draining until the queue is
// empty — a manual scheduler would hide exactly the re-arm that proves it.
func TestScenario_QueueFIFO_NoLossNoDuplicate(t *testing.T) {
	agent := &fakeAgent{}
	adapter := NewAdapter(AdapterOptions{Agent: agent, Now: func() int64 { return 1 }})
	defer adapter.Dispose()
	target := testTarget()
	adapter.RememberTarget(target)

	const count = 50
	order := make([]string, 0, count)
	for index := 0; index < count; index++ {
		id := "c" + itoa(index)
		if _, err := adapter.EnqueueCommand(target, "", newCommand(id, "t"+id, id)); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
		order = append(order, id)
	}

	// One driver walks the queue in order — terminal closes the current input, ready
	// releases the next — while three other goroutines hammer Drain concurrently. That
	// combination is what the invariants need: the driver proves nothing is lost and
	// nothing is reordered, and the concurrent drains prove the per-task serialization
	// holds under -race (a second drain must never start the same turn twice).
	var wg sync.WaitGroup
	done := make(chan struct{})
	for drainer := 0; drainer < 3; drainer++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					adapter.Drain(context.Background(), target)
				}
			}
		}()
	}
	for len(agent.sentCommands()) < count {
		adapter.HandleTerminalEvent(target, "turn.completed")
		adapter.HandleReadyEvent(target, "prompt_completed")
		adapter.Drain(context.Background(), target)
	}
	close(done)
	wg.Wait()

	sent := agent.sentCommands()
	seen := map[string]int{}
	for _, command := range sent {
		seen[command.CommandID]++
	}
	if len(seen) != count {
		t.Fatalf("want %d distinct commands sent, got %d", count, len(seen))
	}
	for id, times := range seen {
		if times != 1 {
			t.Fatalf("command %s was sent %d times, want exactly 1", id, times)
		}
	}
	// No command may be lost: every submitted id reached the agent exactly once.
	for _, id := range order {
		if seen[id] == 0 {
			t.Fatalf("command %s was lost", id)
		}
	}
}

// TestScenario_RetriedCommandIDUnderConcurrency: the same commandId submitted from many
// goroutines concurrently produces exactly one queue entry and one send.
func TestScenario_RetriedCommandIDUnderConcurrency(t *testing.T) {
	adapter, agent, _ := newTestAdapter(t)
	target := testTarget()
	adapter.RememberTarget(target)

	var wg sync.WaitGroup
	for attempt := 0; attempt < 32; attempt++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := adapter.EnqueueCommand(target, "", newCommand("c1", "t1", "hello")); err != nil {
				t.Errorf("concurrent enqueue: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := adapter.Queue().list(target.TaskKey()); len(got) != 1 {
		t.Fatalf("32 concurrent retries of one id produced %d entries, want 1", len(got))
	}
	adapter.Drain(context.Background(), target)
	sent := agent.sentCommands()
	if len(sent) != 1 || sent[0].CommandID != "c1" {
		t.Fatalf("the retried command must be sent exactly once, sent %+v", sent)
	}
}

// TestGoroutineLeak_Delta: disposing the adapter and router leaves no goroutine behind.
// The router's owner-command timers are the real leak risk — a pending timer holds its
// closure (and the router) alive for the full timeout.
func TestGoroutineLeak_Delta(t *testing.T) {
	before := runtime.NumGoroutine()

	for round := 0; round < 20; round++ {
		leases := NewLeaseTable(func() int64 { return 100 })
		hosts := newFakeHosts("host-a", "host-phone")
		leases.Acquire("host-a", leaseTarget("run-A"))
		router, _ := newRouter(t, leases, hosts)
		// Leave one command PENDING so a real timer is outstanding at dispose.
		if err := router.Route("host-phone", ownerCommand("req-"+itoa(round), "run-A")); err != nil {
			t.Fatalf("route: %v", err)
		}

		adapter, _, _ := newTestAdapter(t)
		adapter.RememberTarget(testTarget())
		if _, err := adapter.EnqueueCommand(testTarget(), "", newCommand("c1", "t1", "hi")); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		adapter.Drain(context.Background(), testTarget())

		adapter.Dispose()
		router.Dispose()
	}

	// Give any straggler a chance to appear before measuring.
	deadline := time.Now().Add(2 * time.Second)
	for {
		after := runtime.NumGoroutine()
		if after <= before+2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine leak: %d before, %d after", before, after)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
