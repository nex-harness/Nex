package taskindex

// Internal tests for the two code-level migrations. They run the real functions
// (not a reimplementation) against the row-level output recorded from the Node
// implementation by scripts/go-rewrite/crosscheck-taskindex-node.mts.

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func codeVectorDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "testdata", "taskindex", "migrations")
}

type selectionRow struct {
	AutomationID   string  `json:"automation_id"`
	ModelSelection *string `json:"model_selection"`
}

func nodeSelectionVectors(t *testing.T, name string) []selectionRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(codeVectorDir(t), name))
	if err != nil {
		t.Skipf("no Node vector file %s: %v", name, err)
	}
	var out []selectionRow
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// adoptConn applies 0001 alone on a fresh connection, so a test can seed rows in
// the pre-0002 shape and then run the conversion exactly as the runner would.
func adoptConn(t *testing.T) *sql.Conn {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := conn.ExecContext(context.Background(), "begin immediate"); err != nil {
		t.Fatal(err)
	}
	if err := adoptSchema(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "commit"); err != nil {
		t.Fatal(err)
	}
	return conn
}

// TestProviderSelectionMatchesNode: the frozen 0002 decoding — custom: builtin
// triple, percent escapes, provider/model$level, the glm/nex execution-backend
// exclusion, undecidable values left NULL, and the trailing "no explicit intent"
// pass that writes the JSON null default — must land on Node's rows exactly.
func TestProviderSelectionMatchesNode(t *testing.T) {
	want := nodeSelectionVectors(t, "node-0002-provider-selection.rows.json")
	if len(want) == 0 {
		t.Skip("no 0002 vectors recorded")
	}
	conn := adoptConn(t)
	ctx := context.Background()
	insert := `INSERT INTO automations (automation_id,title,cron_expr,prompt,workspace_key,workspace_path,
      created_at,updated_at,model,provider,thought_level) VALUES (?, 't', '* * * * *', 'p', '/w', '/w', 1, 1, ?, ?, ?)`
	for _, row := range legacySeedRows() {
		if _, err := conn.ExecContext(ctx, insert, row.id, row.model, row.provider, row.thought); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.ExecContext(ctx, "begin immediate"); err != nil {
		t.Fatal(err)
	}
	if err := importLegacyAutomationSelections(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "commit"); err != nil {
		t.Fatal(err)
	}
	got := readSelectionRows(t, conn)
	if len(got) != len(want) {
		t.Fatalf("row count: go %d, node %d", len(got), len(want))
	}
	for i := range want {
		if got[i].AutomationID != want[i].AutomationID {
			t.Fatalf("row %d id: go %q, node %q", i, got[i].AutomationID, want[i].AutomationID)
		}
		if got[i].ModelSelection == nil != (want[i].ModelSelection == nil) {
			t.Fatalf("row %s: NULL-ness differs (go %v, node %v)", got[i].AutomationID, got[i].ModelSelection, want[i].ModelSelection)
		}
		if got[i].ModelSelection != nil && *got[i].ModelSelection != *want[i].ModelSelection {
			t.Fatalf("row %s:\n go:   %s\n node: %s", got[i].AutomationID, *got[i].ModelSelection, *want[i].ModelSelection)
		}
	}
}

type legacySeed struct {
	id       string
	model    any
	provider any
	thought  any
}

// legacySeedRows mirrors LEGACY_INPUTS in crosscheck-taskindex-node.mts.
func legacySeedRows() []legacySeed {
	return []legacySeed{
		{"a1", "custom:builtin:zai:GLM-5.3", "glm", nil},
		{"a2", "openai/gpt-4o$high", "openai", nil},
		{"a3", "glm-4.7", "glm", nil},
		{"a4", nil, nil, nil},
		{"a5", "  ", "openai", "medium"},
		{"a6", "custom:my%20prov:my%3Amodel", "other", "low"},
		{"a7", "custom:noseparator", "other", nil},
		{"a8", "plain-model", "builtin:zai", nil},
		{"a9", "prov/model$", "openai", nil},
		{"a10", "custom:builtin:bigmodel-start-plan:GLM-5.1", "other", nil},
		{"a11", "anthropic/claude-x$", "nex", nil},
	}
}

func readSelectionRows(t *testing.T, conn *sql.Conn) []selectionRow {
	t.Helper()
	rows, err := conn.QueryContext(context.Background(),
		"select automation_id, model_selection from automations order by automation_id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []selectionRow
	for rows.Next() {
		var r selectionRow
		if err := rows.Scan(&r.AutomationID, &r.ModelSelection); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// TestOfficialGlmRenameMatchesNode: 0003 only renames the model inside an
// official Zhipu selection, case-insensitively, and leaves everything else —
// foreign providers, malformed JSON, the JSON null default — untouched.
func TestOfficialGlmRenameMatchesNode(t *testing.T) {
	want := nodeSelectionVectors(t, "node-0003-official-glm.rows.json")
	if len(want) == 0 {
		t.Skip("no 0003 vectors recorded")
	}
	conn := adoptConn(t)
	ctx := context.Background()
	insert := `INSERT INTO automations (automation_id,title,cron_expr,prompt,workspace_key,workspace_path,
      created_at,updated_at,model_selection) VALUES (?, 't', '* * * * *', 'p', '/w', '/w', 1, 1, ?)`
	seeds := [][2]string{
		{"g1", `{"providerId":"account:zai-start-plan","modelId":"glm-5.3"}`},
		{"g2", `{"providerId":"account:bigmodel-individual-coding-plan","modelId":"GLM-4.6V"}`},
		{"g3", `{"providerId":"openai","modelId":"glm-5.3"}`},
		{"g4", `{"providerId":"account:zai-start-plan","modelId":"glm-4.7-flashx"}`},
		{"g5", "not json"},
		{"g6", `{"providerId":"account:bigmodel-team-coding-plan","modelId":"glm-4-flash-250414"}`},
		{"g7", "null"},
	}
	for _, row := range seeds {
		if _, err := conn.ExecContext(ctx, insert, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	list, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	var renameSQL string
	for _, m := range list {
		if m.ID == "0003_official_glm_selection" {
			renameSQL = m.sql
		}
	}
	if renameSQL == "" {
		t.Fatal("0003 SQL not embedded")
	}
	if _, err := conn.ExecContext(ctx, "begin immediate"); err != nil {
		t.Fatal(err)
	}
	if err := execScript(ctx, conn, renameSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "commit"); err != nil {
		t.Fatal(err)
	}
	got := readSelectionRows(t, conn)
	if len(got) != len(want) {
		t.Fatalf("row count: go %d, node %d", len(got), len(want))
	}
	for i := range want {
		if got[i].AutomationID != want[i].AutomationID {
			t.Fatalf("row %d id: go %q, node %q", i, got[i].AutomationID, want[i].AutomationID)
		}
		if *got[i].ModelSelection != *want[i].ModelSelection {
			t.Fatalf("row %s:\n go:   %s\n node: %s", got[i].AutomationID, *got[i].ModelSelection, *want[i].ModelSelection)
		}
	}
}

// TestAdoptSchemaIsIdempotent: running 0001 a second time on an already-adopted
// database must succeed without altering anything — CREATE TABLE IF NOT EXISTS
// skips, every frozen column is present, and no backfill re-runs.
func TestAdoptSchemaIsIdempotent(t *testing.T) {
	conn := adoptConn(t)
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, `INSERT INTO automations (automation_id,title,cron_expr,prompt,
      workspace_key,workspace_path,created_at,updated_at,run_count) VALUES ('a1','t','* * * * *','p','/w','/w',1,1,7)`); err != nil {
		t.Fatal(err)
	}
	if err := adoptSchema(ctx, conn); err != nil {
		t.Fatalf("second adopt: %v", err)
	}
	var scheduled int
	if err := conn.QueryRowContext(ctx, "select scheduled_run_count from automations where automation_id='a1'").Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	if scheduled != 0 {
		t.Fatalf("scheduled_run_count = %d; the backfill must run only when the column is added", scheduled)
	}
}

// TestAdoptSchemaBackfillsScheduledRunCount: on a database that predates the
// column, adding it must copy run_count — otherwise every historical automation
// reports zero scheduled runs.
func TestAdoptSchemaBackfillsScheduledRunCount(t *testing.T) {
	db, err := sql.Open("sqlite", "file:backfill?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	// The frozen AUTOMATION_SCHEMA minus every column migration 0001 adds — this
	// is the shape a pre-0001 database has.
	if _, err := db.ExecContext(ctx, `CREATE TABLE automations (
      automation_id TEXT PRIMARY KEY,
      title TEXT NOT NULL DEFAULT '',
      cron_expr TEXT NOT NULL,
      prompt TEXT NOT NULL,
      model TEXT,
      provider TEXT,
      workspace_key TEXT NOT NULL,
      workspace_path TEXT NOT NULL,
      workspace_identity TEXT,
      location_kind TEXT NOT NULL DEFAULT 'local',
      recurring INTEGER NOT NULL DEFAULT 1,
      max_runs INTEGER,
      run_count INTEGER NOT NULL DEFAULT 0,
      enabled INTEGER NOT NULL DEFAULT 1,
      lifecycle_status TEXT NOT NULL DEFAULT 'active',
      next_run_at INTEGER,
      last_run_at INTEGER,
      running INTEGER NOT NULL DEFAULT 0,
      claimed_at INTEGER,
      dispatch_status TEXT NOT NULL DEFAULT 'idle',
      dispatch_attempts INTEGER NOT NULL DEFAULT 0,
      retry_at INTEGER,
      last_error TEXT,
      created_at INTEGER NOT NULL,
      updated_at INTEGER NOT NULL
    )`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automations (automation_id,title,cron_expr,prompt,
      workspace_key,workspace_path,created_at,updated_at,run_count) VALUES ('a1','t','* * * * *','p','/w','/w',1,1,7)`); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "begin immediate"); err != nil {
		t.Fatal(err)
	}
	if err := adoptSchema(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "commit"); err != nil {
		t.Fatal(err)
	}
	var scheduled int
	if err := conn.QueryRowContext(ctx, "select scheduled_run_count from automations where automation_id='a1'").Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	if scheduled != 7 {
		t.Fatalf("scheduled_run_count = %d, want the run_count backfill 7", scheduled)
	}
}

// TestAdoptSchemaKeepsDuplicateBindings: a legacy database that already holds
// duplicate active off-peak bindings must not fail the whole migration; the
// unique index is simply not created (the frozen duplicate-retention policy).
// The seed drops the index first, because a database reaching this branch is by
// definition one where the index was never created.
func TestAdoptSchemaKeepsDuplicateBindings(t *testing.T) {
	conn := adoptConn(t)
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, "drop index if exists idx_off_peak_bound_active"); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO off_peak_tasks (off_peak_task_id,server_ticket_id,title,prompt,permission_mode,
      workspace_key,workspace_path,status,queued_at,created_at,updated_at) VALUES (?,NULL,'','p','default','/w','/w','running',1,1,1)`
	for _, id := range []string{"o1", "o2"} {
		if _, err := conn.ExecContext(ctx, insert, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.ExecContext(ctx, "update off_peak_tasks set session_id='same'"); err != nil {
		t.Fatal(err)
	}
	if err := adoptSchema(ctx, conn); err != nil {
		t.Fatalf("adopt with duplicate bindings: %v", err)
	}
	var n int
	if err := conn.QueryRowContext(ctx,
		"select count(*) from sqlite_master where type='index' and name='idx_off_peak_bound_active'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("the unique binding index must not be created over duplicate bindings")
	}
}
