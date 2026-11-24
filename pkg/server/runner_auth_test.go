package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

func TestRunnerAuthEndpointsAndEnforcement(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv, err := New(Config{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. GET /api/v1/runners/auth initializes a persistent pairing_secret
	resp, err := http.Get(ts.URL + "/api/v1/runners/auth")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/runners/auth failed: %v %v", resp, err)
	}
	var auth1 struct {
		PairingSecret string `json:"pairing_secret"`
		RequireAuth   bool   `json:"require_auth"`
		EnvManaged    bool   `json:"env_managed"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&auth1)
	_ = resp.Body.Close()
	if len(auth1.PairingSecret) < 32 || auth1.EnvManaged {
		t.Fatalf("unexpected initial runner auth config: %+v", auth1)
	}

	// 2. Enable require_auth via PUT /api/v1/runners/auth
	reqPut, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/runners/auth", bytes.NewReader([]byte(`{"require_auth":true}`)))
	putResp, err := http.DefaultClient.Do(reqPut)
	if err != nil || putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/v1/runners/auth failed: %v %v", putResp, err)
	}
	_ = putResp.Body.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/runner"

	// 3. Runner with wrong token is rejected with StatusPolicyViolation
	wsBad, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	badHello, _ := protocol.NewEnvelope(protocol.TypeRunnerHello, protocol.RunnerHelloPayload{
		RunnerID:  "run_bad",
		Name:      "Bad",
		AuthToken: "wrong-secret",
	})
	bBad, _ := json.Marshal(badHello)
	_ = wsBad.Write(ctx, websocket.MessageText, bBad)
	_, _, rErr := wsBad.Read(ctx)
	_ = wsBad.CloseNow()
	if rErr == nil {
		t.Fatalf("expected unauthorized runner to be rejected")
	}

	// 4. Runner with valid pairing_secret is accepted
	wsGood, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	goodHello, _ := protocol.NewEnvelope(protocol.TypeRunnerHello, protocol.RunnerHelloPayload{
		RunnerID:  "run_good",
		Name:      "Good",
		AuthToken: auth1.PairingSecret,
	})
	bGood, _ := json.Marshal(goodHello)
	_ = wsGood.Write(ctx, websocket.MessageText, bGood)
	_, welcomeRaw, wErr := wsGood.Read(ctx)
	_ = wsGood.CloseNow()
	if wErr != nil || !strings.Contains(string(welcomeRaw), "runner.welcome") {
		t.Fatalf("expected authorized runner to receive welcome, got err=%v raw=%s", wErr, string(welcomeRaw))
	}

	// 5. Rotate pairing_secret via POST /api/v1/runners/auth/rotate
	rotResp, err := http.Post(ts.URL+"/api/v1/runners/auth/rotate", "application/json", nil)
	if err != nil || rotResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/v1/runners/auth/rotate failed: %v %v", rotResp, err)
	}
	var auth2 struct {
		PairingSecret string `json:"pairing_secret"`
	}
	_ = json.NewDecoder(rotResp.Body).Decode(&auth2)
	_ = rotResp.Body.Close()
	if auth2.PairingSecret == "" || auth2.PairingSecret == auth1.PairingSecret {
		t.Fatalf("expected rotated secret to change, got %q", auth2.PairingSecret)
	}

	// 6. Paired device token (for nex-relay paired runners) is also accepted
	devTok := "dev-relay-token-xyz"
	_, err = st.UpsertPairedDevice(ctx, store.PairedDevice{
		ID:        "dev_runner_1",
		Name:      "RelayRunner",
		Platform:  "linux",
		PubKeyFP:  "fp_test_1",
		TokenHash: sha256Hex(devTok),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !srv.verifyRunnerAuthToken(ctx, "run_relay_1", devTok) {
		t.Fatalf("expected paired device token to authorize runner")
	}
}
