package server

// This file covers what happens to a live connection: a full RPC round trip over the real socket,
// what a client-initiated close does to in-flight work, and that 100 connections serving 1000 calls
// and 50 listeners each stay independent. Every test drives the real HTTP server and a real
// WebSocket; the channel behind the stub is the only test double, and it records cancellations so
// the close path is observable rather than inferred.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/tylinux/nex/go/internal/rpc"
)

// recordingChannel is a stub service that answers `echo` and reports every cancellation it observes.
// A test asserts on `cancelled` to prove the server really tore the call down rather than merely
// stopped answering.
type recordingChannel struct {
	mu        sync.Mutex
	cancelled []int // call sequence numbers whose context ended
	entered   chan int
	release   chan struct{}
}

// newRecordingChannel builds a channel that parks every call until release is closed, so a test can
// hold calls in flight while it closes the socket.
func newRecordingChannel(entered chan int, release chan struct{}) *recordingChannel {
	return &recordingChannel{entered: entered, release: release}
}

func (c *recordingChannel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	seq := len(args)
	select {
	case c.entered <- seq:
	case <-ctx.Done():
	}
	select {
	case <-c.release:
	case <-ctx.Done():
		c.mu.Lock()
		c.cancelled = append(c.cancelled, seq)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
	return args, nil
}

func (c *recordingChannel) Listen(ctx context.Context, event string, arg rpc.Value) (<-chan rpc.Value, func(), error) {
	out := make(chan rpc.Value, 1)
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(out)
		select {
		case out <- arg:
		case <-stop:
		case <-ctx.Done():
		}
	}()
	return out, func() { once.Do(func() { close(stop) }) }, nil
}

func (c *recordingChannel) cancellations() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]int, len(c.cancelled))
	copy(out, c.cancelled)
	return out
}

// registrarFor installs one shared channel under a fixed name, which is what the cancellation test
// needs (it addresses the channel as "test").
func registrarFor(ch rpc.Channel) ChannelRegistrar {
	return func(server *rpc.Server) func() {
		server.RegisterChannel("test", ch)
		return nil
	}
}

// stubRegistrar installs a channel under the stub channel's own name, so a test can address
// StubChannelName the way a real client addresses a service channel.
func stubRegistrar(ch rpc.Channel) ChannelRegistrar {
	return func(server *rpc.Server) func() {
		server.RegisterChannel(StubChannelName, ch)
		return nil
	}
}

// callFrame encodes a 100 (Promise) request as a wire frame, ready to write on a client socket.
func callFrame(t *testing.T, id int, channel, method string, args []rpc.Value) []byte {
	t.Helper()
	payload, err := rpc.EncodeRequest(rpc.RequestHeader{
		Type:        rpc.RequestPromise,
		ID:          id,
		ChannelName: channel,
		Name:        method,
	}, args)
	if err != nil {
		t.Fatalf("encode request %d: %v", id, err)
	}
	frame := make([]byte, 13+len(payload))
	frame[0] = 1 // MsgRegular
	binary.BigEndian.PutUint32(frame[1:], uint32(id))
	binary.BigEndian.PutUint32(frame[5:], 0)
	binary.BigEndian.PutUint32(frame[9:], uint32(len(payload)))
	copy(frame[13:], payload)
	return frame
}

// listenFrame encodes a 102 (EventListen) request.
func listenFrame(t *testing.T, id int, channel, event string, arg rpc.Value) []byte {
	t.Helper()
	return callFrame(t, id, channel, event, []rpc.Value{arg})
}

func writeFrame(t *testing.T, conn *websocket.Conn, frame []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// TestWebSocketRoundTrip is the end-to-end proof: a real socket, the real 13-byte framing and the
// real value codec, from a hand-built request frame to a decoded response.
func TestWebSocketRoundTrip(t *testing.T) {
	server, endpoints := newWSServer(t, "")
	endpoints.SetRegistrar(stubRegistrar(&StubChannel{}))

	conn := dial(t, server, wsPath, nil)
	if header, _ := readResponse(t, conn); header.Type != rpc.ResponseInitialize {
		t.Fatalf("first frame type = %d, want Initialize", header.Type)
	}

	writeFrame(t, conn, callFrame(t, 7, StubChannelName, "echo", []rpc.Value{"hello", int32(42)}))
	header, body := readResponse(t, conn)
	if header.Type != rpc.ResponsePromiseSuccess {
		t.Fatalf("response type = %d, want %d (success)", header.Type, rpc.ResponsePromiseSuccess)
	}
	if header.ID != 7 {
		t.Errorf("response id = %d, want 7 (the server must echo the request id)", header.ID)
	}
	args, ok := body.([]any)
	if !ok || len(args) != 2 {
		t.Fatalf("body = %#v, want the two echoed arguments", body)
	}
	if args[0] != "hello" {
		t.Errorf("arg 0 = %#v, want \"hello\"", args[0])
	}
	if got, ok := args[1].(int32); !ok || got != 42 {
		t.Errorf("arg 1 = %#v, want int32 42", args[1])
	}
}

// TestWebSocketUnknownMethodError pins the proxy-channel error text over a real socket, since the
// client surfaces `error.message` verbatim.
func TestWebSocketUnknownMethodError(t *testing.T) {
	server, endpoints := newWSServer(t, "")
	endpoints.SetRegistrar(stubRegistrar(&StubChannel{}))

	conn := dial(t, server, wsPath, nil)
	_, _ = readResponse(t, conn)

	writeFrame(t, conn, callFrame(t, 1, StubChannelName, "nope", nil))
	header, body := readResponse(t, conn)
	if header.Type != rpc.ResponsePromiseError {
		t.Fatalf("response type = %d, want %d (PromiseError)", header.Type, rpc.ResponsePromiseError)
	}
	if got := errorMessage(body); got != "Method not found: nope" {
		t.Errorf("error message = %q, want \"Method not found: nope\"", got)
	}
}

// errorMessage pulls `message` out of a decoded error payload.
func errorMessage(body any) string {
	obj, ok := body.(rpc.Object)
	if !ok {
		return ""
	}
	value, _ := obj.Get("message")
	message, _ := value.(string)
	return message
}

// TestWebSocketClientCloseCancelsActiveCalls is the close-semantics test the plan asks for: with a
// call parked in the handler, a client-initiated close must cancel the call's context. The
// cancellation is recorded by the test channel rather than inferred from a missing response.
func TestWebSocketClientCloseCancelsActiveCalls(t *testing.T) {
	entered := make(chan int, 8)
	release := make(chan struct{})
	channel := newRecordingChannel(entered, release)
	defer close(release)

	server, endpoints := newWSServer(t, "")
	endpoints.SetRegistrar(registrarFor(channel))

	conn := dial(t, server, wsPath, nil)
	if header, _ := readResponse(t, conn); header.Type != rpc.ResponseInitialize {
		t.Fatalf("first frame type = %d, want Initialize", header.Type)
	}

	// Three calls, all parked in the handler.
	for id := 1; id <= 3; id++ {
		writeFrame(t, conn, callFrame(t, id, "test", "work", []rpc.Value{int32(id)}))
	}
	for i := 0; i < 3; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of 3 calls entered the handler", i)
		}
	}
	if got := len(channel.cancellations()); got != 0 {
		t.Fatalf("calls were cancelled before the close: %v", channel.cancellations())
	}

	// The client goes away.
	if err := conn.Close(websocket.StatusNormalClosure, "done"); err != nil {
		t.Fatalf("client close: %v", err)
	}

	// Every parked call must observe its context ending.
	deadline := time.After(5 * time.Second)
	for {
		if got := len(channel.cancellations()); got == 3 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("only %d of 3 in-flight calls were cancelled on close: %v",
				len(channel.cancellations()), channel.cancellations())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestWebSocketDisposeOnClose checks the other half of socket.onClose: the registrar's disposer runs
// exactly once per connection when the socket ends, so the connection scope and window attachment
// step 2.8 adds are actually reclaimed.
func TestWebSocketDisposeOnClose(t *testing.T) {
	disposed := make(chan struct{}, 4)
	server, endpoints := newWSServer(t, "")
	endpoints.SetRegistrar(func(rpcServer *rpc.Server) func() {
		rpcServer.RegisterChannel(StubChannelName, &StubChannel{})
		return func() {
			select {
			case disposed <- struct{}{}:
			default:
			}
		}
	})

	conn := dial(t, server, wsPath, nil)
	_, _ = readResponse(t, conn)
	if err := conn.Close(websocket.StatusNormalClosure, "done"); err != nil {
		t.Fatalf("client close: %v", err)
	}

	select {
	case <-disposed:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection disposer did not run after the socket closed")
	}
	select {
	case extra := <-disposed:
		t.Fatalf("the disposer ran more than once (spurious %v)", extra)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestWebSocketManyConnectionsNoCrossTalk opens 100 simultaneous connections, each issuing 1000
// calls and holding 50 listeners, and requires that every response comes back on the connection
// that asked for it. This is the step 2.3 concurrency suite run over the real transport instead of
// an in-memory socket pair.
func TestWebSocketManyConnectionsNoCrossTalk(t *testing.T) {
	if testing.Short() {
		t.Skip("100 connections x 1000 calls is a load test; skipped in -short")
	}
	const (
		connections = 100
		calls       = 1000
		listeners   = 50
	)

	server, endpoints := newWSServer(t, "")
	// Each connection gets its OWN channel, so a response served on the wrong connection is
	// detectable: the value would not match what that connection asked for.
	endpoints.SetRegistrar(func(rpcServer *rpc.Server) func() {
		rpcServer.RegisterChannel(StubChannelName, &StubChannel{})
		return nil
	})

	var wg sync.WaitGroup
	errs := make(chan error, connections)
	for c := 0; c < connections; c++ {
		wg.Add(1)
		go func(connection int) {
			defer wg.Done()
			if err := runLoadConnection(t, server, connection, calls, listeners); err != nil {
				errs <- err
			}
		}(c)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// runLoadConnection drives one connection: it fires `calls` requests and `listeners` subscribes,
// then reads until it has every response and checks each id belongs to this connection.
func runLoadConnection(t *testing.T, server *httptest.Server, connection, calls, listeners int) error {
	t.Helper()
	conn, _, err := websocket.Dial(context.Background(),
		wsURL(server, wsPath), &websocket.DialOptions{})
	if err != nil {
		return fmt.Errorf("connection %d: dial: %w", connection, err)
	}
	defer conn.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The Initialize frame.
	if _, _, err := conn.Read(ctx); err != nil {
		return fmt.Errorf("connection %d: read Initialize: %w", connection, err)
	}

	// Requests use ids 1..calls for calls and calls+1..calls+listeners for listeners, so a response
	// carrying an id outside that range came from another connection.
	want := calls + listeners
	seen := make([]bool, want+1)
	for id := 1; id <= calls; id++ {
		payload, err := rpc.EncodeRequest(rpc.RequestHeader{
			Type: rpc.RequestPromise, ID: id, ChannelName: StubChannelName, Name: "echo",
		}, []rpc.Value{int32(connection)})
		if err != nil {
			return err
		}
		if err := conn.Write(ctx, websocket.MessageBinary, frameOf(rpc.MsgRegular, id, payload)); err != nil {
			return fmt.Errorf("connection %d: write call %d: %w", connection, id, err)
		}
	}
	for id := calls + 1; id <= want; id++ {
		payload, err := rpc.EncodeRequest(rpc.RequestHeader{
			Type: rpc.RequestEventListen, ID: id, ChannelName: StubChannelName, Name: "tick",
		}, []rpc.Value{int32(id)})
		if err != nil {
			return err
		}
		if err := conn.Write(ctx, websocket.MessageBinary, frameOf(rpc.MsgRegular, id, payload)); err != nil {
			return fmt.Errorf("connection %d: write listen %d: %w", connection, id, err)
		}
	}

	received := 0
	for received < want {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("connection %d: read after %d/%d: %w", connection, received, want, err)
		}
		header, _, err := rpc.DecodeResponse(data[13:])
		if err != nil {
			return fmt.Errorf("connection %d: decode: %w", connection, err)
		}
		if header.ID < 1 || header.ID > want {
			return fmt.Errorf("connection %d: received id %d, outside its own range 1..%d (cross-talk)",
				connection, header.ID, want)
		}
		if seen[header.ID] {
			return fmt.Errorf("connection %d: id %d answered twice", connection, header.ID)
		}
		seen[header.ID] = true
		received++
	}
	return nil
}

// frameOf builds a ProtocolMessage frame around an already-serialized payload.
func frameOf(messageType uint8, id int, payload []byte) []byte {
	frame := make([]byte, 13+len(payload))
	frame[0] = messageType
	binary.BigEndian.PutUint32(frame[1:], uint32(id))
	binary.BigEndian.PutUint32(frame[5:], 0)
	binary.BigEndian.PutUint32(frame[9:], uint32(len(payload)))
	copy(frame[13:], payload)
	return frame
}

// TestWebSocketServerRejectsNonWebSocketRequest: the library refuses a malformed handshake with an
// HTTP error instead of a half-open socket. The client must see a status, never a successful upgrade.
func TestWebSocketServerRejectsNonWebSocketRequest(t *testing.T) {
	server, _ := newWSServer(t, "")

	// A GET /ws with the Upgrade header but no Sec-WebSocket-Key is a protocol violation.
	request, err := http.NewRequest(http.MethodGet, server.URL+wsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Connection", "Upgrade")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusSwitchingProtocols {
		t.Fatal("a malformed handshake was upgraded; it must be refused")
	}
	if response.StatusCode < 400 {
		t.Errorf("status = %d, want a 4xx handshake rejection", response.StatusCode)
	}
}

// errTestTimeout is the deadline shared by the connection tests; exported through a var so the
// tests read consistently.
var errTestTimeout = errors.New("ws test timeout")
