package eventstore

import "testing"

func typesOf(events []Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The counter is strictly increasing and every append consumes exactly one
// number, including the transient types that never reach the protocol.
func TestAppendAssignsSequentialNumbers(t *testing.T) {
	store := NewInMemory(RetentionTurnWindow, nil)
	seqs := []int{}
	for _, typ := range []EventType{
		TypeSessionTitleUpdated, TypeTurnStarted, TypeSessionInputPromoted, TypeModelRequest,
		TypeModelNetworkStatus, TypeModelStreaming, TypeModelStreaming, TypeModelComplete,
		TypeTurnComplete,
	} {
		got := store.Append(Event{SessionID: "s", TurnID: "t1", Type: typ})
		seqs = append(seqs, got.SequenceNumber)
	}
	want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9}
	for i := range want {
		if seqs[i] != want[i] {
			t.Fatalf("seqs = %v, want %v", seqs, want)
		}
	}
}

// A caller-assigned positive number is honoured, and the counter jumps to it so
// the next fresh append continues from there (matches `event.sequenceNumber > 0
// ? event.sequenceNumber : latest + 1` and the max()).
func TestAppendHonoursExplicitNumber(t *testing.T) {
	store := NewInMemory(RetentionTurnWindow, nil)
	store.Append(Event{SessionID: "s", Type: TypeTurnStarted})
	got := store.Append(Event{SessionID: "s", Type: TypeModelStreaming, SequenceNumber: 10})
	if got.SequenceNumber != 10 {
		t.Fatalf("explicit number = %d, want 10", got.SequenceNumber)
	}
	if got := store.LatestSequenceNumber("s"); got != 10 {
		t.Fatalf("latest = %d, want 10", got)
	}
	// Sequence 11 must not be 10 (the counter moved to the explicit number).
	if next := store.Append(Event{SessionID: "s", Type: TypeTurnComplete}); next.SequenceNumber != 11 {
		t.Fatalf("next = %d, want 11", next.SequenceNumber)
	}
}

// A small explicit number never lowers the counter: 2 after 5 must not become 2.
func TestAppendExplicitNumberNeverLowersCounter(t *testing.T) {
	store := NewInMemory(RetentionTurnWindow, nil)
	for i := 0; i < 5; i++ {
		store.Append(Event{SessionID: "s", Type: TypeModelStreaming})
	}
	store.Append(Event{SessionID: "s", Type: TypeModelStreaming, SequenceNumber: 2})
	if got := store.LatestSequenceNumber("s"); got != 5 {
		t.Fatalf("latest = %d, want 5", got)
	}
}

// Sequences are per session: a second session starts at 1.
func TestSequenceIsSessionScoped(t *testing.T) {
	store := NewInMemory(RetentionTurnWindow, nil)
	store.Append(Event{SessionID: "a", Type: TypeTurnStarted})
	store.Append(Event{SessionID: "a", Type: TypeTurnComplete})
	if got := store.Append(Event{SessionID: "b", Type: TypeTurnStarted}); got.SequenceNumber != 1 {
		t.Fatalf("session b first seq = %d, want 1", got.SequenceNumber)
	}
	if got := store.LatestSequenceNumber("a"); got != 2 {
		t.Fatalf("session a latest = %d, want 2", got)
	}
}

// turn-window eviction: a sealed turn's transient events survive turn_complete
// and are evicted only when the next turn_started arrives. Non-transient events
// always survive.
func TestTurnWindowEviction(t *testing.T) {
	store := NewInMemory(RetentionTurnWindow, nil)
	appendAll := func(turn string, types ...EventType) {
		for _, typ := range types {
			store.Append(Event{SessionID: "s", TurnID: turn, Type: typ})
		}
	}
	appendAll("t1", TypeTurnStarted, TypeModelStreaming, TypeModelComplete, TypeTurnComplete)
	// Turn 1 sealed but nothing evicted yet: the transient event is still there.
	if got := typesOf(store.Events("s")); !eqStrings(got, []string{
		TypeTurnStarted, TypeModelStreaming, TypeModelComplete, TypeTurnComplete,
	}) {
		t.Fatalf("after seal = %v", got)
	}
	appendAll("t2", TypeTurnStarted)
	// Turn 1's transient event is gone; turn_started/non-transient remain.
	got := typesOf(store.Events("s"))
	want := []string{TypeTurnStarted, TypeModelComplete, TypeTurnComplete, TypeTurnStarted}
	if !eqStrings(got, want) {
		t.Fatalf("after next turn = %v, want %v", got, want)
	}
}

// Eviction does not rewind the counter: the number after eviction keeps growing
// and never collides with an evicted number.
func TestEvictionKeepsCounterMonotonic(t *testing.T) {
	store := NewInMemory(RetentionTurnWindow, nil)
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeTurnStarted})    // 1
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeModelStreaming}) // 2
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeTurnComplete})   // 3
	if got := store.LatestSequenceNumber("s"); got != 3 {
		t.Fatalf("latest = %d, want 3", got)
	}
	store.Append(Event{SessionID: "s", TurnID: "t2", Type: TypeTurnStarted}) // 4, evicts t1's streaming
	if got := store.LatestSequenceNumber("s"); got != 4 {
		t.Fatalf("latest after eviction = %d, want 4", got)
	}
	if got := store.Events("s"); got[1].SequenceNumber != 3 {
		// seq 2 was evicted; the retained event after seq 1 must be seq 3.
		t.Fatalf("retained[1].seq = %d, want 3", got[1].SequenceNumber)
	}
}

// unbounded mode never evicts.
func TestUnboundedRetentionKeepsEverything(t *testing.T) {
	store := NewInMemory(RetentionUnbounded, nil)
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeTurnStarted})
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeModelStreaming})
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeTurnComplete})
	store.Append(Event{SessionID: "s", TurnID: "t2", Type: TypeTurnStarted})
	if got := len(store.Events("s")); got != 4 {
		t.Fatalf("retained = %d, want 4", got)
	}
}

// turn_error also seals a turn.
func TestTurnErrorSealsTurn(t *testing.T) {
	store := NewInMemory(RetentionTurnWindow, nil)
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeTurnStarted})
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeModelStreaming})
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeTurnError})
	store.Append(Event{SessionID: "s", TurnID: "t2", Type: TypeTurnStarted})
	got := typesOf(store.Events("s"))
	want := []string{TypeTurnStarted, TypeTurnError, TypeTurnStarted}
	if !eqStrings(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The time fallback evicts a one-shot (subagent) turn's transient events once
// grace has elapsed, and is idempotent.
func TestPruneTransientEventsByGrace(t *testing.T) {
	now := int64(1000)
	store := NewInMemory(RetentionTurnWindow, func() int64 { return now })
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeTurnStarted})
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeModelStreaming})
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeModelComplete})
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeTurnComplete})
	// Not yet past the grace window.
	if n := store.PruneTransientEvents(now+SealedTurnTransientGraceMs-1, SealedTurnTransientGraceMs); n != 0 {
		t.Fatalf("premature prune evicted %d", n)
	}
	if n := store.PruneTransientEvents(now+SealedTurnTransientGraceMs, SealedTurnTransientGraceMs); n != 1 {
		t.Fatalf("prune evicted %d, want 1", n)
	}
	if n := store.PruneTransientEvents(now+SealedTurnTransientGraceMs+1, SealedTurnTransientGraceMs); n != 0 {
		t.Fatalf("second prune evicted %d, want 0", n)
	}
	got := typesOf(store.Events("s"))
	want := []string{TypeTurnStarted, TypeModelComplete, TypeTurnComplete}
	if !eqStrings(got, want) {
		t.Fatalf("after prune = %v, want %v", got, want)
	}
}

// A sealed turn with a successor-eligible next turn is not pruned by the time
// fallback: it was already evicted on the next turn_started.
func TestPruneDoesNothingWhenSuperseded(t *testing.T) {
	store := NewInMemory(RetentionTurnWindow, func() int64 { return 0 })
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeTurnStarted})
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeModelStreaming})
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeTurnComplete})
	store.Append(Event{SessionID: "s", TurnID: "t2", Type: TypeTurnStarted})
	if n := store.PruneTransientEvents(SealedTurnTransientGraceMs*10, SealedTurnTransientGraceMs); n != 0 {
		t.Fatalf("prune evicted %d, want 0", n)
	}
}

// Re-entering a turn that is already sealed (stream recovery / resume) does not
// evict that turn's transient events: the policy excludes the current turn from
// the eviction set. This pins `sealed !== turnId`.
func TestTurnStartedForSameTurnKeepsItsTransients(t *testing.T) {
	store := NewInMemory(RetentionTurnWindow, nil)
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeTurnStarted})
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeModelStreaming})
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeTurnComplete})
	// The same turn starts again before any other turn supersedes it.
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeTurnStarted})
	got := typesOf(store.Events("s"))
	want := []string{TypeTurnStarted, TypeModelStreaming, TypeTurnComplete, TypeTurnStarted}
	if !eqStrings(got, want) {
		t.Fatalf("re-entered turn evicted its own transients: %v", got)
	}
}

// Sequences are per session and never reset per turn: a second turn's first
// event continues from the first turn's high-water mark.
func TestCounterDoesNotResetPerTurn(t *testing.T) {
	store := NewInMemory(RetentionUnbounded, nil)
	for i := 1; i <= 3; i++ {
		if got := store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeModelStreaming}); got.SequenceNumber != i {
			t.Fatalf("turn 1 event %d: seq = %d", i, got.SequenceNumber)
		}
	}
	// New turn: the counter keeps going (4, not 1).
	if got := store.Append(Event{SessionID: "s", TurnID: "t2", Type: TypeTurnStarted}); got.SequenceNumber != 4 {
		t.Fatalf("turn 2 first seq = %d, want 4", got.SequenceNumber)
	}
}

func TestEventsAfter(t *testing.T) {
	store := NewInMemory(RetentionUnbounded, nil)
	store.Append(Event{SessionID: "s", Type: TypeTurnStarted})  // 1
	store.Append(Event{SessionID: "s", Type: TypeModelRequest}) // 2
	store.Append(Event{SessionID: "s", Type: TypeTurnComplete}) // 3
	got := store.EventsAfter("s", 1)
	if len(got) != 2 || got[0].SequenceNumber != 2 || got[1].SequenceNumber != 3 {
		t.Fatalf("EventsAfter = %+v", got)
	}
	if got := store.EventsAfter("s", 99); len(got) != 0 {
		t.Fatalf("EventsAfter beyond end = %d", len(got))
	}
}

func TestDeleteSession(t *testing.T) {
	store := NewInMemory(RetentionTurnWindow, nil)
	store.Append(Event{SessionID: "s", Type: TypeTurnStarted})
	store.DeleteSession("s")
	if got := store.LatestSequenceNumber("s"); got != 0 {
		t.Fatalf("latest after delete = %d", got)
	}
	if got := store.Events("s"); len(got) != 0 {
		t.Fatalf("events after delete = %d", len(got))
	}
	// A new append after delete starts from 1 again.
	if got := store.Append(Event{SessionID: "s", Type: TypeTurnStarted}); got.SequenceNumber != 1 {
		t.Fatalf("post-delete seq = %d, want 1", got.SequenceNumber)
	}
}

func TestStats(t *testing.T) {
	store := NewInMemory(RetentionTurnWindow, nil)
	store.Append(Event{SessionID: "a", TurnID: "t", Type: TypeTurnStarted})
	store.Append(Event{SessionID: "a", TurnID: "t", Type: TypeModelStreaming})
	store.Append(Event{SessionID: "b", Type: TypeTurnStarted})
	got := store.Stats()
	if got.Sessions != 2 || got.Events != 3 || got.RetainedTransient != 1 || got.EvictedEvents != 0 {
		t.Fatalf("stats = %+v", got)
	}
}

// Events() must return a copy: mutating it must not affect the store.
func TestEventsReturnsCopy(t *testing.T) {
	store := NewInMemory(RetentionUnbounded, nil)
	store.Append(Event{SessionID: "s", Type: TypeTurnStarted})
	got := store.Events("s")
	got[0].Type = "mutated"
	if store.Events("s")[0].Type == "mutated" {
		t.Fatal("Events() aliases internal storage")
	}
}

// A transient event without a turnId is never evicted (matches
// `isTransientSessionEvent(event) && event.turnId && turnIds.has(event.turnId)`).
func TestTransientWithoutTurnSurvives(t *testing.T) {
	store := NewInMemory(RetentionTurnWindow, nil)
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeTurnStarted})
	store.Append(Event{SessionID: "s", Type: TypeModelStreaming}) // no turnId
	store.Append(Event{SessionID: "s", TurnID: "t1", Type: TypeTurnComplete})
	store.Append(Event{SessionID: "s", TurnID: "t2", Type: TypeTurnStarted})
	got := typesOf(store.Events("s"))
	want := []string{TypeTurnStarted, TypeModelStreaming, TypeTurnComplete, TypeTurnStarted}
	if !eqStrings(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
