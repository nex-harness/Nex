package runnerws

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nex-harness/nex/server/internal/runners"
)

// TestMain shortens the handshake deadline once, before any server goroutine
// reads it (mutating it per test would race with Serve).
func TestMain(m *testing.M) {
	handshakeTimeout = 150 * time.Millisecond
	os.Exit(m.Run())
}

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	server, _ := newServerWithRegistry(t)
	return server
}

func newServerWithRegistry(t *testing.T) (*httptest.Server, *runners.Registry) {
	t.Helper()
	registry := runners.NewRegistry(slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Serve(slog.New(slog.NewTextHandler(io.Discard, nil)), registry, "runner-secret", "9.9.9", w, r)
	})
	return httptest.NewServer(handler), registry
}

func TestRunnerTokenRequired(t *testing.T) {
	server := newServer(t)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	for _, header := range []http.Header{
		nil,
		{"Authorization": []string{"Bearer wrong"}},
		{"Authorization": []string{"runner-secret"}},
	} {
		conn, response, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{HTTPHeader: header})
		if conn != nil {
			_ = conn.CloseNow()
		}
		if err == nil {
			t.Fatalf("handshake with header %v succeeded", header)
		}
		if response != nil && response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", response.StatusCode)
		}
	}
}

// A client that never sends hello must be dropped rather than pinning the
// connection forever (keepalive only starts after the handshake).
func TestHandshakeTimesOutWithoutHello(t *testing.T) {
	server := newServer(t)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer runner-secret"}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("server kept the connection open without hello")
	}
	// Distinguish a server-side close from the client's own read deadline.
	if elapsed := time.Since(started); elapsed > 2000*time.Millisecond {
		t.Fatalf("server did not close the connection within the handshake deadline (waited %s)", elapsed)
	}
}

func TestWelcomeCarriesConfiguredVersion(t *testing.T) {
	server := newServer(t)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer runner-secret"}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText,
		[]byte(`{"t":"hello","runnerId":"r1","name":"n","version":"1","os":"linux","arch":"arm64","workspaces":[{"path":"/w"}]}`)); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	if !strings.Contains(string(data), `"serverVersion":"9.9.9"`) {
		t.Fatalf("welcome = %s, want the configured version", data)
	}
}

// Runner ids are embedded in workspace identities, so ':' and whitespace are
// rejected at hello instead of producing unparseable identities.
func TestHelloRejectsInvalidRunnerID(t *testing.T) {
	server := newServer(t)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	for _, hello := range []string{
		`{"t":"hello","runnerId":"a:b","workspaces":[{"path":"/w"}]}`,
		`{"t":"hello","runnerId":"a b","workspaces":[{"path":"/w"}]}`,
		`{"t":"hello","runnerId":"","workspaces":[{"path":"/w"}]}`,
	} {
		conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": []string{"Bearer runner-secret"}},
		})
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := conn.Write(ctx, websocket.MessageText, []byte(hello)); err != nil {
			cancel()
			_ = conn.CloseNow()
			t.Fatalf("write hello %s: %v", hello, err)
		}
		_, _, readErr := conn.Read(ctx)
		cancel()
		_ = conn.CloseNow()
		if readErr == nil {
			t.Fatalf("server accepted hello %s", hello)
		}
	}
}

// A `workspaces` frame from a connected runner replaces its catalogue in the
// registry (docs/next/runner-folders.md decision 3): the old path stops
// routing, the new one routes by path and identity.
func TestWorkspacesFrameUpdatesRegistry(t *testing.T) {
	server, registry := newServerWithRegistry(t)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer runner-secret"}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText,
		[]byte(`{"t":"hello","runnerId":"r1","name":"n","version":"1","os":"linux","arch":"arm64","workspaces":[{"path":"/old"}]}`)); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	if id, ok := registry.RunnerIDForPath("/old"); !ok || id != "r1" {
		t.Fatalf("hello catalogue = %q,%v", id, ok)
	}

	// The frame may repeat a path and (incorrectly) carry the machine scope; both
	// are normalised away, so `~` never becomes a workspace row.
	if err := conn.Write(ctx, websocket.MessageText,
		[]byte(`{"t":"workspaces","workspaces":[{"path":"/new","label":"demo"},{"path":" /new "},{"path":"~"}]}`)); err != nil {
		t.Fatalf("write workspaces frame: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := registry.RunnerIDForPath("/new"); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := registry.RunnerIDForPath("/old"); ok {
		t.Fatal("old path still routes after the workspaces frame")
	}
	if id, ok := registry.RunnerIDForPath("/new"); !ok || id != "r1" {
		t.Fatalf("RunnerIDForPath(/new) = %q,%v", id, ok)
	}
	if id, ok := registry.RunnerIDForIdentity("runner:r1:/new"); !ok || id != "r1" {
		t.Fatalf("RunnerIDForIdentity(runner:r1:/new) = %q,%v", id, ok)
	}
	workspaces := registry.Workspaces()
	if len(workspaces) != 1 || workspaces[0].Path != "/new" || workspaces[0].WorkspaceIdentity != "runner:r1:/new" {
		t.Fatalf("workspaces = %+v", workspaces)
	}
}

// fakeUpdates is a scripted UpdateHandler: it returns a fixed target and
// records every updateStatus frame.
type fakeUpdates struct {
	target string
	mu     sync.Mutex
	frames []updateStatusCall
}

type updateStatusCall struct {
	runnerID, state, buildID, errMessage string
}

func (f *fakeUpdates) OnRunnerHello(runners.Info) string { return f.target }

func (f *fakeUpdates) UpdateStatus(runnerID, state, buildID, errMessage string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, updateStatusCall{runnerID, state, buildID, errMessage})
}

func (f *fakeUpdates) recorded() []updateStatusCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]updateStatusCall(nil), f.frames...)
}

// newServerWithUpdates serves one runner link with a scripted update handler.
func newServerWithUpdates(t *testing.T, updates UpdateHandler) (*httptest.Server, *runners.Registry) {
	t.Helper()
	registry := runners.NewRegistry(slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWithConfig(slog.New(slog.NewTextHandler(io.Discard, nil)), registry, "runner-secret", "9.9.9", nil, updates, w, r)
	})
	return httptest.NewServer(handler), registry
}

// dialAndHello connects and completes the handshake, returning the connection.
func dialAndHello(t *testing.T, server *httptest.Server, hello string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer runner-secret"}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(hello)); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	return conn
}

// readFrameWithin reads one frame, failing when nothing arrives in time.
func readFrameWithin(t *testing.T, conn *websocket.Conn, timeout time.Duration) ([]byte, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		return nil, false
	}
	return data, true
}

// An outdated runner is told to update once per connection, right after the
// welcome (docs/next/runner-update.md decision 5).
func TestUpdateFrameSentOncePerConnection(t *testing.T) {
	updates := &fakeUpdates{target: "new-build"}
	server, _ := newServerWithUpdates(t, updates)
	defer server.Close()

	conn := dialAndHello(t, server, `{"t":"hello","runnerId":"r1","buildId":"old-build","capabilities":["self-update"],"workspaces":[{"path":"/w"}]}`)
	defer func() { _ = conn.CloseNow() }()

	data, ok := readFrameWithin(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("no update frame after hello")
	}
	if string(data) != `{"buildId":"new-build","t":"update"}` {
		t.Fatalf("update frame = %s", data)
	}

	// A second hello-level decision must not repeat the frame: nothing else is
	// pending on this connection.
	if data, ok := readFrameWithin(t, conn, 300*time.Millisecond); ok {
		t.Fatalf("unexpected extra frame: %s", data)
	}
}

// The handler decides; a nil target (up to date, legacy, no capability, dev)
// sends nothing.
func TestNoUpdateFrameWhenHandlerDeclines(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		target  string
		handler UpdateHandler
	}{
		{"declined", "", &fakeUpdates{}},
		{"nil handler", "", nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server, _ := newServerWithUpdates(t, testCase.handler)
			defer server.Close()
			conn := dialAndHello(t, server, `{"t":"hello","runnerId":"r1","buildId":"old-build","capabilities":["self-update"],"workspaces":[{"path":"/w"}]}`)
			defer func() { _ = conn.CloseNow() }()
			if data, ok := readFrameWithin(t, conn, 300*time.Millisecond); ok {
				t.Fatalf("unexpected frame: %s", data)
			}
		})
	}
}

// updateStatus frames are forwarded to the handler with the runner id.
func TestUpdateStatusFramesTracked(t *testing.T) {
	updates := &fakeUpdates{}
	server, _ := newServerWithUpdates(t, updates)
	defer server.Close()

	conn := dialAndHello(t, server, `{"t":"hello","runnerId":"r1","buildId":"old-build","workspaces":[{"path":"/w"}]}`)
	defer func() { _ = conn.CloseNow() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText,
		[]byte(`{"t":"updateStatus","state":"downloading","buildId":"new-build"}`)); err != nil {
		t.Fatalf("write updateStatus: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText,
		[]byte(`{"t":"updateStatus","state":"failed","buildId":"new-build","error":"dependencies changed; reinstall required"}`)); err != nil {
		t.Fatalf("write updateStatus: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(updates.recorded()) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	frames := updates.recorded()
	if len(frames) != 2 {
		t.Fatalf("frames = %+v, want two", frames)
	}
	if frames[0].runnerID != "r1" || frames[0].state != "downloading" || frames[0].buildID != "new-build" {
		t.Fatalf("first frame = %+v", frames[0])
	}
	if frames[1].state != "failed" || frames[1].errMessage != "dependencies changed; reinstall required" {
		t.Fatalf("second frame = %+v", frames[1])
	}
}

// The hello carries buildId/capabilities into the registry so server-info and
// /api/config/runners can expose them.
func TestHelloStoresBuildAndCapabilities(t *testing.T) {
	server, registry := newServerWithUpdates(t, nil)
	defer server.Close()

	conn := dialAndHello(t, server, `{"t":"hello","runnerId":"r1","name":"box","os":"linux","arch":"x64","buildId":"build-1","capabilities":["self-update"," runner-workspaces ","self-update"],"workspaces":[{"path":"/w"}]}`)
	defer func() { _ = conn.CloseNow() }()

	info, ok := registry.RunnerInfo("r1")
	if !ok {
		t.Fatal("runner not registered")
	}
	if info.BuildID != "build-1" {
		t.Fatalf("BuildID = %q", info.BuildID)
	}
	// Trimmed and de-duplicated, order preserved.
	if len(info.Capabilities) != 2 || info.Capabilities[0] != "self-update" || info.Capabilities[1] != "runner-workspaces" {
		t.Fatalf("Capabilities = %+v", info.Capabilities)
	}
	workspaces := registry.Workspaces()
	if len(workspaces) != 1 || workspaces[0].RunnerBuildID != "build-1" || len(workspaces[0].RunnerCapabilities) != 2 {
		t.Fatalf("workspace info = %+v", workspaces)
	}
}
