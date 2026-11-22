package remote

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"github.com/nex-harness/nex/pkg/relay"
)

func TestLocalProxyPathSelectionWebSocketAndRelayFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	hostID, err := GenerateIdentity("host")
	if err != nil {
		t.Fatal(err)
	}
	phoneID, err := GenerateIdentity("phone")
	if err != nil {
		t.Fatal(err)
	}

	relaySrv := relay.New(relay.Config{})
	relayTS := httptest.NewServer(relaySrv.Handler())
	defer relayTS.Close()

	var sawDeviceToken atomic.Value
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/v1/auth/status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"enabled":false,"authenticated":true}`))
	})
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		sawDeviceToken.Store(r.Header.Get("X-Nex-Device-Token"))
		peer, hasPeer := PeerFromContext(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"via_peer":  hasPeer,
			"transport": peer.Transport,
		})
	})
	mux.HandleFunc("/ws/client", func(w http.ResponseWriter, r *http.Request) {
		ws, aErr := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if aErr != nil {
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		for {
			typ, msg, rErr := ws.Read(r.Context())
			if rErr != nil {
				return
			}
			_ = ws.Write(r.Context(), typ, msg)
		}
	})

	go func() {
		_ = relay.DialHostControl(ctx, relayTS.URL, hostID.NodeID, "", func(cCtx context.Context, _ string, conn net.Conn) {
			_ = ServeTunnelConn(cCtx, conn, hostID, mux, "relay", func(fp string) (string, bool, bool) {
				return "dev_phone", false, fp == phoneID.Fingerprint
			}, func(_ string, _ *yamux.Session) func() { return func() {} })
		})
	}()

	for time.Now().Add(2*time.Second).After(time.Now()) && relaySrv.Stats().OnlineHosts == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	// 1. Dead LAN URL falls back cleanly to Relay mTLS + yamux and proxies both REST + WS binary/text frames
	lp1, err := NewLocalProxy(phoneID, PairedHostProfile{
		NodeID:      hostID.NodeID,
		HostName:    "Lab Box",
		Fingerprint: hostID.Fingerprint,
		DeviceID:    "dev_phone",
		LANURLs:     []string{"http://127.0.0.1:1"}, // unreachable
		RelayURL:    relayTS.URL,
	}, "dev_tok_xyz", LocalProxyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer lp1.Close()

	st1, err := lp1.Connect(ctx)
	if err != nil || !st1.Connected || st1.Transport != "relay" {
		t.Fatalf("expected relay fallback transport, got st=%+v err=%v", st1, err)
	}

	resp1, err := http.Get(lp1.LocalURL() + "/api/v1/sessions")
	if err != nil || resp1.StatusCode != http.StatusOK {
		t.Fatalf("GET via LocalProxy relay failed: resp=%v err=%v", resp1, err)
	}
	var body1 map[string]any
	_ = json.NewDecoder(resp1.Body).Decode(&body1)
	_ = resp1.Body.Close()
	if body1["via_peer"] != true || body1["transport"] != "relay" {
		t.Fatalf("expected proxied request over mTLS relay peer, got %+v", body1)
	}

	wsURL := "ws" + strings.TrimPrefix(lp1.LocalURL(), "http") + "/ws/client"
	wsConn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("WS through LocalProxy failed: %v", err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "")
	_ = wsConn.Write(ctx, websocket.MessageBinary, []byte{0x10, 0x01, 0x02, 0x03})
	mType, mBytes, err := wsConn.Read(ctx)
	if err != nil || mType != websocket.MessageBinary || string(mBytes) != string([]byte{0x10, 0x01, 0x02, 0x03}) {
		t.Fatalf("unexpected WS binary echo through LocalProxy: type=%v data=%x err=%v", mType, mBytes, err)
	}

	// 2. Reachable LAN server takes priority over Relay and injects X-Nex-Device-Token
	lanTS := httptest.NewServer(mux)
	defer lanTS.Close()

	lp2, err := NewLocalProxy(phoneID, PairedHostProfile{
		NodeID:      hostID.NodeID,
		HostName:    "Lab Box",
		Fingerprint: hostID.Fingerprint,
		DeviceID:    "dev_phone",
		LANURLs:     []string{lanTS.URL},
		RelayURL:    relayTS.URL,
	}, "lan_secret_tok", LocalProxyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer lp2.Close()

	st2, err := lp2.Connect(ctx)
	if err != nil || !st2.Connected || st2.Transport != "lan" {
		t.Fatalf("expected LAN priority over relay, got st=%+v err=%v", st2, err)
	}

	resp2, err := http.Get(lp2.LocalURL() + "/api/v1/sessions")
	if err != nil || resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET via LocalProxy LAN failed: resp=%v err=%v", resp2, err)
	}
	_, _ = io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if gotTok, _ := sawDeviceToken.Load().(string); gotTok != "lan_secret_tok" {
		t.Fatalf("expected X-Nex-Device-Token header injected on LAN path, got %q", gotTok)
	}
}
