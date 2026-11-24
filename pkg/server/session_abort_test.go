package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

func TestServerReconcilesOrphanedRunningSessionOnRunnerReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	_, err = st.CreateOrUpdateSession(ctx, store.SessionRecord{
		ID:       "sess_stuck_parent",
		RunnerID: "run_rec_1",
		Title:    "Stuck Parent",
		Status:   "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateOrUpdateSession(ctx, store.SessionRecord{
		ID:              "sess_stuck_child",
		RunnerID:        "run_rec_1",
		ParentSessionID: "sess_stuck_parent",
		Depth:           1,
		Title:           "Stuck Child",
		Status:          "running",
	})
	if err != nil {
		t.Fatal(err)
	}

	srv, err := New(Config{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	srv.reconcileDelay = 50 * time.Millisecond

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	wsBase := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Subscribe a client to observe the synthesized agent_end broadcast
	clientWS, _, err := websocket.Dial(ctx, wsBase+"/ws/client", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientWS.Close(websocket.StatusNormalClosure, "")
	subEnv, _ := protocol.NewEnvelope(protocol.TypeClientSubscribe, protocol.ClientSubscribePayload{
		SessionID: "sess_stuck_parent",
	})
	sb, _ := json.Marshal(subEnv)
	_ = clientWS.Write(ctx, websocket.MessageText, sb)

	runnerWS, _ := dialTestRunnerHello(t, ctx, wsBase+"/ws/runner", protocol.RunnerHelloPayload{
		RunnerID:             "run_rec_1",
		Name:                 "MacMini",
		OS:                   "darwin",
		Arch:                 "arm64",
		Version:              "0.2.0",
		ActiveSessionsSynced: true,
		ActiveSessionIDs:     nil,
	})
	defer runnerWS.Close(websocket.StatusNormalClosure, "")

	// Client should receive agent_end event after reconcileDelay
	deadline := time.Now().Add(3 * time.Second)
	gotParentEnd := false
	for !gotParentEnd && time.Now().Before(deadline) {
		_, raw, rErr := clientWS.Read(ctx)
		if rErr != nil {
			t.Fatal(rErr)
		}
		var env protocol.Envelope
		if json.Unmarshal(raw, &env) == nil && env.Type == protocol.TypeSessionEvent && env.SessionID == "sess_stuck_parent" {
			var se store.SessionEvent
			if json.Unmarshal(env.Payload, &se) == nil && se.EventType == "agent_end" {
				gotParentEnd = true
			}
		}
	}
	if !gotParentEnd {
		t.Fatal("expected reconciled agent_end broadcast to client")
	}

	pRec, _ := st.GetSession(ctx, "sess_stuck_parent")
	cRec, _ := st.GetSession(ctx, "sess_stuck_child")
	if pRec.Status != "idle" || cRec.Status != "idle" {
		t.Fatalf("expected parent & child reconciled to idle, got parent=%q child=%q", pRec.Status, cRec.Status)
	}
}

func TestServerAbortOfflineRunnerImmediatelyFinalizes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	_, err = st.CreateOrUpdateSession(ctx, store.SessionRecord{
		ID:       "sess_off_abort",
		RunnerID: "run_offline_now",
		Title:    "Offline stuck",
		Status:   "running",
	})
	if err != nil {
		t.Fatal(err)
	}

	srv, err := New(Config{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	wsBase := "ws" + strings.TrimPrefix(ts.URL, "http")

	clientWS, _, err := websocket.Dial(ctx, wsBase+"/ws/client", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientWS.Close(websocket.StatusNormalClosure, "")
	subEnv, _ := protocol.NewEnvelope(protocol.TypeClientSubscribe, protocol.ClientSubscribePayload{
		SessionID: "sess_off_abort",
	})
	sb, _ := json.Marshal(subEnv)
	_ = clientWS.Write(ctx, websocket.MessageText, sb)
	time.Sleep(20 * time.Millisecond)

	// Send session.abort with wrong/empty runner_id; server must resolve session's RunnerID and finalize immediately
	abEnv, _ := protocol.NewEnvelope(protocol.TypeSessionAbort, map[string]any{})
	abEnv.SessionID = "sess_off_abort"
	abBytes, _ := json.Marshal(abEnv)
	if err := clientWS.Write(ctx, websocket.MessageText, abBytes); err != nil {
		t.Fatal(err)
	}

	_, raw, err := clientWS.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil || env.Type != protocol.TypeSessionEvent {
		t.Fatalf("expected immediate agent_end session_event, got %s", string(raw))
	}
	var se store.SessionEvent
	_ = json.Unmarshal(env.Payload, &se)
	if se.EventType != "agent_end" || !strings.Contains(string(se.Payload), "aborted_offline") {
		t.Fatalf("expected aborted_offline agent_end, got %+v payload=%s", se, string(se.Payload))
	}

	rec, _ := st.GetSession(ctx, "sess_off_abort")
	if rec.Status != "idle" {
		t.Fatalf("expected session status=idle, got %q", rec.Status)
	}
}

func TestServerAbortOnlineUnresponsiveRunnerFallbackTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	_, err = st.CreateOrUpdateSession(ctx, store.SessionRecord{
		ID:       "sess_hang_1",
		RunnerID: "run_hang",
		Title:    "Hanging",
		Status:   "running",
	})
	if err != nil {
		t.Fatal(err)
	}

	srv, err := New(Config{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	srv.abortFallbackTimeout = 60 * time.Millisecond

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	wsBase := "ws" + strings.TrimPrefix(ts.URL, "http")

	runnerWS, _ := dialTestRunnerHello(t, ctx, wsBase+"/ws/runner", protocol.RunnerHelloPayload{
		RunnerID: "run_hang",
		Name:     "HungRunner",
		OS:       "darwin",
		Arch:     "arm64",
		Version:  "0.2.0",
	})
	defer runnerWS.Close(websocket.StatusNormalClosure, "")

	clientWS, _, err := websocket.Dial(ctx, wsBase+"/ws/client", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientWS.Close(websocket.StatusNormalClosure, "")
	subEnv, _ := protocol.NewEnvelope(protocol.TypeClientSubscribe, protocol.ClientSubscribePayload{
		SessionID: "sess_hang_1",
	})
	sb, _ := json.Marshal(subEnv)
	_ = clientWS.Write(ctx, websocket.MessageText, sb)
	time.Sleep(20 * time.Millisecond)

	abEnv, _ := protocol.NewEnvelope(protocol.TypeSessionAbort, map[string]any{})
	abEnv.SessionID = "sess_hang_1"
	abBytes, _ := json.Marshal(abEnv)
	_ = clientWS.Write(ctx, websocket.MessageText, abBytes)

	// Runner receives session.abort, deliberately ignores it
	_, rawRunnerAbort, err := runnerWS.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawRunnerAbort), protocol.TypeSessionAbort) {
		t.Fatalf("expected runner to receive session.abort, got %s", string(rawRunnerAbort))
	}

	// Client should receive fallback agent_end after abortFallbackTimeout
	_, rawEv, err := clientWS.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var evEnv protocol.Envelope
	_ = json.Unmarshal(rawEv, &evEnv)
	var se store.SessionEvent
	_ = json.Unmarshal(evEnv.Payload, &se)
	if se.EventType != "agent_end" || !strings.Contains(string(se.Payload), "aborted_timeout") {
		t.Fatalf("expected aborted_timeout fallback agent_end, got %+v payload=%s", se, string(se.Payload))
	}
}

func TestServerAbortRESTEndpoint(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	_, _ = st.CreateOrUpdateSession(ctx, store.SessionRecord{
		ID:       "sess_rest_abort",
		RunnerID: "run_missing",
		Title:    "REST Abort",
		Status:   "running",
	})

	srv, err := New(Config{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/v1/sessions/sess_rest_abort/abort", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var body struct {
		Aborted   string `json:"aborted"`
		Finalized bool   `json:"finalized"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Aborted != "sess_rest_abort" || !body.Finalized {
		t.Fatalf("unexpected abort response: %+v", body)
	}
	rec, _ := st.GetSession(ctx, "sess_rest_abort")
	if rec.Status != "idle" {
		t.Fatalf("expected status=idle after REST abort, got %q", rec.Status)
	}
}

func TestHealthExposesBuildMetadata(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// 1. Dev build exposes is_dev=true + build_sha
	devSrv, err := New(Config{
		Store:    st,
		Version:  "v0.2.0-dev",
		BuildSHA: "deadbeef",
	})
	if err != nil {
		t.Fatal(err)
	}
	tsDev := httptest.NewServer(devSrv.Handler())
	defer tsDev.Close()

	respDev, err := http.Get(tsDev.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer respDev.Body.Close()
	var bodyDev struct {
		OK       bool   `json:"ok"`
		Version  string `json:"version"`
		BuildSHA string `json:"build_sha"`
		IsDev    bool   `json:"is_dev"`
	}
	if err := json.NewDecoder(respDev.Body).Decode(&bodyDev); err != nil {
		t.Fatal(err)
	}
	if !bodyDev.OK || bodyDev.Version != "v0.2.0-dev" || bodyDev.BuildSHA != "deadbeef" || !bodyDev.IsDev {
		t.Fatalf("unexpected dev health payload: %+v", bodyDev)
	}

	// 2. Release build exposes is_dev=false
	relSrv, err := New(Config{
		Store:    st,
		Version:  "v0.2.0",
		BuildSHA: "cafebabe",
	})
	if err != nil {
		t.Fatal(err)
	}
	tsRel := httptest.NewServer(relSrv.Handler())
	defer tsRel.Close()

	respRel, err := http.Get(tsRel.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer respRel.Body.Close()
	var bodyRel struct {
		Version  string `json:"version"`
		BuildSHA string `json:"build_sha"`
		IsDev    bool   `json:"is_dev"`
	}
	if err := json.NewDecoder(respRel.Body).Decode(&bodyRel); err != nil {
		t.Fatal(err)
	}
	if bodyRel.Version != "v0.2.0" || bodyRel.BuildSHA != "cafebabe" || bodyRel.IsDev {
		t.Fatalf("unexpected release health payload: %+v", bodyRel)
	}
}
