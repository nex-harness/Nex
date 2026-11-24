package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nex-harness/nex/pkg/agent"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

func TestRunnerReconcilesOrphanedRunningSessionsOnStartup(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	st, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateOrUpdateSession(ctx, store.SessionRecord{
		ID:       "sess_crashed_1",
		RunnerID: "run_reconcile",
		Title:    "Crashed mid-tool",
		Status:   "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendSessionEvent(ctx, store.SessionEvent{
		SessionID: "sess_crashed_1",
		TurnIdx:   0,
		EventType: "tool_execution_start",
		Payload:   json.RawMessage(`{"type":"tool_execution_start","tool_name":"bash"}`),
	}, false); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	node, err := NewNode(Config{
		RunnerID: "run_reconcile",
		DataDir:  dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	rec, err := node.Store().GetSession(ctx, "sess_crashed_1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "idle" {
		t.Fatalf("expected orphaned local session status=idle after startup reconcile, got %q", rec.Status)
	}

	evs, err := node.Store().ListSessionEventsAfter(ctx, "sess_crashed_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 || evs[len(evs)-1].EventType != string(agent.EventAgentEnd) {
		t.Fatalf("expected final event agent_end after startup reconcile, got %+v", evs)
	}
	if !strings.Contains(string(evs[len(evs)-1].Payload), "runner_restarted") {
		t.Fatalf("expected reason=runner_restarted in payload, got %s", string(evs[len(evs)-1].Payload))
	}

	outbox, err := node.Store().ListUnackedOutbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(outbox) == 0 || outbox[len(outbox)-1].SessionID != "sess_crashed_1" {
		t.Fatalf("expected reconciled agent_end enqueued in outbox, got %+v", outbox)
	}
}

func TestRunnerAbortOrphanedSessionEmitsAgentEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	receivedCh := make(chan protocol.Envelope, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")

		// Read runner.hello
		_, rawHello, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var helloEnv protocol.Envelope
		_ = json.Unmarshal(rawHello, &helloEnv)

		// Send runner.welcome
		wEnv, _ := protocol.NewEnvelope(protocol.TypeRunnerWelcome, protocol.RunnerWelcomePayload{RunnerID: "run_orphan_abort"})
		wb, _ := json.Marshal(wEnv)
		_ = ws.Write(ctx, websocket.MessageText, wb)

		// Send session.abort for an orphaned session not in activeSessions
		abEnv, _ := protocol.NewEnvelope(protocol.TypeSessionAbort, map[string]any{})
		abEnv.SessionID = "sess_orphan_99"
		abBytes, _ := json.Marshal(abEnv)
		_ = ws.Write(ctx, websocket.MessageText, abBytes)

		for {
			_, data, rErr := ws.Read(ctx)
			if rErr != nil {
				return
			}
			var e protocol.Envelope
			if json.Unmarshal(data, &e) == nil && e.Type == protocol.TypeSessionEvent {
				receivedCh <- e
				return
			}
		}
	}))
	defer srv.Close()

	node, err := NewNode(Config{
		RunnerID:    "run_orphan_abort",
		ServerWSURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/runner",
		DataDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	go node.StartConnectLoop(ctx)

	select {
	case evEnv := <-receivedCh:
		if evEnv.SessionID != "sess_orphan_99" {
			t.Fatalf("expected SessionID=sess_orphan_99, got %q", evEnv.SessionID)
		}
		var se store.SessionEvent
		if err := json.Unmarshal(evEnv.Payload, &se); err != nil {
			t.Fatal(err)
		}
		if se.EventType != string(agent.EventAgentEnd) || !strings.Contains(string(se.Payload), "aborted_orphan") {
			t.Fatalf("expected agent_end with reason=aborted_orphan, got %+v payload=%s", se, string(se.Payload))
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for synthetic agent_end from orphaned abort")
	}
}

func TestRunnerAbortCancelsChildSessionsAndHook(t *testing.T) {
	ctx := context.Background()
	node, err := NewNode(Config{
		RunnerID: "run_parent_child",
		DataDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	wiring := AttachDefaultSubsystems(node)
	defer wiring.Close()

	parentCtx, parentCancel := context.WithCancel(ctx)
	childCtx, childCancel := context.WithCancel(ctx)
	otherCtx, otherCancel := context.WithCancel(ctx)
	defer parentCancel()
	defer childCancel()
	defer otherCancel()

	node.mu.Lock()
	node.activeSessions["sess_parent"] = &activeSession{
		record: store.SessionRecord{ID: "sess_parent"},
		cancel: parentCancel,
	}
	node.activeSessions["sess_child"] = &activeSession{
		record: store.SessionRecord{ID: "sess_child", ParentSessionID: "sess_parent"},
		cancel: childCancel,
	}
	node.activeSessions["sess_unrelated"] = &activeSession{
		record: store.SessionRecord{ID: "sess_unrelated"},
		cancel: otherCancel,
	}
	node.mu.Unlock()

	node.AbortSession(ctx, "sess_parent")

	if parentCtx.Err() == nil {
		t.Fatal("expected parent session context to be cancelled")
	}
	if childCtx.Err() == nil {
		t.Fatal("expected child session context to be cancelled")
	}
	if otherCtx.Err() != nil {
		t.Fatal("expected unrelated session NOT to be cancelled")
	}
}
