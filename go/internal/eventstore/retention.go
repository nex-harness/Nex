package eventstore

// This file ports apps/nex-cli/packages/contracts/src/events/session-event-retention.ts.

// transientTypes are the event types that stream at the same frequency as the
// message stream. They still get a sequence number and reach the live sink, but
// once their turn is finished and superseded by the next turn they may be
// evicted from memory: the completed turn's text is re-synthesised from the
// persisted message, and the reducer / rewind / fork / checkpoint paths never
// consume these types.
//
// Source: TRANSIENT_SESSION_EVENT_TYPES in session-event-retention.ts.
var transientTypes = map[EventType]bool{
	TypeModelStreaming:             true,
	TypeToolCallProgress:           true,
	TypeStreamingToolLedgerUpdated: true,
	TypeModelNetworkStatus:         true,
}

// SealedTurnTransientGraceMs is SEALED_TURN_TRANSIENT_GRACE_MS: the time
// fallback for a sealed turn that has no successor (a subagent child session has
// a single turn and never sees the next turn_started).
const SealedTurnTransientGraceMs int64 = 120_000

// IsTransient reports whether a type is evictable.
func IsTransient(t EventType) bool { return transientTypes[t] }

// RetentionMode selects the eviction policy. "turn-window" is the production
// default; "unbounded" is used for rollback and comparison tests.
type RetentionMode string

const (
	RetentionUnbounded  RetentionMode = "unbounded"
	RetentionTurnWindow RetentionMode = "turn-window"
)

// retentionPolicy decides, on each append, which turns' transient events to
// evict. A policy only reads the event sequence; it does not depend on
// persistence. collectExpired is the time fallback driven by a low-frequency
// tick.
type retentionPolicy interface {
	onAppend(event Event, nowMs int64) []string
	collectExpired(nowMs, graceMs int64) []string
}

// NewRetentionPolicy returns the policy for a mode. The empty string means the
// production default (turn-window), matching `options.retention ?? "turn-window"`.
func NewRetentionPolicy(mode RetentionMode) retentionPolicy {
	if mode == RetentionUnbounded {
		return unboundedRetention{}
	}
	return newTurnWindowRetention()
}

type unboundedRetention struct{}

func (unboundedRetention) onAppend(Event, int64) []string       { return nil }
func (unboundedRetention) collectExpired(int64, int64) []string { return nil }

// turnWindowRetention seals a turn on turn_complete / turn_error and evicts the
// transient events of all sealed turns when the next turn_started arrives. It
// lags by one turn on purpose: right after a turn ends its messages may not be
// flushed yet, so cold recovery still needs the in-memory transient events; the
// next turn_started means the user sent again, so the previous turn is
// definitely persisted.
type turnWindowRetention struct {
	openTurns map[string]bool
	sealedAt  map[string]int64
}

func newTurnWindowRetention() *turnWindowRetention {
	return &turnWindowRetention{openTurns: map[string]bool{}, sealedAt: map[string]int64{}}
}

func (p *turnWindowRetention) onAppend(event Event, nowMs int64) []string {
	turnID := event.TurnID
	if turnID == "" {
		return nil
	}
	switch event.Type {
	case TypeTurnStarted:
		// Evict every sealed turn except this (already-seen) one.
		var evict []string
		for sealed := range p.sealedAt {
			if sealed != turnID {
				evict = append(evict, sealed)
			}
		}
		p.sealedAt = map[string]int64{}
		p.openTurns[turnID] = true
		return evict
	case TypeTurnComplete, TypeTurnError:
		delete(p.openTurns, turnID)
		if _, ok := p.sealedAt[turnID]; !ok {
			p.sealedAt[turnID] = nowMs
		}
		return nil
	default:
		return nil
	}
}

func (p *turnWindowRetention) collectExpired(nowMs, graceMs int64) []string {
	var expired []string
	for turnID, at := range p.sealedAt {
		if nowMs-at >= graceMs {
			expired = append(expired, turnID)
		}
	}
	for _, turnID := range expired {
		delete(p.sealedAt, turnID)
	}
	return expired
}
