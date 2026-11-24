package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/tools"
	"github.com/nex-harness/nex/pkg/store"
)

type scriptedProvider struct {
	turns []func(req *ai.StreamRequest) []ai.StreamEvent
	idx   int
}

func (p *scriptedProvider) Driver() ai.Driver { return ai.DriverAnthropic }

func (p *scriptedProvider) Stream(_ context.Context, req *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
	ch := make(chan ai.StreamEvent, 16)
	fn := p.turns[p.idx]
	p.idx++
	events := fn(req)
	go func() {
		defer close(ch)
		for _, ev := range events {
			ch <- ev
		}
	}()
	return ch, nil
}

type funcTool struct {
	name     string
	readOnly bool
	fn       func(ctx context.Context, args json.RawMessage) (tools.Result, error)
}

func (t *funcTool) Name() string                      { return t.name }
func (t *funcTool) Category() string                  { return "system" }
func (t *funcTool) IsReadOnly(_ json.RawMessage) bool { return t.readOnly }
func (t *funcTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{Name: t.name, Category: "system", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (t *funcTool) Execute(ctx context.Context, _ string, rawArgs json.RawMessage, _ tools.UpdateFunc) (tools.Result, error) {
	return t.fn(ctx, rawArgs)
}

type mockApprover struct {
	approve bool
	reason  string
	calls   int32
}

func (m *mockApprover) RequestApproval(_ context.Context, _ ApprovalRequest) (bool, string, error) {
	atomic.AddInt32(&m.calls, 1)
	return m.approve, m.reason, nil
}

func TestLoopSteerInterruptFollowUpTelemetryAndApproval(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	sess, err := st.CreateOrUpdateSession(ctx, store.SessionRecord{
		ID: "sess_loop_1", RunnerID: "run_local", Title: "Agent Loop Test",
	})
	if err != nil {
		t.Fatal(err)
	}

	workDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(workDir, "target.txt"), []byte("hello world\n"), 0o644)

	var loopRef *Loop
	var secondToolExecuted atomic.Bool

	// Tool 1 triggers a live Steer message mid-batch so Tool 2 MUST be skipped!
	tool1 := &funcTool{
		name:     "step_one",
		readOnly: true,
		fn: func(_ context.Context, _ json.RawMessage) (tools.Result, error) {
			loopRef.Steer(ai.NewUserTextMessage("WAIT! Stop step_two and pivot immediately."))
			return tools.Result{Output: "step_one_ok"}, nil
		},
	}
	tool2 := &funcTool{
		name:     "step_two",
		readOnly: false,
		fn: func(_ context.Context, _ json.RawMessage) (tools.Result, error) {
			secondToolExecuted.Store(true)
			return tools.Result{Output: "step_two_ok"}, nil
		},
	}

	prov := &scriptedProvider{
		turns: []func(req *ai.StreamRequest) []ai.StreamEvent{
			// Turn 1: emits 2 tool calls -> step_one triggers Steer -> step_two gets skipped!
			func(_ *ai.StreamRequest) []ai.StreamEvent {
				msg := &ai.Message{
					Role: ai.RoleAssistant,
					Content: []ai.ContentBlock{
						ai.ToolCallBlock("tc_1", "step_one", json.RawMessage(`{}`)),
						ai.ToolCallBlock("tc_2", "step_two", json.RawMessage(`{}`)),
					},
				}
				return []ai.StreamEvent{
					{Type: ai.StreamEventStart},
					{Type: ai.StreamEventDone, Message: msg, StopReason: ai.StopReasonToolUse, Usage: ai.Usage{InputTokens: 2000, CacheReadTokens: 8000, OutputTokens: 150}},
				}
			},
			// Turn 2: sees steer message, invokes write tool (requires Interactive Approval!), and enqueues a FollowUp!
			func(req *ai.StreamRequest) []ai.StreamEvent {
				foundSteer := false
				foundSkip := false
				for _, m := range req.Messages {
					if strings.Contains(m.PlainText(), "Stop step_two") {
						foundSteer = true
					}
					if m.Role == ai.RoleToolResult && m.ToolCallID == "tc_2" && strings.Contains(m.PlainText(), "Skipped") {
						foundSkip = true
					}
				}
				if !foundSteer || !foundSkip {
					t.Errorf("steer or skipped tool_result missing in Turn 2: steer=%v skip=%v", foundSteer, foundSkip)
				}
				loopRef.FollowUp(ai.NewUserTextMessage("Now give final confirmation."))
				msg := &ai.Message{
					Role: ai.RoleAssistant,
					Content: []ai.ContentBlock{
						ai.ToolCallBlock("tc_write", "write", json.RawMessage(`{"file_path":"out.txt","content":"done"}`)),
					},
				}
				return []ai.StreamEvent{
					{Type: ai.StreamEventStart},
					{Type: ai.StreamEventDone, Message: msg, StopReason: ai.StopReasonToolUse, Usage: ai.Usage{InputTokens: 1000, CacheReadTokens: 9000, OutputTokens: 100}},
				}
			},
			// Turn 3: after write succeeds, responds to end turn -> FollowUp kicks in for Turn 4!
			func(_ *ai.StreamRequest) []ai.StreamEvent {
				msg := &ai.Message{
					Role:    ai.RoleAssistant,
					Content: []ai.ContentBlock{ai.TextBlock("File written.")},
				}
				return []ai.StreamEvent{
					{Type: ai.StreamEventTextDelta, TextDelta: "File written."},
					{Type: ai.StreamEventDone, Message: msg, StopReason: ai.StopReasonEndTurn, Usage: ai.Usage{InputTokens: 1200, CacheReadTokens: 9000, OutputTokens: 50}},
				}
			},
			// Turn 4: handles the FollowUp message
			func(req *ai.StreamRequest) []ai.StreamEvent {
				last := req.Messages[len(req.Messages)-1]
				if !strings.Contains(last.PlainText(), "final confirmation") {
					t.Errorf("expected followUp message, got %+v", last)
				}
				msg := &ai.Message{
					Role:    ai.RoleAssistant,
					Content: []ai.ContentBlock{ai.TextBlock("All confirmed!")},
				}
				return []ai.StreamEvent{
					{Type: ai.StreamEventTextDelta, TextDelta: "All confirmed!"},
					{Type: ai.StreamEventDone, Message: msg, StopReason: ai.StopReasonEndTurn, Usage: ai.Usage{InputTokens: 1500, CacheReadTokens: 9500, OutputTokens: 40}},
				}
			},
		},
	}

	approver := &mockApprover{approve: true}
	var turnTelemetries []store.TurnTelemetry

	loopRef = NewLoop(LoopConfig{
		SessionID:  sess.ID,
		RunnerID:   "run_local",
		ProviderID: "anthropic",
		Model:      "claude-sonnet-4-5",
		Provider:   prov,
		WorkDir:    workDir,
		SystemSections: []ai.SystemSection{
			{Category: CatSystemPrompt, Content: "Core agent prompt."},
			{Category: CatSkills, Content: "- browser-use: Control browser"},
			{Category: CatMCPTools, Content: "- github (server-hosted, 20 tools)"},
			{Category: CatMetaContext, Content: "OS: darwin arm64"},
		},
		ContextWindow:  200000,
		Pricing:        ai.ModelPricing{InputPer1M: 3.0, OutputPer1M: 15.0, CacheReadPer1M: 0.3},
		PermissionMode: PermissionInteractive,
		Tools:          []tools.Tool{tool1, tool2, tools.NewWriteTool()},
		Approver:       approver,
		Store:          st,
		OnEvent: func(ev Event) {
			if ev.Type == EventTurnEnd && ev.Telemetry != nil {
				turnTelemetries = append(turnTelemetries, *ev.Telemetry)
			}
		},
	}, nil)

	prompt := ai.NewUserTextMessage("Start work")
	finalMsg, err := loopRef.Run(ctx, &prompt)
	if err != nil {
		t.Fatalf("Loop.Run failed: %v", err)
	}

	if secondToolExecuted.Load() {
		t.Fatalf("step_two should have been skipped due to Steer interrupt!")
	}
	if atomic.LoadInt32(&approver.calls) != 1 {
		t.Fatalf("expected 1 interactive approval request for write tool, got %d", approver.calls)
	}
	if finalMsg == nil || finalMsg.PlainText() != "All confirmed!" {
		t.Fatalf("unexpected final message: %+v", finalMsg)
	}
	if len(turnTelemetries) != 4 {
		t.Fatalf("expected 4 turn telemetries, got %d", len(turnTelemetries))
	}

	// Verify 6-category Context Windows breakdown and CAS system prompt blob persistence
	t0 := turnTelemetries[0]
	if len(t0.Breakdown) != 6 || t0.SystemPromptBlobHash == "" || t0.RawTraceBlobHash == "" {
		t.Fatalf("incomplete 6-category telemetry breakdown: %+v", t0)
	}
	sysBytes, _, err := st.GetBlob(ctx, t0.SystemPromptBlobHash)
	if err != nil || string(sysBytes) != "Core agent prompt." {
		t.Fatalf("system prompt trace blob mismatch: %q err=%v", string(sysBytes), err)
	}
}

type blockingSteerProvider struct {
	turn     atomic.Int32
	streamFn func(ctx context.Context, req *ai.StreamRequest, turn int) (<-chan ai.StreamEvent, error)
}

func (p *blockingSteerProvider) Driver() ai.Driver { return ai.DriverAnthropic }
func (p *blockingSteerProvider) Stream(ctx context.Context, req *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
	t := int(p.turn.Add(1))
	return p.streamFn(ctx, req, t)
}

func TestLoopSteerInterruptsActiveLLMStreamImmediately(t *testing.T) {
	ctx := context.Background()
	var loopRef *Loop
	var sawSteerInTurn2 atomic.Bool

	prov := &blockingSteerProvider{
		streamFn: func(streamCtx context.Context, req *ai.StreamRequest, turn int) (<-chan ai.StreamEvent, error) {
			ch := make(chan ai.StreamEvent, 8)
			go func() {
				defer close(ch)
				if turn == 1 {
					ch <- ai.StreamEvent{Type: ai.StreamEventTextDelta, TextDelta: "Starting a long answer..."}
					// Trigger Steer while Turn 1 is still streaming!
					go func() {
						time.Sleep(15 * time.Millisecond)
						loopRef.Steer(ai.NewUserTextMessage("Stop and answer concisely instead."))
					}()
					// Block until streamCtx is canceled by Steer()
					select {
					case <-streamCtx.Done():
						ch <- ai.StreamEvent{Type: ai.StreamEventError, Err: streamCtx.Err()}
						return
					case <-time.After(3 * time.Second):
						ch <- ai.StreamEvent{Type: ai.StreamEventDone, Message: &ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock("never interrupted")}}}
						return
					}
				}
				// Turn 2 should immediately receive the steering message
				for _, m := range req.Messages {
					if m.Role == ai.RoleUser && strings.Contains(m.PlainText(), "Stop and answer concisely") {
						sawSteerInTurn2.Store(true)
					}
				}
				msg := ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock("Concise answer.")}}
				ch <- ai.StreamEvent{Type: ai.StreamEventDone, Message: &msg}
			}()
			return ch, nil
		},
	}

	loopRef = NewLoop(LoopConfig{
		Provider: prov,
		Model:    "test-model",
	}, nil)

	start := time.Now()
	initMsg := ai.NewUserTextMessage("Explain everything")
	finalMsg, err := loopRef.Run(ctx, &initMsg)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("expected Steer to interrupt Turn 1 immediately, took %v", elapsed)
	}
	if !sawSteerInTurn2.Load() {
		t.Fatalf("expected Turn 2 to see steering message")
	}
	if finalMsg == nil || finalMsg.PlainText() != "Concise answer." {
		t.Fatalf("unexpected final message: %+v", finalMsg)
	}
}

func TestPruneOldToolImages(t *testing.T) {
	msgs := []ai.Message{
		ai.NewUserTextMessage("start"),
		ai.NewToolResultMessage("t1", "browser", "shot 1", false, ai.ImageBase64Block("image/png", "111")),
		ai.NewToolResultMessage("t2", "browser", "shot 2", false, ai.ImageBase64Block("image/png", "222")),
		ai.NewToolResultMessage("t3", "browser", "shot 3", false, ai.ImageBase64Block("image/png", "333")),
		ai.NewToolResultMessage("t4", "browser", "shot 4", false, ai.ImageBase64Block("image/png", "444")),
		ai.NewToolResultMessage("t5", "browser", "shot 5", false, ai.ImageBase64Block("image/png", "555")),
	}
	pruned, count := PruneOldToolImages(msgs, 3)
	if count != 2 {
		t.Fatalf("expected 2 older images pruned, got %d", count)
	}
	// First 2 tool results (t1, t2) should have no BlockImage left
	for _, idx := range []int{1, 2} {
		for _, b := range pruned[idx].Content {
			if b.Type == ai.BlockImage {
				t.Fatalf("expected image pruned at msg index %d", idx)
			}
		}
	}
	// Latest 3 tool results (t3, t4, t5) must retain raw BlockImage
	for _, idx := range []int{3, 4, 5} {
		hasImg := false
		for _, b := range pruned[idx].Content {
			if b.Type == ai.BlockImage && b.DataB64 != "" {
				hasImg = true
			}
		}
		if !hasImg {
			t.Fatalf("expected latest screenshot preserved at msg index %d", idx)
		}
	}
}
