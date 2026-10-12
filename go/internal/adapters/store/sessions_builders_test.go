package store_test

import (
	"encoding/json"
	"testing"

	"github.com/tylinux/nex/go/internal/adapters/store"
	"github.com/tylinux/nex/go/internal/sessionstore"
)

// The builders below mirror scripts/go-rewrite/gen-store-vectors.mts exactly:
// same ids, same order, same JSON. If they drift the vector test fails.

func js(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func strp(s string) *string { return &s }
func i64p(v int64) *int64   { return &v }
func boolp(v bool) *bool    { return &v }

func modelSelectionJSON() json.RawMessage {
	return json.RawMessage(`{"providerId":"mock","modelId":"mock-model","options":{"reasoningLevel":"none"}}`)
}

func inputIntentJSON() json.RawMessage {
	raw := `{"sourceCommandId":"cmd-0001","queueItemId":"queue_cmd-0001","clientId":"vec-client",` +
		`"kind":"sendText","text":"Say hello",` +
		`"modelSelection":{"providerId":"mock","modelId":"mock-model","options":{"reasoningLevel":"none"}},` +
		`"mode":"build","planEnabled":false,"admissionSeq":1,"admittedAt":` + itoa(vectorTime.inputAdmit) + `,` +
		`"requestedDelivery":"startNow","admittedDelivery":"startNow","attachmentRefs":[]}`
	return json.RawMessage(raw)
}

func conversationInputIntentJSON(dispatch string) json.RawMessage {
	// Field order matters; build with a raw ordered literal.
	raw := `{"sourceCommandId":"cmd-0001","queueItemId":"queue_cmd-0001","clientId":"vec-client",` +
		`"kind":"sendText","text":"Say hello","attachments":[],` +
		`"modelSelection":{"providerId":"mock","modelId":"mock-model","options":{"reasoningLevel":"none"}},` +
		`"mode":"build","planEnabled":false,` +
		`"delivery":{"requested":"startNow","admitted":"startNow"},` +
		`"order":{"admissionSeq":1},"steer":{"state":"notRequested"},` +
		`"dispatch":{"state":"` + dispatch + `"},"admittedAt":` + itoa(vectorTime.inputAdmit) + `}`
	return json.RawMessage(raw)
}

func createSession(t *testing.T, s *store.SessionStore) {
	t.Helper()
	_, err := s.CreateSession(sessionstore.CreateSessionInput{
		ID: vecSess, ProjectID: vecProj, TaskType: "interactive", TraceID: strp(vecTrace),
		Slug: vecSess, Directory: vecDir, Path: strp(vecDir), Title: "Say hello",
		TitleSource: "first_input", Version: "1.2.3", Permission: []byte(`{"mode":"build"}`),
		TimeCreated: i64p(vectorTime.sessionCreate), TimeUpdated: i64p(vectorTime.sessionCreate),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
}

func sessionEntries(t *testing.T, s *store.SessionStore) {
	t.Helper()
	if err := s.UpsertSessionEntry(sessionstore.SessionEntry{
		ID: vecSess + ":runtime-model-selection", SessionID: vecSess, Type: "runtime/model_selection",
		TimeCreated: vectorTime.sessionCreate, TimeUpdated: vectorTime.sessionCreate,
		Data: modelSelectionJSON(), TouchSession: false,
	}); err != nil {
		t.Fatalf("model selection entry: %v", err)
	}
	if err := s.UpsertSessionEntry(sessionstore.SessionEntry{
		ID: vecSess + ":runtime-execution-state", SessionID: vecSess, Type: "runtime/execution_state",
		TimeCreated: vectorTime.sessionCreate, TimeUpdated: vectorTime.sessionCreate,
		Data: []byte(`{"mode":"build","planEnabled":false}`), TouchSession: false,
	}); err != nil {
		t.Fatalf("execution state entry: %v", err)
	}
}

func admitInput(t *testing.T, s *store.SessionStore) {
	t.Helper()
	payload := `{"text":"Say hello","intent":` + string(inputIntentJSON()) +
		`,"conversationInputIntent":` + string(conversationInputIntentJSON("queued")) +
		`,"attachments":[],"sourceCommandType":"createSession"}`
	if err := s.AdmitInput(sessionstore.SessionInput{
		ID: "queue_cmd-0001", SessionID: vecSess, Kind: "sendText", Delivery: "startNow",
		Payload: []byte(payload),
	}); err != nil {
		t.Fatalf("admit input: %v", err)
	}
}

func userMessage() sessionstore.Message {
	return sessionstore.Message{
		ID: vecUser, SessionID: vecSess, Role: "user",
		TimeCreated: vectorTime.userMessage, Agent: "nex-agent",
		ModelSelection:  modelSelectionJSON(),
		ContextSnapshot: json.RawMessage(`{"envInfo":` + string(envInfoJSON()) + `}`),
		Semantics:       json.RawMessage(`{"origin":"real_user","kind":"user_prompt","uiVisibility":"visible","providerVisibility":"visible","transcriptVisibility":"visible"}`),
		Anchor:          json.RawMessage(`{"turnId":"` + vecTurn + `","origin":"realUser","sourceCommandId":"cmd-0001"}`),
		Tools:           json.RawMessage(`{"Read":true,"Write":true}`),
		LegacyModel:     json.RawMessage(`{"providerID":"mock","modelID":"mock-model","variant":"none"}`),
		Metadata: json.RawMessage(`{"inputIntent":` + string(inputIntentJSON()) +
			`,"conversationInputIntent":` + string(conversationInputIntentJSON("drained")) +
			`,"inputClientId":"vec-client"}`),
	}
}

// envInfoJSON keeps Node's field order for the context snapshot.
func envInfoJSON() json.RawMessage {
	return json.RawMessage(`{"cwd":"` + vecDir + `","platform":"linux","shell":"zsh",` +
		`"osVersion":"linux 6.8.0 x64","nodeVersion":"v24.0.0","isGitRepository":false,"gitStatus":"not_repo"}`)
}

func userParts() []sessionstore.Part {
	return []sessionstore.Part{{
		ID: "part_test_user_text_1", MessageID: vecUser, SessionID: vecSess, Type: "text",
		Text: "Say hello", TimeStart: i64p(vectorTime.userMessage), TimeEnd: i64p(vectorTime.userMessage),
	}}
}

func assistantTokens() json.RawMessage {
	return json.RawMessage(`{"total":120,"input":100,"output":20,"reasoning":0,"cache":{"read":0,"write":0}}`)
}

func assistantMessage(id string, finish string, completed int64) sessionstore.Message {
	return sessionstore.Message{
		ID: id, SessionID: vecSess, Role: "assistant", ParentID: strp(vecUser),
		ModelID: strp("mock-model"), ProviderID: strp("mock"), Mode: strp("build"), PlanEnabled: boolp(false),
		Agent: "nex-agent", Path: json.RawMessage(`{"cwd":"` + vecDir + `","root":"` + vecDir + `"}`),
		TimeCreated: vectorTime.modelStart, TimeCompleted: i64p(completed),
		Cost:   floatp(0),
		Tokens: assistantTokens(), Finish: strp(finish),
		Semantics: json.RawMessage(`{"origin":"agent_runtime","kind":"assistant_response","uiVisibility":"visible","providerVisibility":"visible","transcriptVisibility":"visible"}`),
		Anchor:    json.RawMessage(`{"turnId":"` + vecTurn + `"}`),
	}
}

func assistantCompleteParts() []sessionstore.Part {
	text := "Hello from the mock model."
	return []sessionstore.Part{
		{ID: "part_test_step_start_1", MessageID: "msg_test_assistant_1", SessionID: vecSess, Type: "step-start", TimeCreated: i64p(vectorNow)},
		{ID: "part_test_step_text_1", MessageID: "msg_test_assistant_1", SessionID: vecSess, Type: "text", Text: text, TimeStart: i64p(vectorTime.modelStart), TimeEnd: i64p(vectorTime.assistantComplete)},
		{ID: "part_test_step_finish_1", MessageID: "msg_test_assistant_1", SessionID: vecSess, Type: "step-finish", Reason: json.RawMessage(`"stop"`), Cost: floatp(0), Tokens: assistantTokens(), TimeCreated: i64p(vectorNow)},
	}
}

func floatp(v float64) *float64 { return &v }

func baseModelUsage() sessionstore.ModelUsage {
	raw := json.RawMessage(`{"inputTokens":100,"outputTokens":20,"totalTokens":120,"cacheReadTokens":0,"reasoningTokens":0}`)
	return sessionstore.ModelUsage{
		ID: "usage_model_main_turn_msg_test_assistant_1_0", LogicalRequestID: "msg_test_assistant_1",
		AttemptIndex: 0, SessionID: vecSess, TurnID: strp(vecTurn), TraceID: strp(vecTrace), SpanID: strp(vecSpan),
		AssistantMessageID: strp("msg_test_assistant_1"), ParentUserMessageID: strp(vecUser),
		QuerySource: "main_turn", ProviderID: "mock", ModelID: "mock-model", Variant: strp("none"),
		Agent: strp("nex-agent"), Mode: strp("build"), TaskType: strp("interactive"), Status: "completed",
		StartedAt: vectorTime.modelStart, FirstTokenAt: i64p(vectorTime.firstToken), CompletedAt: i64p(vectorTime.assistantComplete),
		DurationMs: i64p(vectorTime.assistantComplete - vectorTime.modelStart), TimeToFirstTokenMs: i64p(vectorTime.firstToken - vectorTime.modelStart),
		FinishReason: strp("stop"), ToolCallCount: 0, InputTokens: 100, OutputTokens: 20, ReasoningTokens: 0,
		ProviderTotalTokens: i64p(120), ComputedTotalTokens: i64p(120),
		RawUsage: raw, ProviderMetadata: json.RawMessage(`{"rawFinishReason":"stop"}`),
	}
}

func baseTurnUsage() sessionstore.TurnUsage {
	return sessionstore.TurnUsage{
		SessionID: vecSess, TurnID: vecTurn, TraceID: strp(vecTrace), UserMessageID: strp(vecUser),
		Status: "completed", StartedAt: vectorTime.inputAdmit, FirstModelStartAt: i64p(vectorTime.modelStart),
		FirstTokenAt: i64p(vectorTime.firstToken), CompletedAt: i64p(vectorTime.turnComplete),
		DurationMs: i64p(vectorTime.turnComplete - vectorTime.inputAdmit), TimeToFirstTokenMs: i64p(vectorTime.firstToken - vectorTime.inputAdmit),
		ModelRequestCount: 1, InputTokens: 100, OutputTokens: 20, ComputedTotalTokens: 120,
	}
}

func baseToolUsage() sessionstore.ToolUsage {
	return sessionstore.ToolUsage{
		ID: "usage_tool_" + vecSess + "_call_read_1", SessionID: vecSess, TurnID: strp(vecTurn), TraceID: strp(vecTrace),
		ToolCallID: "call_read_1", ToolName: "Read", SideEffectScope: strp("none"),
		ReadOnly: boolp(true), Destructive: boolp(false), ApprovalStatus: strp("none"), Status: "completed",
		StartedAt: vectorTime.toolStart, FirstOutputAt: i64p(vectorTime.toolFirstOutput), CompletedAt: i64p(vectorTime.toolComplete),
		DurationMs: i64p(vectorTime.toolComplete - vectorTime.toolStart), TimeToFirstOutputMs: i64p(vectorTime.toolFirstOutput - vectorTime.toolStart),
		ExitCode: i64p(0), OutputBytes: 13,
	}
}

func intp(v int) *int { return &v }

// inputIntentWithText mirrors the generator's {...inputIntent(), text, queuePosition}.
func inputIntentWithText(text string, queuePosition int) json.RawMessage {
	raw := `{"sourceCommandId":"cmd-0001","queueItemId":"queue_cmd-0001","clientId":"vec-client",` +
		`"kind":"sendText","text":` + mustQuote(text) + `,` +
		`"modelSelection":{"providerId":"mock","modelId":"mock-model","options":{"reasoningLevel":"none"}},` +
		`"mode":"build","planEnabled":false,"admissionSeq":1,"admittedAt":` + itoa(vectorTime.inputAdmit) + `,` +
		`"requestedDelivery":"startNow","admittedDelivery":"startNow","attachmentRefs":[],` +
		`"queuePosition":` + itoa(int64(queuePosition)) + `}`
	return json.RawMessage(raw)
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
