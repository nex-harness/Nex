package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNativeBridgeAndAllInOneEmbeddedMode(t *testing.T) {
	var notifiedTitle string
	bridge := NewNativeBridge(t.TempDir(), func(title, _ string) {
		notifiedTitle = title
	}, nil)

	if err := bridge.SetAuthToken("tok_mygo_123"); err != nil {
		t.Fatal(err)
	}
	got, err := bridge.GetAuthToken()
	if err != nil || got != "tok_mygo_123" {
		t.Fatalf("token round-trip failed: got=%q err=%v", got, err)
	}
	bridge.SetMobilePushToken("apns_xyz")
	if bridge.GetMobilePushToken() != "apns_xyz" {
		t.Fatalf("push token mismatch")
	}
	bridge.Notify("Approval Required", "bash")
	if notifiedTitle != "Approval Required" {
		t.Fatalf("expected notification callback")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	inst, err := StartAllInOne(ctx, t.TempDir(), t.TempDir(), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("StartAllInOne failed: %v", err)
	}
	defer inst.Close()

	// Wait for embedded runner to register online with embedded server
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(inst.URL + "/api/v1/health")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if strings.Contains(string(body), `"online_runners":1`) {
				return
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("embedded runner did not connect to embedded server within timeout")
}
