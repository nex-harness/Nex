package projection

import (
	"testing"
)

// Focused unit tests for code paths the recorded scenarios do not exercise. They
// exist because a mutation check showed the scenario vectors alone do not catch
// these behaviours.

func TestApplyRowRemovedClearsActiveBranch(t *testing.T) {
	rows := func() *obj {
		return newObj().
			set("rows", newObj().
				set("window", []any{
					newObj().set("rowId", int64(1)),
					newObj().set("rowId", int64(2)),
					newObj().set("rowId", int64(3)),
				}).
				set("totalCount", int64(3)).
				set("firstRowId", int64(1)))
	}
	// Removing from the first row clears the active branch entirely.
	got := applyConversationDelta(rows(), newObj().
		set("op", "row.removed").
		set("fromRowId", int64(1)))
	gotRows := got.get("rows").(*obj)
	if gotRows.get("firstRowId") != nil {
		t.Fatalf("firstRowId should be null after trimming from the head, got %v", gotRows.get("firstRowId"))
	}
	if tc := int64Of(gotRows.get("totalCount")); tc != 0 {
		t.Fatalf("totalCount should be 0 after trimming from the head, got %d", tc)
	}

	// Removing a tail row keeps the anchor and decrements the count.
	snapshot := newObj().
		set("rows", newObj().
			set("window", []any{
				newObj().set("rowId", int64(1)),
				newObj().set("rowId", int64(2)),
			}).
			set("totalCount", int64(5)).
			set("firstRowId", int64(1)))
	got2 := applyConversationDelta(snapshot, newObj().
		set("op", "row.removed").
		set("fromRowId", int64(2)))
	gotRows2 := got2.get("rows").(*obj)
	if int64Of(gotRows2.get("firstRowId")) != 1 {
		t.Fatalf("firstRowId should stay 1 for a tail trim, got %v", gotRows2.get("firstRowId"))
	}
	if tc := int64Of(gotRows2.get("totalCount")); tc != 4 {
		t.Fatalf("totalCount should drop by the removed rows (4), got %d", tc)
	}
}

func TestAppendToRowRespectsKindGuard(t *testing.T) {
	toolRow := newObj().set("kind", "toolCall").set("text", "x").set("inputText", "a")
	// path "text" on a toolCall row is a no-op (only assistantText/reasoning take it).
	got := appendToRow(toolRow, "text", "Z")
	if strOf(got.get("text")) != "x" {
		t.Fatalf("text path must not touch a toolCall row, got %q", strOf(got.get("text")))
	}
	// path "inputText" on a toolCall row appends.
	got2 := appendToRow(toolRow, "inputText", "b")
	if strOf(got2.get("inputText")) != "ab" {
		t.Fatalf("inputText append failed, got %q", strOf(got2.get("inputText")))
	}
	// path "inputText" on a userInput row is a no-op.
	userRow := newObj().set("kind", "userInput").set("inputText", "a")
	got3 := appendToRow(userRow, "inputText", "b")
	if strOf(got3.get("inputText")) != "a" {
		t.Fatalf("inputText path must not touch a userInput row, got %q", strOf(got3.get("inputText")))
	}
}

func TestAvailabilityDraftForbidsCompactAndFork(t *testing.T) {
	got := computeAvailability(availabilityContext{phase: "draft", autoDrain: true})
	compact := got.get("compact").(*obj)
	if boolOf(compact.get("allowed")) {
		t.Fatal("draft must not allow compact")
	}
	if strOf(compact.get("reasonCode")) != "idleCannotCompact" {
		t.Fatalf("draft compact reasonCode, got %q", strOf(compact.get("reasonCode")))
	}
	fork := got.get("fork").(*obj)
	if boolOf(fork.get("allowed")) || strOf(fork.get("reasonCode")) != "forkTargetNotStable" {
		t.Fatalf("draft fork should be denied(forkTargetNotStable), got %s", mustJSON(t, fork))
	}
}

func TestToolCallResultFailure(t *testing.T) {
	p := New("sess_t", "epoch_t")
	// Seed a running tool row through the real event path.
	feed(t, p, `{"id":"e1","sessionId":"sess_t","turnId":"turn_1","type":"turn_started","timestampMs":1000,"traceId":"t","sequenceNumber":1,"payload":{"turnNumber":1,"input":"go","messageId":"m1","intent":{"sourceCommandId":"c1"}}}`)
	feed(t, p, `{"id":"e2","sessionId":"sess_t","turnId":"turn_1","type":"model_streaming","timestampMs":1001,"traceId":"t","sequenceNumber":2,"payload":{"assistantMessageId":"a1","toolCallId":"tc1","toolName":"Bash","delta":"","done":false,"kind":"tool_input_start"}}`)
	feed(t, p, `{"id":"e3","sessionId":"sess_t","turnId":"turn_1","type":"tool_call_started","timestampMs":1002,"traceId":"t","sequenceNumber":3,"payload":{"toolCallId":"tc1","toolName":"Bash","startedAt":"x"}}`)
	deltas := feed(t, p, `{"id":"e4","sessionId":"sess_t","turnId":"turn_1","type":"tool_call_result","timestampMs":1003,"traceId":"t","sequenceNumber":4,"payload":{"toolCallId":"tc1","result":{"success":false,"content":"boom"},"duration":1}}`)
	row := findUpsertedToolRow(t, deltas, "tc1")
	if statusOf(row) != "error" {
		t.Fatalf("failed tool result must set status=error, got %q", statusOf(row))
	}
	if errObj, ok := row.get("error").(*obj); !ok || strOf(errObj.get("code")) == "" {
		t.Fatalf("failed tool result must carry an error object, got %s", mustJSON(t, row))
	}
}

func feed(t *testing.T, p *Projection, raw string) []Delta {
	t.Helper()
	event, err := ParseEvent([]byte(raw))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	deltas, err := p.Apply(event)
	if err != nil {
		t.Fatalf("apply event: %v", err)
	}
	return deltas
}

func findUpsertedToolRow(t *testing.T, deltas []Delta, toolCallID string) *obj {
	t.Helper()
	for i := len(deltas) - 1; i >= 0; i-- {
		d := deltas[i]
		if opOf(d) != "row.upserted" {
			continue
		}
		row := rowOf(d)
		if kindOf(row) == "toolCall" && toolCallIDOf(row) == toolCallID {
			return row
		}
	}
	t.Fatalf("no upserted toolCall row for %s in %d deltas", toolCallID, len(deltas))
	return nil
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := marshalValue(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}

// TestPermissionRequestedFullAccessOption pins the fullAccessOption the v4
// interaction payload carries when the tool advertises full-access support and
// the request has no origin/optionsPolicy override (S04/S05 recordings).
func TestPermissionRequestedFullAccessOption(t *testing.T) {
	p := New("sess_fa", "epoch_fa")
	feed(t, p, `{"id":"e1","sessionId":"sess_fa","turnId":"turn_1","type":"turn_started","timestampMs":1000,"traceId":"t","sequenceNumber":1,"payload":{"turnNumber":1,"input":"go","messageId":"m1","intent":{"sourceCommandId":"c1"}}}`)
	feed(t, p, `{"id":"e2","sessionId":"sess_fa","turnId":"turn_1","type":"model_streaming","timestampMs":1001,"traceId":"t","sequenceNumber":2,"payload":{"assistantMessageId":"a1","toolCallId":"tc1","toolName":"Write","delta":"{\"file_path\":\"/x\"}","done":false,"kind":"tool_input_start"}}`)
	deltas := feed(t, p, `{"id":"e3","sessionId":"sess_fa","turnId":"turn_1","type":"permission_requested","timestampMs":1002,"traceId":"t","sequenceNumber":3,"payload":{"toolCallId":"tc1","toolName":"Write","riskLevel":"medium","reason":"Tool has side effects and requires approval","input":{"file_path":"/x"},"requestId":"perm_1","fullAccessSupported":true}}`)
	interaction := findPermissionInteraction(t, deltas)
	fa, ok := interaction.get("fullAccessOption").(*obj)
	if !ok {
		t.Fatalf("fullAccessOption missing from interaction payload: %s", mustJSON(t, interaction))
	}
	if strOf(fa.get("optionId")) != "fullAccess" || strOf(fa.get("kind")) != "custom" {
		t.Fatalf("fullAccessOption = %s", mustJSON(t, fa))
	}

	// Without fullAccessSupported there is no fullAccessOption.
	p2 := New("sess_fa2", "epoch_fa2")
	feed(t, p2, `{"id":"f1","sessionId":"sess_fa2","turnId":"turn_1","type":"turn_started","timestampMs":1000,"traceId":"t","sequenceNumber":1,"payload":{"turnNumber":1,"input":"go","messageId":"m1","intent":{"sourceCommandId":"c1"}}}`)
	feed(t, p2, `{"id":"f2","sessionId":"sess_fa2","turnId":"turn_1","type":"model_streaming","timestampMs":1001,"traceId":"t","sequenceNumber":2,"payload":{"assistantMessageId":"a1","toolCallId":"tc1","toolName":"Write","delta":"{\"file_path\":\"/x\"}","done":false,"kind":"tool_input_start"}}`)
	deltas2 := feed(t, p2, `{"id":"f3","sessionId":"sess_fa2","turnId":"turn_1","type":"permission_requested","timestampMs":1002,"traceId":"t","sequenceNumber":3,"payload":{"toolCallId":"tc1","toolName":"Write","riskLevel":"medium","reason":"r","input":{"file_path":"/x"},"requestId":"perm_2"}}`)
	interaction2 := findPermissionInteraction(t, deltas2)
	if interaction2.has("fullAccessOption") {
		t.Fatalf("unexpected fullAccessOption: %s", mustJSON(t, interaction2))
	}
}

func findPermissionInteraction(t *testing.T, deltas []Delta) *obj {
	t.Helper()
	for _, d := range deltas {
		if opOf(d) != "state.updated" {
			continue
		}
		patch := patchOf(d)
		pending, ok := patch.get("pendingInteractions").([]any)
		if !ok || len(pending) == 0 {
			continue
		}
		interaction, _ := pending[0].(*obj)
		if interaction == nil {
			continue
		}
		payload, _ := interaction.get("payload").(*obj)
		if payload != nil {
			return payload
		}
	}
	t.Fatal("no permission interaction found")
	return nil
}

// TestToolCallErrorRowHasErrorAndNoOutput pins onToolCallError
// (product-projection.ts:3032): a failed tool call settles its row through
// tool_call_error — status error, `error:{code,message}` from the payload's
// error.type/error.message, endedAt, and **no** `output` key, because a failed
// call never publishes tool_call_result.
func TestToolCallErrorRowHasErrorAndNoOutput(t *testing.T) {
	p := New("sess_e", "epoch_e")
	feed(t, p, `{"id":"e1","sessionId":"sess_e","turnId":"turn_1","type":"turn_started","timestampMs":1000,"traceId":"t","sequenceNumber":1,"payload":{"turnNumber":1,"input":"go","messageId":"m1","intent":{"sourceCommandId":"c1"}}}`)
	feed(t, p, `{"id":"e2","sessionId":"sess_e","turnId":"turn_1","type":"model_streaming","timestampMs":1001,"traceId":"t","sequenceNumber":2,"payload":{"assistantMessageId":"a1","toolCallId":"tc1","toolName":"Read","delta":"","done":false,"kind":"tool_input_start"}}`)
	feed(t, p, `{"id":"e3","sessionId":"sess_e","turnId":"turn_1","type":"tool_call_started","timestampMs":1002,"traceId":"t","sequenceNumber":3,"payload":{"toolCallId":"tc1","toolName":"Read","startedAt":"x"}}`)
	deltas := feed(t, p, `{"id":"e4","sessionId":"sess_e","turnId":"turn_1","type":"tool_call_error","timestampMs":1003,"traceId":"t","sequenceNumber":4,"payload":{"toolCallId":"tc1","error":{"type":"tool_execution_failed","message":"File does not exist.","code":"TOOL_EXECUTION_FAILED"}}}`)

	row := findUpsertedToolRow(t, deltas, "tc1")
	if statusOf(row) != "error" {
		t.Fatalf("tool_call_error must set status=error, got %q", statusOf(row))
	}
	errObj, ok := row.get("error").(*obj)
	if !ok {
		t.Fatalf("tool_call_error must carry an error object, got %s", mustJSON(t, row))
	}
	// The row shows the CoreError *type* (lower case), not the upper-cased code.
	if strOf(errObj.get("code")) != "tool_execution_failed" {
		t.Errorf("error.code = %q, want tool_execution_failed (payload error.type)", strOf(errObj.get("code")))
	}
	if strOf(errObj.get("message")) != "File does not exist." {
		t.Errorf("error.message = %q", strOf(errObj.get("message")))
	}
	if hasKey(row, "output") {
		t.Errorf("a failed tool row must not carry output, got %s", mustJSON(t, row))
	}
	if !hasKey(row, "endedAt") {
		t.Errorf("tool_call_error must set endedAt, got %s", mustJSON(t, row))
	}
	// The started fields survive, so the row keeps its running identity.
	if !hasKey(row, "startedAt") {
		t.Errorf("tool_call_error must preserve startedAt, got %s", mustJSON(t, row))
	}
}

// TestToolCallErrorCancellationDropsError pins the cancelled branch: Stop emits
// tool_cancelled before the cancelled turn, and a row already written as error
// would not be corrected by the turn reducer.
func TestToolCallErrorCancellationDropsError(t *testing.T) {
	p := New("sess_c", "epoch_c")
	feed(t, p, `{"id":"e1","sessionId":"sess_c","turnId":"turn_1","type":"turn_started","timestampMs":1000,"traceId":"t","sequenceNumber":1,"payload":{"turnNumber":1,"input":"go","messageId":"m1","intent":{"sourceCommandId":"c1"}}}`)
	feed(t, p, `{"id":"e2","sessionId":"sess_c","turnId":"turn_1","type":"model_streaming","timestampMs":1001,"traceId":"t","sequenceNumber":2,"payload":{"assistantMessageId":"a1","toolCallId":"tc1","toolName":"Bash","delta":"","done":false,"kind":"tool_input_start"}}`)
	deltas := feed(t, p, `{"id":"e3","sessionId":"sess_c","turnId":"turn_1","type":"tool_call_error","timestampMs":1002,"traceId":"t","sequenceNumber":3,"payload":{"toolCallId":"tc1","error":{"type":"tool_cancelled","message":"Tool execution cancelled"}}}`)
	row := findUpsertedToolRow(t, deltas, "tc1")
	if statusOf(row) != "cancelled" {
		t.Fatalf("cancelled error must set status=cancelled, got %q", statusOf(row))
	}
	if hasKey(row, "error") {
		t.Errorf("a cancelled row must not carry error, got %s", mustJSON(t, row))
	}
}

// TestToolCallErrorBeforeScheduledStillUpserts pins the pre-handler failures:
// a registry miss or an input-schema failure never emits tool_call_started, so
// the row must settle straight from inputStreaming to error.
func TestToolCallErrorBeforeScheduledStillUpserts(t *testing.T) {
	p := New("sess_n", "epoch_n")
	feed(t, p, `{"id":"e1","sessionId":"sess_n","turnId":"turn_1","type":"turn_started","timestampMs":1000,"traceId":"t","sequenceNumber":1,"payload":{"turnNumber":1,"input":"go","messageId":"m1","intent":{"sourceCommandId":"c1"}}}`)
	feed(t, p, `{"id":"e2","sessionId":"sess_n","turnId":"turn_1","type":"model_streaming","timestampMs":1001,"traceId":"t","sequenceNumber":2,"payload":{"assistantMessageId":"a1","toolCallId":"tc1","toolName":"NoSuchTool","delta":"","done":false,"kind":"tool_input_start"}}`)
	deltas := feed(t, p, `{"id":"e3","sessionId":"sess_n","turnId":"turn_1","type":"tool_call_error","timestampMs":1002,"traceId":"t","sequenceNumber":3,"payload":{"toolCallId":"tc1","error":{"type":"tool_not_found","message":"Tool not found: NoSuchTool","code":"TOOL_NOT_FOUND"}}}`)
	row := findUpsertedToolRow(t, deltas, "tc1")
	if statusOf(row) != "error" {
		t.Fatalf("status = %q, want error", statusOf(row))
	}
	errObj, _ := row.get("error").(*obj)
	if strOf(errObj.get("code")) != "tool_not_found" || strOf(errObj.get("message")) != "Tool not found: NoSuchTool" {
		t.Errorf("error = %s", mustJSON(t, errObj))
	}
	if hasKey(row, "startedAt") {
		t.Errorf("a pre-start failure must not invent startedAt, got %s", mustJSON(t, row))
	}
}

func hasKey(o *obj, key string) bool { return o.has(key) }
