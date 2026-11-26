package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/prompts"
	"github.com/nex-harness/nex/pkg/harness/tools"
)

func isGoalVerifierCall(req *ai.StreamRequest) bool {
	return strings.Contains(req.SystemPrompt, "skeptical verification judge")
}

func TestRunGoalSingleRoundAchievedLifecycleAndCacheSafety(t *testing.T) {
	var seenCorePrompts []string
	var seenActiveGoals []string
	var mu sync.Mutex

	prov := &retryScriptedProvider{
		fn: func(callIdx int, req *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
			if isGoalVerifierCall(req) {
				return makeDoneStream(&ai.Message{
					Role: ai.RoleAssistant,
					Content: []ai.ContentBlock{ai.TextBlock(`{
						"achieved": true,
						"confidence": "high",
						"summary": "File created and verified."
					}`)},
				}), nil
			}
			mu.Lock()
			for _, sec := range req.SystemSections {
				if sec.Name == "core" {
					seenCorePrompts = append(seenCorePrompts, sec.Content)
				}
				if sec.Name == "active_goal" {
					seenActiveGoals = append(seenActiveGoals, sec.Content)
				}
			}
			mu.Unlock()

			if callIdx == 0 {
				return makeDoneStream(&ai.Message{
					Role: ai.RoleAssistant,
					Content: []ai.ContentBlock{{
						Type:       ai.BlockToolCall,
						ToolCallID: "tc_write_1",
						ToolName:   "write_file",
						Arguments:  json.RawMessage(`{"path":"a.txt"}`),
					}},
				}), nil
			}
			return makeDoneStream(&ai.Message{
				Role:    ai.RoleAssistant,
				Content: []ai.ContentBlock{ai.TextBlock("All done.")},
			}), nil
		},
	}

	writeTool := &funcTool{
		name:     "write_file",
		readOnly: false,
		fn: func(_ context.Context, _ json.RawMessage) (tools.Result, error) {
			return tools.Result{Output: "wrote a.txt"}, nil
		},
	}

	var events []Event
	var evMu sync.Mutex
	coreSec := ai.SystemSection{Category: CatSystemPrompt, Name: "core", Content: prompts.StableCorePrompt(), CacheControl: true}

	loop := NewLoop(LoopConfig{
		Model:          "claude-sonnet-4-5",
		Provider:       prov,
		SystemSections: []ai.SystemSection{coreSec},
		Tools:          []tools.Tool{writeTool},
		OnEvent: func(ev Event) {
			evMu.Lock()
			events = append(events, ev)
			evMu.Unlock()
		},
	}, nil)

	reply, err := RunGoal(context.Background(), loop, "Write a.txt", ai.NewUserTextMessage(prompts.FormatGoalTurnPrompt("Write a.txt")), GoalConfig{MaxRounds: 5})
	if err != nil {
		t.Fatalf("RunGoal returned error: %v", err)
	}
	if reply == nil || reply.PlainText() != "All done." {
		t.Fatalf("unexpected reply: %+v", reply)
	}

	var starts, ends int
	var phases []string
	for _, ev := range events {
		switch ev.Type {
		case EventAgentStart:
			starts++
		case EventAgentEnd:
			ends++
		case EventGoalLifecycle:
			if ev.Goal != nil {
				phases = append(phases, ev.Goal.Phase)
			}
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("expected exactly 1 agent_start and 1 agent_end, got starts=%d ends=%d", starts, ends)
	}
	wantPhases := []string{"started", "round_start", "verifying", "round_verified", "completed"}
	if strings.Join(phases, ",") != strings.Join(wantPhases, ",") {
		t.Fatalf("phases = %v, want %v", phases, wantPhases)
	}
	if len(seenCorePrompts) == 0 || seenCorePrompts[0] != prompts.StableCorePrompt() {
		t.Fatalf("core section must remain byte-identical")
	}
	if len(seenActiveGoals) == 0 || !strings.Contains(seenActiveGoals[0], "Write a.txt") {
		t.Fatalf("expected active_goal meta section with objective, got %v", seenActiveGoals)
	}
}

func TestRunGoalMultiRoundWithVerificationFeedback(t *testing.T) {
	verifierCalls := 0
	var round2SawFeedback bool

	prov := &retryScriptedProvider{
		fn: func(_ int, req *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
			if isGoalVerifierCall(req) {
				verifierCalls++
				if verifierCalls == 1 {
					return makeDoneStream(&ai.Message{
						Role: ai.RoleAssistant,
						Content: []ai.ContentBlock{ai.TextBlock("```json\n" + `{
							"achieved": false,
							"confidence": "high",
							"summary": "Tests were not run yet.",
							"missing_items": ["Run unit tests"],
							"next_step": "Execute go test"
						}` + "\n```")},
					}), nil
				}
				return makeDoneStream(&ai.Message{
					Role: ai.RoleAssistant,
					Content: []ai.ContentBlock{ai.TextBlock(`{
						"achieved": true,
						"confidence": "high",
						"summary": "Unit tests executed and passed."
					}`)},
				}), nil
			}

			for _, m := range req.Messages {
				if m.Role == ai.RoleUser && strings.Contains(m.PlainText(), "<goal-verification-feedback") && strings.Contains(m.PlainText(), "Run unit tests") {
					round2SawFeedback = true
				}
			}

			// Each round makes one mutating tool call then finishes
			lastMsg := req.Messages[len(req.Messages)-1]
			if lastMsg.Role == ai.RoleUser {
				return makeDoneStream(&ai.Message{
					Role: ai.RoleAssistant,
					Content: []ai.ContentBlock{{
						Type:       ai.BlockToolCall,
						ToolCallID: "tc_run",
						ToolName:   "mutate_step",
						Arguments:  json.RawMessage(`{"round":true}`),
					}},
				}), nil
			}
			return makeDoneStream(&ai.Message{
				Role:    ai.RoleAssistant,
				Content: []ai.ContentBlock{ai.TextBlock("Round finished.")},
			}), nil
		},
	}

	mutateTool := &funcTool{
		name:     "mutate_step",
		readOnly: false,
		fn: func(_ context.Context, _ json.RawMessage) (tools.Result, error) {
			return tools.Result{Output: "ok"}, nil
		},
	}

	var events []Event
	loop := NewLoop(LoopConfig{
		Model:    "claude-sonnet-4-5",
		Provider: prov,
		Tools:    []tools.Tool{mutateTool},
		OnEvent:  func(ev Event) { events = append(events, ev) },
	}, nil)

	_, err := RunGoal(context.Background(), loop, "Implement and test", ai.NewUserTextMessage("/goal Implement and test"), GoalConfig{MaxRounds: 4})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if verifierCalls != 2 {
		t.Fatalf("expected 2 verifier calls, got %d", verifierCalls)
	}
	if !round2SawFeedback {
		t.Fatal("expected round 2 to receive <goal-verification-feedback>")
	}
}

func TestRunGoalContinuesAfterRoundHitsMaxTurnsPerRound(t *testing.T) {
	verifierCalls := 0
	var execCalls int

	prov := &retryScriptedProvider{
		fn: func(_ int, req *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
			if isGoalVerifierCall(req) {
				verifierCalls++
				if verifierCalls == 1 {
					return makeDoneStream(&ai.Message{
						Role:    ai.RoleAssistant,
						Content: []ai.ContentBlock{ai.TextBlock(`{"achieved":false,"confidence":"medium","summary":"Round 1 budget hit before completion","missing_items":["Finish remaining work"]}`)},
					}), nil
				}
				return makeDoneStream(&ai.Message{
					Role:    ai.RoleAssistant,
					Content: []ai.ContentBlock{ai.TextBlock(`{"achieved":true,"confidence":"high","summary":"Completed in round 2"}`)},
				}), nil
			}

			execCalls++
			// If tools are disabled (MaxTurns reached), return summary text
			if len(req.Tools) == 0 {
				return makeDoneStream(&ai.Message{
					Role:    ai.RoleAssistant,
					Content: []ai.ContentBlock{ai.TextBlock("Hit round budget, summarizing.")},
				}), nil
			}
			// In round 1 keep calling tool until MaxTurnsPerRound disables tools; in round 2 finish cleanly.
			if verifierCalls == 0 {
				return makeDoneStream(&ai.Message{
					Role: ai.RoleAssistant,
					Content: []ai.ContentBlock{{
						Type:       ai.BlockToolCall,
						ToolCallID: "tc_loop",
						ToolName:   "edit_file",
						Arguments:  json.RawMessage(`{}`),
					}},
				}), nil
			}
			last := req.Messages[len(req.Messages)-1]
			if last.Role == ai.RoleUser {
				return makeDoneStream(&ai.Message{
					Role: ai.RoleAssistant,
					Content: []ai.ContentBlock{{
						Type:       ai.BlockToolCall,
						ToolCallID: "tc_done",
						ToolName:   "edit_file",
						Arguments:  json.RawMessage(`{"final":true}`),
					}},
				}), nil
			}
			return makeDoneStream(&ai.Message{
				Role:    ai.RoleAssistant,
				Content: []ai.ContentBlock{ai.TextBlock("Finished cleanly in round 2.")},
			}), nil
		},
	}

	editTool := &funcTool{
		name:     "edit_file",
		readOnly: false,
		fn: func(_ context.Context, _ json.RawMessage) (tools.Result, error) {
			return tools.Result{Output: "edited"}, nil
		},
	}

	loop := NewLoop(LoopConfig{
		Model:    "claude-sonnet-4-5",
		Provider: prov,
		Tools:    []tools.Tool{editTool},
	}, nil)

	reply, err := RunGoal(context.Background(), loop, "Finish large refactor", ai.NewUserTextMessage("start"), GoalConfig{
		MaxRounds:        3,
		MaxTurnsPerRound: 10, // normalized min is 10
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if verifierCalls != 2 {
		t.Fatalf("expected round 1 max turns to hand off to verifier and continue to round 2, verifierCalls=%d", verifierCalls)
	}
	if reply == nil || reply.PlainText() != "Finished cleanly in round 2." {
		t.Fatalf("unexpected final reply: %+v", reply)
	}
}

func TestRunGoalStagnationGuardBlocksAfterConsecutiveNoProgress(t *testing.T) {
	verifierCalls := 0
	prov := &retryScriptedProvider{
		fn: func(_ int, req *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
			if isGoalVerifierCall(req) {
				verifierCalls++
				return makeDoneStream(&ai.Message{
					Role:    ai.RoleAssistant,
					Content: []ai.ContentBlock{ai.TextBlock(`{"achieved":false,"confidence":"high","summary":"Still missing"}`)},
				}), nil
			}
			// No tool calls at all -> zero progress every round
			return makeDoneStream(&ai.Message{
				Role:    ai.RoleAssistant,
				Content: []ai.ContentBlock{ai.TextBlock("Nothing changed.")},
			}), nil
		},
	}

	var phases []string
	loop := NewLoop(LoopConfig{
		Model:    "claude-sonnet-4-5",
		Provider: prov,
		OnEvent: func(ev Event) {
			if ev.Type == EventGoalLifecycle && ev.Goal != nil {
				phases = append(phases, ev.Goal.Phase)
			}
		},
	}, nil)

	_, err := RunGoal(context.Background(), loop, "Impossible task", ai.NewUserTextMessage("go"), GoalConfig{
		MaxRounds:                5,
		MaxConsecutiveNoProgress: 2,
	})
	if err != nil {
		t.Fatalf("blocked should return nil error gracefully, got: %v", err)
	}
	// Round 1: no progress (count=1) -> verifies -> Round 2: no progress (count=2 >= 2) -> blocked immediately before 2nd verify
	if verifierCalls != 1 {
		t.Fatalf("expected 1 verifier call before round 2 stagnation block, got %d", verifierCalls)
	}
	if phases[len(phases)-1] != "blocked" {
		t.Fatalf("expected final phase blocked, got %v", phases)
	}
}

func TestRunGoalInvalidVerifierJSONDegradesAndFailsAfterTwoConsecutiveErrors(t *testing.T) {
	prov := &retryScriptedProvider{
		fn: func(_ int, req *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
			if isGoalVerifierCall(req) {
				return makeDoneStream(&ai.Message{
					Role:    ai.RoleAssistant,
					Content: []ai.ContentBlock{ai.TextBlock("not valid json at all")},
				}), nil
			}
			last := req.Messages[len(req.Messages)-1]
			if last.Role == ai.RoleUser {
				return makeDoneStream(&ai.Message{
					Role: ai.RoleAssistant,
					Content: []ai.ContentBlock{{
						Type:       ai.BlockToolCall,
						ToolCallID: "tc_step",
						ToolName:   "mutate_step",
						Arguments:  json.RawMessage(`{}`),
					}},
				}), nil
			}
			return makeDoneStream(&ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock("ok")}}), nil
		},
	}
	mutateTool := &funcTool{
		name:     "mutate_step",
		readOnly: false,
		fn: func(_ context.Context, _ json.RawMessage) (tools.Result, error) {
			return tools.Result{Output: "ok"}, nil
		},
	}

	var phases []string
	var endEv *Event
	loop := NewLoop(LoopConfig{
		Model:    "claude-sonnet-4-5",
		Provider: prov,
		Tools:    []tools.Tool{mutateTool},
		OnEvent: func(ev Event) {
			if ev.Type == EventGoalLifecycle && ev.Goal != nil {
				phases = append(phases, ev.Goal.Phase)
			}
			if ev.Type == EventAgentEnd {
				cp := ev
				endEv = &cp
			}
		},
	}, nil)

	_, err := RunGoal(context.Background(), loop, "Test verifier parse fallback", ai.NewUserTextMessage("go"), GoalConfig{MaxRounds: 5})
	if err == nil {
		t.Fatal("expected error after 2 consecutive invalid verifier outputs")
	}
	if phases[len(phases)-1] != "failed" {
		t.Fatalf("expected failed phase, got %v", phases)
	}
	if endEv == nil || endEv.Reason != "error" || endEv.Error == "" {
		t.Fatalf("expected EventAgentEnd with error reason, got %+v", endEv)
	}
}

func TestRunGoalAbortedOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	prov := &retryScriptedProvider{
		fn: func(_ int, req *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
			if isGoalVerifierCall(req) {
				cancel()
				return nil, errors.New("canceled")
			}
			last := req.Messages[len(req.Messages)-1]
			if last.Role == ai.RoleUser {
				return makeDoneStream(&ai.Message{
					Role: ai.RoleAssistant,
					Content: []ai.ContentBlock{{
						Type:       ai.BlockToolCall,
						ToolCallID: "tc_1",
						ToolName:   "mutate_step",
						Arguments:  json.RawMessage(`{}`),
					}},
				}), nil
			}
			return makeDoneStream(&ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock("done")}}), nil
		},
	}
	mutateTool := &funcTool{
		name:     "mutate_step",
		readOnly: false,
		fn: func(_ context.Context, _ json.RawMessage) (tools.Result, error) {
			return tools.Result{Output: "ok"}, nil
		},
	}

	var starts, ends int
	var lastPhase string
	var endReason string
	loop := NewLoop(LoopConfig{
		Model:    "claude-sonnet-4-5",
		Provider: prov,
		Tools:    []tools.Tool{mutateTool},
		OnEvent: func(ev Event) {
			switch ev.Type {
			case EventAgentStart:
				starts++
			case EventAgentEnd:
				ends++
				endReason = ev.Reason
			case EventGoalLifecycle:
				if ev.Goal != nil {
					lastPhase = ev.Goal.Phase
				}
			}
		},
	}, nil)

	_, err := RunGoal(ctx, loop, "Cancelable task", ai.NewUserTextMessage("go"), GoalConfig{MaxRounds: 3})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("expected single outer start/end pair, got starts=%d ends=%d", starts, ends)
	}
	if lastPhase != "aborted" || endReason != "aborted" {
		t.Fatalf("expected aborted phase & reason, got phase=%q endReason=%q", lastPhase, endReason)
	}
}
