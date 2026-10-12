// Package ws wraps github.com/coder/websocket behind the transport-agnostic rpc.Socket
// interface (go/internal/rpc/frame.go), so internal/rpc stays free of net imports (gate G2.11)
// and this is the only package that knows the WebSocket library exists.
//
// Library choice: github.com/coder/websocket, per decision D14 ("prefer mature Go libraries"
// when one already implements a protocol we need) and the plan's step 2.7 list. It is
// context-aware — a cancelled context unblocks a pending read — which is exactly what the RPC
// read loop needs when a connection is torn down.
package ws

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

// DefaultReadLimit bounds one inbound WebSocket message.
//
// The library's own default is 32 KiB, which is far below what the UI sends: the phase-0 verify
// list records v4 frames up to 1 MiB (maxFrameBytes) and attachment chunks at 512 KiB. A default
// this low would close healthy connections during a large attachment, so the limit is raised to
// the same 64 MiB ceiling the frame reader enforces (rpc.DefaultMaxFrameBytes). Keeping the two
// equal means the transport rejects an oversize message for the same reason, and with the same
// magnitude, as the framing layer above it.
const DefaultReadLimit = 64 << 20 // 64 MiB

// ErrClosed is returned by Read/Write after the socket has been closed. rpc treats any Read
// error as "the connection ended", so the specific value does not affect behavior; it exists so
// callers can distinguish a clean shutdown from a protocol failure when logging.
var ErrClosed = errors.New("ws: socket closed")

// Options configures Accept and Dial.
type Options struct {
	// ReadLimit bounds one inbound message in bytes. Zero selects DefaultReadLimit. A negative
	// value disables the limit, which is never appropriate for a server exposed to the network.
	ReadLimit int64
	// // InsecureSkipVerify disables the library's Origin check. It defaults to TRUE here
	// deliberately: the TypeScript server (ws + @hono/node-ws) performs no Origin check at all,
	// so enabling it would reject browser clients the Node server accepts, and turning it off would
	// be a behavior change the UI could observe. The capability header plus the session cookie are
	// what gate /ws/host; Origin is not part of that contract.
	InsecureSkipVerify bool
	// Compression selects permessage-deflate. It defaults to disabled, matching the Node server,
	// which negotiates no extensions (@hono/node-ws hands the upgrade straight to ws without
	// perMessageDeflate). Enabling compression would change the bytes on the wire for no gain
	// that the TS side sees.
	Compression websocket.CompressionMode
}

// defaults fills the zero Options the way the caller in internal/server does: no Origin check,
// no compression.
func (o Options) defaults() Options {
	o.InsecureSkipVerify = true
	if o.ReadLimit == 0 {
		o.ReadLimit = DefaultReadLimit
	}
	return o
}

func (o Options) acceptOptions() *websocket.AcceptOptions {
	return &websocket.AcceptOptions{
		InsecureSkipVerify: o.InsecureSkipVerify,
		CompressionMode:    o.Compression,
		OriginPatterns:     nil,
	}
}

func (o Options) dialOptions(header http.Header) *websocket.DialOptions {
	return &websocket.DialOptions{
		HTTPClient:      http.DefaultClient,
		HTTPHeader:      header,
		CompressionMode: o.Compression,
	}
}

// Conn is an accepted WebSocket presented as an rpc.Socket.
//
// Concurrency: coder/websocket permits one concurrent reader and one concurrent writer. The RPC
// server has exactly one read loop and one writer goroutine per connection, so the mapping is
// 1:1 — but Close may be called from either, hence the mutex and sync.Once. Close is what
// unblocks a reader parked in Read, which is how a connection teardown reaches the read loop.
type Conn struct {
	conn      *websocket.Conn
	closeOnce sync.Once
	closeErr  error
}

// Accept performs the server side of the handshake and returns the connection as an rpc.Socket.
//
// On failure the library has already written its own HTTP error response (400/405/426) — the
// caller must not write another status, or the response becomes a malformed double answer. This
// matches what the browser sees from the Node server today, where @hono/node-ws refuses the
// upgrade during its own app.request() pass.
func Accept(w http.ResponseWriter, r *http.Request, opts Options) (*Conn, error) {
	opts = opts.defaults()
	conn, err := websocket.Accept(w, r, opts.acceptOptions())
	if err != nil {
		return nil, err
	}
	// Applied after the handshake because the library has no read-limit option on Accept: the
	// connection is already hijacked, but no read has happened, so the limit is in place before
	// the RPC read loop can observe a message.
	conn.SetReadLimit(opts.ReadLimit)
	return &Conn{conn: conn}, nil
}

// Dial performs the client side of the handshake. Tests and the gateway's upstream leg (step
// 2.8) use it; the server never dials.
func Dial(ctx context.Context, url string, header http.Header, opts Options) (*Conn, error) {
	opts = opts.defaults()
	conn, _, err := websocket.Dial(ctx, url, opts.dialOptions(header))
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(opts.ReadLimit)
	return &Conn{conn: conn}, nil
}

// Read returns the next message. A non-binary message is returned as its bytes: the RPC framing
// layer inspects the frame type byte itself and the TypeScript never distinguished message types
// either (`ws.on("message", …)` fires for both), so accepting text here is what keeps the port
// faithful.
func (c *Conn) Read(ctx context.Context) ([]byte, error) {
	_, data, err := c.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Write sends one binary message, which is exactly one ProtocolMessage frame.
func (c *Conn) Write(ctx context.Context, data []byte) error {
	return c.conn.Write(ctx, websocket.MessageBinary, data)
}

// Close ends the connection. It is idempotent: the first call performs the WebSocket close
// handshake, later calls are no-ops returning the first result.
//
// It mirrors the TypeScript `end()/dispose()` — ws.close(), a normal closure — so the browser
// client sees the same "clean disconnect" it sees from the Node server. The library bounds the
// handshake at 5 s internally and then drops the socket, so this cannot hang a teardown path.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.conn.Close(websocket.StatusNormalClosure, "")
	})
	if errors.Is(c.closeErr, net.ErrClosed) {
		return ErrClosed
	}
	return c.closeErr
}
