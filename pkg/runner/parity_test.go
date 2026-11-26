package runner

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/nex-harness/nex/pkg/agent"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

type parityScriptProvider struct {
	mu       sync.Mutex
	requests []*ai.StreamRequest
	handler  func(idx int, req *ai.StreamRequest) *ai.Message
}

func (p *parityScriptProvider) Driver() ai.Driver { return ai.DriverAnthropic }

func (p *parityScriptProvider) Stream(_ context.Context, req *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
	p.mu.Lock()
	idx := len(p.requests)
	p.requests = append(p.requests, req)
	p.mu.Unlock()

	msg := p.handler(idx, req)
	ch := make(chan ai.StreamEvent, 2)
	if txt := msg.PlainText(); txt != "" {
		ch <- ai.StreamEvent{Type: ai.StreamEventTextDelta, TextDelta: txt}
	}
	ch <- ai.StreamEvent{Type: ai.StreamEventDone, Message: msg, StopReason: ai.StopReasonEndTurn}
	close(ch)
	return ch, nil
}

func TestRunnerCompactCommandAndCheckpointHydration(t *testing.T) {
	ctx := context.Background()

	prov := &parityScriptProvider{
		handler: func(_ int, req *ai.StreamRequest) *ai.Message {
			if strings.Contains(req.SystemPrompt, "conversation compactor") {
				return &ai.Message{
					Role:    ai.RoleAssistant,
					Content: []ai.ContentBlock{ai.TextBlock("## Goal & Constraints\n- Auth refactor focus.")},
				}
			}
			return &ai.Message{
				Role:    ai.RoleAssistant,
				Content: []ai.ContentBlock{ai.TextBlock("acknowledged")},
			}
		},
	}

	node, err := NewNode(Config{
		RunnerID:         "run_compact_test",
		DataDir:          t.TempDir(),
		WorkspaceDefault: t.TempDir(),
		ProviderOverride: prov,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	sessID := "sess_compact_1"
	// Populate 5 user+assistant turns (10 messages)
	for i := 0; i < 5; i++ {
		if _, err := node.ExecutePrompt(ctx, protocol.PromptPayload{
			SessionID: sessID,
			Text:      "Turn message " + strings.Repeat("details ", 20),
		}, "chat", false); err != nil {
			t.Fatalf("turn %d failed: %v", i, err)
		}
	}

	// Execute manual /compact command
	eventsBefore, _ := node.Store().ListSessionEventsAfter(ctx, sessID, 0)
	var seqBefore uint64
	if len(eventsBefore) > 0 {
		seqBefore = eventsBefore[len(eventsBefore)-1].Seq
	}
	compactReply, err := node.ExecutePrompt(ctx, protocol.PromptPayload{
		SessionID: sessID,
		Text:      "/compact focus on auth",
	}, "chat", false)
	if err != nil {
		t.Fatalf("/compact failed: %v", err)
	}
	if compactReply == nil || !strings.Contains(compactReply.PlainText(), "Auth refactor focus") {
		t.Fatalf("unexpected /compact summary msg: %+v", compactReply)
	}

	// Verify /compact emits structured compacting -> completed compaction events with zero message_end chat bubbles,
	// and updates session.LatestContextTokens.
	compactEvs, err := node.Store().ListSessionEventsAfter(ctx, sessID, seqBefore)
	if err != nil {
		t.Fatal(err)
	}
	var sawCompacting, sawCompleted bool
	for _, ev := range compactEvs {
		if ev.EventType == string(agent.EventMessageEnd) {
			t.Fatalf("expected /compact not to emit chat message_end bubbles, got seq=%d", ev.Seq)
		}
		if ev.EventType == string(agent.EventCompaction) {
			if strings.Contains(string(ev.Payload), `"status":"compacting"`) {
				sawCompacting = true
			}
			if strings.Contains(string(ev.Payload), `"status":"completed"`) {
				sawCompleted = true
			}
		}
	}
	if !sawCompacting || !sawCompleted {
		t.Fatalf("expected both compacting and completed EventCompaction (compacting=%v completed=%v)", sawCompacting, sawCompleted)
	}
	recAfter, err := node.Store().GetSession(ctx, sessID)
	if err != nil || recAfter.LatestContextTokens <= 0 {
		t.Fatalf("expected session LatestContextTokens updated after compaction, got %+v err=%v", recAfter, err)
	}

	// Verify loadSessionMessages hydrates from the compaction checkpoint instead of all 10+ historical messages!
	hydrated := node.loadSessionMessages(ctx, sessID)
	if len(hydrated) == 0 || !strings.Contains(hydrated[0].PlainText(), "<context-compaction-summary") {
		t.Fatalf("expected hydrated history to start with compaction summary, got len=%d first=%+v", len(hydrated), hydrated)
	}
	if len(hydrated) >= 10 {
		t.Fatalf("expected hydrated history to be smaller than 10 messages, got %d", len(hydrated))
	}
}

func TestRunnerPluginSlashCommandDispatch(t *testing.T) {
	ctx := context.Background()

	prov := &parityScriptProvider{
		handler: func(_ int, _ *ai.StreamRequest) *ai.Message {
			t.Errorf("LLM provider should NOT be called for direct plugin slash command!")
			return &ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock("unexpected")}}
		},
	}

	node, err := NewNode(Config{
		RunnerID:         "run_slash_test",
		DataDir:          t.TempDir(),
		WorkspaceDefault: t.TempDir(),
		ProviderOverride: prov,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	// Install a TS plugin registering `/ping-plugin`
	pluginTS := `
	export default function (nex: any) {
		nex.registerCommand("ping-plugin", {
			description: "Test plugin slash command",
			handler: (args: string, ctx: any) => {
				ctx.ui.setStatus("ping", "ran:" + args);
			}
		});
	}
	`
	if err := node.store.UpsertPlugin(ctx, store.PluginRecord{
		ID:       "pl_ping",
		Name:     "ping",
		SourceTS: pluginTS,
		Enabled:  true,
	}); err != nil {
		t.Fatal(err)
	}

	wiring := AttachDefaultSubsystems(node)
	defer wiring.Close()

	reply, err := node.ExecutePrompt(ctx, protocol.PromptPayload{
		SessionID: "sess_slash_1",
		Text:      "/ping-plugin hello-arg",
	}, "chat", false)
	if err != nil {
		t.Fatalf("plugin slash command failed: %v", err)
	}
	if !strings.Contains(reply.PlainText(), "Executed command `/ping-plugin`") {
		t.Fatalf("unexpected slash command reply: %q", reply.PlainText())
	}

	// Verify UI status event from plugin handler was recorded in session events
	evs, err := node.store.ListSessionEventsAfter(ctx, "sess_slash_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	foundStatus := false
	for _, ev := range evs {
		if ev.EventType == string(agent.EventUIStatusUpdated) && strings.Contains(string(ev.Payload), "ran:hello-arg") {
			foundStatus = true
		}
	}
	if !foundStatus {
		t.Fatalf("expected plugin status event with 'ran:hello-arg' in session events")
	}
}

func TestRunnerPlanModePromptAndReadOnlyBashAllowed(t *testing.T) {
	ctx := context.Background()

	var sawPlanPrompt bool
	var sawBashResult string
	var sawWriteBlocked bool

	prov := &parityScriptProvider{
		handler: func(idx int, req *ai.StreamRequest) *ai.Message {
			if strings.Contains(req.FullSystemPrompt(), "# Plan Mode (Read-Only)") {
				sawPlanPrompt = true
			}
			if idx == 0 {
				return &ai.Message{
					Role: ai.RoleAssistant,
					Content: []ai.ContentBlock{
						ai.ToolCallBlock("tc_ro", "bash", json.RawMessage(`{"command":"printf 'ro-ok'"}`)),
						ai.ToolCallBlock("tc_rw", "write", json.RawMessage(`{"file_path":"blocked.txt","content":"nope"}`)),
					},
				}
			}
			for _, m := range req.Messages {
				if m.Role == ai.RoleToolResult && m.ToolCallID == "tc_ro" {
					sawBashResult = m.PlainText()
				}
				if m.Role == ai.RoleToolResult && m.ToolCallID == "tc_rw" && m.IsError && strings.Contains(m.PlainText(), "read-only") {
					sawWriteBlocked = true
				}
			}
			return &ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock("plan ready")}}
		},
	}

	node, err := NewNode(Config{
		RunnerID:         "run_plan_test",
		DataDir:          t.TempDir(),
		WorkspaceDefault: t.TempDir(),
		ProviderOverride: prov,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	_, err = node.ExecutePrompt(ctx, protocol.PromptPayload{
		SessionID:      "sess_plan_1",
		PermissionMode: string(agent.PermissionReadOnly),
		Text:           "Inspect and plan",
	}, "chat", false)
	if err != nil {
		t.Fatalf("plan mode run failed: %v", err)
	}
	if !sawPlanPrompt {
		t.Fatal("expected # Plan Mode (Read-Only) section in system prompt")
	}
	if !strings.Contains(sawBashResult, "ro-ok") {
		t.Fatalf("expected read-only bash printf to run in Plan Mode, got %q", sawBashResult)
	}
	if !sawWriteBlocked {
		t.Fatal("expected write tool to be blocked in Plan Mode")
	}
}

func TestGoalModeMultiRoundEndToEndPersistence(t *testing.T) {
	ctx := context.Background()
	verifyCount := 0
	var sawFeedback bool

	prov := &parityScriptProvider{
		handler: func(_ int, req *ai.StreamRequest) *ai.Message {
			if strings.Contains(req.SystemPrompt, "skeptical verification judge") {
				verifyCount++
				if verifyCount == 1 {
					return &ai.Message{
						Role: ai.RoleAssistant,
						Content: []ai.ContentBlock{ai.TextBlock(`{
							"achieved": false,
							"confidence": "high",
							"summary": "Output file not created yet.",
							"missing_items": ["Create result.txt"],
							"next_step": "Write result.txt"
						}`)},
					}
				}
				return &ai.Message{
					Role: ai.RoleAssistant,
					Content: []ai.ContentBlock{ai.TextBlock(`{
						"achieved": true,
						"confidence": "high",
						"summary": "result.txt written and verified."
					}`)},
				}
			}

			for _, m := range req.Messages {
				if m.Role == ai.RoleUser && strings.Contains(m.PlainText(), "<goal-verification-feedback") {
					sawFeedback = true
				}
			}

			last := req.Messages[len(req.Messages)-1]
			if last.Role == ai.RoleUser {
				return &ai.Message{
					Role: ai.RoleAssistant,
					Content: []ai.ContentBlock{{
						Type:       ai.BlockToolCall,
						ToolCallID: "tc_bash_" + strings.Repeat("1", verifyCount+1),
						ToolName:   "bash",
						Arguments:  json.RawMessage(`{"command":"printf ok > result.txt"}`),
					}},
				}
			}
			return &ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock("Round complete.")}}
		},
	}

	node, err := NewNode(Config{
		RunnerID:         "run_goal_e2e",
		DataDir:          t.TempDir(),
		WorkspaceDefault: t.TempDir(),
		ProviderOverride: prov,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	node.ApplyConfigSnapshot(ctx, &protocol.ConfigSnapshot{
		GoalSettings: &protocol.GoalConfigSync{
			MaxRounds:                4,
			MaxTurnsPerRound:         20,
			MaxConsecutiveNoProgress: 2,
		},
	})

	sessID := "sess_goal_e2e_1"
	_, err = node.ExecutePrompt(ctx, protocol.PromptPayload{
		SessionID:      sessID,
		PermissionMode: string(agent.PermissionFullAccess),
		Text:           "/goal Create result.txt",
	}, "chat", false)
	if err != nil {
		t.Fatalf("ExecutePrompt /goal failed: %v", err)
	}
	if verifyCount != 2 || !sawFeedback {
		t.Fatalf("expected 2 verification rounds with feedback injection, verifyCount=%d sawFeedback=%v", verifyCount, sawFeedback)
	}

	evs, err := node.Store().ListSessionEventsAfter(ctx, sessID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var phases []string
	var agentStarts, agentEnds int
	for _, ev := range evs {
		switch ev.EventType {
		case string(agent.EventAgentStart):
			agentStarts++
		case string(agent.EventAgentEnd):
			agentEnds++
		case string(agent.EventGoalLifecycle):
			var parsed struct {
				Goal *agent.GoalLifecycleInfo `json:"goal"`
			}
			if json.Unmarshal(ev.Payload, &parsed) == nil && parsed.Goal != nil {
				phases = append(phases, parsed.Goal.Phase)
			}
		}
	}
	if agentStarts != 1 || agentEnds != 1 {
		t.Fatalf("expected 1 agent_start and 1 agent_end, got starts=%d ends=%d", agentStarts, agentEnds)
	}
	if len(phases) == 0 || phases[len(phases)-1] != "completed" {
		t.Fatalf("expected persisted goal_lifecycle ending in completed, got %v", phases)
	}
}
