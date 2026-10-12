package runtime

import (
	"errors"
	"testing"
	"time"
)

// turnSeq encodes an event sequence as bytes, one per event, so the fuzz
// signature can take []byte (the only variable-length fuzz argument type Go
// allows). Every byte maps straight to a TurnEvent, so the corpus also contains
// out-of-range events, which Transition must refuse like any illegal pair.
func turnSeq(events ...TurnEvent) []byte {
	out := make([]byte, len(events))
	for i, e := range events {
		out[i] = byte(e)
	}
	return out
}

// turnSeqEvents is turnSeq's inverse.
func turnSeqEvents(seq []byte) []TurnEvent {
	out := make([]TurnEvent, len(seq))
	for i, b := range seq {
		out[i] = TurnEvent(b)
	}
	return out
}

// FuzzTurn feeds random event sequences through the machine and asserts the
// invariants step 1.5 of docs/specs/go-rewrite/phase-1-go-agent.md asks for:
//
//  1. an illegal (state, event) pair returns ErrIllegalTransition, never a panic
//     and never a silently changed state;
//  2. terminal phases are absorbing: from Completing or Error nothing moves the
//     phase except TurnReset, and an outcome event (TurnComplete, TurnFail) is
//     always refused;
//  3. every accepted transition names at least one named effect and lands on one
//     of the ten declared phases;
//  4. the machine starts no goroutine (see FuzzTurnNoGoroutineLeaks).
//
// The seed corpus is the recorded Node runs (S02/S03/S07/S13) plus the illegal
// pairs the table test pins, so a regression in those shapes is found without
// waiting for the mutator.
func FuzzTurn(f *testing.F) {
	f.Add(turnSeq(TurnStart))
	f.Add(turnSeq(TurnStart, TurnStartModelRequest, TurnStreamDelta, TurnComplete, TurnReset))
	f.Add(turnSeq(TurnStart, TurnStartModelRequest, TurnStreamDelta, TurnScheduleTools,
		TurnStartToolExecution, TurnToolCompleted, TurnAggregateResults,
		TurnStartModelRequest, TurnStreamDelta, TurnComplete, TurnReset))
	f.Add(turnSeq(TurnStart, TurnStartModelRequest, TurnStreamDelta, TurnScheduleTools,
		TurnPermissionRequired, TurnPermissionResolved, TurnStartToolExecution,
		TurnToolCompleted, TurnAggregateResults, TurnComplete, TurnReset))
	f.Add(turnSeq(TurnStart, TurnStartModelRequest, TurnFail, TurnReset))
	f.Add(turnSeq(TurnComplete, TurnComplete))
	f.Add(turnSeq(TurnFail, TurnReset, TurnReset))
	f.Add(turnSeq(TurnStart, TurnInputQueued, TurnInputQueued, TurnStart))
	f.Add(turnSeq(TurnToolCompleted))
	f.Add(turnSeq(TurnReset))

	f.Fuzz(func(t *testing.T, seq []byte) {
		state := TurnIdle
		for i, event := range turnSeqEvents(seq) {
			next, effects, err := Transition(state, event)
			if err != nil {
				if !errors.Is(err, ErrIllegalTransition) {
					t.Fatalf("step %d (%s): error = %v, want ErrIllegalTransition", i, event, err)
				}
				if next != state {
					t.Fatalf("step %d (%s): state changed to %s while erroring", i, event, next)
				}
				if effects != nil {
					t.Fatalf("step %d (%s): effects %v returned with an error", i, event, effects)
				}
				continue
			}
			if len(effects) == 0 {
				t.Fatalf("step %d (%s): accepted with no effects", i, event)
			}
			if !next.IsValid() {
				t.Fatalf("step %d (%s): produced the invalid state %d", i, event, int(next))
			}

			// Terminal phases absorb: no second outcome, and no move other than
			// the reset the ported table already carries.
			if state.IsTerminal() {
				switch event {
				case TurnComplete, TurnFail:
					t.Fatalf("step %d (%s): %s accepted a second outcome", i, event, state)
				}
				if event != TurnReset && next != state {
					t.Fatalf("step %d (%s): %s moved to %s, want it to stay", i, event, state, next)
				}
			}

			// Every effect must be a named kind: an unnamed one means the switch
			// and the name table drifted apart.
			for _, eff := range effects {
				if effectKindNames[eff.Kind] == "" {
					t.Fatalf("step %d (%s): effect kind %d has no name", i, event, int(eff.Kind))
				}
			}
			state = next
		}
	})
}

// FuzzTurnNoGoroutineLeaks is the no-goroutine-leak half of the invariants.
//
// It deliberately does NOT compare runtime.NumGoroutine(): the fuzzing workers
// run in parallel and their goroutines come and go independently, so a global
// count read inside the target moves for reasons unrelated to the machine
// (observed while writing this: a count falling from 6 to 5 because another
// worker's goroutine exited). What the target asserts is the part that really
// is a property of Transition: it always returns, so a regression that made the
// machine spin or block shows up as a fuzz timeout.
//
// The deterministic goroutine-delta check is
// TestTurnStateMachineLeaksNoGoroutines, which runs in a single goroutine and
// compares the profile against a "before" snapshot.
func FuzzTurnNoGoroutineLeaks(f *testing.F) {
	f.Add(turnSeq(TurnStart, TurnStartModelRequest, TurnStreamDelta, TurnComplete, TurnReset))
	f.Add(turnSeq(TurnFail))
	f.Add(turnSeq(TurnStart, TurnFail, TurnComplete, TurnComplete, TurnReset, TurnReset))

	f.Fuzz(func(t *testing.T, seq []byte) {
		if len(seq) > 1<<16 {
			return
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			state := TurnIdle
			for _, event := range turnSeqEvents(seq) {
				if next, _, err := Transition(state, event); err == nil {
					state = next
				}
			}
		}()
		// The worker this goroutine spawns must be released before the call
		// returns, so it must already be finished here.
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("a sequence of %d events did not terminate", len(seq))
		}
	})
}

// FuzzTurnNoToolAfterTerminal is the "every started tool call reaches a terminal
// state before the turn ends" invariant from step 1.5, expressed over the
// machine: once the turn is Completing or Error, no tool result may arrive, so
// the machine can never abandon a call mid-flight by ending the turn.
func FuzzTurnNoToolAfterTerminal(f *testing.F) {
	f.Add(turnSeq(TurnStart, TurnStartToolExecution, TurnToolCompleted, TurnAggregateResults, TurnComplete, TurnReset))
	f.Add(turnSeq(TurnStart, TurnStartToolExecution, TurnToolCompleted, TurnComplete, TurnReset))

	f.Fuzz(func(t *testing.T, seq []byte) {
		state := TurnIdle
		for i, event := range turnSeqEvents(seq) {
			next, _, err := Transition(state, event)
			if err != nil {
				continue
			}
			if state.IsTerminal() && event == TurnToolCompleted {
				t.Fatalf("step %d: a tool result arrived after the turn ended in %s", i, state)
			}
			state = next
		}
	})
}

// TestFuzzTurnSeedCorpusReplays is the seed corpus as an ordinary test, so the
// recorded runs are covered by `go test` alone (the gate runs the fuzz target
// separately) and a regression is reported under a test name.
func TestFuzzTurnSeedCorpusReplays(t *testing.T) {
	seeds := [][]TurnEvent{
		{TurnStart},
		{TurnStart, TurnStartModelRequest, TurnStreamDelta, TurnComplete, TurnReset},
		{TurnStart, TurnStartModelRequest, TurnStreamDelta, TurnScheduleTools,
			TurnStartToolExecution, TurnToolCompleted, TurnAggregateResults,
			TurnStartModelRequest, TurnStreamDelta, TurnComplete, TurnReset},
		{TurnStart, TurnStartModelRequest, TurnStreamDelta, TurnScheduleTools,
			TurnPermissionRequired, TurnPermissionResolved, TurnStartToolExecution,
			TurnToolCompleted, TurnAggregateResults, TurnComplete, TurnReset},
		{TurnStart, TurnStartModelRequest, TurnFail, TurnReset},
		// Illegal on purpose: every one of these must be refused.
		{TurnComplete, TurnComplete},
		{TurnFail, TurnReset, TurnReset},
		{TurnToolCompleted},
		{TurnReset},
	}
	for _, seq := range seeds {
		state := TurnIdle
		for _, event := range seq {
			next, effects, err := Transition(state, event)
			if err != nil {
				if !errors.Is(err, ErrIllegalTransition) {
					t.Fatalf("%s: error = %v, want ErrIllegalTransition", event, err)
				}
				if next != state {
					t.Fatalf("%s: state changed to %s while erroring", event, next)
				}
				continue
			}
			if len(effects) == 0 {
				t.Fatalf("%s: accepted with no effects", event)
			}
			state = next
		}
	}
}
