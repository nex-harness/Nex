// client.go ports NexProtocolClient (nexProtocolClient.ts): the request/response
// correlation itself — id allocation, the waiter table, the watchdog and the
// per-call write. The vocabulary it speaks (method names, error types, the
// transport and handler contracts) is in clienttypes.go; the observing and closing
// surface is in clientapi.go; the frame pump and the completion paths are in
// clientpump.go.
//
// The rules this file exists to keep:
//
//   - A response only ever settles the waiter registered under its own id key.
//     Two concurrent calls must never cross, even when the agent answers them out
//     of order. The key is the decimal string of the id (the TS keys by
//     String(id)), so an agent that echoes a numeric id as "7" still matches.
//   - Timeout and cancellation both REMOVE the waiter before delivering the
//     outcome; a response that arrives afterwards finds nothing. An id is never
//     recycled, so a late response cannot settle a later request.
//   - A business request that times out is also reported as a request-timeout
//     event: the subprocess can be alive with a dead protocol event loop, and the
//     process manager keeps reusing it, so a single timeout is not enough. An
//     observation (a short resource probe) is never escalated — it only means
//     "no data this round".
//   - A cancellation that happened BEFORE the write keeps the frame from being
//     sent at all. Otherwise the agent would run a model task whose response
//     nobody can receive.
//   - Closing the transport rejects every waiter exactly once with the close
//     reason, and fires close exactly once.
//
// The storage gate is awaited BEFORE an id is allocated and before the waiter is
// registered, so a gated turn never occupies an id, never starts a watchdog and
// never writes a frame.
package proto

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"

	"github.com/tylinux/nex/go/internal/protocol"
)

// Client is one protocol connection to one agent process generation.
type Client struct {
	transport Transport
	opts      Options
	gate      *StorageStartupGate

	mu           sync.Mutex
	pending      map[string]*pendingCall
	nextID       int64
	disposed     bool
	notified     map[string][]chan Notification
	interactions map[string]InteractionHandler
	timeouts     []chan RequestTimeoutEvent
	closed       []chan struct{}
	closeReason  string
	closeCancel  context.CancelFunc
	pumpCtx      context.Context

	capMu         sync.Mutex
	capabilities  Capabilities
	capDone       bool
	suspendedCall map[string]bool

	closeOnce sync.Once
	// closeDone is closed after the frame pump exits, so Wait can prove the
	// goroutine is gone.
	closeDone chan struct{}
}

// New constructs a client and starts its frame pump.
func New(transport Transport, opts Options) *Client {
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = DefaultRequestTimeout
	}
	pumpCtx, cancel := context.WithCancel(context.Background())
	c := &Client{
		transport:     transport,
		opts:          opts,
		gate:          NewStorageStartupGate(opts.RequireStorageStartup, opts.FirstStatusTimeout),
		pending:       map[string]*pendingCall{},
		nextID:        1,
		notified:      map[string][]chan Notification{},
		interactions:  map[string]InteractionHandler{},
		suspendedCall: map[string]bool{},
		closeCancel:   cancel,
		pumpCtx:       pumpCtx,
		closeDone:     make(chan struct{}),
	}
	go c.pump()
	return c
}

// Gate exposes the storage startup gate for lifecycle observers.
func (c *Client) Gate() *StorageStartupGate { return c.gate }

// Call issues a request and waits for its response. It returns the raw result;
// use Options.Validate for a schema check.
func (c *Client) Call(ctx context.Context, method string, params any, opts CallOptions) (*CallResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.assertUsable(); err != nil {
		return nil, err
	}
	if c.gate.IsWaiting() {
		if err := c.gate.Wait(ctx); err != nil {
			return nil, err
		}
	}
	// The gate released us; the client may have closed while waiting.
	if err := c.assertUsable(); err != nil {
		return nil, err
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = c.opts.RequestTimeout
	}
	c.mu.Lock()
	num := c.nextID
	c.nextID++
	id := strconv.FormatInt(num, 10)
	call := &pendingCall{
		method:      method,
		id:          id,
		num:         num,
		observation: opts.Lifecycle == LifecycleObservation,
		timeout:     timeout,
		validate:    opts.Validate,
		result:      make(chan callOutcome, 1),
	}
	c.pending[id] = call
	// The watchdog is armed while the waiter is already registered, so a timeout
	// can never fire for an id that is not yet resolvable.
	c.armTimer(call)
	c.mu.Unlock()

	if err := ctx.Err(); err != nil {
		// Cancelled before the write: settle so the frame is never sent, because
		// an agent that executes a request nobody awaits runs a task whose
		// response is discarded.
		c.settle(id, callOutcome{err: err})
		return nil, err
	}

	msg := protocol.Message{Kind: protocol.KindRequest, ID: protocol.IntID(num), Method: method}
	if params != nil {
		raw, err := encodeParams(params)
		if err != nil {
			c.settle(id, callOutcome{err: err})
			return nil, err
		}
		msg.Params, msg.HasParams = raw, true
	}
	if err := c.transport.Send(msg); err != nil {
		c.settle(id, callOutcome{err: err})
		return nil, err
	}

	select {
	case outcome := <-call.result:
		return outcome.res, outcome.err
	case <-ctx.Done():
		c.settle(id, callOutcome{err: ctx.Err()})
		// A response that raced the cancel may already be queued; a completed
		// call is reported as completed rather than as a cancellation.
		select {
		case outcome := <-call.result:
			return outcome.res, outcome.err
		default:
			return nil, ctx.Err()
		}
	}
}

// Notify sends a notification. It does not wait for the storage gate: a
// notification expects no answer, so gating it would only add latency.
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	if err := c.assertUsable(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	msg := protocol.Message{Kind: protocol.KindNotification, Method: method}
	if params != nil {
		raw, err := encodeParams(params)
		if err != nil {
			return err
		}
		msg.Params, msg.HasParams = raw, true
	}
	return c.transport.Send(msg)
}

// Respond answers an agent→host request.
func (c *Client) Respond(id protocol.RequestID, result any) error {
	if err := c.assertUsable(); err != nil {
		return err
	}
	raw, err := encodeResult(result)
	if err != nil {
		return err
	}
	return c.transport.Send(protocol.Message{Kind: protocol.KindResponse, ID: id, Result: raw})
}

// RespondError answers an agent→host request with a JSON-RPC error.
func (c *Client) RespondError(id protocol.RequestID, code int, message string, data any) error {
	if err := c.assertUsable(); err != nil {
		return err
	}
	payload := protocol.RPCError{Code: code, Message: message}
	if data != nil {
		if raw, err := encodeParams(data); err == nil {
			payload.Data = raw
		}
	}
	return c.transport.Send(protocol.Message{Kind: protocol.KindError, ID: id, Error: &payload})
}

// Capabilities is the runtime/capabilities answer: the negotiation record that
// gates host behaviour. A field the agent omits is false — the fail-closed
// direction.
type Capabilities struct {
	// IndependentPlanState gates whether the host may send Plan fields; an older
	// remote CLI strips them, so the host must confirm before sending
	// (ensureIndependentPlanSupport in the TS).
	IndependentPlanState bool
	// Raw is the whole answer, so a newer agent can carry fields this build does
	// not know without a change here.
	Raw json.RawMessage
}

// EnsureCapabilities negotiates once per client and caches a positive answer: a
// host update does not imply the remote CLI was updated (the TS caches per client
// for the same reason). A negative answer is NOT cached, because the same agent
// may answer positively once it is past its own startup.
func (c *Client) EnsureCapabilities(ctx context.Context) (Capabilities, error) {
	c.capMu.Lock()
	if c.capDone {
		caps := c.capabilities
		c.capMu.Unlock()
		return caps, nil
	}
	c.capMu.Unlock()

	res, err := c.Call(ctx, MethodRuntimeCapabilities, map[string]any{}, CallOptions{})
	if err != nil {
		return Capabilities{}, err
	}
	var fields struct {
		IndependentPlanState *bool `json:"independentPlanState"`
	}
	if err := json.Unmarshal(res.Raw, &fields); err != nil {
		return Capabilities{}, err
	}
	caps := Capabilities{Raw: res.Raw}
	if fields.IndependentPlanState != nil {
		caps.IndependentPlanState = *fields.IndependentPlanState
	}
	if !caps.IndependentPlanState {
		return caps, ErrCapabilityUnsupported
	}
	c.capMu.Lock()
	c.capabilities, c.capDone = caps, true
	c.capMu.Unlock()
	return caps, nil
}
