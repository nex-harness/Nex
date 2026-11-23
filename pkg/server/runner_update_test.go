package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

func dialTestRunnerHello(t *testing.T, ctx context.Context, wsURL string, hello protocol.RunnerHelloPayload) (*websocket.Conn, protocol.RunnerWelcomePayload) {
	t.Helper()
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial runner: %v", err)
	}
	env, _ := protocol.NewEnvelope(protocol.TypeRunnerHello, hello)
	b, _ := json.Marshal(env)
	if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("ws write hello: %v", err)
	}
	_, rawWelcome, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("ws read welcome: %v", err)
	}
	var wEnv protocol.Envelope
	if err := json.Unmarshal(rawWelcome, &wEnv); err != nil || wEnv.Type != protocol.TypeRunnerWelcome {
		t.Fatalf("expected runner.welcome, got %s err=%v", string(rawWelcome), err)
	}
	var welcome protocol.RunnerWelcomePayload
	if err := json.Unmarshal(wEnv.Payload, &welcome); err != nil {
		t.Fatalf("decode welcome: %v", err)
	}
	return ws, welcome
}

func TestRunnerUpdateCompatibilityGating(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	runnersDir := t.TempDir()
	binBytes := []byte("fake-runner-binary-v0.2.0")
	binPath := filepath.Join(runnersDir, "nex-runner-linux-amd64")
	if err := os.WriteFile(binPath, binBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binBytes)
	targetSHA := hex.EncodeToString(sum[:])

	manifestBytes, _ := json.Marshal(RunnerManifest{
		Version:              "0.2.0",
		CommitSHA:            "abc1234",
		MinProtocolVersion:   2,
		RequiredCapabilities: []string{"self_update_v1"},
		Binaries: map[string]RunnerBinaryEntry{
			"linux/amd64": {
				Filename:  "nex-runner-linux-amd64",
				SHA256:    targetSHA,
				SizeBytes: int64(len(binBytes)),
			},
		},
	})
	if err := os.WriteFile(filepath.Join(runnersDir, "manifest.json"), manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	srv, err := New(Config{
		Store:      st,
		Version:    "0.2.0",
		RunnersDir: runnersDir,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	defer srv.Close()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	wsBase := "ws" + strings.TrimPrefix(ts.URL, "http")

	// 1. Up-to-date runner (same SHA + protocol 2 + capability)
	wsUpToDate, w1 := dialTestRunnerHello(t, ctx, wsBase+"/ws/runner", protocol.RunnerHelloPayload{
		RunnerID:        "run_ok",
		Name:            "OKNode",
		OS:              "linux",
		Arch:            "amd64",
		Version:         "0.2.0",
		BinarySHA256:    targetSHA,
		ProtocolVersion: 2,
		Capabilities:    []string{"self_update_v1"},
	})
	defer wsUpToDate.Close(websocket.StatusNormalClosure, "")
	if w1.UpdateStatus != "up_to_date" || w1.Incompatible || w1.AutoUpdateAllowed {
		t.Fatalf("unexpected up_to_date welcome: %+v", w1)
	}

	// 2. Compatible but outdated non-dev runner -> update_available + AutoUpdateAllowed=true
	wsAvail, w2 := dialTestRunnerHello(t, ctx, wsBase+"/ws/runner", protocol.RunnerHelloPayload{
		RunnerID:        "run_avail",
		Name:            "OldNode",
		OS:              "linux",
		Arch:            "amd64",
		Version:         "0.1.9",
		BinarySHA256:    strings.Repeat("0", 64),
		ProtocolVersion: 2,
		Capabilities:    []string{"self_update_v1"},
	})
	defer wsAvail.Close(websocket.StatusNormalClosure, "")
	if w2.UpdateStatus != "update_available" || w2.Incompatible || !w2.AutoUpdateAllowed {
		t.Fatalf("expected update_available with AutoUpdateAllowed=true, got %+v", w2)
	}

	// 3. Dev runner with old digest -> update_available, AutoUpdateAllowed=false
	wsDev, w3 := dialTestRunnerHello(t, ctx, wsBase+"/ws/runner", protocol.RunnerHelloPayload{
		RunnerID:        "run_dev",
		Name:            "DevNode",
		OS:              "linux",
		Arch:            "amd64",
		Version:         "0.1.9-dev+111",
		BinarySHA256:    strings.Repeat("1", 64),
		ProtocolVersion: 2,
		Capabilities:    []string{"self_update_v1"},
		IsDev:           true,
	})
	defer wsDev.Close(websocket.StatusNormalClosure, "")
	if w3.UpdateStatus != "update_available" || w3.AutoUpdateAllowed || w3.Incompatible {
		t.Fatalf("expected dev update_available with AutoUpdateAllowed=false, got %+v", w3)
	}

	// 4. Incompatible protocol version -> update_required + Incompatible=true, WS remains connected,
	//    and client prompt to this runner returns a clear error instead of executing.
	wsReq, w4 := dialTestRunnerHello(t, ctx, wsBase+"/ws/runner", protocol.RunnerHelloPayload{
		RunnerID:        "run_incompat",
		Name:            "AncientNode",
		OS:              "linux",
		Arch:            "amd64",
		Version:         "0.1.0",
		BinarySHA256:    strings.Repeat("2", 64),
		ProtocolVersion: 1,
		Capabilities:    []string{"self_update_v1"},
	})
	defer wsReq.Close(websocket.StatusNormalClosure, "")
	if w4.UpdateStatus != "update_required" || !w4.Incompatible || !w4.AutoUpdateAllowed {
		t.Fatalf("expected update_required + Incompatible + AutoUpdateAllowed, got %+v", w4)
	}

	// Verify /api/v1/runners exposes enriched compatibility & update metadata
	resp, err := http.Get(ts.URL + "/api/v1/runners")
	if err != nil {
		t.Fatalf("GET /api/v1/runners: %v", err)
	}
	defer resp.Body.Close()
	var list []store.RunnerRecord
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode runners: %v", err)
	}
	byID := map[string]store.RunnerRecord{}
	for _, r := range list {
		byID[r.ID] = r
	}
	if !byID["run_incompat"].Incompatible || byID["run_incompat"].UpdateStatus != "update_required" {
		t.Fatalf("expected run_incompat enriched as incompatible update_required, got %+v", byID["run_incompat"])
	}
	if !byID["run_dev"].IsDev || byID["run_dev"].UpdateStatus != "update_available" {
		t.Fatalf("expected run_dev enriched with is_dev=true & update_available, got %+v", byID["run_dev"])
	}

	// Connect a Web client and try sending a prompt to run_incompat -> rejected cleanly with error frame
	clientWS, _, err := websocket.Dial(ctx, wsBase+"/ws/client", nil)
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer clientWS.Close(websocket.StatusNormalClosure, "")
	promptEnv, _ := protocol.NewEnvelope(protocol.TypeSessionPrompt, protocol.PromptPayload{
		SessionID: "sess_blocked_1",
		RunnerID:  "run_incompat",
		Text:      "hi",
	})
	pb, _ := json.Marshal(promptEnv)
	if err := clientWS.Write(ctx, websocket.MessageText, pb); err != nil {
		t.Fatal(err)
	}
	_, rawErrFrame, err := clientWS.Read(ctx)
	if err != nil {
		t.Fatalf("read client error response: %v", err)
	}
	if !strings.Contains(string(rawErrFrame), "incompatible") || !strings.Contains(string(rawErrFrame), "0.2.0") {
		t.Fatalf("expected incompatibility error frame, got %s", string(rawErrFrame))
	}
}
