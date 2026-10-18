package windowcontroller

import "errors"

// service.go is the small surface the RPC channel and the tests bind to: the
// subscribe/resync ack, the refusals an attachment can raise, the cursor read the
// subscribe ack needs, and the attachment accounting on the runtime.

// SubscribeAck is subscribeAckSchema: the id the caller must remember, the mode
// (always "snapshot" — the controller's subscribe always sends a full state), and
// the log epoch the frame cursor belongs to.
type SubscribeAck struct {
	SubscriptionID string `json:"subscriptionId"`
	Mode           string `json:"mode"`
	LogEpoch       string `json:"logEpoch"`
}

// SubscribeResult is controllerSubscribeResultSchema. The attachment returns the
// bare ack; this wrapper is the wire envelope the RPC channel marshals it in, so
// the schema shape lives here rather than being spelled out at the call site in
// 3.10.9's route switch.
type SubscribeResult struct {
	Ack SubscribeAck `json:"ack"`
}

// ResyncResult is controllerResyncResultSchema — the twin envelope.
type ResyncResult struct {
	Ack SubscribeAck `json:"ack"`
}

// The attachment refusals. They are values rather than formatted sentences so the
// channel can map them to codes without string matching.
var (
	// ErrClosed: the attachment was disposed. Every entry point refuses with it,
	// which is what stops a command that races a window close from touching shared
	// state on behalf of a connection that no longer exists.
	ErrClosed = errors.New("controller attachment is closed")
	// ErrNotOwned: a resync for a subscription this connection does not own.
	ErrNotOwned = errors.New("controller subscription is not owned by this connection")
	// ErrUnknownTopic: a subscribe for a topic outside the two controller topics.
	ErrUnknownTopic = errors.New("unknown controller topic")
)

// cursorFor returns a topic's cursor. It is the ack's logEpoch source.
func (p *projection) cursorFor(topic string) *topicCursor {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cursors[topic]
}

// releaseAttachment decrements the runtime's live-attachment count. The
// Attachment calls it exactly once, from Dispose, and Dispose is idempotent, so
// the count cannot drift below zero.
func (rt *Runtime) releaseAttachment() {
	rt.attachedMu.Lock()
	if rt.attached > 0 {
		rt.attached--
	}
	rt.attachedMu.Unlock()
}
