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

	"github.com/coder/websocket"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

func readClientNotifyFrame(t *testing.T, ctx context.Context, cliWS *websocket.Conn) protocol.ClientNotificationPayload {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rCtx, cancel := context.WithDeadline(ctx, deadline)
		_, raw, err := cliWS.Read(rCtx)
		cancel()
		if err != nil {
			t.Fatalf("client read ws failed while waiting for client.notify: %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		if env.Type != protocol.TypeClientNotify {
			continue
		}
		var p protocol.ClientNotificationPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("unmarshal client.notify payload: %v", err)
		}
		return p
	}
	t.Fatal("timed out waiting for client.notify")
	return protocol.ClientNotificationPayload{}
}

func TestServerClientNotifyBroadcastsAcrossBackgroundAndPromptEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	_, _ = st.CreateOrUpdateSession(ctx, store.SessionRecord{
		ID:       "sess_bg",
		RunnerID: "run_notify_1",
		Title:    "Deploy API Refactor",
	})
	_, _ = st.CreateOrUpdateSession(ctx, store.SessionRecord{
		ID:              "sess_child",
		RunnerID:        "run_notify_1",
		ParentSessionID: "sess_bg",
		Title:           "Subagent Worker",
	})

	srv, err := New(Config{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	wsBase := "ws" + strings.TrimPrefix(ts.URL, "http")
	runnerWS, _, err := websocket.Dial(ctx, wsBase+"/ws/runner", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer runnerWS.Close(websocket.StatusNormalClosure, "")

	hello, _ := protocol.NewEnvelope(protocol.TypeRunnerHello, protocol.RunnerHelloPayload{
		RunnerID: "run_notify_1",
		Name:     "node-1",
	})
	hb, _ := json.Marshal(hello)
	if err := runnerWS.Write(ctx, websocket.MessageText, hb); err != nil {
		t.Fatal(err)
	}
	// Consume runner.welcome
	if _, _, err := runnerWS.Read(ctx); err != nil {
		t.Fatal(err)
	}

	cliWS, _, err := websocket.Dial(ctx, wsBase+"/ws/client", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cliWS.Close(websocket.StatusNormalClosure, "")

	// Client explicitly subscribes ONLY to a different session ("sess_other").
	subEnv, _ := protocol.NewEnvelope(protocol.TypeClientSubscribe, protocol.ClientSubscribePayload{
		SessionID: "sess_other",
	})
	sb, _ := json.Marshal(subEnv)
	if err := cliWS.Write(ctx, websocket.MessageText, sb); err != nil {
		t.Fatal(err)
	}

	sendRunnerSessionEvent := func(sid string, seq uint64, eventType string, inner any) {
		rawInner, _ := json.Marshal(inner)
		ev := store.SessionEvent{
			SessionID: sid,
			TurnIdx:   1,
			Seq:       seq,
			EventType: eventType,
			Payload:   rawInner,
		}
		env, _ := protocol.NewEnvelope(protocol.TypeSessionEvent, ev)
		env.SessionID = sid
		env.RunnerID = "run_notify_1"
		env.Seq = seq
		b, _ := json.Marshal(env)
		if err := runnerWS.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
		// Drain outbox.ack on runner WS
		_, _, _ = runnerWS.Read(ctx)
	}

	// 1. Subagent child session completion MUST NOT notify users
	sendRunnerSessionEvent("sess_child", 1, "agent_end", map[string]any{"type": "agent_end"})

	// 2. Question (ui_prompt_requested) on background session notifies even unsubscribed client
	sendRunnerSessionEvent("sess_bg", 1, "ui_prompt_requested", map[string]any{
		"type": "ui_prompt_requested",
		"ui_prompt": protocol.UIPromptRequestPayload{
			RequestID: "req_q1",
			Kind:      "select",
			Title:     "Which environment?",
			Message:   "Choose target cluster",
		},
	})
	qNote := readClientNotifyFrame(t, ctx, cliWS)
	if qNote.SessionID != "sess_bg" || qNote.Kind != "question" || qNote.Title != "Which environment?" || qNote.Body != "Choose target cluster" || qNote.RequestID != "req_q1" || qNote.SessionTitle != "Deploy API Refactor" {
		t.Fatalf("unexpected question client.notify: %+v", qNote)
	}

	// 3. Tool permission approval
	sendRunnerSessionEvent("sess_bg", 2, "approval_requested", map[string]any{
		"type":        "approval_requested",
		"approval_id": "apr_42",
		"tool_name":   "bash",
	})
	aNote := readClientNotifyFrame(t, ctx, cliWS)
	if aNote.Kind != "approval" || aNote.ToolName != "bash" || aNote.RequestID != "apr_42" {
		t.Fatalf("unexpected approval client.notify: %+v", aNote)
	}

	// 4. Plan submission followed by immediate agent_end emits ONLY plan_approval
	sendRunnerSessionEvent("sess_bg", 3, "tool_execution_end", map[string]any{
		"type":         "tool_execution_end",
		"tool_call_id": "tc_plan",
		"tool_name":    "exit_plan_mode",
		"tool_result":  map[string]any{"output": "ok", "is_error": false},
	})
	pNote := readClientNotifyFrame(t, ctx, cliWS)
	if pNote.Kind != "plan_approval" || pNote.ToolName != "exit_plan_mode" || pNote.RequestID != "tc_plan" {
		t.Fatalf("unexpected plan_approval client.notify: %+v", pNote)
	}
	sendRunnerSessionEvent("sess_bg", 4, "agent_end", map[string]any{"type": "agent_end"})

	// 5. Next turn completes normally with task_completed (proving plan gate reset on agent_start!)
	sendRunnerSessionEvent("sess_bg", 5, "agent_start", map[string]any{"type": "agent_start"})
	sendRunnerSessionEvent("sess_bg", 6, "agent_end", map[string]any{"type": "agent_end"})
	cNote := readClientNotifyFrame(t, ctx, cliWS)
	if cNote.Kind != "task_completed" || cNote.SessionID != "sess_bg" || cNote.SessionTitle != "Deploy API Refactor" {
		t.Fatalf("unexpected task_completed client.notify: %+v", cNote)
	}

	// 6. Browser human handoff
	handoffEnv, _ := protocol.NewEnvelope(protocol.TypeBrowserHandoff, map[string]any{
		"session_id": "sess_bg",
		"tab_id":     "tab_9",
		"state":      "awaiting_human",
		"reason":     "2FA Login Required",
		"message":    "Please complete verification code",
	})
	handoffEnv.SessionID = "sess_bg"
	handoffEnv.RunnerID = "run_notify_1"
	hb2, _ := json.Marshal(handoffEnv)
	if err := runnerWS.Write(ctx, websocket.MessageText, hb2); err != nil {
		t.Fatal(err)
	}
	bNote := readClientNotifyFrame(t, ctx, cliWS)
	if bNote.Kind != "browser_handoff" || bNote.Title != "2FA Login Required" || bNote.Body != "Please complete verification code" || bNote.RequestID != "tab_9" {
		t.Fatalf("unexpected browser_handoff client.notify: %+v", bNote)
	}

	// 7. Partial /api/v1/preferences PUT preserves notify defaults while explicit false overrides cleanly
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/preferences", bytes.NewBufferString(`{"theme":"light"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var updated store.UserPreferences
	_ = json.NewDecoder(resp.Body).Decode(&updated)
	_ = resp.Body.Close()
	if !updated.NotifyEnabled || !updated.NotifyOnComplete || !updated.NotifyOnInput || !updated.NotifyOnlyBackground {
		t.Fatalf("expected notification preference defaults to remain true after partial PUT: %+v", updated)
	}

	req2, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/preferences", bytes.NewBufferString(`{"notify_only_background":false}`))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	var updated2 store.UserPreferences
	_ = json.NewDecoder(resp2.Body).Decode(&updated2)
	_ = resp2.Body.Close()
	if !updated2.NotifyEnabled || updated2.NotifyOnlyBackground {
		t.Fatalf("expected notify_only_background=false while keeping notify_enabled=true, got: %+v", updated2)
	}
}
