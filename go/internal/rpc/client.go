package rpc

import (
	"context"
	"fmt"
	"sync"
)

// This file is the Go port of ChannelClient (packages/rpc/src/channelClient.ts): the
// client half of the channel RPC. The gateway (step 2.8) needs it to talk to a Node
// upstream at the RPC level, and `nex-trace replay-rpc` uses it to replay R01-R11 against
// the real Node server.
//
// Ownership:
//   - The read loop is the only goroutine that decodes responses and the only one that
//     settles calls and fires events. Everything it touches is under mu.
//   - nextID starts at 0 and only increments, exactly like `lastRequestId`, because the
//     frame sequence a client produces IS part of the recorded-trace contract.
//   - Sends go through the shared bounded writer (writer.go), so a peer that stops reading
//     applies back-pressure to producers instead of wedging the read loop.
//   - terminate is the single teardown path. It is idempotent and never waits for the
//     read loop: a reader parked in Socket.Read is released by closing the socket.

// Client speaks channel RPC over one connection.
//
// It is the caller-facing half of a connection: the gateway's upstream leg and the replay
// tooling. Requests are only sent once the server has sent Initialize, so a caller may
// issue a call immediately after NewClient without racing the handshake.
type Client struct {
	sock   Socket
	ctx    context.Context
	cancel context.CancelFunc
	writer *writer

	mu        sync.Mutex
	nextID    int
	pending   map[int]*pendingCall
	listeners map[int]*clientListener
	failure   error
	closed    bool

	// initDone is closed once Initialize arrived (initErr == nil) or the connection ended
	// first (initErr == the cause). Every request parks here before it is written, which is
	// the TS `whenInitialized()` deferral in requestPromise/requestEvent.
	initDone chan struct{}
	initOnce sync.Once
	initErr  error

	endOnce sync.Once
	// done is closed when the read loop has returned.
	done chan struct{}
}

// NewClient wires a Client to sock and starts its read loop.
//
// No error is returned because there is nothing to fail on yet: the server's Initialize is
// still in flight, and a peer that never sends one surfaces as the failure of the first
// Call or Listen. That is deliberate — a connection can be constructed before the peer is
// reachable, and making construction wait would make "connect then register channels" the
// caller's sequencing problem.
func NewClient(sock Socket) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		sock:      sock,
		ctx:       ctx,
		cancel:    cancel,
		pending:   map[int]*pendingCall{},
		listeners: map[int]*clientListener{},
		initDone:  make(chan struct{}),
		done:      make(chan struct{}),
	}
	// A peer that stops reading must not turn a slow client into an unbounded queue; the
	// writer's overflow callback is the connection's abort path, exactly as on the server.
	c.writer = newWriter(ctx, sock, 0, 0, func() { c.abort(ErrWriteBackpressure) })
	go c.readLoop()
	return c
}

// Close tears the connection down: every in-flight call fails with ErrConnectionClosed,
// every listener channel is closed, and the socket is closed so the read loop stops. It is
// idempotent and does not wait for the loop.
func (c *Client) Close() {
	c.abort(ErrConnectionClosed)
}

// Done is closed when the read loop has returned, which happens after the socket closes or
// a frame cannot be decoded. A caller that needs to know a connection really ended (rather
// than that it asked it to) waits here.
func (c *Client) Done() <-chan struct{} { return c.done }

// NextID reports the id the next request will use. It exists for diagnostics and for tests
// that assert on the id sequence, which is observable in the recorded traces.
func (c *Client) NextID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nextID
}

// listenerCount reports how many event subscriptions are live. Diagnostics and tests only.
func (c *Client) listenerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.listeners)
}

// Call invokes channel.method with a positional argument list.
//
// args is sent as the request body, which the server unpacks into positional arguments
// (`target.apply(handler, args || [])`); an empty list is sent as undefined, the TS
// client's `arg?: any` default.
//
// A call made before Initialize is sent is parked until the handshake completes. Cancelling
// ctx before the request is written sends nothing; cancelling afterwards sends the 101 and
// returns ctx.Err(). The request id is consumed either way, so a cancelled call still
// advances the sequence exactly as it does in the TS client.
func (c *Client) Call(ctx context.Context, channel, method string, args []Value) (Value, error) {
	return c.call(ctx, channel, method, argsOrUndefined(args))
}

// call is Call with the request body chosen by the caller. It exists because the recorded
// traces were produced by a TS client that sends whatever `arg` the stub expected — a bare
// object for a single-argument call — and replaying them needs that exact shape, not the
// normalized argument array. `Call` always sends the array form that the proxy convention
// (`target.apply`) expects, which is what a Go caller wants; `call` is for the replay
// tooling, which must reproduce a recorded frame byte for byte.
func (c *Client) call(ctx context.Context, channel, method string, body Value) (Value, error) {
	if err := c.open(); err != nil {
		return nil, err
	}
	// `const id = this.lastRequestId++` runs BEFORE the cancellation check in the TS
	// client, and the id sequence is observable in the recorded frames, so the allocation
	// stays ahead of the check.
	id := c.allocateID()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	call := &pendingCall{done: make(chan struct{})}
	if !c.register(id, call) {
		return nil, c.cause()
	}

	if err := c.awaitInit(ctx); err != nil {
		c.abandon(id, call, err)
		return nil, err
	}
	// Cancelled or closed while parked on Initialize: the answer is already settled, so
	// report it instead of overwriting it with the cancellation.
	if !c.stillPending(id, call) {
		return call.result, call.err
	}
	if !c.sendPromise(id, call, channel, method, body) {
		<-call.done
		return call.result, call.err
	}

	select {
	case <-call.done:
		return call.result, call.err
	case <-ctx.Done():
		if c.abandon(id, call, ctx.Err()) {
			// The frame is on the wire, so the server must be told — the TS client's
			// onCancellationRequested sends PromiseCancel for the same reason.
			c.sendCancelOrDispose(RequestPromiseCancel, id)
			return nil, ctx.Err()
		}
		<-call.done
		return call.result, call.err
	}
}

// Listen subscribes to an event and returns the channel its values arrive on plus the
// dispose func.
//
// The request frame is sent once Initialize has arrived; Listen itself never blocks on it,
// matching the TS emitter, which defers the same work to the first listener. dispose sends
// the 103 and closes the channel, and is safe to call more than once.
func (c *Client) Listen(ctx context.Context, channel, event string, arg Value) (<-chan Value, func(), error) {
	if err := c.open(); err != nil {
		return nil, nil, err
	}
	id := c.allocateID()
	l := &clientListener{out: make(chan Value, EventBufferSize)}
	if !c.registerListener(id, l) {
		return nil, nil, c.cause()
	}

	dispose := sync.OnceFunc(func() {
		if c.closeListener(id, l) {
			c.sendCancelOrDispose(RequestEventDispose, id)
		}
	})

	go c.startListen(id, channel, event, arg, l)
	return l.out, dispose, nil
}

// startListen sends the EventListen frame once the handshake is done, unless the listener
// was disposed in the meantime. It runs on its own goroutine — the same deferral the TS
// emitter performs — and exits on either Initialize or teardown, so a connection that dies
// before the handshake leaves nothing parked.
func (c *Client) startListen(id int, channel, event string, arg Value, l *clientListener) {
	select {
	case <-c.initDone:
	case <-c.ctx.Done():
		return
	}
	if c.initErr != nil {
		return // the connection failed before Initialize; terminate already closed out
	}
	if !c.stillListening(id, l) {
		return
	}
	payload, err := EncodeRequest(RequestHeader{
		Type: RequestEventListen, ID: id, ChannelName: channel, Name: event,
	}, arg)
	if err != nil {
		return // the codec has no wire form for this argument; the TS swallows it too
	}
	c.write(payload)
}

// --- read loop ------------------------------------------------------------------------

func (c *Client) readLoop() {
	defer close(c.done)
	reader := NewMessageReader(c.sock, DefaultMaxFrameBytes)
	for {
		msg, err := reader.Read(c.ctx)
		if err != nil {
			c.abort(err)
			return
		}
		header, body, err := DecodeResponse(msg)
		if err != nil {
			// A response that cannot be decoded cannot be attributed to a request id, so
			// the stream cannot be resynchronised: close rather than guess.
			c.abort(err)
			return
		}
		c.dispatch(header, body)
	}
}

// dispatch routes one response by type, mirroring onBuffer/onResponse. A response for an
// id that is not pending is dropped: a cancelled call's late answer and a listener's fire
// after dispose are both normal, and the TS `this.handlers.get(id)?.(response)` drops them
// the same way.
func (c *Client) dispatch(header ResponseHeader, body Value) {
	switch header.Type {
	case ResponseInitialize:
		c.markInit(nil)
	case ResponsePromiseSuccess:
		c.settleCall(header.ID, body, nil)
	case ResponsePromiseError:
		c.settleCall(header.ID, nil, errorFromPayload(body))
	case ResponsePromiseErrorObj:
		// A rejection that is not an Error, delivered verbatim — the TS `throw "plain
		// string"` case. It is returned as the same RawError a handler uses to produce one,
		// so `errors.As` recovers the raw value on this side too.
		c.settleCall(header.ID, nil, &RawError{Value: body})
	case ResponseEventFire:
		c.fireEvent(header.ID, body)
	default:
		// The TS switch has no default; an unknown type is ignored.
	}
}

func (c *Client) settleCall(id int, result Value, err error) {
	c.mu.Lock()
	call, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if !ok {
		return
	}
	call.settle(result, err)
}

// fireEvent delivers one event, or gives up on a listener that is not draining.
//
// The delivery is a non-blocking send done UNDER Client.mu, which is what makes it safe: both
// close(l.out) — in dispose and in teardown — happen only after the listener has been removed
// from the map, and the send can only find the listener while it is still in the map. So a
// send and a close can never overlap, and no lock ordering is needed to get there.
//
// The same lock makes the overflow path atomic: the removal, and therefore the ownership of
// the 103, cannot be lost to a dispose racing it.
func (c *Client) fireEvent(id int, value Value) {
	c.mu.Lock()
	l, ok := c.listeners[id]
	if !ok {
		c.mu.Unlock()
		return
	}
	select {
	case l.out <- value:
		c.mu.Unlock()
		return
	default:
	}
	// See clientListener: overflow disposes the subscription rather than blocking the read
	// loop. Closing under the lock keeps the send above and this close mutually exclusive.
	delete(c.listeners, id)
	l.close()
	c.mu.Unlock()

	c.sendCancelOrDispose(RequestEventDispose, id)
}

// --- teardown ------------------------------------------------------------------------

// abort ends the connection with cause and closes the socket. It runs from the read loop,
// from the writer's overflow callback and from Close, so it must never wait on either of
// those goroutines.
func (c *Client) abort(cause error) {
	c.terminate(cause)
	_ = c.sock.Close()
}

// terminate is the single teardown path: fail everything that is waiting, release the
// Initialize waiters, and stop the writer. Idempotent.
func (c *Client) terminate(cause error) {
	c.endOnce.Do(func() {
		failure := fmt.Errorf("%w: %w", ErrConnectionClosed, cause)

		c.mu.Lock()
		c.closed = true
		c.failure = failure
		calls := c.pending
		c.pending = map[int]*pendingCall{}
		listeners := c.listeners
		c.listeners = map[int]*clientListener{}
		// Closed under the lock, like closeListener: fireEvent sends while holding it, so a
		// teardown must not close a channel a concurrent delivery is sending on.
		for _, l := range listeners {
			l.close()
		}
		c.mu.Unlock()

		// A request parked on Initialize has no entry in pending only after it is settled,
		// so the waiters are released here — otherwise they would wait for a handshake that
		// is never coming.
		c.markInit(failure)
		for _, call := range calls {
			call.settle(nil, failure)
		}
		// Cancel BEFORE stopping the writer, so a writer parked inside a socket write is
		// released by the cancellation rather than waited on.
		c.cancel()
		c.writer.stop()
	})
}
