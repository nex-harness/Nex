package browserws_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/nex-harness/nex/server/internal/browserws"
	"github.com/nex-harness/nex/server/internal/local"
	"github.com/nex-harness/nex/server/internal/runners"
)

// NEX_ALLOWED_ORIGINS exists for proxies that rewrite Host; an explicitly
// allowed origin must be accepted while any other cross-site origin stays
// rejected.
func TestAllowedOriginsWidensCheck(t *testing.T) {
	registry := runners.NewRegistry(slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := browserws.NewHandler(browserws.Deps{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Registry:       registry,
		Local:          local.NewRegistry(),
		AllowedOrigins: []string{"nex.example"},
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://nex.example"}},
	})
	if err != nil {
		t.Fatalf("allowed origin rejected: %v", err)
	}
	_ = conn.CloseNow()

	conn, response, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://other.example"}},
	})
	if conn != nil {
		_ = conn.CloseNow()
	}
	if err == nil {
		t.Fatal("disallowed origin accepted")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", response)
	}
}
