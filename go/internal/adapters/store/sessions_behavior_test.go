package store_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tylinux/nex/go/internal/adapters/store"
	"github.com/tylinux/nex/go/internal/sessionstore"
)

// TestComputedTotalTokensFallback covers the derivation used when a provider
// returns no explicit computed total: input side = inputTokens, or cache when
// input is zero, plus output.
func TestComputedTotalTokensFallback(t *testing.T) {
	s := openStore(t)
	createSession(t, s)
	// inputTokens > 0: total = input + output.
	u := baseModelUsage()
	u.ComputedTotalTokens = nil
	u.ProviderTotalTokens = nil
	u.InputTokens = 30
	u.OutputTokens = 5
	if err := s.RecordModelUsage(u); err != nil {
		t.Fatal(err)
	}
	// inputTokens == 0: input side falls back to cache creation + cache read.
	u2 := baseModelUsage()
	u2.ID = u.ID + "_cache"
	u2.ComputedTotalTokens = nil
	u2.ProviderTotalTokens = nil
	u2.InputTokens = 0
	u2.OutputTokens = 7
	u2.CacheCreationInputTokens = 10
	u2.CacheReadInputTokens = 3
	if err := s.RecordModelUsage(u2); err != nil {
		t.Fatal(err)
	}
	rows := dumpTable(t, s, "model_usage")
	byID := map[string]map[string]any{}
	for _, r := range rows {
		byID[r["id"].(string)] = r
	}
	if got := byID[u.ID]["computed_total_tokens"].(float64); got != 35 {
		t.Fatalf("computed total want 35 got %v", got)
	}
	if got := byID[u2.ID]["computed_total_tokens"].(float64); got != 20 {
		t.Fatalf("computed total (cache) want 20 got %v", got)
	}
}

// TestMessageReSaveKeepsSequence mirrors the on-conflict rule: re-saving the
// same message id in the same session keeps its original sequence.
func TestMessageReSaveKeepsSequence(t *testing.T) {
	s := openStore(t)
	createSession(t, s)
	if err := s.SaveMessage(userMessage()); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMessage(assistantMessage("msg_test_assistant_1", "stop", vectorTime.assistantComplete)); err != nil {
		t.Fatal(err)
	}
	// Re-save the first message; its sequence must stay 0, not jump to 2.
	if err := s.SaveMessage(userMessage()); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.ListMessages(vecSess)
	if err != nil {
		t.Fatal(err)
	}
	var userSeq int64 = -1
	for _, m := range msgs {
		if m.MessageID == vecUser {
			userSeq = *m.Sequence
		}
	}
	if userSeq != 0 {
		t.Fatalf("user message sequence drift on re-save: %d", userSeq)
	}
}

// TestListMessagesResumeShape checks the read side used for resume: messages in

// sequence order with their parts grouped, decoded back to the stored JSON.
func TestListMessagesResumeShape(t *testing.T) {
	s := openStore(t)
	createSession(t, s)
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
	got, err := s.ListMessages(vecSess)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 messages, got %d", len(got))
	}
	if got[0].MessageID != vecUser || got[1].MessageID != "msg_test_assistant_1" {
		t.Fatalf("wrong message order: %s, %s", got[0].MessageID, got[1].MessageID)
	}
	if *got[0].Sequence != 0 || *got[1].Sequence != 1 {
		t.Fatalf("wrong sequences: %v %v", *got[0].Sequence, *got[1].Sequence)
	}
	if len(got[0].Parts) != 1 || got[0].Parts[0].ID != "part_test_user_text_1" {
		t.Fatalf("user parts wrong: %+v", got[0].Parts)
	}
	if len(got[1].Parts) != 3 {
		t.Fatalf("assistant parts want 3 got %d", len(got[1].Parts))
	}
	// Part data must decode back to the exact JSON the turn wrote.
	var textPart struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(got[1].Parts[1].Data, &textPart); err != nil {
		t.Fatal(err)
	}
	if textPart.Type != "text" || textPart.Text != "Hello from the mock model." {
		t.Fatalf("text part decoded wrong: %+v", textPart)
	}
}

// TestReopenPersistsAcrossConnections closes the store and reopens the same file.
func TestReopenPersistsAcrossConnections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db.sqlite")
	db, err := store.Open(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := store.NewSessionStore(db, store.SessionStoreOptions{Clock: fixedClock{vectorNow}})
	scenarioRunner("text-turn")(t, s)
	db.Close()

	reopened, err := store.Open(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s2 := store.NewSessionStore(reopened, store.SessionStoreOptions{Clock: fixedClock{vectorNow}})
	row, err := s2.GetSession(vecSess)
	if err != nil || row == nil {
		t.Fatalf("get session after reopen: %v", err)
	}
	if row.Title != "Say hello" || string(row.Permission) != `{"mode":"build"}` {
		t.Fatalf("session not persisted: %+v", row)
	}
	msgs, err := s2.ListMessages(vecSess)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("messages after reopen: %v n=%d", err, len(msgs))
	}
}

// TestInputHistoryIDShape pins the one excluded column's format.
func TestInputHistoryIDShape(t *testing.T) {
	s := openStore(t)
	createSession(t, s)
	id, err := s.RecordInputHistory(sessionstore.InputHistory{
		ProjectID: vecProj, Text: "hello", Kind: "prompt", TimeCreated: i64p(vectorNow),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "input_") || strings.Count(id, "_") < 2 {
		t.Fatalf("input history id shape wrong: %q", id)
	}
	// A duplicate of the most recent text is skipped.
	dup, err := s.RecordInputHistory(sessionstore.InputHistory{
		ProjectID: vecProj, Text: "hello", Kind: "prompt", TimeCreated: i64p(vectorNow),
	})
	if err != nil {
		t.Fatal(err)
	}
	if dup != "" {
		t.Fatalf("duplicate input history should be skipped, got %q", dup)
	}
}

// TestAdmitIsIdempotent checks that re-admitting the same id keeps the sequence.
func TestAdmitIsIdempotent(t *testing.T) {
	s := openStore(t)
	createSession(t, s)
	admitInput(t, s)
	// Re-admit with a different kind/payload: sequence stays 0.
	if err := s.AdmitInput(sessionstore.SessionInput{
		ID: "queue_cmd-0001", SessionID: vecSess, Kind: "sendText", Delivery: "guide",
		Payload: []byte(`{"text":"changed"}`),
	}); err != nil {
		t.Fatal(err)
	}
	rows := dumpTable(t, s, "session_input")
	if len(rows) != 1 {
		t.Fatalf("want 1 row got %d", len(rows))
	}
	if rows[0]["admitted_sequence"].(float64) != 0 {
		t.Fatalf("sequence changed on re-admit: %v", rows[0]["admitted_sequence"])
	}
	if rows[0]["delivery"] != "guide" {
		t.Fatalf("delivery not updated: %v", rows[0]["delivery"])
	}
	if rows[0]["status"] != "admitted" {
		t.Fatalf("status changed: %v", rows[0]["status"])
	}
}

// TestSettleOnlyClosesAdmitted mirrors the ledger rule: a late settle must not
// roll a promoted input back.
func TestSettleOnlyClosesAdmitted(t *testing.T) {
	s := openStore(t)
	createSession(t, s)
	admitInput(t, s)
	if err := s.PromoteInput("queue_cmd-0001", vecSess, userMessage(), userParts()); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleInput("queue_cmd-0001", vecSess, "cancelled", "late"); err != nil {
		t.Fatal(err)
	}
	rows := dumpTable(t, s, "session_input")
	if rows[0]["status"] != "promoted" {
		t.Fatalf("late settle rolled back promoted input: %v", rows[0]["status"])
	}
}

// TestPromoteIsAtomic checks the transaction: a bad part makes the whole
// promotion (message + input status) roll back.
func TestPromoteIsAtomic(t *testing.T) {
	s := openStore(t)
	createSession(t, s)
	admitInput(t, s)
	// A part referencing a non-existent part type still encodes, so force an
	// error with a message missing its required session foreign key.
	bad := userMessage()
	bad.SessionID = "sess_missing"
	if err := s.PromoteInput("queue_cmd-0001", vecSess, bad, userParts()); err == nil {
		t.Fatal("expected promotion to fail on foreign key violation")
	}
	if rows := dumpTable(t, s, "message"); len(rows) != 0 {
		t.Fatalf("message leaked out of failed transaction: %d rows", len(rows))
	}
	inputs := dumpTable(t, s, "session_input")
	if inputs[0]["status"] != "admitted" {
		t.Fatalf("input status changed despite rollback: %v", inputs[0]["status"])
	}
}

// TestUsagePrunesOldRows checks the 30-day retention applied on write.
func TestUsagePrunesOldRows(t *testing.T) {
	s := openStore(t)
	createSession(t, s)
	old := baseModelUsage()
	old.StartedAt = vectorNow - store.UsageRetentionMs - 1
	if err := s.RecordModelUsage(old); err != nil {
		t.Fatal(err)
	}
	if rows := dumpTable(t, s, "model_usage"); len(rows) != 0 {
		t.Fatalf("old usage row not pruned: %d", len(rows))
	}
	fresh := baseModelUsage()
	if err := s.RecordModelUsage(fresh); err != nil {
		t.Fatal(err)
	}
	if rows := dumpTable(t, s, "model_usage"); len(rows) != 1 {
		t.Fatalf("fresh usage row pruned: %d", len(rows))
	}
}

// TestToolUsageStatusNotDowngraded mirrors the upsert rule that a terminal row
// is never overwritten by a later 'running' event.
func TestToolUsageStatusNotDowngraded(t *testing.T) {
	s := openStore(t)
	createSession(t, s)
	done := baseToolUsage()
	if err := s.UpsertToolUsage(done); err != nil {
		t.Fatal(err)
	}
	running := baseToolUsage()
	running.Status = "running"
	running.OutputBytes = 0
	if err := s.UpsertToolUsage(running); err != nil {
		t.Fatal(err)
	}
	rows := dumpTable(t, s, "tool_usage")
	if rows[0]["status"] != "completed" {
		t.Fatalf("terminal tool status downgraded: %v", rows[0]["status"])
	}
	if rows[0]["output_bytes"].(float64) != 13 {
		t.Fatalf("output_bytes should keep max: %v", rows[0]["output_bytes"])
	}
}

var _ = store.SessionStoreOptions{}
