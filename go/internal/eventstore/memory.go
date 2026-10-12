package eventstore

// InMemory is the port of InMemorySessionEventStore. It is the single source of
// session event sequence numbers.
//
// All events are numbered by Append — including the transient ones that only
// live for a turn window — but transient events are kept only per turn: a turn
// bucket that has finished and been superseded by the next turn is evicted.
// Without eviction a long session's memory grows linearly with tokens, because
// Append would copy the whole array and never drop anything.
type InMemory struct {
	sessions map[SessionID]*sessionState
	policy   func() retentionPolicy
	now      func() int64
}

type sessionState struct {
	events        []Event
	latestSeq     int
	policy        retentionPolicy
	evictedEvents int
}

// NewInMemory builds a store with the given retention mode and a clock. A nil
// clock defaults to a monotonically increasing counter that never advances on
// its own (tests inject a real clock); production passes time.Now().UnixMilli.
func NewInMemory(mode RetentionMode, now func() int64) *InMemory {
	if now == nil {
		now = func() int64 { return 0 }
	}
	return &InMemory{
		sessions: map[SessionID]*sessionState{},
		policy:   func() retentionPolicy { return NewRetentionPolicy(mode) },
		now:      now,
	}
}

func (s *InMemory) stateFor(sessionID SessionID) *sessionState {
	state, ok := s.sessions[sessionID]
	if !ok {
		state = &sessionState{policy: s.policy()}
		s.sessions[sessionID] = state
	}
	return state
}

// Append implements Store.Append. The numbering rule is copied exactly:
//
//	sequenceNumber := event.SequenceNumber > 0 ? event.SequenceNumber : latest+1
//	latest = max(latest, sequenceNumber)
func (s *InMemory) Append(event Event) Event {
	state := s.stateFor(event.SessionID)
	seq := event.SequenceNumber
	if seq <= 0 {
		seq = state.latestSeq + 1
	}
	// The counter only grows: eviction must never let a later
	// latest+1 collide with an already-used number.
	if seq > state.latestSeq {
		state.latestSeq = seq
	}
	stored := event
	stored.SequenceNumber = seq
	state.events = append(state.events, stored)
	evictTurnIDs := state.policy.onAppend(stored, s.now())
	if len(evictTurnIDs) > 0 {
		evictTransient(state, evictTurnIDs)
	}
	return stored
}

// Events implements Store.Events. The slice is a copy so callers cannot mutate
// the store's backing array.
func (s *InMemory) Events(sessionID SessionID) []Event {
	state, ok := s.sessions[sessionID]
	if !ok {
		return nil
	}
	out := make([]Event, len(state.events))
	copy(out, state.events)
	return out
}

// EventsAfter returns retained events with SequenceNumber > after, in order.
func (s *InMemory) EventsAfter(sessionID SessionID, after int) []Event {
	state, ok := s.sessions[sessionID]
	if !ok {
		return nil
	}
	var out []Event
	for _, event := range state.events {
		if event.SequenceNumber > after {
			out = append(out, event)
		}
	}
	return out
}

// LatestSequenceNumber returns the monotonic high-water mark for a session.
func (s *InMemory) LatestSequenceNumber(sessionID SessionID) int {
	if state, ok := s.sessions[sessionID]; ok {
		return state.latestSeq
	}
	return 0
}

func (s *InMemory) DeleteSession(sessionID SessionID) { delete(s.sessions, sessionID) }

// PruneTransientEvents implements the time fallback.
func (s *InMemory) PruneTransientEvents(nowMs, graceMs int64) int {
	total := 0
	for _, state := range s.sessions {
		expired := state.policy.collectExpired(nowMs, graceMs)
		if len(expired) == 0 {
			continue
		}
		before := state.evictedEvents
		evictTransient(state, expired)
		total += state.evictedEvents - before
	}
	return total
}

func (s *InMemory) Stats() Stats {
	stats := Stats{Sessions: len(s.sessions)}
	for _, state := range s.sessions {
		stats.Events += len(state.events)
		stats.EvictedEvents += state.evictedEvents
		for _, event := range state.events {
			if IsTransient(event.Type) {
				stats.RetainedTransient++
			}
		}
	}
	return stats
}

// evictTransient drops transient events whose turnId is in turnIDs.
func evictTransient(state *sessionState, turnIDs []string) {
	if len(turnIDs) == 0 {
		return
	}
	want := make(map[string]bool, len(turnIDs))
	for _, id := range turnIDs {
		want[id] = true
	}
	retained := make([]Event, 0, len(state.events))
	for _, event := range state.events {
		if IsTransient(event.Type) && event.TurnID != "" && want[event.TurnID] {
			state.evictedEvents++
			continue
		}
		retained = append(retained, event)
	}
	state.events = retained
}

var _ Store = (*InMemory)(nil)
