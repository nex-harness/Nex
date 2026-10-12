package store_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/tylinux/nex/go/internal/adapters/store"
	"github.com/tylinux/nex/go/internal/sessionstore"
)

// TestScenariosMatchNodeVectors drives the Go adapter with the same scripted
// operations as scripts/go-rewrite/gen-store-vectors.mts (which drives the real
// Node repositories) and requires the resulting rows to be identical, table by
// table and column by column.
func TestScenariosMatchNodeVectors(t *testing.T) {
	v := loadVectors(t)
	byName := map[string]func(*testing.T, *store.SessionStore){}
	for _, sc := range v.Scenarios {
		byName[sc.Name] = scenarioRunner(sc.Name)
	}
	for _, sc := range v.Scenarios {
		run, ok := byName[sc.Name]
		if !ok {
			t.Fatalf("no Go runner for scenario %q", sc.Name)
		}
		t.Run(sc.Name, func(t *testing.T) {
			s := openStore(t)
			run(t, s)
			for _, table := range []string{"session", "message", "part", "session_entry", "session_input", "input_history", "model_usage", "turn_usage", "tool_usage", "todo"} {
				got := dumpTable(t, s, table)
				want := sc.Tables[table]
				for _, ex := range v.ExcludedColumns {
					if ex == table+".id" {
						stripColumn(got, "id")
						stripColumn(want, "id")
					}
				}
				sortRows(got)
				sortRows(want)
				assertRowsEqual(t, table, want, got)
			}
		})
	}
}

func assertRowsEqual(t *testing.T, table string, want []map[string]any, got []map[string]any) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s: row count want %d got %d\nwant=%s\ngot=%s",
			table, len(want), len(got), jsonify(want), jsonify(got))
	}
	for i := range want {
		if !reflect.DeepEqual(want[i], got[i]) {
			t.Fatalf("%s[%d]: mismatch\nwant=%s\ngot =%s",
				table, i, jsonify(want[i]), jsonify(got[i]))
		}
	}
}

func jsonify(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func scenarioRunner(name string) func(*testing.T, *store.SessionStore) {
	switch name {
	case "text-turn":
		return func(t *testing.T, s *store.SessionStore) {
			createSession(t, s)
			sessionEntries(t, s)
			admitInput(t, s)
			if err := s.PromoteInput("queue_cmd-0001", vecSess, userMessage(), userParts()); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveMessage(assistantMessage("msg_test_assistant_1", "stop", vectorTime.assistantComplete)); err != nil {
				t.Fatal(err)
			}
			for _, p := range assistantCompleteParts() {
				if err := s.SavePart(p); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.RecordModelUsage(baseModelUsage()); err != nil {
				t.Fatal(err)
			}
			if err := s.UpsertTurnUsage(baseTurnUsage()); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RecordInputHistory(sessionstore.InputHistory{
				ProjectID: vecProj, SessionID: strp(vecSess), Text: "Say hello", Kind: "prompt",
				TimeCreated: i64p(vectorTime.inputAdmit),
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpdateTitle(vecSess, "Say hello", "first_input", vectorTime.title); err != nil {
				t.Fatal(err)
			}
		}
	case "tool-turn":
		return func(t *testing.T, s *store.SessionStore) {
			createSession(t, s)
			sessionEntries(t, s)
			admitInput(t, s)
			if err := s.PromoteInput("queue_cmd-0001", vecSess, userMessage(), userParts()); err != nil {
				t.Fatal(err)
			}
			toolMsg := "msg_test_assistant_tool_1"
			if err := s.SaveMessage(assistantMessage(toolMsg, "tool-calls", vectorTime.toolComplete)); err != nil {
				t.Fatal(err)
			}
			toolPart := sessionstore.Part{
				ID: "part_test_tool_read_1", MessageID: toolMsg, SessionID: vecSess, Type: "tool",
				CallID: strp("call_read_1"), DeclarationIndex: intp(0), Tool: strp("Read"),
				State: json.RawMessage(`{"status":"completed","input":{"file_path":"/work/proj/hello.txt"},` +
					`"output":"1\thi there\n2\t","title":"Read","metadata":{"schemaVersion":1,` +
					`"serialization":{"truncated":false,"originalBytes":13,"returnedBytes":13,"budgetStrategy":"truncate"}},` +
					`"time":{"start":` + itoa(vectorTime.toolStart) + `,"end":` + itoa(vectorTime.toolComplete) + `}}`),
			}
			for _, p := range []sessionstore.Part{
				{ID: "part_test_tool_step_start_1", MessageID: toolMsg, SessionID: vecSess, Type: "step-start", TimeCreated: i64p(vectorNow)},
				toolPart,
				{ID: "part_test_tool_step_finish_1", MessageID: toolMsg, SessionID: vecSess, Type: "step-finish", Reason: json.RawMessage(`"tool-calls"`), Cost: floatp(0), Tokens: assistantTokens(), TimeCreated: i64p(vectorNow)},
			} {
				if err := s.SavePart(p); err != nil {
					t.Fatal(err)
				}
			}
			mu := baseModelUsage()
			mu.ID = "usage_model_main_turn_" + toolMsg + "_0"
			mu.LogicalRequestID = toolMsg
			mu.AssistantMessageID = strp(toolMsg)
			mu.FinishReason = strp("tool-calls")
			if err := s.RecordModelUsage(mu); err != nil {
				t.Fatal(err)
			}
			tu := baseTurnUsage()
			tu.ToolCallCount = 1
			if err := s.UpsertTurnUsage(tu); err != nil {
				t.Fatal(err)
			}
			if err := s.UpsertToolUsage(baseToolUsage()); err != nil {
				t.Fatal(err)
			}
		}
	case "retry":
		return func(t *testing.T, s *store.SessionStore) {
			createSession(t, s)
			sessionEntries(t, s)
			admitInput(t, s)
			if err := s.PromoteInput("queue_cmd-0001", vecSess, userMessage(), userParts()); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveMessage(assistantMessage("msg_test_assistant_1", "stop", vectorTime.assistantComplete)); err != nil {
				t.Fatal(err)
			}
			for _, p := range assistantCompleteParts() {
				if err := s.SavePart(p); err != nil {
					t.Fatal(err)
				}
			}
			first := baseModelUsage()
			first.Status = "error"
			first.ErrorType = strp("rate_limited")
			first.ErrorMessage = strp("Provider rate limited the model request.")
			first.Retryable = true
			first.RetryCount = 1
			first.CompletedAt = i64p(vectorTime.firstToken)
			if err := s.RecordModelUsage(first); err != nil {
				t.Fatal(err)
			}
			second := baseModelUsage()
			second.ID = "usage_model_main_turn_msg_test_assistant_1_1"
			second.AttemptIndex = 1
			second.Retryable = true
			second.RetryCount = 1
			if err := s.RecordModelUsage(second); err != nil {
				t.Fatal(err)
			}
			if err := s.UpsertTurnUsage(baseTurnUsage()); err != nil {
				t.Fatal(err)
			}
		}
	case "denied":
		return func(t *testing.T, s *store.SessionStore) {
			createSession(t, s)
			sessionEntries(t, s)
			admitInput(t, s)
			if err := s.PromoteInput("queue_cmd-0001", vecSess, userMessage(), userParts()); err != nil {
				t.Fatal(err)
			}
			toolMsg := "msg_test_assistant_denied_1"
			if err := s.SaveMessage(assistantMessage(toolMsg, "tool-calls", vectorTime.toolComplete)); err != nil {
				t.Fatal(err)
			}
			errText := "The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). STOP what you are doing and wait for the user to tell you how to proceed."
			if err := s.SavePart(sessionstore.Part{
				ID: "part_test_denied_step_start_1", MessageID: toolMsg, SessionID: vecSess, Type: "step-start", TimeCreated: i64p(vectorNow),
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.SavePart(sessionstore.Part{
				ID: "part_test_denied_write_1", MessageID: toolMsg, SessionID: vecSess, Type: "tool",
				CallID: strp("call_w1"), DeclarationIndex: intp(0), Tool: strp("Write"),
				State: json.RawMessage(`{"status":"error","input":{"file_path":"/work/proj/notes.txt","content":"alpha\nbeta\ngamma\n"},` +
					`"error":` + mustQuote(errText) + `,"metadata":{},` +
					`"time":{"start":` + itoa(vectorTime.toolComplete) + `,"end":` + itoa(vectorTime.toolComplete) + `}}`),
			}); err != nil {
				t.Fatal(err)
			}
			denied := sessionstore.ToolUsage{
				ID: "usage_tool_" + vecSess + "_call_w1", SessionID: vecSess, TurnID: strp(vecTurn), TraceID: strp(vecTrace),
				ToolCallID: "call_w1", ToolName: "Write", SideEffectScope: strp("workspace"),
				ReadOnly: boolp(false), Destructive: boolp(false), ApprovalStatus: strp("none"), Status: "error",
				StartedAt: vectorTime.toolStart, FirstOutputAt: i64p(vectorTime.toolFirstOutput), CompletedAt: i64p(vectorTime.toolComplete),
				DurationMs: i64p(0), TimeToFirstOutputMs: i64p(0), OutputBytes: 0,
				ErrorType: strp("permission_denied"), ErrorMessage: strp(errText),
			}
			if err := s.UpsertToolUsage(denied); err != nil {
				t.Fatal(err)
			}
		}
	case "admit-settle-cancelled":
		return func(t *testing.T, s *store.SessionStore) {
			createSession(t, s)
			sessionEntries(t, s)
			admitInput(t, s)
			if err := s.SettleInput("queue_cmd-0001", vecSess, "cancelled", "user_cancelled"); err != nil {
				t.Fatal(err)
			}
		}
	case "queue-edit":
		return func(t *testing.T, s *store.SessionStore) {
			createSession(t, s)
			sessionEntries(t, s)
			admitInput(t, s)
			intent := inputIntentWithText("Edited hello", 2)
			if err := s.UpdateInputs(vecSess, []sessionstore.SessionInputUpdate{
				{ID: "queue_cmd-0001", Text: strp("Edited hello"), QueuePosition: intp(2), Intent: intent},
			}); err != nil {
				t.Fatal(err)
			}
		}
	default:
		return nil
	}
}

func mustQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
