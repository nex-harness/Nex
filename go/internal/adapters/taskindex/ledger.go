package taskindex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ledger.go — the ledger read/write half of the runner, split out of migrate.go
// only to keep each file readable. Semantics are unchanged.

// inspect is inspectTasksMigrationKind: no ledger table or no data tables means
// a fresh database ("initialize"); pending migrations on an existing one is
// "upgrade". Read-only preflight: it does not authorize execution, the runner
// re-checks every row after taking the lock.
func inspect(ctx context.Context, db *sql.DB, list []migration) (*MigrationFacts, error) {
	var hasLedger int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name='"+ledgerTable+"'").Scan(&hasLedger); err != nil {
		return nil, err
	}
	pending := false
	for _, m := range list {
		if hasLedger == 0 {
			pending = true
			break
		}
		var applied sql.NullString
		err := db.QueryRowContext(ctx, "SELECT checksum FROM "+ledgerTable+" WHERE id=?", m.ID).Scan(&applied)
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
	if !pending {
		return f, nil
	}
	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT IN ('"+ledgerTable+"', 'sqlite_sequence')").Scan(&n); err != nil {
		return nil, err
	}
	f.Kind = "initialize"
	if n > 0 {
		f.Kind = "upgrade"
	}
	return f, nil
}

// MigrationsApplied is areTasksDatabaseMigrationsApplied: the ledger must list
// every migration with the expected checksum. Startup handover only reuses a
// completed initialization; replacing or truncating the file must not fake ready.
func MigrationsApplied(ctx context.Context, db *sql.DB) (bool, error) {
	var exists int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name='"+ledgerTable+"'").Scan(&exists); err != nil {
		return false, err
	}
	if exists == 0 {
		return false, nil
	}
	list, err := Migrations()
	if err != nil {
		return false, err
	}
	for _, m := range list {
		var applied sql.NullString
		err := db.QueryRowContext(ctx, "SELECT checksum FROM "+ledgerTable+" WHERE id=?", m.ID).Scan(&applied)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if !applied.Valid {
			return false, nil
		}
		if applied.String != m.Checksum {
			return false, fmt.Errorf("%w: %s", ErrChecksumMismatch, m.ID)
		}
	}
	return true, nil
}

// ensureKnown refuses a ledger holding a migration this binary does not have.
func ensureKnown(ctx context.Context, conn *sql.Conn, list []migration) error {
	known := map[string]bool{}
	for _, m := range list {
		known[m.ID] = true
	}
	rows, err := conn.QueryContext(ctx, "SELECT id FROM "+ledgerTable)
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

// classify maps an error to the storage startup error code the host understands.
func classify(err error) string {
	switch {
	case errors.Is(err, ErrChecksumMismatch):
		return "checksum_mismatch"
	case errors.Is(err, ErrDatabaseNewer):
		return "database_newer"
	case errors.Is(err, ErrLockTimeout):
		return "lock_timeout"
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
