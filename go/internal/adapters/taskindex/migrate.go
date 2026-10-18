// The SQLite adapter for tasks-index.sqlite: the frozen migrations, the ledger
// and the repository that owns the tasks table.
//
// The three migrations are frozen exactly as packages/services/src/session/
// tasksDatabase/migrations.ts declares them: the checksum is sha256 over the
// Node-produced JSON.stringify(checksumInput) bytes, exported verbatim by
// scripts/go-rewrite/export-tasks-migrations.mts, so Node and Go can open the same
// file during the strangler transition. 0001 (adopt schema + ALTER/backfill) and
// 0002 (legacy automation selection import) are Go functions in adopt.go, the
// same approach Phase 1 took for the code-level session-store migrations; 0003 is
// plain SQL, executed verbatim.
package taskindex

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.json migrations/*.sql
var migrationFiles embed.FS

// Phase names match TasksStoragePhase in tasksDatabase/startup.ts.
const (
	PhaseChecking       = "checking"
	PhaseWaitingForLock = "waiting_for_lock"
	PhaseMigrating      = "migrating"
	PhaseMaintaining    = "maintaining"
	PhaseCommitting     = "committing"
	PhaseReady          = "ready"
	PhaseFailed         = "failed"
	ledgerTable         = "tasks_schema_migration"
	activePredicate     = "session_id IS NOT NULL AND status NOT IN ('completed','failed','cancelled')"
)

// frozenColumn is one entry of the `columns` constant in migrations.ts. The
// historical column declarations are frozen: substituting a live repo/schema
// would change the checksum of an already-applied migration.
type frozenColumn struct {
	Table      string
	Column     string
	Definition string
}

// frozenColumns is the `columns` constant in migrations.ts, in order. The order
// matters: it is part of the 0001 checksum input and of the ALTER sequence.
var frozenColumns = []frozenColumn{
	{"tasks", "title_overridden", "INTEGER NOT NULL DEFAULT 0"},
	{"tasks", "last_unread_at", "INTEGER NOT NULL DEFAULT 0"},
	{"tasks", "searchable_text", "TEXT NOT NULL DEFAULT ''"},
	{"tasks", "cron_automation_id", "TEXT"},
	{"tasks", "off_peak_task_id", "TEXT"},
	{"automations", "target_task_id", "TEXT"},
	{"automations", "bot_delivery_target", "TEXT"},
	{"automations", "mode", "TEXT"},
	{"automations", "end_at", "INTEGER"},
	{"automations", "schedule_rule", "TEXT"},
	{"automations", "schedule_edited_by_user", "INTEGER NOT NULL DEFAULT 0"},
	{"automations", "thought_level", "TEXT"},
	{"automations", "model_selection", "TEXT"},
	{"automations", "scheduled_run_count", "INTEGER NOT NULL DEFAULT 0"},
	{"automation_runs", "model_selection", "TEXT"},
	{"off_peak_tasks", "thought_level", "TEXT"},
	{"off_peak_tasks", "model_selection", "TEXT"},
	{"off_peak_tasks", "history_deleted_at", "INTEGER"},
}

// MigrationFacts mirrors DatabaseMigrationFacts. hasLast keeps three states
// apart, as the TypeScript object does: key absent (before the ledger is read),
// null (fresh database) and a migration id.
type MigrationFacts struct {
	Kind                   string  `json:"kind"` // initialize | upgrade | none
	ExecutedCount          int     `json:"executedCount"`
	CommittedCount         int     `json:"committedCount"`
	LastAppliedMigrationID *string `json:"-"`
	hasLast                bool
}

// MarshalJSON keeps the lastAppliedMigrationId tri-state visible to callers that
// report storage startup progress.
func (f MigrationFacts) MarshalJSON() ([]byte, error) {
	out := map[string]any{"kind": f.Kind, "executedCount": f.ExecutedCount, "committedCount": f.CommittedCount}
	if f.hasLast {
		out["lastAppliedMigrationId"] = f.LastAppliedMigrationID
	}
	return json.Marshal(out)
}

func (f *MigrationFacts) copy() *MigrationFacts { c := *f; return &c }

type migration struct {
	ID         string `json:"id"`
	Checksum   string `json:"checksum"`
	InputBytes int    `json:"inputBytes"`
	inputJSON  []byte
	sql        string
}

// Migrations returns the frozen migration list in order.
func Migrations() ([]migration, error) {
	raw, err := migrationFiles.ReadFile("migrations/manifest.json")
	if err != nil {
		return nil, err
	}
	var list []migration
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	for i := range list {
		input, err := migrationFiles.ReadFile("migrations/" + list[i].ID + ".checksum-input.json")
		if err != nil {
			return nil, err
		}
		list[i].inputJSON = input
		if got := Checksum(input); got != list[i].Checksum {
			return nil, fmt.Errorf("embedded migration %s does not match manifest checksum", list[i].ID)
		}
		if list[i].InputBytes != len(input) {
			return nil, fmt.Errorf("embedded migration %s has %d checksum-input bytes, manifest says %d", list[i].ID, len(input), list[i].InputBytes)
		}
	}
	// 0003 is plain SQL: the migration body is the sole checksum input element.
	if body, err := migrationFiles.ReadFile("migrations/0003_official_glm_selection.sql"); err == nil {
		for i := range list {
			if list[i].ID == "0003_official_glm_selection" {
				list[i].sql = string(body)
			}
		}
	}
	return list, nil
}

// SchemaFragments is the 0001 DDL, exported verbatim from schema-v1.ts and
// migrations.ts: Node's db.exec concatenates exactly these strings, so the text
// stored in sqlite_master must be identical in both engines.
type SchemaFragments struct {
	TaskIndexSchema  string
	AutomationSchema string
	OffPeakSchema    string
	Indexes          string
	BoundIndex       string
}

// Fragments loads the embedded 0001 SQL. It fails loudly rather than falling back
// to a paraphrase: a drifted copy would silently produce a different schema.
func Fragments() (SchemaFragments, error) {
	read := func(name string) (string, error) {
		raw, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	var f SchemaFragments
	var err error
	if f.TaskIndexSchema, err = read("task_index_schema.sql"); err != nil {
		return f, err
	}
	if f.AutomationSchema, err = read("automation_schema.sql"); err != nil {
		return f, err
	}
	if f.OffPeakSchema, err = read("off_peak_schema.sql"); err != nil {
		return f, err
	}
	if f.Indexes, err = read("task_index_indexes.sql"); err != nil {
		return f, err
	}
	if f.BoundIndex, err = read("bound_index.sql"); err != nil {
		return f, err
	}
	return f, nil
}

// Checksum is the sha256 of the frozen checksum input, byte for byte what
// createHash("sha256").update(JSON.stringify(checksumInput)) produces in Node.
func Checksum(inputJSON []byte) string {
	sum := sha256.Sum256(inputJSON)
	return hex.EncodeToString(sum[:])
}

var (
	// ErrChecksumMismatch is returned when a migration already applied to the
	// file differs from the embedded one. Historical migrations are immutable.
	ErrChecksumMismatch = errors.New("task database migration checksum mismatch")
	// ErrDatabaseNewer is returned when the ledger contains a migration this
	// binary does not know.
	ErrDatabaseNewer = errors.New("task database was migrated by a newer build")
	// ErrLockTimeout is returned when the write lock could not be taken before
	// the deadline (lock_timeout in startup.ts).
	ErrLockTimeout = errors.New("task storage lock wait expired")
)

// Progress mirrors the callback of prepareTasksIndexStorage: the phase plus the
// migration facts as they were at that moment (the callback never shares the
// object the runner keeps mutating).
type Progress struct {
	Phase     string
	Migration *MigrationFacts
}

// Open opens (creating if needed) the tasks index at path and applies pending
// migrations inside one library-level serial transaction, exactly as
// runTasksDatabaseMigrations does.
func Open(ctx context.Context, path string, progress func(Progress) error) (*sql.DB, error) {
	return OpenWithBusyTimeout(ctx, path, 5000, progress)
}

// OpenWithBusyTimeout is Open with an explicit busy timeout in milliseconds.
// The task index is shared by every window Host, so the caller owns the wait
// budget (TaskIndexRepo's startupBusyTimeoutMs).
func OpenWithBusyTimeout(ctx context.Context, path string, busyTimeoutMs int, progress func(Progress) error) (*sql.DB, error) {
	if busyTimeoutMs <= 0 {
		busyTimeoutMs = 5000
	}
	list, err := Migrations()
	if err != nil {
		return nil, err
	}
	// One connection: the Node side runs the migrations on a single synchronous
	// connection, and it keeps the startup transaction and pragmas on one session.
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)", path, busyTimeoutMs))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	emit := func(p Progress) error {
		if progress == nil {
			return nil
		}
		return progress(p)
	}
	fail := func(err error) (*sql.DB, error) {
		_ = emit(Progress{Phase: PhaseFailed})
		db.Close()
		return nil, err
	}
	if err := emit(Progress{Phase: PhaseChecking}); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "pragma journal_mode = wal"); err != nil {
		return fail(err)
	}
	if _, err := db.ExecContext(ctx, "pragma synchronous = NORMAL"); err != nil {
		return fail(err)
	}
	facts, err := inspect(ctx, db, list)
	if err != nil {
		return fail(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fail(err)
	}
	defer conn.Close()
	if err := runOnConn(ctx, conn, list, facts, emit); err != nil {
		_ = emit(Progress{Phase: PhaseFailed})
		db.Close()
		return nil, err
	}
	return db, nil
}

// runOnConn is runTasksDatabaseMigrations with transactionOpen: the caller has
// already taken the write lock. Progress is reported per pending migration and
// once before the commit, never after it (a later close failure must not be
// reported as an uncommitted migration).
func runOnConn(ctx context.Context, conn *sql.Conn, list []migration, facts *MigrationFacts, emit func(Progress) error) error {
	if _, err := conn.ExecContext(ctx, "begin immediate"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "rollback")
		}
	}()
	// 账本表的 DDL 文本与 Node 的 migrations.ts 逐字节一致（含大小写）：它会原样落进
	// sqlite_master.sql，跨引擎 schema 比对依赖这一点。
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS tasks_schema_migration (
      id TEXT PRIMARY KEY, checksum TEXT NOT NULL, time_applied INTEGER NOT NULL
    )`); err != nil {
		return err
	}
	// Collected inside the lock, before the version SQL runs: an empty ledger is
	// "none", and an unexpected id is not sent as raw telemetry.
	var baseline sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT id FROM "+ledgerTable+" ORDER BY id DESC LIMIT 1").Scan(&baseline); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	facts.hasLast = true
	if baseline.Valid {
		id := baseline.String
		facts.LastAppliedMigrationID = &id
	} else {
		facts.LastAppliedMigrationID = nil
	}
	if err := ensureKnown(ctx, conn, list); err != nil {
		return err
	}
	for _, m := range list {
		var applied sql.NullString
		err := conn.QueryRowContext(ctx, "SELECT checksum FROM "+ledgerTable+" WHERE id=?", m.ID).Scan(&applied)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if applied.Valid {
			if applied.String != m.Checksum {
				return fmt.Errorf("%w: %s", ErrChecksumMismatch, m.ID)
			}
			continue
		}
		if facts.Kind == "none" {
			facts.Kind = "upgrade"
		}
		if err := emit(Progress{Phase: PhaseMigrating, Migration: facts.copy()}); err != nil {
			return err
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return fmt.Errorf("migration %s: %w", m.ID, err)
		}
		facts.ExecutedCount++
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO "+ledgerTable+" VALUES(?,?,?)", m.ID, m.Checksum, time.Now().UnixMilli()); err != nil {
			return err
		}
	}
	if err := emit(Progress{Phase: PhaseCommitting, Migration: facts.copy()}); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "commit"); err != nil {
		return err
	}
	committed = true
	facts.CommittedCount = facts.ExecutedCount
	return nil
}

func applyMigration(ctx context.Context, conn *sql.Conn, m migration) error {
	switch m.ID {
	case "0001_adopt_task_schema":
		return adoptSchema(ctx, conn)
	case "0002_provider_selection":
		return importLegacyAutomationSelections(ctx, conn)
	default:
		// 0003 is verbatim SQL; an unknown id can only come from a ledger this
		// binary already accepted, so executing it is the frozen behaviour.
		return execScript(ctx, conn, m.sql)
	}
}

// execScript runs a possibly multi-statement script the way db.exec does.
func execScript(ctx context.Context, conn *sql.Conn, script string) error {
	if strings.TrimSpace(script) == "" {
		return nil
	}
	_, err := conn.ExecContext(ctx, script)
	return err
}
