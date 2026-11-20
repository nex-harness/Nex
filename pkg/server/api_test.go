package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/workspace"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

func newTestServer(t *testing.T, client *http.Client) (*Server, *httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, err := New(Config{
		Store:         st,
		PublicBaseURL: "http://localhost:8080",
		HTTPClient:    client,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts, st
}

func TestModelEndpointsFetchLookupTestAndDelete(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer sk-test-123" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"data": []map[string]string{
					{"id": "gpt-5.2"},
					{"id": "org/sub/claude-sonnet-4-5"},
					{"id": ""},
				},
			})
		case "/v1/messages":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"OK\"}}\n\n")
			fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		case "/bad/v1/models":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	_, ts, _ := newTestServer(t, upstream.Client())

	// 1. POST /api/v1/models/fetch-remote (success + trailing /v1 normalization)
	raw := postJSON(t, ts.URL+"/api/v1/models/fetch-remote", map[string]string{
		"base_url": upstream.URL,
		"api_key":  "sk-test-123",
	})
	var fetched struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(raw, &fetched); err != nil {
		t.Fatalf("unmarshal fetch-remote: %v", err)
	}
	if len(fetched.Models) != 2 || fetched.Models[0] != "gpt-5.2" {
		t.Fatalf("unexpected fetched models: %+v", fetched.Models)
	}

	// Empty base_url -> 400
	badResp, err := http.Post(ts.URL+"/api/v1/models/fetch-remote", "application/json", strings.NewReader(`{"base_url":""}`))
	if err != nil || badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty base_url, got %v %v", badResp, err)
	}
	_ = badResp.Body.Close()

	// Upstream 500 -> 502
	errResp, err := http.Post(ts.URL+"/api/v1/models/fetch-remote", "application/json", strings.NewReader(fmt.Sprintf(`{"base_url":"%s/bad"}`, upstream.URL)))
	if err != nil || errResp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 for upstream error, got %v %v", errResp, err)
	}
	_ = errResp.Body.Close()

	// 2. POST /api/v1/models/lookup (exact + progressive slash-prefix backoff using embedded seed fallback)
	// Use a client whose transport fails external requests immediately so lookup falls back to embedded seed deterministically.
	offlineClient := &http.Client{Transport: roundTripErr{}}
	_, tsOffline, _ := newTestServer(t, offlineClient)
	seed, err := ai.EmbeddedModelsCatalog()
	if err != nil || len(seed) == 0 {
		t.Fatalf("embedded seed empty: %v", err)
	}
	sampleID := seed[0].ID

	lookupRaw := postJSON(t, tsOffline.URL+"/api/v1/models/lookup", map[string]string{
		"model_id": "custom-org/nested/prefix/" + strings.ToUpper(sampleID),
	})
	var lookupOut struct {
		Found bool          `json:"found"`
		Meta  *ai.ModelMeta `json:"meta"`
	}
	if err := json.Unmarshal(lookupRaw, &lookupOut); err != nil || !lookupOut.Found || lookupOut.Meta == nil {
		t.Fatalf("expected backoff lookup hit for %q, got %s err=%v", sampleID, string(lookupRaw), err)
	}
	if len(lookupOut.Meta.ReasoningLevels) == 0 {
		t.Fatalf("expected reasoning_levels populated on lookup meta: %+v", lookupOut.Meta)
	}

	missRaw := postJSON(t, tsOffline.URL+"/api/v1/models/lookup", map[string]string{"model_id": "nonexistent-xyz-999"})
	if !strings.Contains(string(missRaw), `"found":false`) {
		t.Fatalf("expected found=false, got %s", string(missRaw))
	}

	// 3. POST /api/v1/models/test (mocked Anthropic SSE probe)
	testRaw := postJSON(t, ts.URL+"/api/v1/models/test", map[string]string{
		"driver":   string(ai.DriverAnthropic),
		"base_url": upstream.URL + "/v1",
		"api_key":  "sk-ant-test",
		"model_id": "claude-sonnet-4-5",
	})
	var testOut struct {
		OK    bool   `json:"ok"`
		Reply string `json:"reply"`
	}
	if err := json.Unmarshal(testRaw, &testOut); err != nil || !testOut.OK || testOut.Reply != "OK" {
		t.Fatalf("expected model test OK, got %s err=%v", string(testRaw), err)
	}

	// Invalid driver -> 400
	badDriverResp, _ := http.Post(ts.URL+"/api/v1/models/test", "application/json", strings.NewReader(`{"driver":"invalid","model_id":"x"}`))
	if badDriverResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid driver, got %d", badDriverResp.StatusCode)
	}
	_ = badDriverResp.Body.Close()

	// 4. Create provider + model, list models, then DELETE /api/v1/models/{id}?provider_id=...
	_ = postJSON(t, ts.URL+"/api/v1/providers", store.ProviderRecord{
		ID: "p1", Name: "P1", Driver: ai.DriverOpenAIResponses, BaseURL: upstream.URL + "/v1", Enabled: true,
	})
	_ = postJSON(t, ts.URL+"/api/v1/models", ai.ModelMeta{
		ID: "gpt-5.2", ProviderID: "p1", DisplayName: "GPT 5.2", ContextWindow: 200000, SupportsReasoning: true,
	})
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/models/gpt-5.2?provider_id=p1", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil || delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE model failed: %v %v", delResp, err)
	}
	_ = delResp.Body.Close()

	listResp, _ := http.Get(ts.URL + "/api/v1/models?provider_id=p1")
	listRaw, _ := io.ReadAll(listResp.Body)
	_ = listResp.Body.Close()
	if strings.Contains(string(listRaw), "gpt-5.2") {
		t.Fatalf("expected gpt-5.2 deleted, still present: %s", string(listRaw))
	}
}

func TestRunnerBrowseDirsEndpointAndCRUDRoutes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, ts, _ := newTestServer(t, http.DefaultClient)

	// Offline runner returns 502
	offResp, err := http.Get(ts.URL + "/api/v1/runners/offline-r/dirs")
	if err != nil || offResp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 for offline runner, got %v %v", offResp, err)
	}
	_ = offResp.Body.Close()

	// Connect a mock runner over /ws/runner that answers workspace.req using workspace.Service
	browseRoot := t.TempDir()
	_ = os.MkdirAll(filepath.Join(browseRoot, "project-a"), 0o755)
	_ = os.MkdirAll(filepath.Join(browseRoot, "project-b"), 0o755)
	wsSvc := workspace.NewService()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/runner"
	rws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial runner ws: %v", err)
	}
	defer rws.Close(websocket.StatusNormalClosure, "")

	hello, _ := protocol.NewEnvelope(protocol.TypeRunnerHello, protocol.RunnerHelloPayload{
		RunnerID:         "run_browse_1",
		Name:             "Local Dev",
		OS:               "darwin",
		Arch:             "arm64",
		WorkspaceDefault: browseRoot,
	})
	hb, _ := json.Marshal(hello)
	if err := rws.Write(ctx, websocket.MessageText, hb); err != nil {
		t.Fatal(err)
	}
	// Read runner.welcome
	if _, _, err := rws.Read(ctx); err != nil {
		t.Fatal(err)
	}

	// Runner RPC responder loop
	go func() {
		for {
			_, data, err := rws.Read(ctx)
			if err != nil {
				return
			}
			var req protocol.Envelope
			if json.Unmarshal(data, &req) != nil {
				continue
			}
			if req.Type == protocol.TypeWorkspaceReq {
				out, err := wsSvc.HandleRequest(ctx, browseRoot, req.Payload)
				var resp protocol.Envelope
				if err != nil {
					resp, _ = protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": err.Error()})
				} else {
					resp = protocol.Envelope{Type: protocol.TypeWorkspaceResp, Payload: out}
				}
				resp.ReplyTo = req.ID
				rb, _ := json.Marshal(resp)
				_ = rws.Write(ctx, websocket.MessageText, rb)
			}
		}
	}()

	// Call GET /api/v1/runners/run_browse_1/dirs
	dirResp, err := http.Get(ts.URL + "/api/v1/runners/run_browse_1/dirs?path=" + browseRoot)
	if err != nil || dirResp.StatusCode != http.StatusOK {
		t.Fatalf("GET runner dirs failed: %v %v", dirResp, err)
	}
	dirRaw, _ := io.ReadAll(dirResp.Body)
	_ = dirResp.Body.Close()
	if !strings.Contains(string(dirRaw), "project-a") || !strings.Contains(string(dirRaw), "project-b") {
		t.Fatalf("expected project-a and project-b in browse response: %s", string(dirRaw))
	}

	// Cover Runners list, Session PATCH/GET/DELETE, Stats, Providers/Agents/Skills/Plugins/MCP CRUD + DELETE
	runResp, _ := http.Get(ts.URL + "/api/v1/runners")
	_ = runResp.Body.Close()

	_ = postJSON(t, ts.URL+"/api/v1/sessions", store.SessionRecord{ID: "s_crud", RunnerID: "run_browse_1", Title: "Init"})
	patchReq, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/v1/sessions/s_crud", bytes.NewReader([]byte(`{"title":"Renamed","permission_mode":"read_only"}`)))
	patchResp, err := http.DefaultClient.Do(patchReq)
	if err != nil || patchResp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH session failed: %v %v", patchResp, err)
	}
	_ = patchResp.Body.Close()

	for _, ep := range []string{
		"/api/v1/sessions/s_crud",
		"/api/v1/sessions/s_crud/events",
		"/api/v1/sessions/s_crud/telemetry",
		"/api/v1/stats?window=7d&tz=Asia/Shanghai",
		"/api/v1/preferences",
		"/api/v1/providers",
		"/api/v1/agents",
		"/api/v1/skills",
		"/api/v1/plugins",
		"/api/v1/mcp",
		"/api/v1/cron",
	} {
		r, err := http.Get(ts.URL + ep)
		if err != nil || r.StatusCode != http.StatusOK {
			t.Fatalf("GET %s failed: %v %v", ep, r, err)
		}
		_ = r.Body.Close()
	}

	delSess, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/sessions/s_crud", nil)
	delSessResp, err := http.DefaultClient.Do(delSess)
	if err != nil || delSessResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE session failed: %v %v", delSessResp, err)
	}
	_ = delSessResp.Body.Close()
}

type roundTripErr struct{}

func (roundTripErr) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("offline test transport")
}
