package remote

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"github.com/nex-harness/nex/pkg/relay"
)

func TestIdentityPersistenceAndPairingURIRoundTrip(t *testing.T) {
	dir := t.TempDir()
	idPath := filepath.Join(dir, "remote_identity.json")

	id1, err := LoadOrCreateIdentity(idPath, "MyMac")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id1.NodeID, "nex_") || len(id1.Fingerprint) != 64 {
		t.Fatalf("unexpected identity format: node=%q fp=%q", id1.NodeID, id1.Fingerprint)
	}

	id2, err := LoadOrCreateIdentity(idPath, "MyMac")
	if err != nil {
		t.Fatal(err)
	}
	if id1.NodeID != id2.NodeID || id1.Fingerprint != id2.Fingerprint {
		t.Fatalf("reloaded identity mismatch: %s vs %s", id1.NodeID, id2.NodeID)
	}

	fpFromPEM, err := FingerprintFromPubKeyPEM(id1.PubKeyPEM)
	if err != nil || fpFromPEM != id1.Fingerprint {
		t.Fatalf("FingerprintFromPubKeyPEM mismatch: got=%q want=%q err=%v", fpFromPEM, id1.Fingerprint, err)
	}

	uri := EncodePairingURI(PairingInvite{
		Version:     1,
		NodeID:      id1.NodeID,
		Fingerprint: id1.Fingerprint,
		HostName:    "MacBook Pro",
		Token:       "tok_one_time_123",
		RelayURL:    "https://nex.tylinux.cn",
		DirectURLs:  []string{"https://mac.tailnet.ts.net/"},
		LANURLs:     []string{"http://192.168.1.20:8080/"},
	})
	parsed, err := ParsePairingURI(uri)
	if err != nil {
		t.Fatalf("ParsePairingURI failed: %v (uri=%s)", err, uri)
	}
	if parsed.NodeID != id1.NodeID || parsed.Fingerprint != id1.Fingerprint || parsed.HostName != "MacBook Pro" || parsed.Token != "tok_one_time_123" || len(parsed.DirectURLs) != 1 || len(parsed.LANURLs) != 1 {
		t.Fatalf("parsed URI mismatch: %+v", parsed)
	}
	if _, err := ParsePairingURI("https://example.com"); err == nil {
		t.Fatalf("expected non-nex scheme to be rejected")
	}
}

func TestPinnedMTLSYamuxTunnelMITMAndPairingGuard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hostID, err := GenerateIdentity("host")
	if err != nil {
		t.Fatal(err)
	}
	clientID, err := GenerateIdentity("phone")
	if err != nil {
		t.Fatal(err)
	}

	relaySrv := relay.New(relay.Config{})
	ts := httptest.NewServer(relaySrv.Handler())
	defer ts.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		peer, _ := PeerFromContext(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]any{"device_id": peer.DeviceID, "transport": peer.Transport})
	})
	mux.HandleFunc("/api/v1/remote/pair/complete", func(w http.ResponseWriter, r *http.Request) {
		peer, _ := PeerFromContext(r.Context())
		_, _ = w.Write([]byte(`{"paired_fp":"` + peer.PubKeyFP + `"}`))
	})
	mux.HandleFunc("/ws/echo", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		typ, msg, err := ws.Read(r.Context())
		if err == nil {
			_ = ws.Write(r.Context(), typ, msg)
		}
	})

	pairingOnly := true
	go func() {
		_ = relay.DialHostControl(ctx, ts.URL, hostID.NodeID, "", func(cCtx context.Context, _ string, conn net.Conn) {
			_ = ServeTunnelConn(cCtx, conn, hostID, mux, "relay", func(fp string) (string, bool, bool) {
				if fp == clientID.Fingerprint {
					return "dev_1", pairingOnly, true
				}
				return "", false, false
			}, func(_ string, _ *yamux.Session) func() { return func() {} })
		})
	}()

	// Wait until host registers on relay
	for time.Now().Add(2*time.Second).After(time.Now()) && relaySrv.Stats().OnlineHosts == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	// 1. Wrong server fingerprint MUST fail TLS pinning against MITM/rogue relay
	rawBad, err := relay.DialClientTunnel(ctx, ts.URL, hostID.NodeID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DialTunnelSession(ctx, rawBad, clientID, strings.Repeat("0", 64)); err == nil {
		t.Fatalf("expected fingerprint mismatch error for wrong expectedServerFP")
	}

	// 2. PairingOnly mode blocks /api/v1/sessions (403) while permitting /api/v1/remote/pair/complete
	raw1, err := relay.DialClientTunnel(ctx, ts.URL, hostID.NodeID, "")
	if err != nil {
		t.Fatal(err)
	}
	sess1, err := DialTunnelSession(ctx, raw1, clientID, hostID.Fingerprint)
	if err != nil {
		t.Fatalf("DialTunnelSession failed: %v", err)
	}
	hc1 := NewTunnelHTTPClient(sess1)

	respBlocked, err := hc1.Get("http://nex.tunnel/api/v1/sessions")
	if err != nil {
		t.Fatal(err)
	}
	_ = respBlocked.Body.Close()
	if respBlocked.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 during pairingOnly tunnel, got %d", respBlocked.StatusCode)
	}

	respPair, err := hc1.Post("http://nex.tunnel/api/v1/remote/pair/complete", "application/json", strings.NewReader("{}"))
	if err != nil || respPair.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on pair/complete, got resp=%v err=%v", respPair, err)
	}
	_ = respPair.Body.Close()
	_ = sess1.Close()

	// 3. Full paired tunnel supports HTTP + WebSocket streams over yamux
	pairingOnly = false
	raw2, err := relay.DialClientTunnel(ctx, ts.URL, hostID.NodeID, "")
	if err != nil {
		t.Fatal(err)
	}
	sess2, err := DialTunnelSession(ctx, raw2, clientID, hostID.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer sess2.Close()

	hc2 := NewTunnelHTTPClient(sess2)
	respOK, err := hc2.Get("http://nex.tunnel/api/v1/sessions")
	if err != nil || respOK.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on paired tunnel, got resp=%v err=%v", respOK, err)
	}
	var sessOut map[string]string
	_ = json.NewDecoder(respOK.Body).Decode(&sessOut)
	_ = respOK.Body.Close()
	if sessOut["device_id"] != "dev_1" || sessOut["transport"] != "relay" {
		t.Fatalf("unexpected PeerInfo propagated to handler: %+v", sessOut)
	}

	wsConn, _, err := websocket.Dial(ctx, "ws://nex.tunnel/ws/echo", &websocket.DialOptions{
		HTTPClient: hc2,
	})
	if err != nil {
		t.Fatalf("WebSocket over yamux tunnel failed: %v", err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "")
	_ = wsConn.Write(ctx, websocket.MessageText, []byte("WS_OVER_YAMUX_OK"))
	_, wsData, err := wsConn.Read(ctx)
	if err != nil || string(wsData) != "WS_OVER_YAMUX_OK" {
		t.Fatalf("unexpected WS echo over tunnel: %q err=%v", string(wsData), err)
	}
}
