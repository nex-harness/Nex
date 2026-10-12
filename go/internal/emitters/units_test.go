package emitters

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/tylinux/nex/go/internal/projection"
)

func testEvent(id, typ string, seq int64, payload string) projection.Event {
	ev := projection.Event{
		ID:             id,
		SessionID:      "sess",
		TurnID:         "turn_1",
		HasTurnID:      true,
		Type:           typ,
		TimestampMs:    1000 + seq,
		SequenceNumber: seq,
		Payload:        json.RawMessage(payload),
	}
	return ev
}

// TestUsageDeltaPrefersTotalTokens proves totalTokensOf uses usage.totalTokens
// when present, not input+output.
func TestUsageDeltaPrefersTotalTokens(t *testing.T) {
	tel := New("sess", Metadata{MemoryEnabled: boolPtr(false)})
	// seed a completed request so the usage.delta carries identity.
	_, _, _ = tel.OnSessionEventTelemetry(testEvent("e1", projection.TypeModelNetworkStatus, 5,
		`{"type":"model_request_completed","requestId":"r1","providerId":"p","modelId":"m","transport":"sse","attempt":1,"maxAttempts":11,"durationMs":0}`))
	msg, ok, err := tel.OnSessionEventTelemetry(testEvent("e2", projection.TypeModelComplete, 6,
		`{"querySource":"main_turn","stopReason":"stop","usage":{"inputTokens":10,"outputTokens":3,"totalTokens":999}}`))
	if err != nil || !ok {
		t.Fatalf("no usage fact: %v", err)
	}
	var p struct {
		TotalTokens int64 `json:"totalTokens"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	if p.TotalTokens != 999 {
		t.Fatalf("totalTokens = %d, want 999", p.TotalTokens)
	}
}

// TestUsageDeltaFallsBackToInputPlusOutput proves the fallback when totalTokens
// is absent.
func TestUsageDeltaFallsBackToInputPlusOutput(t *testing.T) {
	tel := New("sess", Metadata{MemoryEnabled: boolPtr(false)})
	msg, ok, _ := tel.OnSessionEventTelemetry(testEvent("e2", projection.TypeModelComplete, 6,
		`{"querySource":"main_turn","stopReason":"stop","usage":{"inputTokens":10,"outputTokens":3}}`))
	if !ok {
		t.Fatal("no usage fact")
	}
	var p struct {
		TotalTokens int64 `json:"totalTokens"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	if p.TotalTokens != 13 {
		t.Fatalf("totalTokens = %d, want 13", p.TotalTokens)
	}
}

// TestQueuedAndAdmittedAreSkipped proves the two admission statuses produce no
// telemetry fact.
func TestQueuedAndAdmittedAreSkipped(t *testing.T) {
	tel := New("sess", Metadata{MemoryEnabled: boolPtr(false)})
	for _, typ := range []string{"model_request_queued", "model_request_admitted"} {
		_, ok, err := tel.OnSessionEventTelemetry(testEvent("e", projection.TypeModelNetworkStatus, 5,
			`{"type":"`+typ+`","requestId":"r","providerId":"p","modelId":"m","transport":"sse","attempt":1,"maxAttempts":11}`))
		if err != nil || ok {
			t.Fatalf("%s: expected no fact, got ok=%v err=%v", typ, ok, err)
		}
	}
}

// TestSidecarModelCompleteProducesNoUsage proves the session_title / tool_internal
// model completes are dropped.
func TestSidecarModelCompleteProducesNoUsage(t *testing.T) {
	tel := New("sess", Metadata{MemoryEnabled: boolPtr(false)})
	_, ok, _ := tel.OnSessionEventTelemetry(testEvent("e", projection.TypeModelComplete, 6,
		`{"querySource":"session_title","stopReason":"stop","usage":{"inputTokens":1,"outputTokens":1}}`))
	if ok {
		t.Fatal("session_title model_complete should produce no usage.delta")
	}
}

// TestToolNameLookupSurvivesStarted proves a started event without toolName
// recovers the name remembered at scheduled time, and that the name is dropped
// after completion (a later unnamed progress event cannot recover it).
func TestToolNameLookupSurvivesStarted(t *testing.T) {
	tel := New("sess", Metadata{MemoryEnabled: boolPtr(false)})
	_, _, _ = tel.OnSessionEventTelemetry(testEvent("e1", projection.TypeToolCallScheduled, 12,
		`{"toolCallId":"c1","toolName":"Read","input":{},"schedule":{"parallelGroups":[["c1"]],"executionOrder":["c1"]}}`))
	msg, ok, _ := tel.OnSessionEventTelemetry(testEvent("e2", projection.TypeToolCallStarted, 16, `{"toolCallId":"c1"}`))
	if !ok {
		t.Fatal("no started fact")
	}
	if !containsKey(msg.Params, "toolName", "Read") {
		t.Fatalf("started fact lost toolName: %s", msg.Params)
	}
	// complete it -> name forgotten.
	_, _, _ = tel.OnSessionEventTelemetry(testEvent("e3", projection.TypeToolCallResult, 19,
		`{"toolCallId":"c1","result":{"success":true,"content":"x"},"duration":0}`))
	msg2, ok2, _ := tel.OnSessionEventTelemetry(testEvent("e4", projection.TypeToolCallProgress, 20, `{"toolCallId":"c1"}`))
	if !ok2 {
		t.Fatal("no progress fact")
	}
	if containsKey(msg2.Params, "toolName", "Read") {
		t.Fatalf("toolName should have been dropped after completion: %s", msg2.Params)
	}
}

// TestCronCreateAutomationID proves completed CronCreate facts carry the
// automationId parsed from the result content.
func TestCronCreateAutomationID(t *testing.T) {
	tel := New("sess", Metadata{MemoryEnabled: boolPtr(false)})
	_, _, _ = tel.OnSessionEventTelemetry(testEvent("e1", projection.TypeToolCallScheduled, 12,
		`{"toolCallId":"c1","toolName":"CronCreate","input":{},"schedule":{"parallelGroups":[["c1"]],"executionOrder":["c1"]}}`))
	msg, ok, _ := tel.OnSessionEventTelemetry(testEvent("e2", projection.TypeToolCallResult, 19,
		`{"toolCallId":"c1","result":{"success":true,"content":"{\"automation\":{\"automationId\":\"auto-9\"}}"},"duration":0}`))
	if !ok {
		t.Fatal("no fact")
	}
	if !containsKey(msg.Params, "automationId", "auto-9") {
		t.Fatalf("automationId missing: %s", msg.Params)
	}
	// automationId must be the last key (schema order).
	if lastKey(msg.Params) != "automationId" {
		t.Fatalf("automationId is not last: %s", msg.Params)
	}
}

// TestTurnStartedOperationNeedsTurnID proves an operation turn-started without a
// turn id is dropped.
func TestTurnStartedOperationNeedsTurnID(t *testing.T) {
	ev := projection.Event{ID: "e", SessionID: "s", Type: projection.TypeTurnStarted, HasTurnID: false, Payload: json.RawMessage(`{}`)}
	if got := mapOperationEvent(ev); got != nil {
		t.Fatalf("expected nil, got %s", got)
	}
}

// TestCancelledTurnTerminal proves resultType cancelled maps to interrupted and
// carries the USER_INTERRUPT error.
func TestCancelledTurnTerminal(t *testing.T) {
	tel := New("sess", Metadata{MemoryEnabled: boolPtr(false)})
	msg, ok, _ := tel.OnSessionEventTelemetry(testEvent("e", projection.TypeTurnComplete, 9,
		`{"response":"","tokenCount":0,"toolCallCount":0,"duration":0,"resultType":"cancelled"}`))
	if !ok {
		t.Fatal("no fact")
	}
	if !containsKey(msg.Params, "status", "interrupted") || !containsKey(msg.Params, "errorCode", "USER_INTERRUPT") {
		t.Fatalf("cancelled mapping wrong: %s", msg.Params)
	}
}

// TestMCPToolSchemasSeparateFromSystemTools proves an mcp__ tool lands in the
// mcp_tool_schemas category, not system_tool_schemas.
func TestMCPToolSchemasSeparateFromSystemTools(t *testing.T) {
	tools := []ToolContract{
		{Name: "Read", Description: "x", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "mcp__srv__do", Description: "y", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	got := ContextUsage(nil, tools, nil)
	sources := map[string]int64{}
	for _, it := range got {
		sources[it.Source] = it.Chars
	}
	if sources["system_tool_schemas"] == 0 || sources["mcp_tool_schemas"] == 0 {
		t.Fatalf("expected both categories, got %v", sources)
	}
	// Only the two tools are present, so a mixed-up implementation would fold
	// the mcp tool into system_tool_schemas and drop mcp_tool_schemas entirely.
}

// TestEstimateTokensDivisor pins the token estimator used by the category gate.
func TestEstimateTokensDivisor(t *testing.T) {
	// 30 ASCII chars -> ceil(30/3) = 10.
	if got := estimateTokens("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); got != 10 {
		t.Fatalf("estimateTokens = %d, want 10", got)
	}
	// 3 chinese chars count double: ceil(6/3) = 2.
	if got := estimateTokens("中中中"); got != 2 {
		t.Fatalf("estimateTokens chinese = %d, want 2", got)
	}
}

// TestChunkLengthCountsUTF16 proves chunkLength uses String.length, so an
// astral-plane character in the delta counts as two.
func TestChunkLengthCountsUTF16(t *testing.T) {
	tel := New("sess", Metadata{MemoryEnabled: boolPtr(false)})
	msg, ok, _ := tel.OnSessionEventTelemetry(testEvent("e", projection.TypeModelStreaming, 8,
		`{"kind":"text_delta","delta":"a😀","done":false,"assistantMessageId":"m"}`))
	if !ok {
		t.Fatal("no chunk fact")
	}
	var p struct {
		ChunkLength int64 `json:"chunkLength"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	if p.ChunkLength != 3 {
		t.Fatalf("chunkLength = %d, want 3 (UTF-16 units)", p.ChunkLength)
	}
}

// TestCronCreateAutomationIDWithPerformance proves automationId stays last even
// when a performance block is present (schema order).
func TestCronCreateAutomationIDWithPerformance(t *testing.T) {
	tel := New("sess", Metadata{MemoryEnabled: boolPtr(false)})
	_, _, _ = tel.OnSessionEventTelemetry(testEvent("e1", projection.TypeToolCallScheduled, 12,
		`{"toolCallId":"c1","toolName":"CronCreate","input":{},"schedule":{"parallelGroups":[["c1"]],"executionOrder":["c1"]}}`))
	msg, ok, _ := tel.OnSessionEventTelemetry(testEvent("e2", projection.TypeToolCallResult, 19,
		`{"toolCallId":"c1","result":{"success":true,"content":"{\"automation\":{\"automationId\":\"auto-9\"}}","perf":{"totalMs":5}},"duration":1}`))
	if !ok {
		t.Fatal("no fact")
	}
	if lastKey(msg.Params) != "automationId" {
		t.Fatalf("automationId must be last, got %s", msg.Params)
	}
	if !containsKey(msg.Params, "automationId", "auto-9") {
		t.Fatalf("automationId missing: %s", msg.Params)
	}
}

func boolPtr(v bool) *bool { return &v }

// containsKey reports whether a compact JSON object has key with the string value.
func containsKey(raw json.RawMessage, key, value string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	v, ok := m[key]
	if !ok {
		return false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return false
	}
	return s == value
}

func lastKey(raw json.RawMessage) string {
	keys := jsonKeyOrder(raw)
	if len(keys) == 0 {
		return ""
	}
	return keys[len(keys)-1]
}

// jsonKeyOrder returns the top-level object keys in document order.
func jsonKeyOrder(raw []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil
	}
	var keys []string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return keys
		}
		key, _ := keyTok.(string)
		keys = append(keys, key)
		if err := skipValue(dec); err != nil {
			return keys
		}
	}
	return keys
}

// skipValue consumes one JSON value from the decoder.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if d == '{' {
		for dec.More() {
			if _, err := dec.Token(); err != nil { // key
				return err
			}
			if err := skipValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token() // '}'
		return err
	}
	if d == '[' {
		for dec.More() {
			if err := skipValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token() // ']'
		return err
	}
	return nil
}

// TestToolLifecycleFailedFact pins the failed fact of a tool_call_error event
// (conversation-telemetry-facts.ts:504): errorCode is `error.code ?? error.type`
// — the upper-cased CoreError code the executor sets — and errorMessage is the
// payload's projected message. No durationMs and no performance: a failed call
// publishes no result payload to read them from.
func TestToolLifecycleFailedFact(t *testing.T) {
	cases := []struct {
		name      string
		payload   string
		wantCode  string
		wantMsg   string
		wantPhase string
	}{
		{
			name:     "handler failure carries the numeric code",
			payload:  `{"toolCallId":"c1","error":{"type":"tool_execution_failed","message":"String to replace not found in file. String: zzz","code":"8"}}`,
			wantCode: "8", wantMsg: "String to replace not found in file. String: zzz", wantPhase: "failed",
		},
		{
			name:     "thrown CoreError falls back to the upper-cased type",
			payload:  `{"toolCallId":"c1","error":{"type":"tool_execution_failed","message":"File does not exist.","code":"TOOL_EXECUTION_FAILED"}}`,
			wantCode: "TOOL_EXECUTION_FAILED", wantMsg: "File does not exist.", wantPhase: "failed",
		},
		{
			name:     "registry miss",
			payload:  `{"toolCallId":"c1","error":{"type":"tool_not_found","message":"Tool not found: NoSuchTool","code":"TOOL_NOT_FOUND"}}`,
			wantCode: "TOOL_NOT_FOUND", wantMsg: "Tool not found: NoSuchTool", wantPhase: "failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tel := New("sess", Metadata{MemoryEnabled: boolPtr(false)})
			// A failure before tool_call_started still reports the toolName the
			// scheduled event recorded, so the fact is attributable.
			_, _, _ = tel.OnSessionEventTelemetry(testEvent("e1", projection.TypeToolCallScheduled, 12,
				`{"toolCallId":"c1","toolName":"Read","input":{},"schedule":{"parallelGroups":[["c1"]],"executionOrder":["c1"]}}`))
			msg, ok, err := tel.OnSessionEventTelemetry(testEvent("e2", projection.TypeToolCallError, 18, tc.payload))
			if err != nil || !ok {
				t.Fatalf("no failed fact (ok=%v err=%v)", ok, err)
			}
			if !containsKey(msg.Params, "phase", tc.wantPhase) {
				t.Errorf("phase = %s", msg.Params)
			}
			if !containsKey(msg.Params, "toolName", "Read") {
				t.Errorf("failed fact lost toolName: %s", msg.Params)
			}
			if !containsKey(msg.Params, "errorCode", tc.wantCode) {
				t.Errorf("errorCode = %s, want %q", msg.Params, tc.wantCode)
			}
			if !containsKey(msg.Params, "errorMessage", tc.wantMsg) {
				t.Errorf("errorMessage = %s, want %q", msg.Params, tc.wantMsg)
			}
			// durationMs / performance come from the result payload, which a
			// failure never has.
			if containsAnyKey(msg.Params, "durationMs", "performance") {
				t.Errorf("a failed fact must not carry result-derived fields: %s", msg.Params)
			}
		})
	}
}

// TestToolLifecycleFailedForgetsToolName proves the terminal phase clears the
// per-call toolName table, like completed does.
func TestToolLifecycleFailedForgetsToolName(t *testing.T) {
	tel := New("sess", Metadata{MemoryEnabled: boolPtr(false)})
	_, _, _ = tel.OnSessionEventTelemetry(testEvent("e1", projection.TypeToolCallScheduled, 12,
		`{"toolCallId":"c1","toolName":"Read","input":{},"schedule":{"parallelGroups":[["c1"]],"executionOrder":["c1"]}}`))
	_, _, _ = tel.OnSessionEventTelemetry(testEvent("e2", projection.TypeToolCallError, 18,
		`{"toolCallId":"c1","error":{"type":"tool_execution_failed","message":"boom","code":"TOOL_EXECUTION_FAILED"}}`))
	msg, ok, _ := tel.OnSessionEventTelemetry(testEvent("e3", projection.TypeToolCallProgress, 19, `{"toolCallId":"c1"}`))
	if !ok {
		t.Fatal("no progress fact")
	}
	if containsKey(msg.Params, "toolName", "Read") {
		t.Fatalf("toolName should have been dropped after a failure: %s", msg.Params)
	}
}

func containsAnyKey(params json.RawMessage, keys ...string) bool {
	for _, k := range keys {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(params, &m); err != nil {
			return false
		}
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}
