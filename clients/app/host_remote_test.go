//go:build !ios && !android

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nex-harness/nex/pkg/relay"
	"github.com/nex-harness/nex/pkg/remote"
	"github.com/nex-harness/nex/pkg/server"
	"github.com/nex-harness/nex/pkg/store"
)

func TestHostServicePairConnectProxyAndRemove(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	relaySrv := relay.New(relay.Config{})
	relayTS := httptest.NewServer(relaySrv.Handler())
	defer relayTS.Close()

	srvStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer srvStore.Close()

	cpSrv, err := server.New(server.Config{Store: srvStore})
	if err != nil {
		t.Fatal(err)
	}
	defer cpSrv.Close()

	srvTS := httptest.NewServer(cpSrv.Handler())
	defer srvTS.Close()

	// Enable Web Password so only paired E2EE mTLS / device token can access APIs
	reqPwd, _ := http.NewRequest(http.MethodPut, srvTS.URL+"/api/v1/auth/password", strings.NewReader(`{"new_password":"pw1234"}`))
	pwdRes, err := http.DefaultClient.Do(reqPwd)
	if err != nil {
		t.Fatal(err)
	}
	var pwdOut struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(pwdRes.Body).Decode(&pwdOut)
	_ = pwdRes.Body.Close()

	authedReq := func(method, url string, body []byte) *http.Response {
		req, _ := http.NewRequest(method, url, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+pwdOut.Token)
		req.Header.Set("Content-Type", "application/json")
		res, dErr := http.DefaultClient.Do(req)
		if dErr != nil {
			t.Fatal(dErr)
		}
		return res
	}

	cfgBody, _ := json.Marshal(remote.HostConfig{
		Enabled:  true,
		HostName: "Studio-Mac",
		RelayURL: relayTS.URL,
	})
	cfgRes := authedReq(http.MethodPut, srvTS.URL+"/api/v1/remote/config", cfgBody)
	_ = cfgRes.Body.Close()

	for time.Now().Add(2*time.Second).After(time.Now()) && relaySrv.Stats().OnlineHosts == 0 {
		time.Sleep(15 * time.Millisecond)
	}

	pairRes := authedReq(http.MethodPost, srvTS.URL+"/api/v1/remote/pair/start", []byte(`{}`))
	var pairStart remote.PairingSession
	_ = json.NewDecoder(pairRes.Body).Decode(&pairStart)
	_ = pairRes.Body.Close()

	cfgDir := t.TempDir()
	secrets := &fileSecretStore{path: filepath.Join(cfgDir, "secrets.json")}
	svc := NewHostService(HostServiceOptions{
		ConfigDir: cfgDir,
		Secrets:   secrets,
	})
	defer svc.closeHost()

	info, err := svc.PairWithURI(ctx, pairStart.PairURI, "Field-Laptop")
	if err != nil {
		t.Fatalf("PairWithURI failed: %v", err)
	}
	if info.ConnectionMode != "paired" || info.ActiveTransport != "relay" || info.ActiveHostName != "Studio-Mac" || len(info.PairedHosts) != 1 {
		t.Fatalf("unexpected HostInfo after PairWithURI: %+v", info)
	}
	if !strings.HasPrefix(info.ServerURL, "http://127.0.0.1:") {
		t.Fatalf("expected loopback LocalProxy URL, got %q", info.ServerURL)
	}

	// Frontend calls ServerURL without needing the Web Password
	sessRes, err := http.Get(info.ServerURL + "/api/v1/sessions")
	if err != nil || sessRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 through LocalProxy URL, got resp=%v err=%v", sessRes, err)
	}
	_ = sessRes.Body.Close()

	// Persisted reload retains paired host
	reloaded := NewHostService(HostServiceOptions{ConfigDir: cfgDir, Secrets: secrets})
	defer reloaded.closeHost()
	if len(reloaded.ListPairedHosts()) != 1 || reloaded.ListPairedHosts()[0].NodeID != pairStart.NodeID {
		t.Fatalf("expected paired host persisted across reload, got %+v", reloaded.ListPairedHosts())
	}

	// Remove paired host resets active mode cleanly
	afterRemove, err := svc.RemovePairedHost(pairStart.NodeID)
	if err != nil {
		t.Fatalf("RemovePairedHost failed: %v", err)
	}
	if len(afterRemove.PairedHosts) != 0 || afterRemove.ConnectionMode == "paired" {
		t.Fatalf("unexpected HostInfo after RemovePairedHost: %+v", afterRemove)
	}
}
