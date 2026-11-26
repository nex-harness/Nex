package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/prompts"
)

// GoalConfig controls the outer autonomous goal loop and independent verifier.
type GoalConfig struct {
	MaxRounds                int         // default 8, clamped 1..30
	MaxTurnsPerRound         int         // default 40, clamped 10..100
	MaxConsecutiveNoProgress int         // default 2, clamped 1..5
	VerifierProvider         ai.Provider // optional; nil reuses Loop provider
	VerifierModel            string      // optional; empty reuses Loop model
}

// NormalizeGoalConfig fills zero defaults and clamps fields into safe bounds.
func NormalizeGoalConfig(cfg GoalConfig) GoalConfig {
	if cfg.MaxRounds <= 0 {
		cfg.MaxRounds = 8
	} else if cfg.MaxRounds > 30 {
		cfg.MaxRounds = 30
	}

	if cfg.MaxTurnsPerRound <= 0 {
		cfg.MaxTurnsPerRound = 40
	} else if cfg.MaxTurnsPerRound < 10 {
		cfg.MaxTurnsPerRound = 10
	} else if cfg.MaxTurnsPerRound > 100 {
		cfg.MaxTurnsPerRound = 100
	}

	if cfg.MaxConsecutiveNoProgress <= 0 {
		cfg.MaxConsecutiveNoProgress = 2
	} else if cfg.MaxConsecutiveNoProgress > 5 {
		cfg.MaxConsecutiveNoProgress = 5
	}

	return cfg
}

// RunGoal drives the ZCode-aligned outer Plan -> Execute -> Independent Verify -> Repeat
// state machine around an Agent Loop while emitting a single outer AgentStart/AgentEnd pair.
func RunGoal(
	ctx context.Context,
	loop *Loop,
	objective string,
	initialMsg ai.Message,
	cfg GoalConfig,
) (*ai.Message, error) {
	if loop == nil {
		return nil, errors.New("agent: loop is required for RunGoal")
	}
	cfg = NormalizeGoalConfig(cfg)

	loop.mu.Lock()
	loop.cfg.MaxTurns = cfg.MaxTurnsPerRound
	loop.cfg.SuppressAgentLifecycle = true
	baseProvider := loop.cfg.Provider
	baseModel := loop.cfg.Model
	loop.mu.Unlock()

	vProv := cfg.VerifierProvider
	if vProv == nil {
		vProv = baseProvider
	}
	vModel := strings.TrimSpace(cfg.VerifierModel)
	if vModel == "" {
		vModel = baseModel
	}

	goalID := "goal_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	cleanObjective := strings.TrimSpace(objective)
	if cleanObjective == "" {
		cleanObjective = "Goal"
	}

	loop.emit(Event{Type: EventAgentStart})
	endReason := ""
	endErr := ""
	defer func() {
		loop.emit(Event{
			Type:   EventAgentEnd,
			Reason: endReason,
			Error:  endErr,
		})
	}()

	loop.emit(Event{
		Type: EventGoalLifecycle,
		Goal: &GoalLifecycleInfo{
			GoalID:    goalID,
			Phase:     "started",
			Objective: cleanObjective,
			Round:     0,
			MaxRounds: cfg.MaxRounds,
		},
	})

	prevTodoDigest := computeTodoDigest(loop.Messages())
	prevToolDigest := ""
	consecutiveNoProgress := 0
	consecutiveVerifyErrors := 0
	var missingItems []string
	var lastReply *ai.Message
	roundPrompt := initialMsg

	for round := 1; round <= cfg.MaxRounds; round++ {
		if err := ctx.Err(); err != nil {
			endReason = "aborted"
			loop.emit(Event{
				Type:    EventGoalLifecycle,
				TurnIdx: round,
				Goal: &GoalLifecycleInfo{
					GoalID:    goalID,
					Phase:     "aborted",
					Objective: cleanObjective,
					Round:     round,
					MaxRounds: cfg.MaxRounds,
					Error:     err.Error(),
				},
			})
			return lastReply, err
		}

		// Anchor the active objective and remaining gaps in volatile meta_context
		// so prompt prefix caching on the core section stays intact across rounds and compaction.
		loop.UpsertSystemSection(ai.SystemSection{
			Category: CatMetaContext,
			Name:     "active_goal",
			Content:  prompts.FormatActiveGoalMetaSection(cleanObjective, round, cfg.MaxRounds, missingItems),
		})

		loop.emit(Event{
			Type:    EventGoalLifecycle,
			TurnIdx: round,
			Goal: &GoalLifecycleInfo{
				GoalID:    goalID,
				Phase:     "round_start",
				Objective: cleanObjective,
				Round:     round,
				MaxRounds: cfg.MaxRounds,
			},
		})

		beforeCount := len(loop.Messages())
		msgCopy := roundPrompt
		reply, err := loop.Run(ctx, &msgCopy)
		if reply != nil {
			lastReply = reply
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			endReason = "aborted"
			loop.emit(Event{
				Type:    EventGoalLifecycle,
				TurnIdx: round,
				Goal: &GoalLifecycleInfo{
					GoalID:    goalID,
					Phase:     "aborted",
					Objective: cleanObjective,
					Round:     round,
					MaxRounds: cfg.MaxRounds,
					Error:     ctxErr.Error(),
				},
			})
			return lastReply, ctxErr
		}
		if err != nil {
			endReason = "error"
			endErr = err.Error()
			loop.emit(Event{
				Type:    EventGoalLifecycle,
				TurnIdx: round,
				Goal: &GoalLifecycleInfo{
					GoalID:    goalID,
					Phase:     "failed",
					Objective: cleanObjective,
					Round:     round,
					MaxRounds: cfg.MaxRounds,
					Error:     err.Error(),
				},
			})
			return lastReply, err
		}

		allMsgs := loop.Messages()
		roundMsgs := allMsgs
		if beforeCount >= 0 && beforeCount <= len(allMsgs) {
			roundMsgs = allMsgs[beforeCount:]
		}

		progressed, nextTodoDigest, nextToolDigest := assessRoundProgress(loop, allMsgs, roundMsgs, prevTodoDigest, prevToolDigest)
		prevTodoDigest = nextTodoDigest
		prevToolDigest = nextToolDigest
		if progressed {
			consecutiveNoProgress = 0
		} else {
			consecutiveNoProgress++
		}

		if consecutiveNoProgress >= cfg.MaxConsecutiveNoProgress {
			msg := fmt.Sprintf("Stopped after %d consecutive rounds without measurable progress.", consecutiveNoProgress)
			loop.emit(Event{
				Type:    EventGoalLifecycle,
				TurnIdx: round,
				Goal: &GoalLifecycleInfo{
					GoalID:       goalID,
					Phase:        "blocked",
					Objective:    cleanObjective,
					Round:        round,
					MaxRounds:    cfg.MaxRounds,
					Verdict:      msg,
					MissingItems: missingItems,
				},
			})
			return lastReply, nil
		}

		loop.emit(Event{
			Type:    EventGoalLifecycle,
			TurnIdx: round,
			Goal: &GoalLifecycleInfo{
				GoalID:    goalID,
				Phase:     "verifying",
				Objective: cleanObjective,
				Round:     round,
				MaxRounds: cfg.MaxRounds,
			},
		})

		verdict, parseOK, vErr := VerifyGoalRound(ctx, vProv, vModel, cleanObjective, round, cfg.MaxRounds, allMsgs, beforeCount)
		if ctxErr := ctx.Err(); ctxErr != nil {
			endReason = "aborted"
			loop.emit(Event{
				Type:    EventGoalLifecycle,
				TurnIdx: round,
				Goal: &GoalLifecycleInfo{
					GoalID:    goalID,
					Phase:     "aborted",
					Objective: cleanObjective,
					Round:     round,
					MaxRounds: cfg.MaxRounds,
					Error:     ctxErr.Error(),
				},
			})
			return lastReply, ctxErr
		}

		if vErr != nil || !parseOK {
			consecutiveVerifyErrors++
			if consecutiveVerifyErrors >= 2 {
				errMsg := verdict.Summary
				if vErr != nil {
					errMsg = vErr.Error()
				}
				if errMsg == "" {
					errMsg = "verifier failed across consecutive rounds"
				}
				endReason = "error"
				endErr = errMsg
				loop.emit(Event{
					Type:    EventGoalLifecycle,
					TurnIdx: round,
					Goal: &GoalLifecycleInfo{
						GoalID:    goalID,
						Phase:     "failed",
						Objective: cleanObjective,
						Round:     round,
						MaxRounds: cfg.MaxRounds,
						Verdict:   verdict.Summary,
						Error:     errMsg,
					},
				})
				if vErr != nil {
					return lastReply, vErr
				}
				return lastReply, errors.New(errMsg)
			}
			if vErr != nil {
				verdict = GoalVerdict{
					Achieved:   false,
					Confidence: "low",
					Summary:    "Verification request failed: " + vErr.Error(),
					NextStep:   "Re-run verification checks and confirm concrete outputs.",
				}
			}
		} else {
			consecutiveVerifyErrors = 0
		}

		achievedCopy := verdict.Achieved
		loop.emit(Event{
			Type:    EventGoalLifecycle,
			TurnIdx: round,
			Goal: &GoalLifecycleInfo{
				GoalID:       goalID,
				Phase:        "round_verified",
				Objective:    cleanObjective,
				Round:        round,
				MaxRounds:    cfg.MaxRounds,
				Achieved:     &achievedCopy,
				Verdict:      verdict.Summary,
				MissingItems: verdict.MissingItems,
				NextStep:     verdict.NextStep,
			},
		})

		if verdict.Achieved {
			doneTrue := true
			loop.emit(Event{
				Type:    EventGoalLifecycle,
				TurnIdx: round,
				Goal: &GoalLifecycleInfo{
					GoalID:    goalID,
					Phase:     "completed",
					Objective: cleanObjective,
					Round:     round,
					MaxRounds: cfg.MaxRounds,
					Achieved:  &doneTrue,
					Verdict:   verdict.Summary,
				},
			})
			return lastReply, nil
		}

		if round >= cfg.MaxRounds {
			doneFalse := false
			loop.emit(Event{
				Type:    EventGoalLifecycle,
				TurnIdx: round,
				Goal: &GoalLifecycleInfo{
					GoalID:       goalID,
					Phase:        "max_rounds",
					Objective:    cleanObjective,
					Round:        round,
					MaxRounds:    cfg.MaxRounds,
					Achieved:     &doneFalse,
					Verdict:      verdict.Summary,
					MissingItems: verdict.MissingItems,
					NextStep:     verdict.NextStep,
				},
			})
			return lastReply, nil
		}

		missingItems = verdict.MissingItems
		feedback := prompts.FormatGoalVerificationFeedback(round, cfg.MaxRounds, verdict.Summary, verdict.MissingItems, verdict.NextStep)
		roundPrompt = ai.NewUserTextMessage(feedback)
	}

	return lastReply, nil
}

func assessRoundProgress(loop *Loop, allMsgs, roundMsgs []ai.Message, prevTodoDigest, prevToolDigest string) (bool, string, string) {
	nextTodoDigest := computeTodoDigest(allMsgs)
	if nextTodoDigest != "" && nextTodoDigest != prevTodoDigest {
		return true, nextTodoDigest, computeRoundToolDigest(roundMsgs)
	}

	resultByID := make(map[string]ai.Message)
	for _, m := range roundMsgs {
		if m.Role == ai.RoleToolResult && m.ToolCallID != "" {
			resultByID[m.ToolCallID] = m
		}
	}

	hasMutatingSuccess := false
	for _, m := range roundMsgs {
		if m.Role != ai.RoleAssistant {
			continue
		}
		for _, b := range m.Content {
			if b.Type != ai.BlockToolCall {
				continue
			}
			if !loop.IsToolReadOnly(b.ToolName, b.Arguments) {
				if res, ok := resultByID[b.ToolCallID]; ok && !res.IsError {
					hasMutatingSuccess = true
					break
				}
			}
		}
		if hasMutatingSuccess {
			break
		}
	}

	nextToolDigest := computeRoundToolDigest(roundMsgs)
	if hasMutatingSuccess {
		return true, nextTodoDigest, nextToolDigest
	}
	if nextToolDigest != "" && nextToolDigest != prevToolDigest {
		return true, nextTodoDigest, nextToolDigest
	}
	return false, nextTodoDigest, nextToolDigest
}

func computeTodoDigest(msgs []ai.Message) string {
	todos := extractLatestTodos(msgs)
	if len(todos) == 0 {
		return ""
	}
	raw, _ := json.Marshal(todos)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func computeRoundToolDigest(roundMsgs []ai.Message) string {
	var parts []string
	resultByID := make(map[string]ai.Message)
	for _, m := range roundMsgs {
		if m.Role == ai.RoleToolResult && m.ToolCallID != "" {
			resultByID[m.ToolCallID] = m
		}
	}
	for _, m := range roundMsgs {
		if m.Role != ai.RoleAssistant {
			continue
		}
		for _, b := range m.Content {
			if b.Type != ai.BlockToolCall {
				continue
			}
			resTxt := ""
			errFlag := false
			if res, ok := resultByID[b.ToolCallID]; ok {
				resTxt = res.PlainText()
				errFlag = res.IsError
			}
			parts = append(parts, fmt.Sprintf("%s|%s|%v|%s", b.ToolName, string(b.Arguments), errFlag, resTxt))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}
