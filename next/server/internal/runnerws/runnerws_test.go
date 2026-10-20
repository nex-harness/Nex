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

	"github.com/tylinux/nex-next/server/internal/runners"
)

// TestMain shortens the handshake deadline once, before any server goroutine
// reads it (mutating it per test would race with Serve).
func TestMain(m *testing.M) {
	handshakeTimeout = 150 * time.Millisecond
	os.Exit(m.Run())
}

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	registry := runners.NewRegistry(slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Serve(slog.New(slog.NewTextHandler(io.Discard, nil)), registry, "runner-secret", "9.9.9", w, r)
	})
	return httptest.NewServer(handler)
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
