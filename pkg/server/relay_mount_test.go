package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nex-harness/nex/pkg/relay"
	"github.com/nex-harness/nex/pkg/store"
)

func TestServerEmbeddedRelayMountBypassesWebPasswordMiddleware(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv, err := New(Config{
		Store:       st,
		EnableRelay: true,
		RelayToken:  "embedded-relay-tok",
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	reqPwd, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/auth/password", strings.NewReader(`{"new_password":"web-pass-123"}`))
	pwdResp, err := http.DefaultClient.Do(reqPwd)
	if err != nil || pwdResp.StatusCode != http.StatusOK {
		t.Fatalf("failed to enable web password: resp=%v err=%v", pwdResp, err)
	}
	_ = pwdResp.Body.Close()

	// Normal REST API requires web password cookie/bearer
	unauthResp, err := http.Get(ts.URL + "/api/v1/sessions")
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 on /api/v1/sessions when password enabled, got %d", unauthResp.StatusCode)
	}

	// Mounted /relay/v1/health remains reachable independently of web password
	relHealth, err := http.Get(ts.URL + "/relay/v1/health")
	if err != nil || relHealth.StatusCode != http.StatusOK {
		t.Fatalf("expected /relay/v1/health 200 OK, got resp=%v err=%v", relHealth, err)
	}
	var payload map[string]any
	_ = json.NewDecoder(relHealth.Body).Decode(&payload)
	_ = relHealth.Body.Close()
	if payload["ok"] != true || payload["auth_required"] != true {
		t.Fatalf("unexpected relay health payload: %+v", payload)
	}

	// Embedded relay bridges tunnel cleanly using RelayToken
	hostCtx, hostCancel := context.WithCancel(ctx)
	defer hostCancel()
	go func() {
		_ = relay.DialHostControl(hostCtx, ts.URL, "nex_embedded_1", "embedded-relay-tok", func(_ context.Context, _ string, conn net.Conn) {
			defer conn.Close()
			_, _ = conn.Write([]byte("EMBEDDED_PING"))
		})
	}()

	deadline := time.Now().Add(2 * time.Second)
	var cConn net.Conn
	for time.Now().Before(deadline) {
		cConn, err = relay.DialClientTunnel(ctx, ts.URL, "nex_embedded_1", "embedded-relay-tok")
		if err == nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("DialClientTunnel through mounted relay failed: %v", err)
	}
	defer cConn.Close()

	buf := make([]byte, len("EMBEDDED_PING"))
	if _, err := io.ReadFull(cConn, buf); err != nil || string(buf) != "EMBEDDED_PING" {
		t.Fatalf("unexpected relayed data: %q err=%v", string(buf), err)
	}
}
