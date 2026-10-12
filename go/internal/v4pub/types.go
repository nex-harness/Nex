package v4pub

import "encoding/json"

// SnapshotSource is the projection the publisher owns for one session. Agent B's
// product projection implements it; the publisher never inspects the snapshot's
// inner shape, it only measures and emits it.
//
// All methods are called from the publisher's goroutine; implementations need no
// internal locking for the publisher's use.
type SnapshotSource interface {
	// Seq is the current authoritative log sequence (snapshot.seq).
	Seq() int64
	// SnapshotRaw is JSON.stringify of the full ConversationSnapshot at Seq.
	// It is used to build the wire snapshot (tail window trimmed by the
	// publisher).
	SnapshotRaw() json.RawMessage
	// WindowRowCount is len(snapshot.rows.window), used for the ACK openTiming.
	WindowRowCount() int
	// ApplyIngest applies one authoritative event. It returns the deltas the
	// event produced (empty when the event produced none). Seq() reflects the
	// new sequence after it returns.
	ApplyIngest(event json.RawMessage) ([]Delta, error)
	// WorkflowRunsRaw is JSON.stringify of the projection's workflowRuns state,
	// or nil when the key is absent. Used only by the legacy folder.
	WorkflowRunsRaw() json.RawMessage
}

// DeltaSink receives the per-subscription delta fan-out. The publisher filters
// and buffers; the sink only records. It exists so tests (and a future metrics
// layer) observe the exact pipeline output.
type DeltaSink interface {
	OnBuffered(subscriptionID string, coalesced []Delta, encodedBytes int)
	OnOverflow(subscriptionID string)
}

// ---------------------------------------------------------------------------
// Frames

// FramePayload is one logical frame payload: a snapshot or a deltas batch.
type FramePayload struct {
	Kind     string          // "snapshot" | "deltas"
	Snapshot json.RawMessage // Kind == "snapshot"
	Deltas   []Delta         // Kind == "deltas"
}

// Frame is one logical conversation topic frame.
type Frame struct {
	Topic          string
	SubscriptionID string
	FromSeq        int64
	ToSeq          int64
	SentAt         int64
	Payload        FramePayload
}

// FrameJSON renders the frame with JSON.stringify-compatible bytes: header
// fields first (topic, subscriptionId, sentAt, fromSeq, toSeq), then payload.
// This is the exact object the recorded trace shows inside a wire frame.
func (f Frame) FrameJSON() []byte {
	payload := f.payloadJSON()
	return Obj(
		Member("topic", Str(f.Topic)),
		Member("subscriptionId", Str(f.SubscriptionID)),
		Member("sentAt", Int(f.SentAt)),
		Member("fromSeq", Int(f.FromSeq)),
		Member("toSeq", Int(f.ToSeq)),
		Member("payload", payload),
	).appendJSON(nil)
}

func (f Frame) payloadJSON() JSONValue {
	if f.Payload.Kind == "snapshot" {
		return Obj(
			Member("kind", Str("snapshot")),
			Member("snapshot", RawMessageOf(f.Payload.Snapshot)),
		)
	}
	items := make([]JSONValue, len(f.Payload.Deltas))
	for i, d := range f.Payload.Deltas {
		items[i] = RawMessageOf(d.Raw)
	}
	return Obj(Member("kind", Str("deltas")), Member("deltas", Arr(items...)))
}

// Reservation is a two-phase-commit handle for a pending frame. The physical
// split and the consumer must not guess deliveryKind from timing; the publisher
// assigns it. Only a reservation whose subscription generation is still valid
// commits.
type Reservation struct {
	DeliveryKind        DeliveryKind
	LogicalFrameID      string
	LogicalFrameOrdinal int64
	Frame               Frame
	commit              func() bool
}

// Commit advances the watermark when the subscription generation is still
// current. It returns whether it committed.
func (r *Reservation) Commit() bool {
	if r == nil || r.commit == nil {
		return false
	}
	return r.commit()
}

// ---------------------------------------------------------------------------
// Subscribe / resync

// Base is a client's confirmed watermark.
type Base struct {
	LogEpoch string
	Seq      int64
}

// SubscribeParams is the input to Subscribe.
type SubscribeParams struct {
	ConnectionID      string
	Base              *Base
	DeliveryProfile   DeliveryProfileName
	WorkflowRunDeltas bool
}

// SubscribeAck is the subscribe verdict (transport.ts SubscribeAck).
type SubscribeAck struct {
	SubscriptionID string
	Mode           AckMode
	LogEpoch       string
}

// OpenTiming is the low-frequency desktop open diagnostic attached only to the
// conversation subscribe ACK.
type OpenTiming struct {
	Version              int
	InitialFrameEncodeMs int
	SessionRuntimeState  string
	SnapshotRowCount     int
}

// SubscribeResult is the internal dispatch result. The public JSON-RPC result is
// ACK-only; the initial frame is carried separately for the request-scoped
// post-response outbox.
type SubscribeResult struct {
	Ack          SubscribeAck
	OpenTiming   OpenTiming
	Reservation  *Reservation
	InitialFrame *Frame
	rollback     func() bool
}

// Rollback atomically restores a replaced subscription when the initial encode
// failed before the ACK was admitted.
func (r *SubscribeResult) Rollback() bool {
	if r == nil || r.rollback == nil {
		return false
	}
	return r.rollback()
}

// ResyncRequest re-arbitrates an active subscription from the client's base.
type ResyncRequest struct {
	Base          *Base
	ForceSnapshot bool
}
