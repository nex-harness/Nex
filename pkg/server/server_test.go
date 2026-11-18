package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

func TestServerRESTArchiveLLMProxyAndWSReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Upstream mock LLM + models.dev server
	var gotUpstreamAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models.json":
			_, _ = w.Write([]byte(`{"anthropic":{"id":"anthropic","name":"Anthropic","models":{"claude-opus-4-5":{"id":"claude-opus-4-5","name":"Opus","tool_call":true,"reasoning":true,"cost":{"input":5,"output":25,"cache_read":0.5},"limit":{"context":200000,"output":64000}}}}}`))
		case "/v1/messages":
			gotUpstreamAuth = r.Header.Get("x-api-key")
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv, err := New(Config{
		Store:         st,
		PublicBaseURL: "http://localhost:8080",
		HTTPClient:    upstream.Client(),
		StaticFS: fstest.MapFS{
			"index.html": &fstest.MapFile{Data: []byte("<html>SPA_OK</html>")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Health & SPA fallback
	resp, err := http.Get(ts.URL + "/api/v1/health")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("health failed: %v", err)
	}
	_ = resp.Body.Close()

	spaResp, err := http.Get(ts.URL + "/deep/route/chat")
	if err != nil || spaResp.StatusCode != 200 {
		t.Fatalf("SPA fallback failed: %v", err)
	}
	spaBody, _ := io.ReadAll(spaResp.Body)
	_ = spaResp.Body.Close()
	if !strings.Contains(string(spaBody), "SPA_OK") {
		t.Fatalf("unexpected SPA body: %q", string(spaBody))
	}

	// 2. Preferences, Provider & LLM Proxy key injection
	_ = postJSON(t, ts.URL+"/api/v1/preferences", map[string]any{"theme": "dark", "auto_archive_after_days": 30})
	_ = postJSON(t, ts.URL+"/api/v1/providers", store.ProviderRecord{
		ID: "anthropic", Name: "Anthropic", Driver: ai.DriverAnthropic,
		BaseURL: upstream.URL + "/v1", APIKey: "sk-secret-server-held", ProxyMode: "server_proxy", Enabled: true,
	})
	_ = postJSON(t, ts.URL+"/api/v1/models/sync", map[string]string{"url": upstream.URL + "/models.json"})
	_ = postJSON(t, ts.URL+"/api/v1/agents", store.AgentProfile{Slug: "reviewer", DisplayName: "Reviewer"})
	_ = postJSON(t, ts.URL+"/api/v1/skills", store.SkillRecord{Name: "web", Description: "Web", Enabled: true})
	_ = postJSON(t, ts.URL+"/api/v1/mcp", store.MCPServerRecord{Name: "linear", HostMode: "server_hosted", Enabled: true})
	cronOut := postJSON(t, ts.URL+"/api/v1/cron", store.CronJobRecord{RunnerID: "r1", Title: "Daily", CronExpr: "0 9 * * *", Prompt: "hi", Enabled: true})
	var createdCron store.CronJobRecord
	_ = json.Unmarshal(cronOut, &createdCron)

	// Test /llm-proxy/anthropic/messages injects Server's x-api-key
	proxyResp, err := http.Post(ts.URL+"/llm-proxy/anthropic/messages", "application/json", strings.NewReader(`{}`))
	if err != nil || proxyResp.StatusCode != 200 {
		t.Fatalf("llm-proxy failed: %v", err)
	}
	_ = proxyResp.Body.Close()
	if gotUpstreamAuth != "sk-secret-server-held" {
		t.Fatalf("expected llm-proxy to inject server API key, got %q", gotUpstreamAuth)
	}

	// 3. Session creation, WS last_seen_seq replay, and Archive/Unarchive/Delete HTTP endpoints
	sessOut := postJSON(t, ts.URL+"/api/v1/sessions", store.SessionRecord{ID: "sess_s1", RunnerID: "r1", Title: "Demo"})
	if !strings.Contains(string(sessOut), "sess_s1") {
		t.Fatalf("create session failed: %s", string(sessOut))
	}
	_, _ = st.AppendSessionEvent(ctx, store.SessionEvent{SessionID: "sess_s1", EventType: "message_update", Payload: json.RawMessage(`{"delta":"1"}`)}, false)
	_, _ = st.AppendSessionEvent(ctx, store.SessionEvent{SessionID: "sess_s1", EventType: "message_update", Payload: json.RawMessage(`{"delta":"2"}`)}, false)

	// Subscribe with last_seen_seq=1 -> must replay seq=2 only!
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/client"
	cws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cws.Close(websocket.StatusNormalClosure, "")
	subEnv, _ := protocol.NewEnvelope(protocol.TypeClientSubscribe, protocol.ClientSubscribePayload{
		SessionID: "sess_s1", LastSeenSeq: 1,
	})
	sb, _ := json.Marshal(subEnv)
	_ = cws.Write(ctx, websocket.MessageText, sb)
	_, rawReplay, err := cws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var rep protocol.Envelope
	_ = json.Unmarshal(rawReplay, &rep)
	if rep.Seq != 2 {
		t.Fatalf("expected replayed seq=2, got %d", rep.Seq)
	}

	// Archive -> List -> Unarchive -> Archive -> Delete Permanently
	_ = postJSON(t, ts.URL+"/api/v1/sessions/sess_s1/archive", nil)
	archResp, _ := http.Get(ts.URL + "/api/v1/archives")
	archBytes, _ := io.ReadAll(archResp.Body)
	_ = archResp.Body.Close()
	if !strings.Contains(string(archBytes), "sess_s1") {
		t.Fatalf("archived session missing from /api/v1/archives: %s", string(archBytes))
	}

	_ = postJSON(t, ts.URL+"/api/v1/archives/sess_s1/unarchive", nil)
	_ = postJSON(t, ts.URL+"/api/v1/sessions/sess_s1/archive", nil)
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/archives/sess_s1", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil || delResp.StatusCode != 200 {
		t.Fatalf("DELETE archive failed: %v", err)
	}
	_ = delResp.Body.Close()

	delCronReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/cron/"+createdCron.ID, nil)
	delCronResp, _ := http.DefaultClient.Do(delCronReq)
	if delCronResp != nil {
		_ = delCronResp.Body.Close()
	}
}

func postJSON(t *testing.T, url string, body any) []byte {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	resp, err := http.Post(url, "application/json", r)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		t.Fatalf("POST %s status %d: %s", url, resp.StatusCode, string(raw))
	}
	return raw
}
