package browserws_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nex-harness/nex/server/internal/browserws"
	"github.com/nex-harness/nex/server/internal/local"
	"github.com/nex-harness/nex/server/internal/rpcwire"
	"github.com/nex-harness/nex/server/internal/runners"
)

func newHandler(t *testing.T) (*httptest.Server, *runners.Registry) {
	t.Helper()
	registry := runners.NewRegistry(slog.New(slog.NewTextHandler(io.Discard, nil)))
	localRegistry := local.NewRegistry()
	localRegistry.Register("setting", local.NewSettings(t.TempDir(), nil))
	localRegistry.Register("broadcast", local.NewBroadcast())
	localRegistry.Register("slow", slowChannel{})
	handler := browserws.NewHandler(browserws.Deps{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Registry: registry,
		Local:    localRegistry,
	})
	return httptest.NewServer(handler), registry
}

// A browser session cookie is bearer-equivalent; accepting a cross-site Origin
// would let any page open /ws with the user's login (CSWSH).
func TestRejectsCrossSiteOrigin(t *testing.T) {
	server, _ := newHandler(t)
	defer server.Close()

	header := http.Header{"Origin": []string{"https://evil.example"}}
	conn, response, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{
		HTTPHeader: header,
	})
	if conn != nil {
		_ = conn.CloseNow()
	}
	if err == nil {
		t.Fatal("cross-site websocket handshake succeeded")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", response)
	}
}

func TestAcceptsSameOrigin(t *testing.T) {
	server, _ := newHandler(t)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	host := strings.TrimPrefix(server.URL, "http://")
	conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://" + host}},
	})
	if err != nil {
		t.Fatalf("same-origin handshake failed: %v", err)
	}
	_ = conn.CloseNow()
}

func TestPromiseCancelSentBeforeHandlerStartsCancelsCall(t *testing.T) {
	server, _ := newHandler(t)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	readCtx, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRead()
	if _, _, err := conn.Read(readCtx); err != nil {
		t.Fatalf("reading initialize: %v", err)
	}

	// Write promise + cancel back-to-back. The server must treat the call as
	// cancelled and must not reply (a reply would mean the cancel was lost).
	payload, err := rpcwire.EncodeRequest(rpcwire.RequestPromise, 77, "slow", "wait", rpcwire.Undefined{})
	if err != nil {
		t.Fatalf("encode promise: %v", err)
	}
	cancelPayload, err := rpcwire.EncodePromiseCancel(77)
	if err != nil {
		t.Fatalf("encode cancel: %v", err)
	}
	if err := conn.Write(context.Background(), websocket.MessageBinary,
		append(rpcwire.EncodeFrame(rpcwire.Frame{Type: rpcwire.MessageRegular, Payload: payload}),
			rpcwire.EncodeFrame(rpcwire.Frame{Type: rpcwire.MessageRegular, Payload: cancelPayload})...)); err != nil {
		t.Fatalf("write: %v", err)
	}

	shortCtx, cancelShort := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelShort()
	_, data, err := conn.Read(shortCtx)
	if err == nil {
		t.Fatalf("cancelled call still produced a response: %s", data)
	}
}

func TestLocalListenReuseReleasesPreviousSubscription(t *testing.T) {
	server, _ := newHandler(t)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("reading initialize: %v", err)
	}

	sendRequest := func(reqType rpcwire.RequestType, id uint32, channel, name string) {
		t.Helper()
		payload, err := rpcwire.EncodeRequest(reqType, id, channel, name, rpcwire.Undefined{})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if err := conn.Write(ctx, websocket.MessageBinary,
			rpcwire.EncodeFrame(rpcwire.Frame{Type: rpcwire.MessageRegular, Payload: payload})); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// Two listens re-using id 9 on broadcast.onMessage, then a send must
	// deliver exactly one event, not two.
	sendRequest(rpcwire.RequestEventListen, 9, "broadcast", "onMessage")
	sendRequest(rpcwire.RequestEventListen, 9, "broadcast", "onMessage")

	arg, _ := json.Marshal(map[string]any{"channel": "settings:app-runtime-preferences", "payload": 1})
	callPayload, err := rpcwire.EncodeRequest(rpcwire.RequestPromise, 10, "broadcast", "send", json.RawMessage(arg))
	if err != nil {
		t.Fatalf("encode call: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary,
		rpcwire.EncodeFrame(rpcwire.Frame{Type: rpcwire.MessageRegular, Payload: callPayload})); err != nil {
		t.Fatalf("write call: %v", err)
	}

	// Read the promise success and collect events for a short window.
	events := 0
	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		readCtx, cancelRead := context.WithTimeout(context.Background(), 300*time.Millisecond)
		_, data, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			break
		}
		frame, _, err := rpcwire.ReadFrame(data, 0)
		if err != nil {
			continue
		}
		response, err := rpcwire.DecodeResponse(frame.Payload)
		if err != nil {
			continue
		}
		if response.Type == rpcwire.ResponseEventFire && response.ID == 9 {
			events++
		}
	}
	if events != 1 {
		t.Fatalf("broadcast events = %d, want 1 (stale listener leaked)", events)
	}
}

// The server must unregister every binding of a closed browser connection so a
// reconnecting browser does not receive phantom events.
func TestBrowserCloseUnregistersListens(t *testing.T) {
	server, registry := newHandler(t)
	defer server.Close()
	registry.Register(runners.Info{RunnerID: "r", Workspaces: []runners.Workspace{{Path: "/w"}}}, newFakeLink())

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("reading initialize: %v", err)
	}
	payload, _ := rpcwire.EncodeRequest(rpcwire.RequestEventListen, 3, "nex-task", "onDynamicWorkspaceEvent",
		json.RawMessage(`{"workspacePath":"/w"}`))
	if err := conn.Write(ctx, websocket.MessageBinary,
		rpcwire.EncodeFrame(rpcwire.Frame{Type: rpcwire.MessageRegular, Payload: payload})); err != nil {
		t.Fatalf("write listen: %v", err)
	}
	waitForBindings(t, registry, "r", 1)

	_ = conn.CloseNow()
	waitForBindings(t, registry, "r", 0)
}

func waitForBindings(t *testing.T, registry *runners.Registry, runnerID string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(registry.BindingsForRunner(runnerID)) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("bindings for %s = %v, want %d", runnerID, registry.BindingsForRunner(runnerID), want)
}

// slowChannel blocks briefly so a cancel frame written right after the promise
// is read before the call completes.
type slowChannel struct{}

func (slowChannel) Call(ctx context.Context, _ *local.Env, _ string, _ rpcwire.Value) (rpcwire.Value, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(250 * time.Millisecond):
		return json.RawMessage(`"finished"`), nil
	}
}

func (slowChannel) Listen(context.Context, *local.Env, string, rpcwire.Value, func(rpcwire.Value)) (func(), error) {
	return nil, nil
}

type fakeLink struct {
	listens map[string]string
}

func newFakeLink() *fakeLink { return &fakeLink{listens: make(map[string]string)} }

func (f *fakeLink) Call(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage("null"), nil
}

func (f *fakeLink) Listen(id, channel, event string, _ json.RawMessage, _ func(json.RawMessage)) error {
	f.listens[id] = channel + "." + event
	return nil
}

func (f *fakeLink) Unlisten(id string) { delete(f.listens, id) }
func (f *fakeLink) Close()             {}
func (f *fakeLink) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
