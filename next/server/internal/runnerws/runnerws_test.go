package runnerws

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
