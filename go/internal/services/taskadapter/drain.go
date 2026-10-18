package taskadapter

import (
	"context"
	"sync"
)

// drain.go — the per-task drain scheduler (ports scheduleRuntimeCommandDrain, guard
// D4, and the send/fail edges D5, D20, D19).
//
// The TS serialises drains by chaining promises per task key: a second schedule while
// one is in flight chains another drain AFTER the first settles. That is not an
// optimisation — it is the rule that keeps one turn at a time per task while still
// guaranteeing that a command enqueued during a send is drained afterwards rather than
// dropped.
//
// Go's goroutines would race if the chain were dropped, so the same invariant is
// implemented with a per-task mutex plus a "dirty" flag instead of a promise chain:
// a drain runs alone, and re-arms itself once for whatever arrived while it ran.

// SendFunc is the sendPrompt the drain invokes. The TS calls sendPromptToAgent, which
// claims the active run, sends through the agent service and clears the claim on
// failure; the adapter owns that here, the bridge owns the agent service.
type SendFunc func(ctx context.Context, target Target, command Command) error

// drainer owns the per-task serialization and the send/fail edges.
type drainer struct {
	queue *queue
	owner *ownerState

	mu sync.Mutex
	// states is taskKey → the per-task drain state.
	states map[string]*drainState
	// send performs the actual prompt submission.
	send SendFunc
	// schedule defers a drain off the caller's stack. The TS chains a promise, so a
	// drain requested during a send runs after that send settles; the default runs it
	// on its own goroutine and tests replace it with a manual trigger.
	schedule func(fn func())
	// now is the clock, injected for the command timestamps.
	now func() int64
	// onDrainRefused records WHY a drain did nothing. Purely diagnostic; it never
	// changes the decision.
	onDrainRefused func(target Target, reason string)
	// disposed stops new drains; set by Dispose.
	disposed bool
}

// drainState is one task's drain state.
type drainState struct {
	// running is held for a whole drain pass. A second drain for the same task waits
	// on it instead of running concurrently.
	running sync.Mutex
	// dirty means "there is work to look at". It is set by Schedule and cleared by the
	// drain before it decides, so a refusal does not spin and a schedule that arrives
	// after the decision re-arms it.
	dirtyMu sync.Mutex
	dirty   bool
}

func newDrainer(q *queue, o *ownerState, send SendFunc, schedule func(fn func()), now func() int64) *drainer {
	if schedule == nil {
		schedule = func(fn func()) { go fn() }
	}
	if now == nil {
		now = func() int64 { return 0 }
	}
	return &drainer{
		queue:    q,
		owner:    o,
		states:   map[string]*drainState{},
		send:     send,
		schedule: schedule,
		now:      now,
	}
}

func (d *drainer) state(key string) *drainState {
	state, ok := d.states[key]
	if !ok {
		state = &drainState{}
		d.states[key] = state
	}
	return state
}

// Schedule ports scheduleRuntimeCommandDrain.
func (d *drainer) Schedule(target Target) {
	key := target.TaskKey()
	d.mu.Lock()
	if d.disposed {
		d.mu.Unlock()
		return
	}
	state := d.state(key)
	state.dirtyMu.Lock()
	state.dirty = true
	state.dirtyMu.Unlock()
	d.mu.Unlock()
	d.schedule(func() { d.Drain(context.Background(), target) })
}

// Drain ports drainRuntimeCommands. It repeats while work keeps arriving, so a command
// enqueued mid-send is picked up by the same pass (D4) instead of needing a second
// schedule — and it stops the moment there is nothing drainable.
func (d *drainer) Drain(ctx context.Context, target Target) {
	key := target.TaskKey()
	d.mu.Lock()
	if d.disposed {
		d.mu.Unlock()
		return
	}
	state := d.state(key)
	d.mu.Unlock()

	// Held for the whole loop, so a concurrent Drain for the same task waits here.
	state.running.Lock()
	defer state.running.Unlock()

	for {
		state.dirtyMu.Lock()
		pending := state.dirty
		state.dirty = false
		state.dirtyMu.Unlock()
		if !pending {
			return
		}

		command, refused := d.takeDrainable(target)
		if refused != "" {
			d.mu.Lock()
			observe := d.onDrainRefused
			d.mu.Unlock()
			if observe != nil {
				observe(target, refused)
			}
			return
		}
		if err := d.send(ctx, target, command); err != nil {
			// D5: the failure lands ON the entry. It must not disappear (the phone's
			// pending list shows it) and must not retry forever (only `accepted` drains).
			d.queue.MarkFailed(target, command.CommandID, d.now(), err.Error())
			return
		}
	}
}

// takeDrainable reads the two blocking guards plus the atomic flip. The active-run check
// comes from the owner's table rather than a cached boolean, because the TS consults
// activePromptInputIds at drain time (D1).
func (d *drainer) takeDrainable(target Target) (Command, string) {
	_, hasActiveRun := d.owner.ActiveInput(target)
	return d.queue.TakeDrainable(target, hasActiveRun, d.now())
}

// Dispose stops new drains and forgets the per-task states, so a disposed adapter
// leaves nothing a later Schedule could revive.
func (d *drainer) Dispose() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.disposed = true
	d.states = map[string]*drainState{}
}
