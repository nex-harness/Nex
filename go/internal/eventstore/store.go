// Package eventstore is the port of the Node agent's session event store
// (apps/nex-cli/packages/contracts/src/events/in-memory-session-event-store.ts).
//
// It is the single source of truth for session event sequence numbers: live,
// replay and snapshot all read the number assigned here. The rules, verified
// against the real Node agent by scripts/go-rewrite/gen-eventseq-vectors.mts
// (see docs/specs/go-rewrite/event-store.md):
//
//   - Every event that reaches Append consumes a sequence number, including the
//     transient streaming/network events that never reach the protocol.
//   - The number is per session (the "session-scoped counter"); there is no
//     turn-scoped counter. stream.chunk telemetry, delta createdAtSeq and the
//     protocol event list are all views of this one counter.
//   - A caller that pre-assigns a positive number keeps it; otherwise the store
//     assigns latest+1. The counter only ever grows.
//   - Transient events are evicted from memory only when the *next* turn starts
//     (turn-window retention); eviction never rewinds the counter.
package eventstore

// EventType is the SessionEventType string value from the contracts
// (apps/nex-cli/packages/contracts/src/events/session.events.ts). Only the
// string is modelled here; payloads stay opaque to the store.
type EventType = string

// SessionID identifies a session; sequences are scoped to it.
type SessionID = string

// Event is the minimal shape the store needs. Payloads and the other fields of
// the Node SessionEvent (id, timestamp, traceId) do not affect numbering or
// retention and are not modelled.
type Event struct {
	SessionID SessionID
	// TurnID is empty for session-scoped events. Retention keys transient
	// eviction off it.
	TurnID string
	Type   EventType
	// SequenceNumber is 0 for a fresh event ("assign the next number"). A
	// positive value is a caller-assigned number that the store keeps.
	SequenceNumber int
}

// Store is the port. It mirrors SessionEventStorePort plus the retention
// maintenance entry point the runtime calls on its low-frequency tick.
type Store interface {
	// Append assigns (or honours) a sequence number, stores the event, and
	// runs the retention policy. It returns the stored event with its final
	// sequence number. Append is serialised per session by the caller.
	Append(event Event) Event
	// Events returns the retained events of a session in append order.
	Events(sessionID SessionID) []Event
	// EventsAfter returns retained events with SequenceNumber > after.
	EventsAfter(sessionID SessionID, after int) []Event
	// LatestSequenceNumber returns the highest number ever assigned to a
	// session. It is monotonic: eviction does not lower it.
	LatestSequenceNumber(sessionID SessionID) int
	// DeleteSession drops all state for a session.
	DeleteSession(sessionID SessionID)
	// PruneTransientEvents is the time fallback: it evicts the transient
	// events of sealed turns that finished at least graceMs ago and have had
	// no successor turn (one-shot sessions such as subagents). It returns the
	// number of events evicted.
	PruneTransientEvents(nowMs, graceMs int64) int
	// Stats reports residency for diagnostics.
	Stats() Stats
}

// Stats mirrors SessionEventStoreStats.
type Stats struct {
	Sessions          int
	Events            int
	EvictedEvents     int
	RetainedTransient int
}
