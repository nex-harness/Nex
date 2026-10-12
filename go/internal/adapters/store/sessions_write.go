package store

import (
	"database/sql"
	"time"

	"github.com/tylinux/nex/go/internal/sessionstore"
)

// CreateSession inserts or updates a session row, matching repositories/sessions.ts.
func (s *SessionStore) CreateSession(input sessionstore.CreateSessionInput) (sessionstore.SessionRow, error) {
	now := s.now()
	timeCreated := now
	if input.TimeCreated != nil {
		timeCreated = *input.TimeCreated
	}
	timeUpdated := timeCreated
	if input.TimeUpdated != nil {
		timeUpdated = *input.TimeUpdated
	}
	taskType := input.TaskType
	if taskType == "" {
		taskType = "interactive"
	}
	titleSource := input.TitleSource
	if titleSource == "" {
		titleSource = "first_input"
	}
	titleUpdated := any(nil)
	if titleSource != "" || input.TitleMessage != nil {
		titleUpdated = timeUpdated
	}

	_, err := s.db.Exec(`
		insert into session (
			id, project_id, workspace_id, parent_id, trace_id, task_type, slug, directory, path,
			title, title_source, title_message_id, version,
			share_url, summary_additions, summary_deletions, summary_files, summary_diffs,
			revert, permission, time_created, time_updated, time_title_updated,
			time_compacting, time_archived
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, null, null, null, null, null, ?, ?, ?, ?, null, null)
		on conflict(id) do update set
			project_id = excluded.project_id,
			workspace_id = excluded.workspace_id,
			parent_id = excluded.parent_id,
			trace_id = coalesce(session.trace_id, excluded.trace_id),
			task_type = excluded.task_type,
			slug = excluded.slug,
			directory = excluded.directory,
			path = excluded.path,
			title = excluded.title,
			title_source = excluded.title_source,
			title_message_id = excluded.title_message_id,
			version = excluded.version,
			share_url = excluded.share_url,
			permission = coalesce(excluded.permission, session.permission),
			time_title_updated = excluded.time_title_updated,
			time_updated = excluded.time_updated`,
		input.ID, input.ProjectID, nullString(input.WorkspaceID), nullString(input.ParentID),
		nullString(input.TraceID), taskType, input.Slug, input.Directory, nullString(input.Path),
		input.Title, titleSource, nullString(input.TitleMessage), input.Version,
		nil, // share_url: always null on create
		nullableBlob(input.Permission), timeCreated, timeUpdated, titleUpdated,
	)
	if err != nil {
		return sessionstore.SessionRow{}, err
	}
	return mustGetSession(s.db, input.ID)
}

// UpdateTitle applies the title branch of updateSession, which is what the turn
// loop calls after the first input (title_source first_input -> generated).
func (s *SessionStore) UpdateTitle(id, title, titleSource string, timeUpdated int64) (sessionstore.SessionRow, error) {
	current, err := s.GetSession(id)
	if err != nil {
		return sessionstore.SessionRow{}, err
	}
	if current == nil {
		return sessionstore.SessionRow{}, sql.ErrNoRows
	}
	titleChanged := title != current.Title
	nextSource := titleSource
	if nextSource == "" {
		nextSource = current.TitleSource
	}
	now := s.now()
	titleTime := any(current.TimeTitleUpdated)
	if titleChanged || titleSource != "" {
		titleTime = now
	}
	_, err = s.db.Exec(`
		update session set
			title = ?, title_source = ?,
			time_title_updated = ?,
			time_updated = max(time_updated, ?)
		where id = ?`,
		title, nextSource, titleTime, timeUpdated, id,
	)
	if err != nil {
		return sessionstore.SessionRow{}, err
	}
	return mustGetSession(s.db, id)
}

// GetSession reads and decodes a session row.
func (s *SessionStore) GetSession(id string) (*sessionstore.SessionRow, error) {
	return getSession(s.db, id)
}

func getSession(q queryer, id string) (*sessionstore.SessionRow, error) {
	row := q.QueryRow(`select `+sessionColumns+` from session where id = ?`, id)
	decoded, err := scanSessionRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &decoded, nil
}

// UpsertSessionEntry mirrors repositories/session-entries.ts. For the
// model_selection type the adapter wraps the value as {"modelSelection": ...}.
func (s *SessionStore) UpsertSessionEntry(entry sessionstore.SessionEntry) error {
	isModelSelection := entry.Type == "runtime/model_selection"
	encoded := entry.Data
	if isModelSelection {
		encoded = wrapModelSelection(entry.Data)
	}
	if len(encoded) == 0 {
		return sql.ErrNoRows
	}
	_, err := s.db.Exec(`
		insert into session_entry (id, session_id, type, time_created, time_updated, data)
		values (?, ?, ?, ?, ?, ?)
		on conflict(id) do update set
			session_id = excluded.session_id,
			type = excluded.type,
			time_updated = excluded.time_updated,
			data = case
				when ? and session_entry.type = excluded.type
					and session_entry.session_id = excluded.session_id
					and json_type(session_entry.data) = 'object'
				then json_set(session_entry.data, '$.modelSelection', json_extract(excluded.data, '$.modelSelection'))
				else excluded.data
			end`,
		entry.ID, entry.SessionID, entry.Type, entry.TimeCreated, entry.TimeUpdated, string(encoded),
		boolInt(isModelSelection),
	)
	if err != nil {
		return err
	}
	if entry.TouchSession {
		return s.touchSession(entry.SessionID, entry.TimeUpdated)
	}
	return nil
}

// touchSession mirrors touchSession: time_updated = max(time_updated, ?).
func (s *SessionStore) touchSession(id string, timeUpdated int64) error {
	_, err := s.db.Exec("update session set time_updated = max(time_updated, ?) where id = ?", timeUpdated, id)
	return err
}

func wrapModelSelection(data []byte) []byte {
	if len(data) == 0 {
		return []byte(`{"modelSelection":null}`)
	}
	out := make([]byte, 0, len(data)+20)
	out = append(out, `{"modelSelection":`...)
	out = append(out, data...)
	out = append(out, '}')
	return out
}

var _ = time.Now
