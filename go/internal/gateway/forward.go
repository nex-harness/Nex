package gateway

// forward.go is the per-connection gateway: it owns one client connection's rpc.Server, one
// upstream leg, and the id→route map that decides what a cancel applies to.
//
// Event order per accepted connection:
//
//	1. the routing table is read (once, at connection start) and frozen
//	2. the upstream leg is opened in the client's mode ── fails ──► close the client with 1013
//	3. rpc.Server is built and sends Initialize (200) to the client — exactly one, always
//	4. two loops run until either side ends:
//	     client→Go/upstream: inspect the header, forward the ORIGINAL bytes or dispatch locally
//	     upstream→client:   forward the original bytes, except the upstream's Initialize
//	5. when either loop ends, both sockets close. No response is ever fabricated.
//
// The read loop is the gateway's own rather than rpc.Server's, because routing has to happen
// BEFORE dispatch: a frame for a Node-owned channel must never reach a Go handler, and a frame for
// a Go-owned channel must never reach Node. rpc.Server therefore appears here only as the local
// dispatcher for frames the router kept.

import (
	"context"
	"errors"
	"sync"

	"github.com/tylinux/nex/go/internal/rpc"
)

// ConnectionMode names the endpoint a client connected to. It decides both the upstream endpoint
// and the per-connection scope Node builds for it.
type ConnectionMode string

// The two endpoints, mirroring server.ModeWebRemoteReplayable / server.ModeDesktopContinuous.
const (
	// ModeWeb is /ws: always terminal-client on the Node side.
	ModeWeb ConnectionMode = ModeWebRemoteReplayable
	// ModeHost is /ws/host: the trusted host relay.
	ModeHost ConnectionMode = ModeDesktopContinuous
)

// Connection is one client connection, its upstream leg and its frozen routing decision.
type Connection struct {
	router *Router
	up     *Upstream
	client *lockedSocket
	log    Logger
	mode   ConnectionMode

	// server dispatches the frames the router kept. Built by HandleConnection.
	server *rpc.Server

	// mu guards routes and closed. routes answers one question only: was request id N forwarded
	// upstream? A 101/103 is forwarded iff it is, which is what stops a cancel for a Go-owned
	// call from reaching Node as a cancel for an unrelated id Node happens to be using.
	mu     sync.Mutex
	routes map[int]struct{}
	closed bool
	// forwarded counts the client frames this connection passed upstream. A test asserts the leg
	// saw exactly the frames Go decided to forward and nothing else.
	forwarded int
}

// connectionOptions are the per-connection inputs.
type connectionOptions struct {
	router *Router
	up     *Upstream
	client *lockedSocket
	log    Logger
	mode   ConnectionMode
}

// newConnection builds a Connection, failing on a nil router or upstream rather than at first
// use: both are read on the first frame, and a nil dereference there would be far harder to read.
func newConnection(opts connectionOptions) (*Connection, error) {
	if opts.router == nil {
		return nil, errors.New("gateway: nil router")
	}
	if opts.up == nil {
		return nil, ErrNoUpstream
	}
	if opts.client == nil {
		return nil, errors.New("gateway: nil client socket")
	}
	return &Connection{
		router: opts.router,
		up:     opts.up,
		client: opts.client,
		log:    opts.log,
		mode:   opts.mode,
		routes: map[int]struct{}{},
	}, nil
}

// Forwarded reports how many client frames this connection passed to the upstream.
func (c *Connection) Forwarded() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.forwarded
}

// Run drives both directions until either ends, then tears the connection down. It returns why
// the connection ended; the caller (Gateway.Handle) closes the client socket, and no response is
// ever written from here — nothing here knows the request in a form it could answer.
//
// The upstream ending is the case that needs care: the client socket is still open, so the read
// loop would sit in Read forever and the goroutines behind it would never be released. So the
// upstream goroutine closes the CLIENT socket on its way out. That is the plan's failure semantics
// — "upstream drops mid-connection → close the client socket (the UI reconnects)" — and it is why
// a dropped upstream costs exactly one goroutine per dead connection instead of one per reconnect.
func (c *Connection) Run(ctx context.Context) error {
	errs := make(chan error, 2)

	// Upstream → client. One goroutine, one writer to the client (the shared lockedSocket), so
	// frames for one id keep their order on the way back.
	go func() {
		err := c.up.Run(ctx, c.fromUpstream)
		// The upstream is gone: nothing can answer the client any more, so its socket ends here.
		// The caller's Close is idempotent, so a client-initiated close racing this one is fine.
		_ = c.client.Close()
		errs <- err
	}()

	// Client → Go and client → upstream, in the calling goroutine.
	readErr := c.readClient(ctx)

	// One side ended; stop the other. Close unblocks the reader parked in Read.
	c.dispose()
	upErr := <-errs
	if readErr != nil {
		return readErr
	}
	return upErr
}

// readClient is the single client→upstream forwarder.
//
// It reads RAW frames, not RPC payloads: MessageReader would strip the 13-byte ProtocolMessage
// header, and the upstream needs those bytes back — Node's SocketProtocol expects a framed
// message, and re-framing from the payload would rebuild a header the client chose (its id, its
// ack) rather than the one it actually sent. So the reader is DecodeFrameLimit over a raw
// MessageReader-free stream, and the frame is passed onward untouched.
//
// One goroutine writing to the upstream is what makes the client's frame order the upstream's
// frame order: a 101 can never arrive at Node before the 100 it cancels, because this loop wrote
// them in that order.
func (c *Connection) readClient(ctx context.Context) error {
	reader := rpc.NewMessageReader(c.client, rpc.DefaultMaxFrameBytes)
	for {
		frame, err := reader.ReadFrame(ctx)
		if err != nil {
			return err
		}
		if err := c.route(ctx, frame); err != nil {
			return err
		}
	}
}

// route decides one frame's owner and acts on it.
//
// Only the request header is decoded (see inspectPayload). A Node-owned frame is written upstream as
// the client's own bytes; a Go-owned frame is handed to the local rpc.Server, which decodes it again —
// the double decode is the price of the router sitting outside the dispatcher, and it is only on
// the Go path, where the handler is about to decode the body anyway.
func (c *Connection) route(ctx context.Context, frame []byte) error {
	payload := frame[rpc.FrameHeaderSize:]
	info := inspectPayload(payload)
	if !info.OK {
		// Not decodable channel traffic. This is the same shape the channel server's own reader
		// would see, so pass it through unchanged and let the codec decide.
		return c.dispatch(ctx, payload)
	}
	forward := false
	switch info.RequestType {
	case rpc.RequestPromise, rpc.RequestEventListen:
		forward = !c.router.ChannelOwnedByGo(info.ChannelName)
	case rpc.RequestPromiseCancel, rpc.RequestEventDispose:
		// Forwarded iff the request it names was forwarded. The route cannot be re-derived here:
		// 101/103 carry only [type, id], with no channel.
		forward = c.wasForwarded(info.RequestID)
	default:
		return nil
	}
	if !forward {
		return c.dispatch(ctx, payload)
	}
	return c.forward(ctx, frame, info)
}

// forward writes the client's own frame upstream and records the id as forwarded.
//
// The route is recorded BEFORE the write: a 101 that names this id is only meaningful once Node
// could have seen the 100, and the client may send it in the very next message. Recording first
// costs nothing when the write then fails — the connection ends, and the map dies with it.
func (c *Connection) forward(ctx context.Context, frame []byte, info frameInspect) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("gateway: connection is closed")
	}
	c.routes[info.RequestID] = struct{}{}
	c.forwarded++
	c.mu.Unlock()
	// The client's own bytes, header included: Node's SocketProtocol reads a framed message and
	// the plan requires the SAME frame to reach it, not a re-encoding of its payload.
	return c.up.Write(ctx, frame)
}

// wasForwarded reports whether the request with this id went upstream.
func (c *Connection) wasForwarded(id int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.routes[id]
	return ok
}

// dispatch hands a Go-owned frame to the local channel server.
func (c *Connection) dispatch(ctx context.Context, payload []byte) error {
	if c.server == nil {
		return nil
	}
	return c.server.HandleMessage(ctx, payload)
}

// fromUpstream moves one Node frame to the client, swallowing the upstream's Initialize.
//
// Go already sent Initialize when the rpc.Server was built, so forwarding Node's would give the
// client two handshake frames — an observable difference from the Node server, where there is
// exactly one. Swallowing it here is what the plan's order guarantee names.
func (c *Connection) fromUpstream(message []byte) error {
	if isInitializeMessage(message) {
		return nil
	}
	return c.client.Write(context.Background(), message)
}

// dispose closes the upstream leg and stops recording routes. The client socket belongs to the
// caller, which closes it after this returns.
func (c *Connection) dispose() {
	c.mu.Lock()
	already := c.closed
	c.closed = true
	c.routes = map[int]struct{}{}
	c.mu.Unlock()
	if already {
		return
	}
	if c.server != nil {
		c.server.Close()
	}
	if err := c.up.Close(); err != nil && c.log != nil {
		c.log.Debug("gateway: upstream close failed", "mode", string(c.mode), "error", err)
	}
}

// lockedSocket serializes writes to the client WebSocket.
//
// rpc.Server's writer goroutine and the gateway's upstream→client loop both write to the same
// socket, and a WebSocket permits exactly one concurrent writer — interleaved frames would
// corrupt the stream in a way that looks like a codec bug. One mutex around the whole write is
// what makes "frames for one id keep their order" true across both directions.
type lockedSocket struct {
	inner rpc.Socket
	mu    sync.Mutex
}

func newLockedSocket(inner rpc.Socket) *lockedSocket { return &lockedSocket{inner: inner} }

func (s *lockedSocket) Read(ctx context.Context) ([]byte, error) { return s.inner.Read(ctx) }

func (s *lockedSocket) Write(ctx context.Context, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.Write(ctx, data)
}

func (s *lockedSocket) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.Close()
}
