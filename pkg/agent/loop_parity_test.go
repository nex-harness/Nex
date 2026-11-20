package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/tools"
)

type retryScriptedProvider struct {
	mu    sync.Mutex
	calls int
	fn    func(callIdx int, req *ai.StreamRequest) (<-chan ai.StreamEvent, error)
}

func (p *retryScriptedProvider) Driver() ai.Driver { return ai.DriverAnthropic }

func (p *retryScriptedProvider) Stream(_ context.Context, req *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
	p.mu.Lock()
	idx := p.calls
	p.calls++
	p.mu.Unlock()
	return p.fn(idx, req)
}

func makeDoneStream(msg *ai.Message) <-chan ai.StreamEvent {
	ch := make(chan ai.StreamEvent, 2)
	if txt := msg.PlainText(); txt != "" {
		ch <- ai.StreamEvent{Type: ai.StreamEventTextDelta, TextDelta: txt}
	}
	ch <- ai.StreamEvent{Type: ai.StreamEventDone, Message: msg, StopReason: ai.StopReasonEndTurn}
	close(ch)
	return ch
}

func TestLoopRetryBackoffAndNonRetryableAuth(t *testing.T) {
	// 1. Transient 429 + 503 should retry and succeed on attempt 3
	prov := &retryScriptedProvider{
		fn: func(callIdx int, _ *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
			switch callIdx {
			case 0:
				return nil, errors.New("ai: anthropic HTTP 429 rate_limit_error")
			case 1:
				return nil, errors.New("ai: openai HTTP 503 service unavailable")
			default:
				return makeDoneStream(&ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock("recovered!")}}), nil
			}
		},
	}

	loop := NewLoop(LoopConfig{
		Model:          "claude-sonnet-4-5",
		Provider:       prov,
		MaxRetries:     3,
		RetryBaseDelay: 5 * time.Millisecond,
	}, nil)

	prompt := ai.NewUserTextMessage("hello")
	msg, err := loop.Run(context.Background(), &prompt)
	if err != nil {
		t.Fatalf("expected retry recovery, got err: %v", err)
	}
	if msg == nil || msg.PlainText() != "recovered!" {
		t.Fatalf("unexpected message: %+v", msg)
	}
	if prov.calls != 3 {
		t.Fatalf("expected 3 provider calls, got %d", prov.calls)
	}

	// 2. HTTP 401 should NOT retry
	authProv := &retryScriptedProvider{
		fn: func(_ int, _ *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
			return nil, errors.New("ai: anthropic HTTP 401 invalid x-api-key")
		},
	}
	loop2 := NewLoop(LoopConfig{
		Model:          "claude-sonnet-4-5",
		Provider:       authProv,
		MaxRetries:     3,
		RetryBaseDelay: 5 * time.Millisecond,
	}, nil)
	_, err = loop2.Run(context.Background(), &prompt)
	if err == nil {
		t.Fatalf("expected 401 error")
	}
	if authProv.calls != 1 {
		t.Fatalf("401 should fail immediately without retry, got %d calls", authProv.calls)
	}
}

func TestLoopOverflowAutoCompactRecovery(t *testing.T) {
	// Build a multi-turn history large enough to split safely (>6 messages)
	var history []ai.Message
	for i := 0; i < 8; i++ {
		history = append(history,
			ai.NewUserTextMessage("User turn "+strings.Repeat("alpha ", 80)),
			ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock("Assistant reply " + strings.Repeat("beta ", 80))}},
		)
	}

	var sawCompactionEvent bool
	var compactedPayloadLen int
	prov := &retryScriptedProvider{
		fn: func(callIdx int, req *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
			// Compaction summary call uses compactionSystemPrompt
			if strings.Contains(req.SystemPrompt, "conversation compactor") {
				return makeDoneStream(&ai.Message{
					Role:    ai.RoleAssistant,
					Content: []ai.ContentBlock{ai.TextBlock("## Goal & Constraints\n- Keep coding.")},
				}), nil
			}
			if callIdx == 0 {
				return nil, errors.New("ai: anthropic HTTP 400 prompt is too long: 215000 tokens > 200000 maximum")
			}
			// Retried turn after overflow compaction should have far fewer messages and start with compaction summary!
			if len(req.Messages) >= len(history) {
				t.Errorf("expected compacted message count < %d, got %d", len(history), len(req.Messages))
			}
			if !strings.Contains(req.Messages[0].PlainText(), "<context-compaction-summary") {
				t.Errorf("expected first message to be compaction summary, got %q", req.Messages[0].PlainText())
			}
			return makeDoneStream(&ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock("continued cleanly")}}), nil
		},
	}

	loop := NewLoop(LoopConfig{
		Model:          "claude-sonnet-4-5",
		Provider:       prov,
		MaxRetries:     1,
		RetryBaseDelay: 5 * time.Millisecond,
		OnEvent: func(ev Event) {
			if ev.Type == EventCompaction {
				sawCompactionEvent = true
				compactedPayloadLen = len(ev.CompactedMessages)
			}
		},
	}, history)

	prompt := ai.NewUserTextMessage("continue")
	msg, err := loop.Run(context.Background(), &prompt)
	if err != nil {
		t.Fatalf("expected overflow recovery, got err: %v", err)
	}
	if msg == nil || msg.PlainText() != "continued cleanly" {
		t.Fatalf("unexpected msg: %+v", msg)
	}
	if !sawCompactionEvent || compactedPayloadLen == 0 {
		t.Fatalf("expected EventCompaction with compacted snapshot, got saw=%v len=%d", sawCompactionEvent, compactedPayloadLen)
	}
}

func TestLoopParallelReadOnlyToolsAndDeterministicOrder(t *testing.T) {
	// Two consecutive read-only tools synchronize through a rendezvous channel:
	// if they ran serially, this test would deadlock/timeout!
	started := make(chan string, 2)
	release := make(chan struct{})
	var bothRunning atomic.Bool

	mkReadTool := func(name string) *funcTool {
		return &funcTool{
			name:     name,
			readOnly: true,
			fn: func(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
				started <- name
				select {
				case <-release:
					return tools.Result{Output: "ok:" + name}, nil
				case <-ctx.Done():
					return tools.Result{}, ctx.Err()
				}
			},
		}
	}

	go func() {
		<-started
		<-started
		bothRunning.Store(true)
		close(release)
	}()

	prov := &scriptedProvider{
		turns: []func(req *ai.StreamRequest) []ai.StreamEvent{
			func(_ *ai.StreamRequest) []ai.StreamEvent {
				msg := &ai.Message{
					Role: ai.RoleAssistant,
					Content: []ai.ContentBlock{
						ai.ToolCallBlock("tc_a", "read_a", json.RawMessage(`{}`)),
						ai.ToolCallBlock("tc_b", "read_b", json.RawMessage(`{}`)),
					},
				}
				return []ai.StreamEvent{{Type: ai.StreamEventDone, Message: msg, StopReason: ai.StopReasonToolUse}}
			},
			func(req *ai.StreamRequest) []ai.StreamEvent {
				// Verify results are appended in deterministic original call order: tc_a then tc_b
				var order []string
				for _, m := range req.Messages {
					if m.Role == ai.RoleToolResult {
						order = append(order, m.ToolCallID+":"+m.PlainText())
					}
				}
				if len(order) != 2 || order[0] != "tc_a:ok:read_a" || order[1] != "tc_b:ok:read_b" {
					t.Errorf("unexpected tool result order: %v", order)
				}
				return []ai.StreamEvent{{
					Type:       ai.StreamEventDone,
					Message:    &ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock("done")}},
					StopReason: ai.StopReasonEndTurn,
				}}
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	loop := NewLoop(LoopConfig{
		Model:    "claude-sonnet-4-5",
		Provider: prov,
		Tools:    []tools.Tool{mkReadTool("read_a"), mkReadTool("read_b")},
	}, nil)

	prompt := ai.NewUserTextMessage("read both")
	if _, err := loop.Run(ctx, &prompt); err != nil {
		t.Fatalf("parallel read-only batch failed: %v", err)
	}
	if !bothRunning.Load() {
		t.Fatalf("expected both read-only tools to execute concurrently")
	}
}

type schemaTool struct {
	name     string
	schema   json.RawMessage
	executed atomic.Bool
}

func (t *schemaTool) Name() string                      { return t.name }
func (t *schemaTool) Category() string                  { return "system" }
func (t *schemaTool) IsReadOnly(_ json.RawMessage) bool { return false }
func (t *schemaTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{Name: t.name, Category: "system", InputSchema: t.schema}
}
func (t *schemaTool) Execute(_ context.Context, _ string, _ json.RawMessage, _ tools.UpdateFunc) (tools.Result, error) {
	t.executed.Store(true)
	return tools.Result{Output: strings.Repeat("Z", DefaultMaxToolOutputBytes+4096)}, nil
}

type sysPromptHookMock struct{}

func (h *sysPromptHookMock) OnContextBuild(_ context.Context, msgs []ai.Message) ([]ai.Message, error) {
	return msgs, nil
}
func (h *sysPromptHookMock) OnToolCall(_ context.Context, _ string, _ json.RawMessage) (ToolCallDecision, error) {
	return ToolCallDecision{}, nil
}
func (h *sysPromptHookMock) OnToolResult(_ context.Context, _ string, _ json.RawMessage, _ tools.Result) (ToolResultMutation, error) {
	return ToolResultMutation{}, nil
}
func (h *sysPromptHookMock) OnSystemPrompt(_ context.Context, secs []ai.SystemSection) ([]ai.SystemSection, error) {
	out := append([]ai.SystemSection(nil), secs...)
	out = append(out, ai.SystemSection{Category: CatMetaContext, Name: "ext_hook", Content: "Injected by OnSystemPrompt"})
	return out, nil
}

func TestLoopValidationOutputGuardAndSystemPromptHook(t *testing.T) {
	st := &schemaTool{
		name: "strict_tool",
		schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string"},
				"mode": {"type": "string", "enum": ["fast", "deep"]}
			},
			"required": ["path"]
		}`),
	}

	var sawHookSection bool
	var sawValidationErr bool
	var sawTruncatedOutput bool

	prov := &scriptedProvider{
		turns: []func(req *ai.StreamRequest) []ai.StreamEvent{
			// Turn 1: send invalid args (missing required "path")
			func(req *ai.StreamRequest) []ai.StreamEvent {
				if strings.Contains(req.FullSystemPrompt(), "Injected by OnSystemPrompt") {
					sawHookSection = true
				}
				return []ai.StreamEvent{{
					Type: ai.StreamEventDone,
					Message: &ai.Message{
						Role: ai.RoleAssistant,
						Content: []ai.ContentBlock{
							ai.ToolCallBlock("tc_bad", "strict_tool", json.RawMessage(`{"mode":"fast"}`)),
						},
					},
					StopReason: ai.StopReasonToolUse,
				}}
			},
			// Turn 2: see validation error, then send valid args -> output exceeds 64KB and gets truncated!
			func(req *ai.StreamRequest) []ai.StreamEvent {
				for _, m := range req.Messages {
					if m.Role == ai.RoleToolResult && m.ToolCallID == "tc_bad" && m.IsError && strings.Contains(m.PlainText(), "missing required parameter") {
						sawValidationErr = true
					}
				}
				return []ai.StreamEvent{{
					Type: ai.StreamEventDone,
					Message: &ai.Message{
						Role: ai.RoleAssistant,
						Content: []ai.ContentBlock{
							ai.ToolCallBlock("tc_good", "strict_tool", json.RawMessage(`{"path":"a.go","mode":"deep"}`)),
						},
					},
					StopReason: ai.StopReasonToolUse,
				}}
			},
			// Turn 3: check truncated output
			func(req *ai.StreamRequest) []ai.StreamEvent {
				for _, m := range req.Messages {
					if m.Role == ai.RoleToolResult && m.ToolCallID == "tc_good" && strings.Contains(m.PlainText(), "truncated") && len(m.PlainText()) <= DefaultMaxToolOutputBytes+256 {
						sawTruncatedOutput = true
					}
				}
				return []ai.StreamEvent{{
					Type:       ai.StreamEventDone,
					Message:    &ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock("all checked")}},
					StopReason: ai.StopReasonEndTurn,
				}}
			},
		},
	}

	approver := &mockApprover{approve: true}
	loop := NewLoop(LoopConfig{
		Model:          "claude-sonnet-4-5",
		Provider:       prov,
		PermissionMode: PermissionInteractive,
		Approver:       approver,
		Hooks:          &sysPromptHookMock{},
		Tools:          []tools.Tool{st},
		SystemSections: []ai.SystemSection{{Category: CatSystemPrompt, Content: "Base"}},
	}, nil)

	prompt := ai.NewUserTextMessage("run strict")
	if _, err := loop.Run(context.Background(), &prompt); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !sawHookSection {
		t.Fatalf("expected SystemPromptHook section to be injected")
	}
	if !sawValidationErr {
		t.Fatalf("expected missing required argument validation error")
	}
	// Approver should have been called ONLY ONCE (for the valid call, NOT for the invalid call!)
	if atomic.LoadInt32(&approver.calls) != 1 {
		t.Fatalf("expected approver called once (skipped on invalid args), got %d", approver.calls)
	}
	if !sawTruncatedOutput {
		t.Fatalf("expected oversized tool output to be truncated by GuardToolOutput")
	}
}

func TestMicrocompactAndGuardUTF8(t *testing.T) {
	// UTF-8 safety on CJK strings
	cjk := strings.Repeat("你好世界", 500)
	guarded := GuardToolOutput(cjk, 300)
	if !strings.Contains(guarded, "truncated") {
		t.Fatalf("expected truncation marker")
	}

	// Microcompact trims old large tool outputs while keeping recent ones and errors intact
	msgs := []ai.Message{
		ai.NewUserTextMessage("turn 1"),
		ai.NewToolResultMessage("c1", "read", strings.Repeat("x", 3000), false),
		ai.NewToolResultMessage("c2", "bash", strings.Repeat("e", 3000), true), // error kept!
		ai.NewUserTextMessage("turn 2"),
		ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock("ok")}},
		ai.NewToolResultMessage("c3", "read", strings.Repeat("y", 3000), false), // recent kept!
	}
	pruned, count := MicrocompactMessages(msgs, 3)
	if count != 1 {
		t.Fatalf("expected 1 pruned tool output, got %d", count)
	}
	if !strings.Contains(pruned[1].PlainText(), "[Compacted tool output") {
		t.Fatalf("expected c1 to be microcompacted: %q", pruned[1].PlainText())
	}
	if strings.Contains(pruned[2].PlainText(), "[Compacted") {
		t.Fatalf("error tool_result c2 should NOT be microcompacted")
	}
	if strings.Contains(pruned[5].PlainText(), "[Compacted") {
		t.Fatalf("recent tool_result c3 should NOT be microcompacted")
	}

	// ParseCompactCommand
	if focus, ok := ParseCompactCommand("/compact focus on auth bugs"); !ok || focus != "focus on auth bugs" {
		t.Fatalf("unexpected ParseCompactCommand result: ok=%v focus=%q", ok, focus)
	}
	if _, ok := ParseCompactCommand("/compaction"); ok {
		t.Fatalf("/compaction should not match /compact")
	}
}
