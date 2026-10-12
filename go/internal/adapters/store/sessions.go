package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/tylinux/nex/go/internal/sessionstore"
)

// sqliteClock is the default wall clock. Tests inject a fixed clock through
// Options.Clock so that Date.now()-derived columns are reproducible.
type sqliteClock struct{}

func (sqliteClock) Now() int64 { return time.Now().UnixMilli() }

// SessionStoreOptions configures the session-store adapter.
type SessionStoreOptions struct {
	Clock sessionstore.Clock
}

// SessionStore writes the session tables exactly as the Node agent does.
type SessionStore struct {
	db    *sql.DB
	clock sessionstore.Clock
}

// NewSessionStore wraps an already-migrated *sql.DB (from Open).
func NewSessionStore(db *sql.DB, options SessionStoreOptions) *SessionStore {
	clock := options.Clock
	if clock == nil {
		clock = sqliteClock{}
	}
	return &SessionStore{db: db, clock: clock}
}

func (s *SessionStore) now() int64 { return s.clock.Now() }

// DB exposes the underlying handle for read-only cross-checks and tests.
func (s *SessionStore) DB() *sql.DB { return s.db }

// beginTx starts an immediate transaction, matching the Node repositories which
// use `begin immediate` for multi-statement writes.
func (s *SessionStore) beginTx() (*sql.Tx, error) {
	return s.db.Begin()
}

// nullString converts an optional string to a nullable SQL value.
func nullString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullBool(p *bool) any {
	if p == nil {
		return nil
	}
	if *p {
		return int64(1)
	}
	return int64(0)
}

// boolInt renders a bool the way the Node repositories do (0/1).
func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

// nullableBlob returns the raw bytes or nil, so an absent JSON column stays NULL
// rather than an empty string.
func nullableBlob(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

func clampNonNegative(v int) int64 {
	if v < 0 {
		return 0
	}
	return int64(v)
}

// mustInt64Ptr reads a nullable integer column into *int64.
func scanSessionRow(row interface{ Scan(...any) error }) (sessionstore.SessionRow, error) {
	var r sessionstore.SessionRow
	var (
		workspaceID, parentID, traceID, path, titleMessageID, shareURL *string
		summaryAdditions, summaryDeletions, summaryFiles               *int64
		summaryDiffs, revert, permission                               []byte
		timeTitleUpdated, timeCompacting, timeArchived                 *int64
	)
	if err := row.Scan(
		&r.ID, &r.ProjectID, &workspaceID, &parentID, &traceID, &r.TaskType,
		&r.Slug, &r.Directory, &path, &r.Title, &r.TitleSource, &titleMessageID,
		&r.Version, &shareURL, &summaryAdditions, &summaryDeletions, &summaryFiles,
		&summaryDiffs, &revert, &permission, &r.TimeCreated, &r.TimeUpdated,
		&timeTitleUpdated, &timeCompacting, &timeArchived,
	); err != nil {
		return r, err
	}
	r.WorkspaceID, r.ParentID, r.TraceID, r.Path = workspaceID, parentID, traceID, path
	r.TitleMessageID, r.ShareURL = titleMessageID, shareURL
	r.SummaryAdditions, r.SummaryDeletions, r.SummaryFiles = summaryAdditions, summaryDeletions, summaryFiles
	r.SummaryDiffs, r.Revert, r.Permission = summaryDiffs, revert, permission
	r.TimeTitleUpdated, r.TimeCompacting, r.TimeArchived = timeTitleUpdated, timeCompacting, timeArchived
	return r, nil
}

const sessionColumns = `id, project_id, workspace_id, parent_id, trace_id, task_type, slug, directory, path,
	title, title_source, title_message_id, version, share_url, summary_additions, summary_deletions,
	summary_files, summary_diffs, revert, permission, time_created, time_updated, time_title_updated,
	time_compacting, time_archived`

func mustGetSession(q queryer, id string) (sessionstore.SessionRow, error) {
	row, err := getSession(q, id)
	if err != nil {
		return sessionstore.SessionRow{}, err
	}
	if row == nil {
		return sessionstore.SessionRow{}, fmt.Errorf("session not found after write: %s", id)
	}
	return *row, nil
}

// queryer is satisfied by both *sql.DB and *sql.Tx.
type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}
