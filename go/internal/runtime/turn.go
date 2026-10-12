package runtime

import (
	"context"
	"encoding/json"

	"github.com/tylinux/nex/go/internal/modelclient"
	"github.com/tylinux/nex/go/internal/modelreq"
)

// runTurn runs one turn for a session. It is started by createSession and runs
// on its own goroutine; it takes runMu for every state mutation.
func (r *Runtime) runTurn(ctx context.Context, s *session, inputID string) {
	// The host subscribes immediately after the createSession response; wait for
	// that subscription so the initial snapshot is taken before turn_started.
	select {
	case <-s.firstSub:
	case <-ctx.Done():
		return
	}

	r.runMu.Lock()
	defer r.runMu.Unlock()

	turnID := r.deps.IDs.TurnID()
	userMessageID := r.deps.IDs.MessageID()
	s.turnNumber++
	modelText := s.firstText

	// input admission + promotion (persistence; no observable events).
	if err := s.admitAndPromote(inputID, userMessageID, turnID, modelText); err != nil {
		return
	}

	// turn_started (raw 2).
	s.appendEvent(sessionEvent{
		Type: evTurnStarted, TurnID: turnID, HasTurnID: true,
		Payload: turnStartedPayload(s, modelText, userMessageID, inputID),
	})

	// session_input_promoted (raw 3).
	s.appendEvent(sessionEvent{
		Type: evSessionInputPromoted, TurnID: turnID, HasTurnID: true,
		Payload: mustJSON(map[string]any{"pendingInputId": "queue_" + s.cmd.ID, "messageId": userMessageID}),
	})

	history := []modelreq.Message{{Role: "user", Content: modelText}}
	lastStep := (*modelStep)(nil)
	var cumulative modelUsage
	totalToolCalls := 0
	rounds := 0
	for {
		step := s.runModelStep(ctx, turnID, inputID, history)
		if step == nil {
			return
		}
		lastStep = step
		rounds++
		cumulative.InputTokens += step.usage.InputTokens
		cumulative.OutputTokens += step.usage.OutputTokens
		cumulative.ReasoningTokens += step.usage.ReasoningTokens
		cumulative.CacheReadTokens += step.usage.CacheReadTokens
		cumulative.CacheWriteTokens += step.usage.CacheWriteTokens
		totalToolCalls += step.toolCount
		s.appendEvent(sessionEvent{
			Type: evModelComplete, TurnID: turnID, HasTurnID: true,
			Payload: s.modelCompletePayload(history, modelText, step),
		})
		if len(step.toolCalls) == 0 {
			break
		}
		s.startDeferredTools(ctx, turnID)
		s.appendToolResults(turnID, step)
		history = s.extendHistory(history, step)
	}

	assistantMessageID := s.lastAssistantMessageID
	if err := s.persistAssistant(assistantMessageID, userMessageID, turnID, lastStep); err != nil {
		_ = err
	}

	s.appendEvent(sessionEvent{
		Type: evTurnComplete, TurnID: turnID, HasTurnID: true,
		Payload: mustJSON(map[string]any{
			"response":          lastStep.text,
			"tokenCount":        cumulative.totalTokens(),
			"toolCallCount":     totalToolCalls,
			"duration":          0,
			"resultType":        "success",
			"historyRoundCount": rounds,
			"inputId":           inputID,
		}),
	})

	// Flush the turn's deltas before the title sidecar: the recorded trace has
	// the sidecar's meta update in a later frame than the turn_complete revision.
	r.flushSubscribers(s)

	if title, ok := r.runTitleSidecar(ctx, s, turnID, inputID, modelText); ok {
		s.appendEvent(sessionEvent{
			Type: evSessionTitleUpdated, TurnID: turnID, HasTurnID: true,
			Payload: mustJSON(map[string]any{
				"previousTitle": titleFromInput(modelText), "source": "generated", "title": title,
			}),
		})
		_, _ = r.deps.Store.UpdateTitle(s.id, title, "generated", s.nowMs())
	}

	r.flushSubscribers(s)
	r.emitStateUpdated(s, "prompt_completed")
}

// effectiveHistory is the messages the breakdown counts: the request history,
// or the first user message when the history is empty (first step).
func effectiveHistory(history []modelreq.Message, userText string) []modelreq.Message {
	if len(history) == 0 {
		return []modelreq.Message{{Role: "user", Content: userText}}
	}
	return history
}

func turnStartedPayload(s *session, text, userMessageID, inputID string) json.RawMessage {
	return mustJSON(map[string]any{
		"turnNumber":            s.turnNumber,
		"input":                 text,
		"messageId":             userMessageID,
		"inputId":               inputID,
		"foregroundExecutionId": "runtime_command_" + itoa(s.turnNumber),
		"intent": map[string]any{
			"sourceCommandId": s.cmd.ID,
			"clientId":        s.cmd.ClientID,
			"provenance":      map[string]any{"sourceCommandId": s.cmd.ID},
		},
	})
}

// modelStep is the outcome of one assistant model step.
type modelStep struct {
	text         string
	finishReason string
	usage        modelUsage
	toolCalls    []modelclient.ToolCall
	// tools are the executions started during this step (in order).
	tools     []toolExecution
	toolCount int
	// toolInputBuffers are the pending tool-argument deltas for this step.
	toolInputBuffers map[string]string
}

func (s *session) modelCompletePayload(history []modelreq.Message, modelText string, step *modelStep) json.RawMessage {

	// Cache stats accumulate across the turn's model requests.
	s.cacheRequests++
	s.cacheTotalInput += step.usage.InputTokens
	s.cacheTotalRead += step.usage.CacheReadTokens
	s.cacheTotalWrite += step.usage.CacheWriteTokens
	payload := map[string]any{
		"content":               step.text,
		"querySource":           "main_turn",
		"stopReason":            step.finishReason,
		"usage":                 step.usage,
		"contextWindow":         s.rt.deps.Provider.ContextWindow,
		"toolCallCount":         step.toolCount,
		"cacheHit":              s.cacheHit(step.usage),
		"contextUsageBreakdown": emittersContextUsage(s, effectiveHistory(history, modelText)),
	}
	// The turn's file changes project onto the turn header only on the final
	// model step (the recorded traces show a single fileChanges upsert).
	if len(step.toolCalls) == 0 {
		if files := s.fileChangesSummary(); files != nil {
			payload["fileChanges"] = files
		}
	}
	return mustJSON(payload)
}

// modelUsage mirrors the usage object the projection/emitters read.
type modelUsage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	ReasoningTokens  int64 `json:"reasoningTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
}

func (u modelUsage) totalTokens() int64 { return u.InputTokens + u.OutputTokens }

// cacheHit builds the cacheHit payload the projection reads for the usage patch.
// Per-request fields are the current request's; the totals and request count
// accumulate over the turn.
func (s *session) cacheHit(u modelUsage) map[string]any {
	return map[string]any{
		"inputTokens":           u.InputTokens,
		"cacheReadTokens":       u.CacheReadTokens,
		"cacheWriteTokens":      u.CacheWriteTokens,
		"latestHitRate":         0,
		"hitRate":               0,
		"hitRateRequestCount":   s.cacheRequests,
		"totalInputTokens":      s.cacheTotalInput,
		"totalCacheReadTokens":  s.cacheTotalRead,
		"totalCacheWriteTokens": s.cacheTotalWrite,
	}
}

// fileChangesSummary reports the net file changes the turn produced, or nil
// when nothing changed.
func (s *session) fileChangesSummary() map[string]any {
	if len(s.changedFilesByPath) == 0 {
		return nil
	}
	additions, deletions := 0, 0
	for _, c := range s.changedFilesByPath {
		additions += c.additions
		deletions += c.deletions
	}
	return map[string]any{
		"additions": additions,
		"deletions": deletions,
		"files":     len(s.changedFilesByPath),
	}
}
