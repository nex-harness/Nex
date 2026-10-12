package v4pub

import (
	"encoding/json"
	"fmt"
)

// LogEntry is one retained log record: an authoritative sequence number and the
// deltas its event produced (possibly empty).
type LogEntry struct {
	Seq    int64
	Deltas []Delta
}

type subscription struct {
	subscriptionID string
	connectionID   string
	profile        DeliveryProfile
	// workflowRunDeltas: whether this subscription understands key-level
	// `workflowRun.*` deltas (a handshake capability injected by the trusted
	// host). false = legacy consumer (delta folded to a whole-key patch, snapshot
	// clamped to the legacy bounds).
	workflowRunDeltas bool

	// buffer holds profile-filtered deltas already encoded for this
	// subscription; Flush coalesces and frames them.
	buffer      []Delta
	bufferBytes int
	// resyncRequired: after the buffer overflows, keep only the recovery intent;
	// do not keep accumulating deltas for a slow subscriber.
	resyncRequired bool
	// sentSeq is the frame-interval watermark: the next frame's fromSeq
	// ((fromSeq, toSeq] semantics).
	sentSeq int64
	// inFlight is the stable logical frame held during encode/write.
	inFlight                *Reservation
	nextLogicalFrameOrdinal int64
}

// Options configures a Publisher. Zero-value limit pointers mean "use the
// protocol maximum".
type Options struct {
	// Now is the CLI clock (frame.sentAt and the clockOffset estimate source).
	Now func() int64
	// Retention is the event retention window in entries.
	Retention *int
	// SubscriberBufferMaxOps caps coalesced ops per subscriber buffer.
	SubscriberBufferMaxOps *int
	// SubscriberBufferMaxBytes caps the UTF-8 bytes of a subscriber's deltas.
	SubscriberBufferMaxBytes *int
	// Bounds are the per-table wire bounds used by coalesce rule 6.
	Bounds *WorkflowRunWireBounds
	// Source is the session projection.
	Source SnapshotSource
	// Sink observes the per-subscription pipeline output; may be nil.
	Sink DeltaSink
}

// Publisher is the authoritative conversation topic publisher for one session.
type Publisher struct {
	topic     string
	sessionID string
	logEpoch  string
	source    SnapshotSource
	sink      DeltaSink
	now       func() int64
	retention int
	maxOps    int
	maxBytes  int
	bounds    WorkflowRunWireBounds

	// log is the bounded log: seq ascending; resume is only valid within
	// (floorSeq, currentSeq].
	log      []LogEntry
	floorSeq int64

	subscriptions              map[string]*subscription
	subscriptionIDByConnection map[string]string
	nextSubscriptionSerial     int64
	nextLogicalFrameSerial     int64
}

// NewPublisher constructs a publisher. It mirrors the ConversationTopicPublisher
// constructor: non-negative hard bounds are applied to the buffer limits.
func NewPublisher(sessionID, logEpoch string, opts Options) (*Publisher, error) {
	if opts.Source == nil {
		return nil, fmt.Errorf("v4pub: Options.Source is required")
	}
	retention, err := nonNegativeHardBound(opts.Retention, EventRetentionPerSession, "retention")
	if err != nil {
		return nil, err
	}
	maxOps, err := nonNegativeHardBound(opts.SubscriberBufferMaxOps, SubscriberBufferMaxOps, "subscriberBufferMaxOps")
	if err != nil {
		return nil, err
	}
	maxBytes, err := nonNegativeHardBound(opts.SubscriberBufferMaxBytes, SubscriberBufferMaxBytes, "subscriberBufferMaxBytes")
	if err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		return nil, fmt.Errorf("v4pub: Options.Now is required (time is injected)")
	}
	bounds := DefaultWorkflowRunWireBounds()
	if opts.Bounds != nil {
		bounds = *opts.Bounds
	}
	return &Publisher{
		topic:                      "conversation/" + sessionID,
		sessionID:                  sessionID,
		logEpoch:                   logEpoch,
		source:                     opts.Source,
		sink:                       opts.Sink,
		now:                        now,
		retention:                  retention,
		maxOps:                     maxOps,
		maxBytes:                   maxBytes,
		bounds:                     bounds,
		subscriptions:              map[string]*subscription{},
		subscriptionIDByConnection: map[string]string{},
		nextSubscriptionSerial:     1,
		nextLogicalFrameSerial:     1,
	}, nil
}

// Topic is "conversation/<sessionId>".
func (p *Publisher) Topic() string { return p.topic }

// LogEpoch is the projection generation of this publisher.
func (p *Publisher) LogEpoch() string { return p.logEpoch }

func (p *Publisher) currentSeq() int64 { return p.source.Seq() }

// Ingest applies one authoritative event: it advances the projection, records
// the log entry and fans the new deltas out to every non-resyncing subscriber.
// A projection error is returned to the caller.
func (p *Publisher) Ingest(event json.RawMessage) error {
	deltas, err := p.source.ApplyIngest(event)
	if err != nil {
		return err
	}
	p.log = append(p.log, LogEntry{Seq: p.currentSeq(), Deltas: deltas})
	for len(p.log) > p.retention {
		p.floorSeq = p.log[0].Seq
		p.log = p.log[1:]
	}
	if len(deltas) == 0 {
		return nil
	}
	for _, sub := range p.subscriptions {
		if sub.resyncRequired {
			continue
		}
		filtered := p.encodeDeltasForSubscription(deltas, sub)
		next, encodedBytes, overflow := appendSubscriberBuffer(sub.buffer, filtered, p.maxOps, p.maxBytes)
		if overflow {
			sub.buffer = nil
			sub.bufferBytes = 0
			sub.resyncRequired = true
			p.notifyOverflow(sub.subscriptionID)
			continue
		}
		sub.buffer = next
		sub.bufferBytes = encodedBytes
		p.notifyBuffered(sub.subscriptionID, next, encodedBytes)
	}
	return nil
}

func (p *Publisher) notifyBuffered(subscriptionID string, deltas []Delta, encodedBytes int) {
	if p.sink != nil {
		p.sink.OnBuffered(subscriptionID, deltas, encodedBytes)
	}
}

func (p *Publisher) notifyOverflow(subscriptionID string) {
	if p.sink != nil {
		p.sink.OnOverflow(subscriptionID)
	}
}

// encodeDeltasForSubscription is the per-subscription encoding of a delta batch:
// profile filter + legacy whole-key folding. The fold uses the *current*
// projection state, so historical increments collapse to the terminal state on a
// recovery replay - intermediate states are skipped, the terminal state matches.
func (p *Publisher) encodeDeltasForSubscription(deltas []Delta, sub *subscription) []Delta {
	filtered := FilterConversationDeltasForProfile(deltas, sub.profile)
	if sub.workflowRunDeltas {
		return filtered
	}
	return encodeDeltasForLegacy(filtered, p.currentWorkflowRuns())
}

// currentWorkflowRuns is the projection's workflowRuns state, or nil. It comes
// from the snapshot source; the publisher never interprets it beyond handing it
// to the legacy folder.
func (p *Publisher) currentWorkflowRuns() json.RawMessage {
	return p.source.WorkflowRunsRaw()
}
