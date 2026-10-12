package gateway

// The fake upstream is a real HTTP + WebSocket server that speaks the RPC protocol, so the gateway
// is exercised over its actual transports (handshake, framing, close codes) rather than through an
// in-memory socket pair. It stands in for the Node process in the tests that do not need Node's
// actual services; the tests that DO need them spawn the real one (node_test.go).
//
// What it records is what the ordering and cancel tests assert on:
//   - every frame it received, in order, so "the client's order is Node's order" is checkable;
//   - which connection served which request, so cross-talk between two client connections is
//     detectable;
//   - the client mode of each upgrade, so the 1:1 leg is provable.

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/tylinux/nex/go/internal/rpc"
)

// fakeUpstream is a stand-in Node server.
type fakeUpstream struct {
	server *httptest.Server
	// authToken is the shared secret the fake requires; empty means no auth.
	authToken string

	mu sync.Mutex
	// modes records the client mode of each accepted upstream connection, in order.
	modes []string
	// received records every Regular frame payload, tagged with the connection index it arrived on.
	received []receivedFrame
	// conns is how many upstream connections were opened. A test asserts it equals the number of
	// client connections: the 1:1 leg the plan requires.
	conns int
	// onFrame, when set, answers a frame; otherwise the default answerer runs.
	onFrame func(connIndex int, payload []byte) [][]byte
	// live tracks accepted connections so a test can kill them: httptest.CloseClientConnections
	// does NOT reach a hijacked WebSocket, so "the upstream died" has to be arranged here.
	live []*websocket.Conn
}

// DropAll closes every accepted upstream connection abruptly, the way a killed Node process would.
// CloseNow skips the close handshake, so the gateway sees a transport failure rather than a
// deliberate close — which is the case the failure semantics are about.
func (f *fakeUpstream) DropAll() {
	f.mu.Lock()
	conns := make([]*websocket.Conn, len(f.live))
	copy(conns, f.live)
	f.mu.Unlock()
	for _, conn := range conns {
		_ = conn.CloseNow()
	}
}

// receivedFrame is one frame the fake upstream saw, with the connection it arrived on.
type receivedFrame struct {
	conn    int
	payload []byte
}

// newFakeUpstream starts the fake and returns it. authToken, when non-empty, is required on both
// the capability route and the WebSocket upgrades — the same fail-closed shape Node has, so a
// gateway that forgot to pass the secret fails here rather than in production.
func newFakeUpstream(t *testing.T, authToken string) *fakeUpstream {
	t.Helper()
	fake := &fakeUpstream{authToken: authToken}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/rpc-host-capability", func(w http.ResponseWriter, r *http.Request) {
		if !fake.authorized(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"capability":"fake-capability","expiresAt":` +
			strconv.FormatInt(time.Now().Add(30*time.Second).UnixMilli(), 10) + `}`))
	})
	mux.HandleFunc("/ws", fake.serveWS(ModeWebRemoteReplayable))
	mux.HandleFunc("/ws/host", fake.serveWS(ModeDesktopContinuous))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// A catch-all HTTP route, for the reverse-proxy tests.
		body := r.URL.Path + "?" + r.URL.RawQuery + " " + r.Method
		_, _ = w.Write([]byte(body))
	})
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

// authorized enforces the shared secret the way webAuth does: a wrong or missing token on a
// protected path is 401, and the cookie is irrelevant because the gateway authenticates as a machine.
func (f *fakeUpstream) authorized(w http.ResponseWriter, r *http.Request) bool {
	if f.authToken == "" {
		return true
	}
	if r.URL.Query().Get("token") != f.authToken {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))
		return false
	}
	return true
}

// serveWS accepts one upstream WebSocket in the given mode.
func (f *fakeUpstream) serveWS(mode string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !f.authorized(w, r) {
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		f.track(conn)

		index := f.accepted(mode)

		// The Initialize the real Node sends the moment the ChannelServer is constructed. The
		// gateway must swallow exactly one of these per connection.
		initPayload, _ := rpc.EncodeResponse(rpc.ResponseHeader{Type: rpc.ResponseInitialize}, nil)
		_ = conn.Write(r.Context(), websocket.MessageBinary,
			encodeFrameBytes(rpc.MsgRegular, 0, initPayload))

		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			f.record(index, data)
			payload, ok := stripFrame(data)
			if !ok {
				return
			}
			for _, answer := range f.answersFor(index, payload) {
				if err := conn.Write(r.Context(), websocket.MessageBinary, answer); err != nil {
					return
				}
			}
		}
	}
}

// track records a live connection so DropAll can kill it.
func (f *fakeUpstream) track(conn *websocket.Conn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live = append(f.live, conn)
}

// accepted registers a new upstream connection and returns its index.
func (f *fakeUpstream) accepted(mode string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns++
	f.modes = append(f.modes, mode)
	return f.conns - 1
}

// stripFrame returns the body of a framed WebSocket message, or false when it is not one.
func stripFrame(message []byte) ([]byte, bool) {
	decoded, ok, err := rpc.DecodeFrame(message)
	if err != nil || !ok {
		return nil, false
	}
	return decoded.Data, true
}

// record logs one inbound frame.
func (f *fakeUpstream) record(conn int, message []byte) {
	decoded, ok, err := rpc.DecodeFrame(message)
	if err != nil || !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.received = append(f.received, receivedFrame{conn: conn, payload: decoded.Data})
}

// answersFor produces the response frames for one inbound frame, as complete WebSocket messages.
func (f *fakeUpstream) answersFor(conn int, payload []byte) [][]byte {
	f.mu.Lock()
	handler := f.onFrame
	f.mu.Unlock()
	var answers [][]byte
	if handler != nil {
		answers = handler(conn, payload)
	} else {
		answers = defaultAnswer(conn, payload)
	}
	out := make([][]byte, 0, len(answers))
	for _, answer := range answers {
		out = append(out, encodeFrameBytes(rpc.MsgRegular, 0, answer))
	}
	return out
}

// defaultAnswer answers the calls and echoes the events, using the connection index as the
// connection-scoped value a cross-talk test can check. A 101/103 (cancel/dispose) is answered with
// nothing, which is what a real cancellation produces.
func defaultAnswer(conn int, payload []byte) [][]byte {
	header, err := rpc.DecodeRequestHeader(payload)
	if err != nil {
		return nil
	}
	switch header.Type {
	case rpc.RequestPromiseCancel, rpc.RequestEventDispose:
		return nil
	case rpc.RequestEventListen:
		body, _ := rpc.EncodeResponse(rpc.ResponseHeader{
			Type: rpc.ResponseEventFire, ID: header.ID,
		}, conn)
		return [][]byte{body}
	default:
		// Echo [channel, method, conn] so the test can prove the answer came from ITS upstream.
		body, err := rpc.EncodeResponse(rpc.ResponseHeader{
			Type: rpc.ResponsePromiseSuccess, ID: header.ID,
		}, []any{header.ChannelName, header.Name, conn})
		if err != nil {
			return nil
		}
		return [][]byte{body}
	}
}

// SetOnFrame installs a custom answerer.
func (f *fakeUpstream) SetOnFrame(fn func(conn int, payload []byte) [][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onFrame = fn
}

// BaseURL is the fake's http address.
func (f *fakeUpstream) BaseURL() string { return f.server.URL }

// WebSocketURL is the fake's ws address for a path, with the shared secret attached the way the
// gateway attaches it.
func (f *fakeUpstream) WebSocketURL(path string) string {
	base := "ws" + strings.TrimPrefix(f.server.URL, "http") + path
	if f.authToken == "" {
		return base
	}
	return base + "?token=" + f.authToken
}

// Frames returns every inbound payload in arrival order.
func (f *fakeUpstream) Frames() []receivedFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]receivedFrame, len(f.received))
	copy(out, f.received)
	return out
}

// Modes returns the client mode of each upstream connection, in the order they were accepted.
func (f *fakeUpstream) Modes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.modes))
	copy(out, f.modes)
	return out
}

// Connections reports how many upstream WebSockets were opened.
func (f *fakeUpstream) Connections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

// WaitForFrames blocks until the fake has seen at least n frames, so an assertion never races the
// forward loop instead of testing it.
func (f *fakeUpstream) WaitForFrames(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if len(f.Frames()) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("upstream saw %d frames, want at least %d", len(f.Frames()), n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// encodeFrameBytes wraps a serialized payload in the 13-byte ProtocolMessage header.
func encodeFrameBytes(messageType uint8, id int, payload []byte) []byte {
	frame := make([]byte, 13+len(payload))
	frame[0] = messageType
	binary.BigEndian.PutUint32(frame[1:], uint32(id))
	binary.BigEndian.PutUint32(frame[5:], 0)
	binary.BigEndian.PutUint32(frame[9:], uint32(len(payload)))
	copy(frame[13:], payload)
	return frame
}

// newTestGateway builds a gateway in front of the fake upstream, with the given routing table body.
// An empty body means "no table", which is the strangler default.
func newTestGateway(t *testing.T, fake *fakeUpstream, table string) *Gateway {
	t.Helper()
	path := ""
	if table != "" {
		path = writeRoutes(t, table)
	} else {
		path = filepath.Join(t.TempDir(), "absent.json")
	}
	gateway, err := New(Options{
		RoutesPath: path,
		Upstream: UpstreamConfig{
			BaseURL:   fake.BaseURL(),
			AuthToken: fake.authToken,
		},
		Log: testLogger{t},
	})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	return gateway
}

// errTestTimeout is the deadline shared by the connection tests, named so a failure says which
// wait ran out.
var errTestTimeout = errors.New("gateway test timeout")

// testLogger sends gateway logs to the test, at debug so a passing run stays quiet.
type testLogger struct{ t *testing.T }

func (l testLogger) Debug(msg string, args ...any) { l.t.Logf("DEBUG "+msg, args...) }
func (l testLogger) Info(msg string, args ...any)  { l.t.Logf("INFO  "+msg, args...) }
func (l testLogger) Warn(msg string, args ...any)  { l.t.Logf("WARN  "+msg, args...) }
func (l testLogger) Error(msg string, args ...any) { l.t.Logf("ERROR "+msg, args...) }

// testClient is a minimal WebSocket client for driving a gateway from the outside.
type testClient struct {
	conn *websocket.Conn
}

// dialClient opens a WebSocket to url.
func dialClient(t *testing.T, url string, header http.Header) (*testClient, error) {
	t.Helper()
	conn, _, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return &testClient{conn: conn}, nil
}

// Write sends one pre-framed message.
func (c *testClient) Write(t *testing.T, data []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageBinary, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// WriteRequest encodes and sends a 100 (Promise) call.
func (c *testClient) WriteRequest(t *testing.T, id int, channel, method string, args []rpc.Value) {
	t.Helper()
	payload, err := rpc.EncodeRequest(rpc.RequestHeader{
		Type: rpc.RequestPromise, ID: id, ChannelName: channel, Name: method,
	}, args)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	c.Write(t, encodeFrameBytes(rpc.MsgRegular, id, payload))
}

// WriteCancel sends a 101 (cancel) for an id.
func (c *testClient) WriteCancel(t *testing.T, id int) {
	t.Helper()
	payload, err := rpc.EncodeCancelOrDispose(rpc.RequestPromiseCancel, id)
	if err != nil {
		t.Fatalf("encode cancel: %v", err)
	}
	c.Write(t, encodeFrameBytes(rpc.MsgRegular, id, payload))
}

// ReadFrame reads the next message, strips the protocol header and returns the payload.
func (c *testClient) ReadFrame(t *testing.T) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := c.conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	decoded, ok, err := rpc.DecodeFrame(data)
	if err != nil || !ok {
		t.Fatalf("decode frame: ok=%v err=%v", ok, err)
	}
	return decoded.Data
}

// ReadHeader reads the next frame and decodes its response header.
func (c *testClient) ReadHeader(t *testing.T) rpc.ResponseHeader {
	t.Helper()
	payload := c.ReadFrame(t)
	header, err := rpc.DecodeResponseHeader(payload)
	if err != nil {
		t.Fatalf("decode response header: %v", err)
	}
	return header
}

// Close ends the client connection with the normal-closure handshake.
func (c *testClient) Close(t *testing.T) {
	t.Helper()
	if err := c.conn.Close(websocket.StatusNormalClosure, "done"); err != nil {
		t.Fatalf("client close: %v", err)
	}
}
