// Package store is the SQLite adapter for the session database. It runs the same
// frozen migrations as the Node agent (migrations/*.sql are exported byte for byte
// by scripts/go-rewrite/export-migrations.mts) so both agents can open the same file.
package store

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

//go:embed migrations/*.sql migrations/manifest.json
var migrationFiles embed.FS

// Phase names match SqliteMigrationPhase in migration-runner.ts.
const (
	PhaseChecking   = "checking"
	PhaseMigrating  = "migrating"
	PhaseCommitting = "committing"
	PhaseReady      = "ready"
	PhaseFailed     = "failed"
)

// MigrationFacts mirrors DatabaseMigrationFacts.
type MigrationFacts struct {
	Kind                   string  `json:"kind"` // initialize | upgrade | none
	ExecutedCount          int     `json:"executedCount"`
	CommittedCount         int     `json:"committedCount"`
	LastAppliedMigrationID *string `json:"-"`
	hasLast                bool
}

// MarshalJSON keeps three states apart, as the TypeScript object does: key absent
// (before the ledger is read), null (fresh database) and a migration id.
func (f MigrationFacts) MarshalJSON() ([]byte, error) {
	out := map[string]any{"kind": f.Kind, "executedCount": f.ExecutedCount, "committedCount": f.CommittedCount}
	if f.hasLast {
		out["lastAppliedMigrationId"] = f.LastAppliedMigrationID
	}
	return json.Marshal(out)
}

// Progress mirrors SqliteMigrationProgress. Pointer fields are omitted when nil
// so the JSON matches what the TypeScript side emits.
type Progress struct {
	Phase       string
	Migration   *MigrationFacts
	ElapsedMs   int64
	MigrationID string
	Completed   *int
	Total       *int
	ErrorCode   string
}

type migration struct {
	ID         string `json:"id"`
	AppVersion string `json:"appVersion"`
	Checksum   string `json:"checksum"`
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
		body, err := migrationFiles.ReadFile("migrations/" + list[i].ID + ".sql")
		if err != nil {
			return nil, err
		}
		list[i].sql = string(body)
		if got := checksum(list[i].sql); got != list[i].Checksum {
			return nil, fmt.Errorf("embedded migration %s does not match manifest checksum", list[i].ID)
		}
	}
	return list, nil
}

// checksum is sha256 of the trimmed SQL, as migrationChecksum in migration-runner.ts.
func checksum(sqlText string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sqlText)))
	return hex.EncodeToString(sum[:])
}

// ErrChecksumMismatch is returned when a migration already applied to the file
// differs from the embedded one. Historical migrations are immutable.
var ErrChecksumMismatch = errors.New("sqlite migration checksum mismatch")

// ErrDatabaseNewer is returned when the ledger contains a migration this binary
// does not know: opening it could write a schema we do not understand.
var ErrDatabaseNewer = errors.New("database was migrated by a newer agent")

// Open opens (creating if needed) the database at path and applies pending
// migrations. progress, if non-nil, receives the same sequence of states as the
// Node runner: checking, checking(with facts), migrating per pending migration,
// committing, ready.
func Open(ctx context.Context, path string, progress func(Progress) error) (*sql.DB, error) {
	list, err := Migrations()
	if err != nil {
		return nil, err
	}
	// One connection: the Node store is single-connection too, and it keeps the
	// startup transaction and pragmas on the same session.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	started := time.Now()
	emit := func(p Progress) error {
		if progress == nil {
			return nil
		}
		p.ElapsedMs = time.Since(started).Milliseconds()
		return progress(p)
	}
	fail := func(err error) (*sql.DB, error) {
		_ = emit(Progress{Phase: PhaseFailed, ErrorCode: classify(err)})
		db.Close()
		return nil, err
	}

	if err := emit(Progress{Phase: PhaseChecking}); err != nil {
		db.Close()
		return nil, err
	}
	var mode string
	if err := db.QueryRowContext(ctx, "pragma journal_mode = wal").Scan(&mode); err != nil {
		return fail(err)
	}
	facts, err := inspect(ctx, db, list)
	if err != nil {
		return fail(err)
	}
	if err := emit(Progress{Phase: PhaseChecking, Migration: facts.copy()}); err != nil {
		db.Close()
		return nil, err
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return fail(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "begin immediate"); err != nil {
		return fail(err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "rollback")
		}
	}()
	if _, err := conn.ExecContext(ctx, `create table if not exists schema_migration (
      id text primary key, checksum text not null, app_version text, time_applied integer not null
    )`); err != nil {
		return fail(err)
	}
	var last sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT id FROM schema_migration ORDER BY id DESC LIMIT 1").Scan(&last); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fail(err)
	}
	facts.hasLast = true
	if last.Valid {
		facts.LastAppliedMigrationID = &last.String
	} else {
		facts.LastAppliedMigrationID = nil
	}
	if err := ensureKnown(ctx, conn, list); err != nil {
		return fail(err)
	}

	completed := 0
	for _, m := range list {
		var applied sql.NullString
		err := conn.QueryRowContext(ctx, "select checksum from schema_migration where id = ?", m.ID).Scan(&applied)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fail(err)
		}
		if applied.Valid {
			if applied.String != m.Checksum {
				return fail(fmt.Errorf("%w: %s", ErrChecksumMismatch, m.ID))
			}
			completed++
			continue
		}
		if facts.Kind == "none" {
			facts.Kind = "upgrade"
		}
		total := len(list)
		c := completed
		if err := emit(Progress{Phase: PhaseMigrating, Migration: facts.copy(), MigrationID: m.ID, Completed: &c, Total: &total}); err != nil {
			return fail(err)
		}
		if _, err := conn.ExecContext(ctx, m.sql); err != nil {
			return fail(fmt.Errorf("migration %s: %w", m.ID, err))
		}
		facts.ExecutedCount++
		if _, err := conn.ExecContext(ctx, "insert into schema_migration (id, checksum, app_version, time_applied) values (?, ?, ?, ?)",
			m.ID, m.Checksum, m.AppVersion, time.Now().UnixMilli()); err != nil {
			return fail(err)
		}
		completed++
	}
	if err := emit(Progress{Phase: PhaseCommitting, Migration: facts.copy()}); err != nil {
		return fail(err)
	}
	if _, err := conn.ExecContext(ctx, "commit"); err != nil {
		return fail(err)
	}
	committed = true
	facts.CommittedCount = facts.ExecutedCount
	if err := emit(Progress{Phase: PhaseReady, Migration: facts.copy()}); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (f *MigrationFacts) copy() *MigrationFacts { c := *f; return &c }

// MarshalJSON renders the progress as the fields of a startup/storageState
// notification (without the attempt/database/sequence envelope, added by the caller).
func (p Progress) MarshalJSON() ([]byte, error) {
	out := map[string]any{"phase": p.Phase, "elapsedMs": p.ElapsedMs}
	if p.Migration != nil {
		out["migration"] = p.Migration
	}
	if p.MigrationID != "" {
		out["migrationId"] = p.MigrationID
	}
	if p.Completed != nil {
		out["completed"] = *p.Completed
	}
	if p.Total != nil {
		out["total"] = *p.Total
	}
	if p.ErrorCode != "" {
		out["errorCode"] = p.ErrorCode
	}
	return json.Marshal(out)
}

// inspect is inspectMigrationKind: no ledger table or no data tables means a fresh
// database ("initialize"); pending migrations on an existing one is "upgrade".
func inspect(ctx context.Context, db *sql.DB, list []migration) (*MigrationFacts, error) {
	var hasLedger int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migration'").Scan(&hasLedger); err != nil {
		return nil, err
	}
	pending := false
	for _, m := range list {
		if hasLedger == 0 {
			pending = true
			break
		}
		var applied sql.NullString
		err := db.QueryRowContext(ctx, "select checksum from schema_migration where id = ?", m.ID).Scan(&applied)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if !applied.Valid {
			pending = true
		} else if applied.String != m.Checksum {
			return nil, fmt.Errorf("%w: %s", ErrChecksumMismatch, m.ID)
		}
	}
	f := &MigrationFacts{Kind: "none"}
	if pending {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT IN ('schema_migration', 'sqlite_sequence')").Scan(&n); err != nil {
			return nil, err
		}
		f.Kind = "initialize"
		if n > 0 {
			f.Kind = "upgrade"
		}
	}
	return f, nil
}

// ensureKnown refuses a database whose ledger lists a migration we do not have.
func ensureKnown(ctx context.Context, conn *sql.Conn, list []migration) error {
	known := map[string]bool{}
	for _, m := range list {
		known[m.ID] = true
	}
	rows, err := conn.QueryContext(ctx, "select id from schema_migration")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if !known[id] {
			return fmt.Errorf("%w: %s", ErrDatabaseNewer, id)
		}
	}
	return rows.Err()
}

// classify maps an error to the DatabaseStartupErrorCode the host understands.
// Only a small subset is derivable from the modernc driver's text; everything
// else is "sql_failed", which is what the TypeScript code uses as its fallback.
func classify(err error) string {
	switch {
	case errors.Is(err, ErrChecksumMismatch):
		return "checksum_mismatch"
	case strings.Contains(err.Error(), "database is locked"):
		return "lock_timeout"
	case strings.Contains(err.Error(), "disk I/O error"):
		return "io_error"
	case strings.Contains(err.Error(), "database or disk is full"):
		return "storage_full"
	case strings.Contains(err.Error(), "malformed") || strings.Contains(err.Error(), "not a database"):
		return "corrupt"
	}
	return "sql_failed"
}
