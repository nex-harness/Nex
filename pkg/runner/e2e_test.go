package runner

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
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nex-harness/nex/pkg/agent"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/server"
	"github.com/nex-harness/nex/pkg/store"
)

type mockE2EProvider struct {
	mu   sync.Mutex
	step int
}

func (m *mockE2EProvider) Driver() ai.Driver { return ai.DriverAnthropic }

func (m *mockE2EProvider) Stream(_ context.Context, _ *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
	m.mu.Lock()
	s := m.step
	m.step++
	m.mu.Unlock()

	ch := make(chan ai.StreamEvent, 8)
	go func() {
		defer close(ch)
		switch s {
		case 0:
			// Offline standalone run
			msg := &ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock("Offline cron done.")}}
			ch <- ai.StreamEvent{Type: ai.StreamEventTextDelta, TextDelta: "Offline cron done."}
			ch <- ai.StreamEvent{Type: ai.StreamEventDone, Message: msg, StopReason: ai.StopReasonEndTurn, Usage: ai.Usage{InputTokens: 1000, OutputTokens: 50}}
		case 1:
			// Online interactive run: requests write tool (needs remote UI approval!)
			msg := &ai.Message{
				Role: ai.RoleAssistant,
				Content: []ai.ContentBlock{
					ai.ToolCallBlock("call_w1", "write", json.RawMessage(`{"file_path":"approved.txt","content":"hello from approved tool"}`)),
				},
			}
			ch <- ai.StreamEvent{Type: ai.StreamEventDone, Message: msg, StopReason: ai.StopReasonToolUse, Usage: ai.Usage{InputTokens: 2000, CacheReadTokens: 4000, OutputTokens: 80}}
		default:
			msg := &ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock("Approved write completed!")}}
			ch <- ai.StreamEvent{Type: ai.StreamEventTextDelta, TextDelta: "Approved write completed!"}
			ch <- ai.StreamEvent{Type: ai.StreamEventDone, Message: msg, StopReason: ai.StopReasonEndTurn, Usage: ai.Usage{InputTokens: 500, CacheReadTokens: 5500, OutputTokens: 40}}
		}
	}()
	return ch, nil
}

func TestEndToEndOfflineOutboxOnlineApprovalAndBlobPull(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 1. Setup Control Plane Server
	srvStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer srvStore.Close()

	cpServer, err := server.New(server.Config{
		Store:         srvStore,
		PairingSecret: "pair-secret-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := httptest.NewServer(cpServer.Handler())
	defer httpSrv.Close()
	wsBase := "ws" + strings.TrimPrefix(httpSrv.URL, "http")

	// 2. Setup Autonomous Runner (initially run a prompt OFFLINE before connecting!)
	workDir := t.TempDir()
	prov := &mockE2EProvider{}
	node, err := NewNode(Config{
		RunnerID:         "run_e2e_1",
		Name:             "E2E Mac Runner",
		AuthToken:        "pair-secret-123",
		ServerWSURL:      wsBase + "/ws/runner",
		DataDir:          t.TempDir(),
		WorkspaceDefault: workDir,
		ProviderOverride: prov,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	// Run offline session -> events go into Runner's local SQLite Outbox!
	offMsg, err := node.ExecutePrompt(ctx, protocol.PromptPayload{
		SessionID: "sess_offline_1",
		Text:      "Run offline health check",
	}, "cron", false)
	if err != nil || offMsg.PlainText() != "Offline cron done." {
		t.Fatalf("offline prompt failed: msg=%+v err=%v", offMsg, err)
	}
	unackedBefore, _ := node.Store().ListUnackedOutbox(ctx, 100)
	if len(unackedBefore) == 0 {
		t.Fatalf("expected offline events queued in Outbox!")
	}

	// 3. Connect Runner to Server -> Outbox must flush automatically!
	connectDone := make(chan struct{})
	go func() {
		defer close(connectDone)
		node.StartConnectLoop(ctx)
	}()
	defer func() {
		cancel()
		httpSrv.Close()
		<-connectDone
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs, _ := srvStore.ListSessionEventsAfter(ctx, "sess_offline_1", 0)
		if len(evs) >= 4 {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	syncedEvs, err := srvStore.ListSessionEventsAfter(ctx, "sess_offline_1", 0)
	if err != nil || len(syncedEvs) < 4 {
		t.Fatalf("offline outbox did not sync to server: len=%d err=%v", len(syncedEvs), err)
	}

	// 4. Connect UI Client to /ws/client, trigger interactive prompt requiring remote approval!
	cliWS, _, err := websocket.Dial(ctx, wsBase+"/ws/client", nil)
	if err != nil {
		t.Fatalf("dial /ws/client: %v", err)
	}
	defer cliWS.Close(websocket.StatusNormalClosure, "done")

	promptEnv, _ := protocol.NewEnvelope(protocol.TypeSessionPrompt, protocol.PromptPayload{
		SessionID:      "sess_online_2",
		RunnerID:       "run_e2e_1",
		WorkspacePath:  workDir,
		PermissionMode: string(agent.PermissionInteractive),
		Text:           "Create approved.txt",
	})
	pBytes, _ := json.Marshal(promptEnv)
	if err := cliWS.Write(ctx, websocket.MessageText, pBytes); err != nil {
		t.Fatal(err)
	}

	var sysPromptHash string
	gotAgentEnd := false

	for !gotAgentEnd {
		_, raw, err := cliWS.Read(ctx)
		if err != nil {
			t.Fatalf("client read ws: %v", err)
		}
		var env protocol.Envelope
		_ = json.Unmarshal(raw, &env)
		if env.Type != protocol.TypeSessionEvent || env.SessionID != "sess_online_2" {
			continue
		}
		var sev store.SessionEvent
		_ = json.Unmarshal(env.Payload, &sev)

		switch sev.EventType {
		case string(agent.EventApprovalRequested):
			var inner agent.Event
			_ = json.Unmarshal(sev.Payload, &inner)
			// Approve the write tool from the remote UI client!
			resEnv, _ := protocol.NewEnvelope(protocol.TypeApprovalResolve, protocol.ApprovalResolvePayload{
				ApprovalID: inner.ApprovalID,
				Approved:   true,
			})
			resEnv.SessionID = "sess_online_2"
			resEnv.RunnerID = "run_e2e_1"
			rb, _ := json.Marshal(resEnv)
			_ = cliWS.Write(ctx, websocket.MessageText, rb)

		case string(agent.EventTurnEnd):
			var inner agent.Event
			_ = json.Unmarshal(sev.Payload, &inner)
			if inner.Telemetry != nil && inner.Telemetry.SystemPromptBlobHash != "" {
				sysPromptHash = inner.Telemetry.SystemPromptBlobHash
			}

		case string(agent.EventAgentEnd):
			gotAgentEnd = true
		}
	}

	// Verify file was actually written on Runner workspace after UI approval
	written, err := os.ReadFile(filepath.Join(workDir, "approved.txt"))
	if err != nil || string(written) != "hello from approved tool" {
		t.Fatalf("approved tool write failed: %q err=%v", string(written), err)
	}

	// 5. Verify on-demand CAS Trace Blob pull via REST GET /api/v1/blobs/<hash> (Server pulls from Runner over WS!)
	if sysPromptHash == "" {
		t.Fatalf("missing SystemPromptBlobHash in telemetry")
	}
	blobResp, err := http.Get(fmt.Sprintf("%s/api/v1/blobs/%s", httpSrv.URL, sysPromptHash))
	if err != nil {
		t.Fatal(err)
	}
	defer blobResp.Body.Close()
	blobBody, _ := io.ReadAll(blobResp.Body)
	if blobResp.StatusCode != http.StatusOK || !strings.Contains(string(blobBody), "You are Nex") {
		t.Fatalf("on-demand CAS blob fetch failed: status=%d body=%q", blobResp.StatusCode, string(blobBody))
	}

	// 6. Verify REST /api/v1/stats and TypeScript plugin esbuild endpoint
	plBody := bytes.NewBufferString(`{"name":"demo-ts","source_ts":"const x: number = 42; export default function() { return x; }","enabled":true}`)
	plResp, err := http.Post(httpSrv.URL+"/api/v1/plugins", "application/json", plBody)
	if err != nil || plResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/v1/plugins esbuild failed: err=%v", err)
	}
	_ = plResp.Body.Close()

	statsResp, err := http.Get(httpSrv.URL + "/api/v1/stats?runner_id=run_e2e_1&window=today")
	if err != nil || statsResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/stats failed: err=%v", err)
	}
	var sum store.UsageSummary
	_ = json.NewDecoder(statsResp.Body).Decode(&sum)
	_ = statsResp.Body.Close()
	if sum.TurnCount < 2 || sum.TotalTokens == 0 {
		t.Fatalf("expected non-zero stats in server hourly rollup, got %+v", sum)
	}
}
