package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/rpc/rpctest"
)

// This file is the client half of the step 2.3 tests: a test driver that speaks the same
// framing the TypeScript ChannelClient does (EncodeRequest framed into a Frame, responses
// decoded with DecodeResponse), plus a small stub Channel for the table tests. The driver
// deliberately does NOT use a full Channel client — that is step 2.4 — so these tests pin
// the wire behaviour the server must produce, one frame at a time.

// testConn wires a Server to an in-memory peer and collects the frames the server writes.
type testConn struct {
	server   *Server
	peer     *rpctest.Endpoint
	serverIO *rpctest.Endpoint
	t        *testing.T
	cancel   context.CancelFunc
	done     chan struct{}

	mu     sync.Mutex
	frames []Frame
}

// newTestConn starts a Server over one side of an in-memory pair. The returned testConn
// reads frames the server sends on peer.
func newTestConn(t *testing.T, opts Options) *testConn {
	t.Helper()
	pair := rpctest.NewPair()
	ctx, cancel := context.WithCancel(context.Background())
	srv := NewServer(pair.A, ctx, opts)

	tc := &testConn{
		server:   srv,
		peer:     pair.B,
		serverIO: pair.A,
		t:        t,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	go func() {
		defer close(tc.done)
		tc.collectFrames()
	}()
	// Serve takes the cancellable context, so cancelling it unblocks the reader even
	// though the in-memory socket itself stays open.
	go func() {
		defer cancel()
		_ = srv.Serve(ctx)
	}()
	t.Cleanup(tc.Close)
	return tc
}

// collectFrames reads every Regular frame the server writes until the socket closes.
func (tc *testConn) collectFrames() {
	reader := NewMessageReader(tc.peer, DefaultMaxFrameBytes)
	for {
		msg, err := reader.Read(context.Background())
		if err != nil {
			return
		}
		tc.mu.Lock()
		tc.frames = append(tc.frames, Frame{Type: MsgRegular, Data: msg})
		tc.mu.Unlock()
	}
}

// Close tears down the connection: it cancels the Serve context, closes both endpoints so
// neither reader stays parked, and waits for the collector goroutine to exit.
func (tc *testConn) Close() {
	tc.cancel()
	tc.serverIO.Close()
	tc.peer.Close()
	select {
	case <-tc.done:
	case <-time.After(2 * time.Second):
		tc.t.Error("frame collector did not stop")
	}
}

// call sends a Promise request and returns its request id.
func (tc *testConn) call(id int, channel, method string, arg Value) {
	tc.t.Helper()
	tc.send(RequestPromise, id, channel, method, arg)
}

// listen sends an EventListen request and returns its request id.
func (tc *testConn) listen(id int, channel, event string, arg Value) {
	tc.t.Helper()
	tc.send(RequestEventListen, id, channel, event, arg)
}

// sendCancel sends a PromiseCancel for an id.
func (tc *testConn) sendCancel(id int) {
	tc.t.Helper()
	tc.sendCancelOrDispose(RequestPromiseCancel, id)
}

// dispose sends an EventDispose for an id.
func (tc *testConn) dispose(id int) {
	tc.t.Helper()
	tc.sendCancelOrDispose(RequestEventDispose, id)
}

func (tc *testConn) send(reqType int, id int, channel, method string, arg Value) {
	tc.t.Helper()
	body, err := EncodeRequest(RequestHeader{Type: reqType, ID: id, ChannelName: channel, Name: method}, arg)
	if err != nil {
		tc.t.Fatalf("encode request %s/%s: %v", channel, method, err)
	}
	tc.writeFrame(body)
}

func (tc *testConn) sendCancelOrDispose(reqType, id int) {
	tc.t.Helper()
	body, err := EncodeCancelOrDispose(reqType, id)
	if err != nil {
		tc.t.Fatalf("encode cancel %d: %v", id, err)
	}
	tc.writeFrame(body)
}

func (tc *testConn) writeFrame(payload []byte) {
	tc.t.Helper()
	frame := EncodeFrame(Frame{Type: MsgRegular, Data: payload})
	if err := tc.peer.Write(context.Background(), frame); err != nil {
		tc.t.Fatalf("write frame: %v", err)
	}
}

// response is one decoded server response.
type response struct {
	Type int
	ID   int
	Body Value
}

// awaitResponses waits for n server responses, decodes them, and removes them from the
// collected queue. Fewer than n means the server sent nothing more; the caller asserts.
func (tc *testConn) awaitResponses(n int, timeout time.Duration) []response {
	tc.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		tc.mu.Lock()
		count := len(tc.frames)
		tc.mu.Unlock()
		if count >= n || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	tc.mu.Lock()
	defer tc.mu.Unlock()
	out := make([]response, 0, n)
	for _, f := range tc.frames {
		if len(out) == n {
			break
		}
		header, body, err := DecodeResponse(f.Data)
		if err != nil {
			tc.t.Fatalf("decode response: %v", err)
		}
		out = append(out, response{Type: header.Type, ID: header.ID, Body: body})
	}
	tc.frames = tc.frames[len(out):]
	return out
}

// nextResponse decodes and removes the next response, blocking until one arrives.
func (tc *testConn) nextResponse(timeout time.Duration) response {
	tc.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		tc.mu.Lock()
		if len(tc.frames) > 0 {
			f := tc.frames[0]
			tc.frames = tc.frames[1:]
			tc.mu.Unlock()
			header, body, err := DecodeResponse(f.Data)
			if err != nil {
				tc.t.Fatalf("decode response: %v", err)
			}
			return response{Type: header.Type, ID: header.ID, Body: body}
		}
		tc.mu.Unlock()
		if time.Now().After(deadline) {
			tc.t.Fatal("timed out waiting for a response")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// awaitInitialize waits for the Initialize handshake frame.
func (tc *testConn) awaitInitialize() response {
	tc.t.Helper()
	return tc.nextResponse(2 * time.Second)
}

// stubChannel is a configurable Channel for the table tests: callFunc and listenFunc decide
// behaviour per method/event.
type stubChannel struct {
	callFunc   func(ctx context.Context, method string, args []Value) (Value, error)
	listenFunc func(ctx context.Context, event string, arg Value) (<-chan Value, func(), error)
}

func (s *stubChannel) Call(ctx context.Context, method string, args []Value) (Value, error) {
	if s.callFunc == nil {
		return nil, &Error{Name: "Error", Message: "Method not found: " + method}
	}
	return s.callFunc(ctx, method, args)
}

func (s *stubChannel) Listen(ctx context.Context, event string, arg Value) (<-chan Value, func(), error) {
	if s.listenFunc == nil {
		return nil, nil, &Error{Name: "Error", Message: "Event not found: " + event}
	}
	return s.listenFunc(ctx, event, arg)
}
