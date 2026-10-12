package rpc

import (
	"context"
	"errors"
	"sync"
	"time"
)

// This file is the port of ChannelServer (packages/rpc/src/channelServer.ts). One Server owns
// one connection. It never runs a handler inline in the read loop: Promise calls and event
// dispatch each get their own goroutine, bounded by a per-connection semaphore so a client
// cannot spawn unbounded work.
//
// Ownership:
//   - channels map and its contents are read-only after RegisterChannel returns.
//   - activeRequests and pendingRequests are guarded by mu; every mutation happens under it.
//   - ctx is the connection context; closing the socket cancels it, which cancels every
//     in-flight call and listener.

// Options configures a Server.
type Options struct {
	// TimeoutDelayMS is how long a request for a channel that is not registered yet waits
	// before it is dropped with the Unknown-channel PromiseError. Zero selects
	// DefaultTimeoutDelayMS (1000).
	TimeoutDelayMS int
	// DeferInit suppresses the Initialize response on construction; call Ready to send it
	// later (mirrors the TS deferInit / ready() pair).
	DeferInit bool
	// MaxConcurrentCalls bounds in-flight Promise goroutines per connection. Zero selects
	// DefaultMaxConcurrentCalls (256).
	MaxConcurrentCalls int
	// OutboundQueueSize bounds framed responses waiting to be written before a producer
	// gives up and the connection is closed (see ErrWriteBackpressure). Zero selects
	// DefaultOutboundQueueSize.
	OutboundQueueSize int
	// EnqueueTimeout bounds how long a response waits for room in the outbound queue before
	// the connection is closed as back-pressured. Zero selects DefaultEnqueueTimeout.
	EnqueueTimeout time.Duration
}

// Defaults matching the TS constructor and the plan.
const (
	DefaultTimeoutDelayMS     = 1000
	DefaultMaxConcurrentCalls = 256
)

// Server dispatches one connection's channel RPC.
type Server struct {
	sock   Socket
	opts   Options
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	channels map[string]Channel
	// activeRequests holds the cancel/teardown for each in-flight Promise call or active
	// listener, keyed by request id. 101 and 103 cancel and remove an entry; connection
	// teardown cancels all.
	activeRequests map[int]*activeRequest
	// pendingRequests buffers requests for unregistered channels, keyed by channel name.
	pendingRequests map[string][]*pendingRequest
	// calls is the per-connection semaphore that bounds concurrent Promise goroutines.
	calls chan struct{}
	// writer owns the socket's write side; see writer.go for why this is a bounded queue
	// and not a mutex.
	writer *writer
	closed bool
	// failOnce/failure record why the connection was aborted, so Serve can return the
	// cause (ErrWriteBackpressure) instead of a bare EOF once the socket closes.
	failOnce sync.Once
	failure  error
}

// activeRequest is the cancellable unit behind one request id.
type activeRequest struct {
	// cancel ends a Promise call's context (nil for a listener).
	cancel context.CancelFunc
	// dispose ends a listener's subscription, flushing events already produced. It is what
	// a 103 EventDispose invokes (nil for a Promise call).
	dispose func()
	// abandon ends a listener immediately, dropping anything still buffered. It is what
	// connection teardown and id eviction invoke; nil for a Promise call and for a
	// graceful dispose that has nothing to abandon.
	abandon func()
}

// release runs the teardown appropriate to why a request ended: a graceful dispose for a
// 103, an immediate abandon when the connection is going away or the id was reused.
func (a *activeRequest) release(immediate bool) {
	if a == nil {
		return
	}
	if a.cancel != nil {
		a.cancel()
	}
	if a.dispose != nil {
		a.dispose()
	}
	if immediate && a.abandon != nil {
		a.abandon()
	}
}

// NewServer wires a Server to sock. It does not start reading; call Serve to run the read
// loop until the socket closes or ctx is done. Initialize is sent immediately unless
// DeferInit is set.
func NewServer(sock Socket, ctx context.Context, opts Options) *Server {
	if opts.TimeoutDelayMS <= 0 {
		opts.TimeoutDelayMS = DefaultTimeoutDelayMS
	}
	if opts.MaxConcurrentCalls <= 0 {
		opts.MaxConcurrentCalls = DefaultMaxConcurrentCalls
	}
	connCtx, cancel := context.WithCancel(ctx)
	s := &Server{
		sock:            sock,
		opts:            opts,
		ctx:             connCtx,
		cancel:          cancel,
		channels:        map[string]Channel{},
		activeRequests:  map[int]*activeRequest{},
		pendingRequests: map[string][]*pendingRequest{},
		calls:           make(chan struct{}, opts.MaxConcurrentCalls),
	}
	// The writer's overflow handler closes the connection: the peer stopped reading, so
	// nothing on this connection can make progress and holding the goroutines would only
	// accumulate. Closing cancels the context, which unblocks the writer that is stuck in
	// the socket write, and lets the client reconnect.
	s.writer = newWriter(s.ctx, sock, opts.OutboundQueueSize, opts.EnqueueTimeout, func() {
		s.abort(ErrWriteBackpressure)
	})
	if !opts.DeferInit {
		// Enqueued, not written inline: a peer that has stopped reading must not be able
		// to block construction, so a stalled connection can still be built and torn down.
		s.sendResponse(ResponseInitialize, 0, Undefined, nil)
	}
	return s
}

// abort tears the connection down because of a write-path failure. It is separate from
// Close because it runs from inside the writer's own overflow callback: it must never wait
// on the writer goroutine, which is the goroutine calling it.
func (s *Server) abort(cause error) {
	s.failOnce.Do(func() { s.failure = cause })
	s.Close()
	_ = s.sock.Close()
}

// RegisterChannel makes a channel available and flushes any requests parked for its name.
// This mirrors registerChannel, which defers the flush to a microtask only so the caller can
// finish wiring; here there is nothing to defer past a lock, so the flush is synchronous and
// a request that arrived before registration is served in arrival order.
func (s *Server) RegisterChannel(name string, ch Channel) {
	s.mu.Lock()
	s.channels[name] = ch
	pending := s.pendingRequests[name]
	delete(s.pendingRequests, name)
	s.mu.Unlock()

	for _, req := range pending {
		req.timer.Stop()
		s.handleRequest(req.header, req.body)
	}
}

// Ready sends Initialize; the deferred counterpart of the constructor's eager send.
func (s *Server) Ready() { s.sendResponse(ResponseInitialize, 0, Undefined, nil) }

// Serve runs the read loop until the socket ends or the connection context is cancelled. It
// tears everything down on return, cancelling all in-flight calls and listeners.
//
// The returned error is the reason the connection ended. When the connection was aborted
// because the peer stopped reading, that cause (ErrWriteBackpressure) is returned instead of
// the socket error it produced, so a caller logging it says "the peer stalled" rather than
// "EOF", which is the difference between an actionable message and a shrug.
func (s *Server) Serve(ctx context.Context) error {
	reader := NewMessageReader(s.sock, DefaultMaxFrameBytes)
	defer s.Close()
	for {
		msg, err := reader.Read(ctx)
		if err != nil {
			return s.serveError(err)
		}
		header, body, err := DecodeRequest(msg)
		if err != nil {
			// A malformed message cannot be attributed to a request id, so the connection
			// cannot be resynchronised: close rather than guess.
			return err
		}
		s.handleRequest(header, body)
	}
}

// serveError prefers a recorded abort cause over the socket error that ended the loop.
func (s *Server) serveError(err error) error {
	if s.failure != nil {
		return s.failure
	}
	return err
}

// handleRequest routes one decoded request by header type.
func (s *Server) handleRequest(header RequestHeader, body Value) {
	switch header.Type {
	case RequestPromise:
		s.onPromise(header, body)
	case RequestEventListen:
		s.onEventListen(header, body)
	case RequestPromiseCancel, RequestEventDispose:
		// Only header[1] (the id) is meaningful for these.
		s.disposeActiveRequest(header.ID)
	default:
		// The TS switch has no default; an unknown type is ignored. Do the same.
	}
}

// onPromise starts (or re-dispatches) a Promise call.
func (s *Server) onPromise(header RequestHeader, body Value) {
	ch, ok := s.channel(header.ChannelName)
	if !ok {
		s.collectPendingRequest(header, body)
		return
	}

	// Bound the number of concurrent handler goroutines. Acquiring here (on the read loop)
	// would block the loop when the semaphore is full, which is the one thing the read loop
	// must never do. Instead the call is spawned and the goroutine acquires; if the
	// semaphore is exhausted the new goroutine simply waits, still off the read loop.
	callCtx, cancel := context.WithCancel(s.ctx)
	active := &activeRequest{cancel: cancel}
	args := toArgs(body)

	// Register before the goroutine starts so a 101 that arrives in the same instant still
	// finds the request. A duplicate id evicts the older call: both would answer on the same
	// id and the client could not tell the two responses apart, so the newest call owns the
	// id and the older one is cancelled (its handler observes the cancelled context and its
	// response is the cancellation error, which is what the TS client discards too).
	if evicted := s.beginRequest(header.ID, active); evicted != nil {
		evicted.release(true)
	}

	go func() {
		// Wait for a slot without blocking the reader. A cancelled call releases its slot
		// immediately rather than waiting behind queued work.
		select {
		case s.calls <- struct{}{}:
			defer func() { <-s.calls }()
		case <-callCtx.Done():
			// Cancelled before it ever ran: report so the client's promise settles instead
			// of hanging until the connection dies.
			s.sendPromiseError(header.ID, callCtx.Err())
			s.finishRequest(header.ID, active)
			return
		}

		result, err := ch.Call(callCtx, header.Name, args)
		if err != nil {
			s.sendCallError(header.ID, err)
		} else {
			s.sendResponse(ResponsePromiseSuccess, header.ID, bodyOrUndefined(result), callCtx.Done())
		}
		s.finishRequest(header.ID, active)
	}()
}

// sendPromiseError settles a call that never produced a handler error of its own (cancelled
// before start, connection closed). A cancelled call is reported as a plain error so the
// client rejects rather than hangs.
func (s *Server) sendPromiseError(id int, err error) {
	s.sendResponse(ResponsePromiseError, id, errorPayload(err), nil)
}

// sendCallError maps a handler error onto the wire: an *Error (or a chain containing one)
// becomes PromiseError (202) with the structured payload; anything else also becomes
// PromiseError because Go errors always carry a message. PromiseErrorObj (203) is for a
// non-Error rejection value, which only a handler that wants the raw value can produce — it
// signals this by returning a channel that yields the object to send, modeled here by
// returning a *RawError.
func (s *Server) sendCallError(id int, err error) {
	var raw *RawError
	if errors.As(err, &raw) {
		s.sendResponse(ResponsePromiseErrorObj, id, bodyOrUndefined(raw.Value), nil)
		return
	}
	s.sendPromiseError(id, err)
}

// disposeActiveRequest handles 101 (cancel) and 103 (dispose): both cancel and remove the
// entry for the id, exactly like disposeActiveRequest in the TS.
func (s *Server) disposeActiveRequest(id int) {
	s.mu.Lock()
	active, ok := s.activeRequests[id]
	if ok {
		delete(s.activeRequests, id)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	active.release(false)
}

// beginRequest installs active under id and returns whatever it displaced, so the caller can
// release the evicted request instead of leaving two live requests sharing one id.
func (s *Server) beginRequest(id int, active *activeRequest) *activeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	evicted, had := s.activeRequests[id]
	s.activeRequests[id] = active
	if !had {
		return nil
	}
	return evicted
}

// finishRequest removes a completed request's bookkeeping. It only removes the entry when it
// is still the caller's own: with id reuse a finished old call must not delete the new call's
// entry, which would leave the new call uncancellable. The call's context is released so a
// context.WithCancel never leaks.
func (s *Server) finishRequest(id int, active *activeRequest) {
	s.mu.Lock()
	if current, ok := s.activeRequests[id]; ok && current == active {
		delete(s.activeRequests, id)
	}
	s.mu.Unlock()
	if active != nil && active.cancel != nil {
		active.cancel()
	}
}

// channel looks up a registered channel.
func (s *Server) channel(name string) (Channel, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.channels[name]
	return ch, ok
}

// sendResponse encodes one response into a Regular frame and hands it to the connection's
// writer.
//
// Framing happens here, at the single write path, so no caller can forget it: the peer reads
// with MessageReader, which expects the 13-byte header and ignores anything else.
//
// `abandon` lets a caller give up waiting for queue room; pass nil when the caller has
// nothing to abandon (the writer's own timeout then bounds the wait). A delivery goroutine
// passes its listener's stop channel so a subscription that has already ended is not held up
// by a full queue.
//
// A serialization failure drops the response, exactly as the TS `try { protocol.send } catch
// {}` does — a payload the codec cannot encode has no wire representation and the TS
// swallows it rather than closing a connection that is otherwise healthy.
func (s *Server) sendResponse(respType, id int, body Value, abandon <-chan struct{}) {
	payload, err := EncodeResponse(ResponseHeader{Type: respType, ID: id}, body)
	if err != nil {
		return
	}
	s.writer.enqueue(EncodeFrame(Frame{Type: MsgRegular, Data: payload}), abandon)
}

// Close tears down the connection: cancels every in-flight call and listener and marks the
// server closed. It is idempotent and safe to call from Serve's defer and from a handler.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	active := make([]*activeRequest, 0, len(s.activeRequests))
	for _, a := range s.activeRequests {
		active = append(active, a)
	}
	s.activeRequests = map[int]*activeRequest{}
	s.mu.Unlock()

	for _, a := range active {
		a.release(true)
	}
	s.cancel()
	// Stop the writer AFTER cancelling the context, so a writer parked inside a socket
	// write is released by the cancellation rather than waited on.
	s.writer.stop()
}
