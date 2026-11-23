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

func TestRunnerManifestAndBinaryDownloadAuth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	runnersDir := t.TempDir()
	binBytes := []byte("real-runner-binary-bytes-0.2.0")
	binPath := filepath.Join(runnersDir, "nex-runner-linux-amd64")
	if err := os.WriteFile(binPath, binBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binBytes)
	targetSHA := hex.EncodeToString(sum[:])

	manifestBytes, _ := json.Marshal(RunnerManifest{
		Version:            "0.2.0",
		CommitSHA:          "sha020",
		MinProtocolVersion: 1,
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

	// Host a simulated GitHub/Gitea Release manifest server
	var serveRelease bool = true
	releaseSHA := targetSHA
	releaseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !serveRelease {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		relManifest := RunnerManifest{
			Version:            "0.2.0",
			CommitSHA:          "sha020",
			MinProtocolVersion: 1,
			ReleaseBaseURL:     "https://releases.example.com/v0.2.0",
			Binaries: map[string]RunnerBinaryEntry{
				"linux/amd64": {
					Filename:  "nex-runner-linux-amd64",
					SHA256:    releaseSHA,
					SizeBytes: int64(len(binBytes)),
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(relManifest)
	}))
	defer releaseSrv.Close()

	srv, err := New(Config{
		Store:              st,
		Version:            "0.2.0",
		PairingSecret:      "pair-secret-123",
		RunnersDir:         runnersDir,
		ReleaseManifestURL: releaseSrv.URL,
	})
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	// Register a runner with an individual AuthToken too
	if err := st.UpsertRunner(ctx, store.RunnerRecord{
		ID:        "run_token_1",
		Name:      "TokenRunner",
		OS:        "linux",
		Arch:      "amd64",
		Version:   "0.1.0",
		Status:    "online",
		AuthToken: "runner-individual-tok",
	}); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Verify GET /api/v1/runners/manifest includes ReleaseURL & SHA256
	resp, err := http.Get(ts.URL + "/api/v1/runners/manifest")
	if err != nil {
		t.Fatalf("GET manifest: %v", err)
	}
	var mf RunnerManifest
	if err := json.NewDecoder(resp.Body).Decode(&mf); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	_ = resp.Body.Close()
	if mf.Version != "0.2.0" || mf.Binaries["linux/amd64"].SHA256 != targetSHA || mf.Binaries["linux/amd64"].ReleaseURL != "https://releases.example.com/v0.2.0/nex-runner-linux-amd64" {
		t.Fatalf("unexpected combined manifest: %+v", mf)
	}

	// 2. Verify Release-first download URL ordering vs fallback-only when Release mismatches
	_, _, _, urls, _, _ := srv.resolveTargetForPlatform(ctx, "linux", "amd64")
	if len(urls) != 2 || urls[0] != "https://releases.example.com/v0.2.0/nex-runner-linux-amd64" || urls[1] != "/api/v1/runners/binary/linux/amd64" {
		t.Fatalf("expected release-first ordering, got %v", urls)
	}

	// Reset cache & simulate mismatched Release SHA -> fallback-only
	srv.mu.Lock()
	srv.releaseCache = releaseManifestCache{}
	srv.mu.Unlock()
	releaseSHA = strings.Repeat("f", 64)
	_, _, _, urlsMismatch, _, _ := srv.resolveTargetForPlatform(ctx, "linux", "amd64")
	if len(urlsMismatch) != 1 || urlsMismatch[0] != "/api/v1/runners/binary/linux/amd64" {
		t.Fatalf("expected server fallback only when release SHA mismatches, got %v", urlsMismatch)
	}

	// 3. Verify GET /api/v1/runners/binary/linux/amd64 auth & headers
	// Missing token -> 401
	unauthResp, err := http.Get(ts.URL + "/api/v1/runners/binary/linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", unauthResp.StatusCode)
	}

	// Wrong token -> 401
	reqBad, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/runners/binary/linux/amd64", nil)
	reqBad.Header.Set("Authorization", "Bearer wrong-tok")
	badResp, err := http.DefaultClient.Do(reqBad)
	if err != nil {
		t.Fatal(err)
	}
	_ = badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 with bad token, got %d", badResp.StatusCode)
	}

	// Bearer PairingSecret -> 200 with exact bytes & X-Binary-SHA256
	reqOK, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/runners/binary/linux/amd64", nil)
	reqOK.Header.Set("Authorization", "Bearer pair-secret-123")
	okResp, err := http.DefaultClient.Do(reqOK)
	if err != nil {
		t.Fatal(err)
	}
	if okResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with Bearer PairingSecret, got %d", okResp.StatusCode)
	}
	if okResp.Header.Get("X-Binary-SHA256") != targetSHA {
		t.Fatalf("expected X-Binary-SHA256=%s, got %s", targetSHA, okResp.Header.Get("X-Binary-SHA256"))
	}
	if okResp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("expected application/octet-stream, got %s", okResp.Header.Get("Content-Type"))
	}
	var gotBuf [128]byte
	n, _ := okResp.Body.Read(gotBuf[:])
	_ = okResp.Body.Close()
	if string(gotBuf[:n]) != string(binBytes) {
		t.Fatalf("downloaded bytes mismatch: %q vs %q", string(gotBuf[:n]), string(binBytes))
	}

	// Query ?token= with registered runner AuthToken -> 200
	qResp, err := http.Get(ts.URL + "/api/v1/runners/binary/linux/amd64?token=runner-individual-tok")
	if err != nil {
		t.Fatal(err)
	}
	_ = qResp.Body.Close()
	if qResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with runner token query param, got %d", qResp.StatusCode)
	}

	// Unsupported OS/arch -> 400; missing binary -> 404
	badPlat, _ := http.Get(ts.URL + "/api/v1/runners/binary/plan9/mips?token=pair-secret-123")
	_ = badPlat.Body.Close()
	if badPlat.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for unsupported platform, got %d", badPlat.StatusCode)
	}
	missResp, _ := http.Get(ts.URL + "/api/v1/runners/binary/darwin/arm64?token=pair-secret-123")
	_ = missResp.Body.Close()
	if missResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for missing binary, got %d", missResp.StatusCode)
	}
}

func TestRunnerUpdateTriggerAndUpdateAll(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	runnersDir := t.TempDir()
	binBytes := []byte("runner-0.2.0-bytes")
	if err := os.WriteFile(filepath.Join(runnersDir, "nex-runner-linux-amd64"), binBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binBytes)
	targetSHA := hex.EncodeToString(sum[:])

	manifestBytes, _ := json.Marshal(RunnerManifest{
		Version:            "0.2.0",
		MinProtocolVersion: 1,
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
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	wsBase := "ws" + strings.TrimPrefix(ts.URL, "http")

	wsRel, _ := dialTestRunnerHello(t, ctx, wsBase+"/ws/runner", protocol.RunnerHelloPayload{
		RunnerID:        "run_rel_old",
		Name:            "ReleaseOld",
		OS:              "linux",
		Arch:            "amd64",
		Version:         "0.1.0",
		BinarySHA256:    strings.Repeat("a", 64),
		ProtocolVersion: 1,
		Capabilities:    []string{"self_update_v1"},
	})
	defer wsRel.Close(websocket.StatusNormalClosure, "")

	wsDev, _ := dialTestRunnerHello(t, ctx, wsBase+"/ws/runner", protocol.RunnerHelloPayload{
		RunnerID:        "run_dev_local",
		Name:            "DevNode",
		OS:              "linux",
		Arch:            "amd64",
		Version:         "dev",
		BinarySHA256:    strings.Repeat("b", 64),
		ProtocolVersion: 1,
		Capabilities:    []string{"self_update_v1"},
	})
	defer wsDev.Close(websocket.StatusNormalClosure, "")

	// Offline runner saved in store
	_ = st.UpsertRunner(ctx, store.RunnerRecord{
		ID:      "run_offline",
		Name:    "Offline",
		OS:      "linux",
		Arch:    "amd64",
		Version: "0.1.0",
		Status:  "offline",
	})

	// 1. POST /api/v1/runners/update-all triggers release runner and skips dev/offline runners
	respAll, err := http.Post(ts.URL+"/api/v1/runners/update-all", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if respAll.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 from update-all, got %d", respAll.StatusCode)
	}
	var allBody struct {
		Triggered []struct {
			RunnerID string `json:"runner_id"`
			Mode     string `json:"mode"`
		} `json:"triggered"`
		Skipped []struct {
			RunnerID string `json:"runner_id"`
			Reason   string `json:"reason"`
		} `json:"skipped"`
	}
	if err := json.NewDecoder(respAll.Body).Decode(&allBody); err != nil {
		t.Fatal(err)
	}
	_ = respAll.Body.Close()
	if len(allBody.Triggered) != 1 || allBody.Triggered[0].RunnerID != "run_rel_old" {
		t.Fatalf("expected only run_rel_old triggered, got %+v", allBody)
	}

	// Read runner.update frame sent to wsRel
	_, rawRelFrame, err := wsRel.Read(ctx)
	if err != nil {
		t.Fatalf("wsRel read runner.update: %v", err)
	}
	var envRel protocol.Envelope
	if err := json.Unmarshal(rawRelFrame, &envRel); err != nil || envRel.Type != protocol.TypeRunnerUpdate {
		t.Fatalf("expected runner.update on wsRel, got %s", string(rawRelFrame))
	}

	// 2. Manual POST /api/v1/runners/run_dev_local/update triggers dev runner with Force=true
	respDev, err := http.Post(ts.URL+"/api/v1/runners/run_dev_local/update", "application/json", strings.NewReader(`{"mode":"immediate"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = respDev.Body.Close()
	if respDev.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 for manual dev update, got %d", respDev.StatusCode)
	}
	_, rawDevFrame, err := wsDev.Read(ctx)
	if err != nil {
		t.Fatalf("wsDev read runner.update: %v", err)
	}
	var envDev protocol.Envelope
	if err := json.Unmarshal(rawDevFrame, &envDev); err != nil || envDev.Type != protocol.TypeRunnerUpdate {
		t.Fatalf("expected runner.update on wsDev, got %s", string(rawDevFrame))
	}
	var updPayload protocol.RunnerUpdatePayload
	if err := json.Unmarshal(envDev.Payload, &updPayload); err != nil || !updPayload.Force || updPayload.Mode != "immediate" {
		t.Fatalf("expected Force=true immediate payload, got %+v", updPayload)
	}

	// Verify runtime UpdatePhase updated to queued
	meta := srv.getRunnerRuntimeMetaCopy("run_dev_local")
	if meta == nil || meta.UpdatePhase != "queued" {
		t.Fatalf("expected UpdatePhase=queued for run_dev_local, got %+v", meta)
	}

	// 3. Offline runner -> 409, unknown -> 404
	respOff, _ := http.Post(ts.URL+"/api/v1/runners/run_offline/update", "application/json", nil)
	_ = respOff.Body.Close()
	if respOff.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for offline runner, got %d", respOff.StatusCode)
	}
	respMiss, _ := http.Post(ts.URL+"/api/v1/runners/nonexistent/update", "application/json", nil)
	_ = respMiss.Body.Close()
	if respMiss.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent runner, got %d", respMiss.StatusCode)
	}
}
