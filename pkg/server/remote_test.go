package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nex-harness/nex/pkg/relay"
	"github.com/nex-harness/nex/pkg/remote"
	"github.com/nex-harness/nex/pkg/store"
)

func TestServerRemotePairingMTLSAndRevocationLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	relaySrv := relay.New(relay.Config{AuthToken: "relay-sec"})
	relayTS := httptest.NewServer(relaySrv.Handler())
	defer relayTS.Close()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv, err := New(Config{
		Store:      st,
		ListenPort: 18999,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Configure remote access + enable Web Password to prove paired mTLS & device tokens authenticate independently
	reqPwd, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/auth/password", strings.NewReader(`{"new_password":"admin-secret"}`))
	respPwd, err := http.DefaultClient.Do(reqPwd)
	if err != nil || respPwd.StatusCode != http.StatusOK {
		t.Fatalf("set password failed: resp=%v err=%v", respPwd, err)
	}
	var pwdJSON struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(respPwd.Body).Decode(&pwdJSON)
	_ = respPwd.Body.Close()

	authedDo := func(method, url string, body []byte) *http.Response {
		req, _ := http.NewRequest(method, url, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+pwdJSON.Token)
		req.Header.Set("Content-Type", "application/json")
		res, dErr := http.DefaultClient.Do(req)
		if dErr != nil {
			t.Fatalf("%s %s failed: %v", method, url, dErr)
		}
		return res
	}

	cfgPayload, _ := json.Marshal(remote.HostConfig{
		Enabled:    true,
		HostName:   "Test-Workstation",
		RelayURL:   relayTS.URL,
		RelayToken: "relay-sec",
		DirectURLs: []string{ts.URL},
		PreferLAN:  false,
	})
	cfgRes := authedDo(http.MethodPut, ts.URL+"/api/v1/remote/config", cfgPayload)
	_ = cfgRes.Body.Close()
	if cfgRes.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/v1/remote/config status=%d", cfgRes.StatusCode)
	}

	// Wait until host registers onto relay
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && relaySrv.Stats().OnlineHosts == 0 {
		time.Sleep(15 * time.Millisecond)
	}
	if relaySrv.Stats().OnlineHosts != 1 {
		t.Fatalf("expected 1 host registered on relay, stats=%+v", relaySrv.Stats())
	}

	// 2. Start pairing window & decode nex://pair URI
	pairStartRes := authedDo(http.MethodPost, ts.URL+"/api/v1/remote/pair/start", []byte(`{"ttl_seconds":300}`))
	var pairStart remote.PairingSession
	_ = json.NewDecoder(pairStartRes.Body).Decode(&pairStart)
	_ = pairStartRes.Body.Close()
	invite, err := remote.ParsePairingURI(pairStart.PairURI)
	if err != nil {
		t.Fatalf("ParsePairingURI failed: %v", err)
	}

	// 3. Unpaired client connects through relay -> blocked (403) on /api/v1/sessions, succeeds on pair/complete
	phoneID, err := remote.GenerateIdentity("iPhone-16")
	if err != nil {
		t.Fatal(err)
	}
	rawConn1, err := relay.DialClientTunnel(ctx, invite.RelayURL, invite.NodeID, invite.RelayToken)
	if err != nil {
		t.Fatalf("DialClientTunnel failed: %v", err)
	}
	sess1, err := remote.DialTunnelSession(ctx, rawConn1, phoneID, invite.Fingerprint)
	if err != nil {
		t.Fatalf("DialTunnelSession failed: %v", err)
	}
	hc1 := remote.NewTunnelHTTPClient(sess1)

	blockedResp, err := hc1.Get("http://nex.tunnel/api/v1/sessions")
	if err != nil {
		t.Fatal(err)
	}
	_ = blockedResp.Body.Close()
	if blockedResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 before pairing complete, got %d", blockedResp.StatusCode)
	}

	completeBody, _ := json.Marshal(map[string]string{
		"pair_token":        invite.Token,
		"device_name":       "My iPhone",
		"platform":          "ios",
		"client_pubkey_pem": phoneID.PubKeyPEM,
	})
	compResp, err := hc1.Post("http://nex.tunnel/api/v1/remote/pair/complete", "application/json", bytes.NewReader(completeBody))
	if err != nil || compResp.StatusCode != http.StatusOK {
		t.Fatalf("pair/complete failed: resp=%v err=%v", compResp, err)
	}
	var compOut struct {
		DeviceID    string `json:"device_id"`
		DeviceToken string `json:"device_token"`
		NodeID      string `json:"node_id"`
	}
	_ = json.NewDecoder(compResp.Body).Decode(&compOut)
	_ = compResp.Body.Close()
	if compOut.DeviceID == "" || compOut.DeviceToken == "" || compOut.NodeID != invite.NodeID {
		t.Fatalf("unexpected pair/complete output: %+v", compOut)
	}
	_ = sess1.Close()

	// 4. Paired client reconnects via relay mTLS -> accesses /api/v1/sessions cleanly WITHOUT web password
	rawConn2, err := relay.DialClientTunnel(ctx, invite.RelayURL, invite.NodeID, invite.RelayToken)
	if err != nil {
		t.Fatal(err)
	}
	sess2, err := remote.DialTunnelSession(ctx, rawConn2, phoneID, invite.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer sess2.Close()

	hc2 := remote.NewTunnelHTTPClient(sess2)
	okResp, err := hc2.Get("http://nex.tunnel/api/v1/sessions")
	if err != nil || okResp.StatusCode != http.StatusOK {
		t.Fatalf("expected paired mTLS tunnel to authorize /api/v1/sessions, got resp=%v err=%v", okResp, err)
	}
	_ = okResp.Body.Close()

	// 5. Direct HTTPS/HTTP path with X-Nex-Device-Token also succeeds
	directReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/sessions", nil)
	directReq.Header.Set("X-Nex-Device-Token", compOut.DeviceToken)
	directRes, err := http.DefaultClient.Do(directReq)
	if err != nil || directRes.StatusCode != http.StatusOK {
		t.Fatalf("expected paired device token to authorize direct request, got resp=%v err=%v", directRes, err)
	}
	_ = directRes.Body.Close()

	// 6. Revoke device -> terminates active yamux tunnel immediately & rejects future direct/relay calls
	delRes := authedDo(http.MethodDelete, ts.URL+"/api/v1/remote/devices/"+compOut.DeviceID, nil)
	_ = delRes.Body.Close()
	if delRes.StatusCode != http.StatusOK {
		t.Fatalf("DELETE device failed: %d", delRes.StatusCode)
	}

	// Active sess2 should be torn down shortly
	closedDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(closedDeadline) && !sess2.IsClosed() {
		time.Sleep(15 * time.Millisecond)
	}
	if !sess2.IsClosed() {
		t.Fatalf("expected active yamux session for revoked device to close immediately")
	}

	// Direct X-Nex-Device-Token is now rejected (401)
	revReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/sessions", nil)
	revReq.Header.Set("X-Nex-Device-Token", compOut.DeviceToken)
	revRes, err := http.DefaultClient.Do(revReq)
	if err != nil || revRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for revoked device token, got resp=%v err=%v", revRes, err)
	}
	_ = revRes.Body.Close()
}
