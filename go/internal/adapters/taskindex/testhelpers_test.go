package taskindex_test

// testhelpers_test.go — the small raw-SQL escape hatches the repo tests need to
// simulate damage a real writer would never produce (a stale identity column, a
// user's manual grouping).

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

type rawDB struct {
	db *sql.DB
}

func (r rawDB) close() { _ = r.db.Close() }

func (r rawDB) exec(query string, args ...any) error {
	_, err := r.db.ExecContext(context.Background(), query, args...)
	return err
}

func openRaw(path string) (rawDB, error) {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return rawDB{}, err
	}
	// The repo holds its own connection with WAL; opening raw with busy_timeout
	// avoids SQLITE_BUSY while a repo write is in flight.
	db.Exec("pragma busy_timeout = 5000")
	return rawDB{db: db}, nil
}

// groupOf reads the membership group a task currently belongs to.
func groupOf(t *testing.T, path, taskID string) string {
	t.Helper()
	raw, err := openRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.close()
	var group string
	err = raw.db.QueryRowContext(context.Background(),
		"SELECT group_id FROM task_group_members WHERE task_id = ?", taskID).Scan(&group)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return group
}
