package runtime

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/sessionstore"
)

// admitAndPromote writes the input ledger row, the user message and its parts in
// the same order the Node runtime does (AdmitInput then PromoteInput). No
// protocol event is produced; the observable facts are session_input_promoted
// (appended by the turn loop) and the transcript rows.
func (s *session) admitAndPromote(inputID, userMessageID, turnID, text string) error {
	now := s.nowMs()
	queueID := "queue_" + s.cmd.ID
	admitPayload := mustJSON(map[string]any{
		"text": text,
		"intent": map[string]any{
			"sourceCommandId": s.cmd.ID,
			"queueItemId":     queueID,
			"clientId":        s.cmd.ClientID,
			"kind":            "sendText",
			"text":            text,
			"modelSelection":  json.RawMessage(s.modelSelection),
			"mode":            "build",
			"planEnabled":     false,
		},
		"conversationInputIntent": map[string]any{
			"sourceCommandId": s.cmd.ID,
			"queueItemId":     queueID,
			"clientId":        s.cmd.ClientID,
			"kind":            "sendText",
			"text":            text,
			"attachments":     []any{},
			"modelSelection":  json.RawMessage(s.modelSelection),
			"mode":            "build",
			"planEnabled":     false,
		},
		"attachments":       []any{},
		"sourceCommandType": "createSession",
	})
	if err := s.rt.deps.Store.AdmitInput(sessionstore.SessionInput{
		ID: queueID, SessionID: s.id, Kind: "sendText", Delivery: "startNow",
		Payload: admitPayload, TimeNow: &now,
	}); err != nil {
		return err
	}

	msg := sessionstore.Message{
		ID: userMessageID, SessionID: s.id, Role: "user",
		TimeCreated: now, Agent: "nex-agent",
		ModelSelection: s.modelSelection,
		Semantics:      mustJSON(userSemantics("real_user", "user_prompt")),
		Anchor:         mustJSON(map[string]any{"turnId": turnID, "origin": "realUser", "sourceCommandId": s.cmd.ID}),
		Tools:          mustJSON(map[string]bool{"Read": true, "Write": true}),
	}
	part := sessionstore.Part{
		ID: s.rt.deps.IDs.PartID(), MessageID: userMessageID, SessionID: s.id,
		Type: "text", Text: text, TimeStart: &now, TimeEnd: &now, TimeCreated: &now,
	}
	if err := s.rt.deps.Store.PromoteInput(queueID, s.id, msg, []sessionstore.Part{part}); err != nil {
		return err
	}
	// input_history row for recall.
	sid := s.id
	_, _ = s.rt.deps.Store.RecordInputHistory(sessionstore.InputHistory{
		ProjectID: projectIDForDirectory(s.ws.Path), SessionID: &sid, Text: text,
		Kind: "prompt", TimeCreated: &now,
	})
	return nil
}

// persistAssistant writes the assistant message, its parts and the usage rows.
func (s *session) persistAssistant(assistantMessageID, userMessageID, turnID string, step *modelStep) error {
	now := s.nowMs()
	u := step.usage
	tokens := mustJSON(map[string]any{
		"total": u.totalTokens(), "input": u.InputTokens, "output": u.OutputTokens,
		"reasoning": u.ReasoningTokens,
		"cache":     map[string]any{"read": u.CacheReadTokens, "write": u.CacheWriteTokens},
	})
	if err := s.rt.deps.Store.SaveMessage(sessionstore.Message{
		ID: assistantMessageID, SessionID: s.id, Role: "assistant",
		ParentID: &userMessageID, ModelID: strp(s.rt.deps.Provider.ModelID),
		ProviderID: strp(s.rt.deps.Provider.ProviderID), Mode: strp("build"), PlanEnabled: boolPtr(false),
		Agent:       "nex-agent",
		Path:        mustJSON(map[string]any{"cwd": s.ws.Path, "root": s.ws.Path}),
		TimeCreated: now, TimeCompleted: &now,
		Cost: floatp(0), Tokens: tokens, Finish: strp(step.finishReason),
		Semantics: mustJSON(userSemantics("agent_runtime", "assistant_response")),
		Anchor:    mustJSON(map[string]any{"turnId": turnID}),
	}); err != nil {
		return err
	}
	stepStart := sessionstore.Part{ID: s.rt.deps.IDs.PartID(), MessageID: assistantMessageID, SessionID: s.id, Type: "step-start", TimeCreated: &now}
	textPart := sessionstore.Part{ID: s.rt.deps.IDs.PartID(), MessageID: assistantMessageID, SessionID: s.id, Type: "text", Text: step.text, TimeStart: &now, TimeEnd: &now, TimeCreated: &now}
	finishPart := sessionstore.Part{ID: s.rt.deps.IDs.PartID(), MessageID: assistantMessageID, SessionID: s.id, Type: "step-finish", Reason: mustJSON(step.finishReason), Cost: floatp(0), Tokens: tokens, TimeCreated: &now}
	for _, p := range []sessionstore.Part{stepStart, textPart, finishPart} {
		if err := s.rt.deps.Store.SavePart(p); err != nil {
			return err
		}
	}
	if err := s.recordUsage(assistantMessageID, userMessageID, turnID, step); err != nil {
		return err
	}
	return nil
}

// recordUsage writes the model_usage and turn_usage rows.
func (s *session) recordUsage(assistantMessageID, userMessageID, turnID string, step *modelStep) error {
	now := s.nowMs()
	u := step.usage
	raw := mustJSON(map[string]any{
		"inputTokens": u.InputTokens, "outputTokens": u.OutputTokens, "totalTokens": u.totalTokens(),
		"cacheReadTokens": u.CacheReadTokens, "reasoningTokens": u.ReasoningTokens,
	})
	total := u.totalTokens()
	if err := s.rt.deps.Store.RecordModelUsage(sessionstore.ModelUsage{
		ID:               "usage_model_main_turn_" + assistantMessageID + "_0",
		LogicalRequestID: assistantMessageID, SessionID: s.id,
		TurnID: &turnID, AssistantMessageID: &assistantMessageID, ParentUserMessageID: &userMessageID,
		QuerySource: "main_turn", ProviderID: s.rt.deps.Provider.ProviderID, ModelID: s.rt.deps.Provider.ModelID,
		Agent: strp("nex-agent"), Mode: strp("build"), TaskType: strp("interactive"), Status: "completed",
		StartedAt: now, CompletedAt: &now,
		FinishReason: strp(step.finishReason),
		InputTokens:  int(u.InputTokens), OutputTokens: int(u.OutputTokens), ReasoningTokens: int(u.ReasoningTokens),
		ComputedTotalTokens: &total,
		RawUsage:            raw, ProviderMetadata: json.RawMessage(`{"rawFinishReason":"stop"}`),
	}); err != nil {
		return err
	}
	computed := int(total)
	return s.rt.deps.Store.UpsertTurnUsage(sessionstore.TurnUsage{
		SessionID: s.id, TurnID: turnID, UserMessageID: &userMessageID,
		Status: "completed", StartedAt: now, CompletedAt: &now,
		ModelRequestCount: 1, InputTokens: int(u.InputTokens), OutputTokens: int(u.OutputTokens),
		ReasoningTokens: int(u.ReasoningTokens), ComputedTotalTokens: computed,
	})
}

func userSemantics(origin, kind string) map[string]any {
	return map[string]any{
		"origin": origin, "kind": kind,
		"uiVisibility": "visible", "providerVisibility": "visible", "transcriptVisibility": "visible",
	}
}

func strp(s string) *string     { return &s }
func floatp(f float64) *float64 { return &f }
