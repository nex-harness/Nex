package server

import (
	"bytes"
	"compress/gzip"
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
	"testing/fstest"
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

func TestPreferencesGoalSettingsClampingAndSnapshot(t *testing.T) {
	srv, ts, _ := newTestServer(t, http.DefaultClient)

	putBody := `{
		"goal_max_rounds": 99,
		"goal_max_turns_per_round": 5,
		"goal_max_consecutive_no_progress": 9,
		"goal_verifier_provider_id": " anthropic ",
		"goal_verifier_model_id": " claude-sonnet-4-5 "
	}`
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/preferences", strings.NewReader(putBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/v1/preferences status=%d", resp.StatusCode)
	}
	var saved store.UserPreferences
	if err := json.NewDecoder(resp.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	if saved.GoalMaxRounds != 30 || saved.GoalMaxTurnsPerRound != 10 || saved.GoalMaxConsecutiveNoProgress != 5 ||
		saved.GoalVerifierProviderID != "anthropic" || saved.GoalVerifierModelID != "claude-sonnet-4-5" {
		t.Fatalf("unexpected clamped goal prefs: %+v", saved)
	}

	snap, err := srv.buildConfigSnapshot(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if snap.GoalSettings == nil || snap.GoalSettings.MaxRounds != 30 || snap.GoalSettings.MaxTurnsPerRound != 10 ||
		snap.GoalSettings.MaxConsecutiveNoProgress != 5 || snap.GoalSettings.VerifierProviderID != "anthropic" ||
		snap.GoalSettings.VerifierModelID != "claude-sonnet-4-5" {
		t.Fatalf("unexpected ConfigSnapshot.GoalSettings: %+v", snap.GoalSettings)
	}
}

func TestRunnerBrowseDirsEndpointAndCRUDRoutes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, ts, st := newTestServer(t, http.DefaultClient)

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

	// Cover Runners list + rename + sticky name across UpsertRunner, Session PATCH/GET/DELETE, Stats, Providers/Agents/Skills/Plugins/MCP CRUD + DELETE
	runResp, _ := http.Get(ts.URL + "/api/v1/runners")
	_ = runResp.Body.Close()

	renameReq, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/v1/runners/run_browse_1", bytes.NewReader([]byte(`{"name":"Custom Studio Box"}`)))
	renameResp, err := http.DefaultClient.Do(renameReq)
	if err != nil || renameResp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH runner rename failed: %v %v", renameResp, err)
	}
	var renamedRunner store.RunnerRecord
	_ = json.NewDecoder(renameResp.Body).Decode(&renamedRunner)
	_ = renameResp.Body.Close()
	if renamedRunner.Name != "Custom Studio Box" {
		t.Fatalf("expected renamed runner name 'Custom Studio Box', got %q", renamedRunner.Name)
	}
	// Simulate runner reconnect reporting original hostname/name; custom name must stay sticky
	if err := st.UpsertRunner(ctx, store.RunnerRecord{
		ID: "run_browse_1", Name: "OrigBoxName", Status: "online", LastSeenAt: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	reloadedRunner, err := st.GetRunner(ctx, "run_browse_1")
	if err != nil || reloadedRunner.Name != "Custom Studio Box" {
		t.Fatalf("expected sticky runner name 'Custom Studio Box' after upsert, got %+v err=%v", reloadedRunner, err)
	}

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

func TestSessionForkEndpointCopiesEventsUpToSeq(t *testing.T) {
	ctx := context.Background()
	_, ts, st := newTestServer(t, http.DefaultClient)

	_, err := st.CreateOrUpdateSession(ctx, store.SessionRecord{
		ID: "sess_src", RunnerID: "run_offline", Title: "Original", WorkspacePath: "/tmp",
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		_, err := st.AppendSessionEvent(ctx, store.SessionEvent{
			SessionID: "sess_src",
			TurnIdx:   i,
			EventType: "message_end",
			Payload:   json.RawMessage(fmt.Sprintf(`{"message":{"role":"user","content":[{"type":"text","text":"msg %d"}]}}`, i)),
		}, false)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Fork before seq 3 -> should copy only seq 1 & 2!
	rawResp := postJSON(t, ts.URL+"/api/v1/sessions/sess_src/fork", map[string]any{
		"before_seq": 3,
		"title":      "Edited Branch",
	})
	var parsed struct {
		Session      store.SessionRecord `json:"session"`
		CopiedEvents int                 `json:"copied_events"`
	}
	if err := json.Unmarshal(rawResp, &parsed); err != nil {
		t.Fatalf("unmarshal fork response: %v: %s", err, string(rawResp))
	}
	if parsed.CopiedEvents != 2 {
		t.Fatalf("expected 2 copied_events, got %+v", parsed)
	}
	if parsed.Session.Title != "Edited Branch" {
		t.Fatalf("unexpected forked title: %+v", parsed.Session)
	}
	evs, err := st.ListSessionEventsAfter(ctx, parsed.Session.ID, 0)
	if err != nil || len(evs) != 2 {
		t.Fatalf("expected 2 events in forked session, got len=%d err=%v", len(evs), err)
	}

	// Forking mid-run (after agent_start + message_end, before agent_end) MUST seal with agent_end!
	_, _ = st.CreateOrUpdateSession(ctx, store.SessionRecord{
		ID: "sess_seal", RunnerID: "run_offline", Title: "SealTest", WorkspacePath: "/tmp",
	})
	_, _ = st.AppendSessionEvent(ctx, store.SessionEvent{SessionID: "sess_seal", TurnIdx: 1, EventType: "agent_start", Payload: json.RawMessage(`{"type":"agent_start"}`)}, false)
	_, _ = st.AppendSessionEvent(ctx, store.SessionEvent{SessionID: "sess_seal", TurnIdx: 1, EventType: "message_end", Payload: json.RawMessage(`{"message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`)}, false)
	_, _ = st.AppendSessionEvent(ctx, store.SessionEvent{SessionID: "sess_seal", TurnIdx: 1, EventType: "turn_end", Payload: json.RawMessage(`{"type":"turn_end"}`)}, false)
	_, _ = st.AppendSessionEvent(ctx, store.SessionEvent{SessionID: "sess_seal", TurnIdx: 1, EventType: "agent_end", Payload: json.RawMessage(`{"type":"agent_end"}`)}, false)

	sealRaw := postJSON(t, ts.URL+"/api/v1/sessions/sess_seal/fork", map[string]any{"up_to_seq": 2})
	var sealParsed struct {
		Session store.SessionRecord `json:"session"`
	}
	if err := json.Unmarshal(sealRaw, &sealParsed); err != nil {
		t.Fatal(err)
	}
	sealEvs, _ := st.ListSessionEventsAfter(ctx, sealParsed.Session.ID, 0)
	if len(sealEvs) == 0 || sealEvs[len(sealEvs)-1].EventType != "agent_end" {
		t.Fatalf("expected forked session to end cleanly with agent_end, got %+v", sealEvs)
	}
}

type roundTripErr struct{}

func (roundTripErr) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("offline test transport")
}

type rewriteModelsDevTransport struct {
	modelsDevURL string
	base         http.RoundTripper
}

func (rt rewriteModelsDevTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() == ai.DefaultModelsDevURL {
		cloned := req.Clone(req.Context())
		rewritten, err := http.NewRequestWithContext(req.Context(), req.Method, rt.modelsDevURL, req.Body)
		if err != nil {
			return nil, err
		}
		rewritten.Header = cloned.Header
		return rt.base.RoundTrip(rewritten)
	}
	return rt.base.RoundTrip(req)
}

func TestModelsDevCatalogCacheSharedAcrossLookupAndFetchRemote(t *testing.T) {
	var modelsDevHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models-dev.json":
			modelsDevHits++
			_, _ = w.Write([]byte(`{
				"google": {
					"id": "google",
					"models": {
						"gemini-4-carbon": {
							"id": "gemini-4-carbon",
							"name": "Gemini 4 Carbon",
							"reasoning": true,
							"reasoning_options": [{"type": "effort", "values": ["low", "medium", "high"]}],
							"limit": {"context": 1048576, "output": 943718}
						}
					}
				}
			}`))
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"google/gemini-4-carbon"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	client := &http.Client{
		Transport: rewriteModelsDevTransport{
			modelsDevURL: upstream.URL + "/models-dev.json",
			base:         upstream.Client().Transport,
		},
	}

	_, ts, st := newTestServer(t, client)

	// 1. First lookup fetches from models.dev and caches in memory + disk
	r1 := postJSON(t, ts.URL+"/api/v1/models/lookup", map[string]string{"model_id": "gemini-4-carbon"})
	if !strings.Contains(string(r1), `"low","medium","high"`) {
		t.Fatalf("expected gemini-4-carbon reasoning_levels from models.dev, got %s", string(r1))
	}
	if modelsDevHits != 1 {
		t.Fatalf("expected 1 models.dev fetch, got %d", modelsDevHits)
	}

	// 2. Subsequent lookup + batch lookup + /v1/models fetch-remote all reuse the cache (0 new hits!)
	_ = postJSON(t, ts.URL+"/api/v1/models/lookup", map[string]any{"model_ids": []string{"google/gemini-4-carbon"}})
	frRaw := postJSON(t, ts.URL+"/api/v1/models/fetch-remote", map[string]string{"base_url": upstream.URL + "/v1"})
	if !strings.Contains(string(frRaw), `"matched_meta"`) || !strings.Contains(string(frRaw), `"gemini-4-carbon"`) {
		t.Fatalf("expected fetch-remote to include matched_meta from cache, got %s", string(frRaw))
	}
	if modelsDevHits != 1 {
		t.Fatalf("expected cache reuse (still 1 hit), got %d", modelsDevHits)
	}

	// 3. Verify disk cache file was persisted and works on a brand-new Server instance offline
	if _, err := os.Stat(filepath.Join(st.DataDir(), modelsDevCacheFileName)); err != nil {
		t.Fatalf("expected disk cache file persisted: %v", err)
	}
	srvReloaded, err := New(Config{
		Store:      st,
		HTTPClient: &http.Client{Transport: roundTripErr{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tsReloaded := httptest.NewServer(srvReloaded.Handler())
	defer tsReloaded.Close()

	rReload := postJSON(t, tsReloaded.URL+"/api/v1/models/lookup", map[string]string{"model_id": "gemini-4-carbon"})
	if !strings.Contains(string(rReload), `"Gemini 4 Carbon"`) {
		t.Fatalf("expected reloaded server to serve disk cache offline, got %s", string(rReload))
	}
}

func TestGzipCompressionAndStaticAssetCacheHeaders(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mockFS := fstest.MapFS{
		"index.html":          {Data: []byte("<!doctype html><html><body>Nex</body></html>")},
		"assets/app-1234.js":  {Data: []byte(strings.Repeat("console.log('nex');\n", 200))},
		"assets/app-1234.css": {Data: []byte(strings.Repeat(".nex{color:#fff;}\n", 200))},
	}
	srv, err := New(Config{
		Store:    st,
		StaticFS: mockFS,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	rawClient := &http.Client{
		Transport: &http.Transport{DisableCompression: true},
	}

	// 1. Hashed asset gets gzip encoding + immutable Cache-Control
	reqAsset, _ := http.NewRequest(http.MethodGet, ts.URL+"/assets/app-1234.js", nil)
	reqAsset.Header.Set("Accept-Encoding", "gzip")
	respAsset, err := rawClient.Do(reqAsset)
	if err != nil {
		t.Fatal(err)
	}
	defer respAsset.Body.Close()
	if respAsset.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip Content-Encoding on JS asset, got %q", respAsset.Header.Get("Content-Encoding"))
	}
	if !strings.Contains(respAsset.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("expected immutable Cache-Control on hashed asset, got %q", respAsset.Header.Get("Cache-Control"))
	}
	gr, err := gzip.NewReader(respAsset.Body)
	if err != nil {
		t.Fatalf("gzip reader failed: %v", err)
	}
	unzipped, err := io.ReadAll(gr)
	_ = gr.Close()
	if err != nil || !strings.Contains(string(unzipped), "console.log('nex')") {
		t.Fatalf("unexpected unzipped JS content: err=%v len=%d", err, len(unzipped))
	}

	// 2. JSON API endpoint gets gzip encoding when requested
	reqAPI, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/health", nil)
	reqAPI.Header.Set("Accept-Encoding", "gzip")
	respAPI, err := rawClient.Do(reqAPI)
	if err != nil {
		t.Fatal(err)
	}
	defer respAPI.Body.Close()
	if respAPI.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip Content-Encoding on JSON API, got %q", respAPI.Header.Get("Content-Encoding"))
	}

	// 3. SPA index.html uses no-cache so deployments take effect immediately
	reqIdx, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	respIdx, err := rawClient.Do(reqIdx)
	if err != nil {
		t.Fatal(err)
	}
	defer respIdx.Body.Close()
	if respIdx.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("expected no-cache on index.html, got %q", respIdx.Header.Get("Cache-Control"))
	}
}

func TestSessionRenamePreservedAcrossRunnerSessionMetaReplay(t *testing.T) {
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

	// 1. User has a session that was renamed on Server
	_, err = st.CreateOrUpdateSession(ctx, store.SessionRecord{
		ID:       "sess_rename_keep",
		RunnerID: "run_1",
		Title:    "My Custom Renamed Title",
		Status:   "idle",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 2. Runner sends agent_start with stale local SessionMeta carrying the old title
	stalePayload, _ := json.Marshal(map[string]any{
		"type": "agent_start",
		"session_meta": store.SessionRecord{
			ID:       "sess_rename_keep",
			RunnerID: "run_1",
			Title:    "Old Stale Runner Title",
			Status:   "running",
		},
	})
	rc := &runnerConn{id: "run_1"}
	env, err := protocol.NewEnvelope(protocol.TypeSessionEvent, store.SessionEvent{
		SessionID: "sess_rename_keep",
		Seq:       1,
		EventType: "agent_start",
		Payload:   stalePayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.ingestRunnerSessionEvent(ctx, rc, env)

	// 3. Server must preserve the renamed title while still updating status
	got, err := st.GetSession(ctx, "sess_rename_keep")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "My Custom Renamed Title" {
		t.Fatalf("expected renamed title to be preserved, got %q", got.Title)
	}
	if got.Status != "running" {
		t.Fatalf("expected status to update to running, got %q", got.Status)
	}
}
