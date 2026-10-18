package taskindex

// write.go — the row writer and the group/order projections it owns.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

const insertTaskSQL = `INSERT INTO tasks (
          workspace_key, workspace_path, workspace_identity, task_id, title, task_status,
          provider, mode, model, migration_source, forked_from_task_id, cron_automation_id,
          off_peak_task_id, created_at, updated_at, unread_at, last_unread_at, pinned, archived,
          deleted, title_overridden, searchable_text, meta_json
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(workspace_key, task_id) DO UPDATE SET
          workspace_path = excluded.workspace_path,
          workspace_identity = excluded.workspace_identity,
          title = excluded.title,
          task_status = excluded.task_status,
          provider = excluded.provider,
          mode = excluded.mode,
          model = excluded.model,
          migration_source = excluded.migration_source,
          forked_from_task_id = excluded.forked_from_task_id,
          cron_automation_id = excluded.cron_automation_id,
          off_peak_task_id = excluded.off_peak_task_id,
          created_at = excluded.created_at,
          updated_at = excluded.updated_at,
          unread_at = CASE
            WHEN ? = 1 THEN excluded.unread_at
            ELSE tasks.unread_at
          END,
          last_unread_at = MAX(
            tasks.last_unread_at,
            COALESCE(tasks.unread_at, 0),
            CASE WHEN ? = 1 THEN excluded.last_unread_at ELSE 0 END
          ),
          pinned = excluded.pinned,
          archived = excluded.archived,
          deleted = excluded.deleted,
          title_overridden = excluded.title_overridden,
          searchable_text = excluded.searchable_text,
          meta_json = excluded.meta_json`

// writeRecord ports writeRecord: the single upsert every write path funnels
// through. searchableText nil means "keep the existing value" — otherwise an
// ON CONFLICT would assign an empty string and wipe the indexed body.
func (r *Repo) writeRecord(ctx context.Context, db execer, rec writeRecord) (TaskMeta, error) {
	searchableText := ""
	if rec.SearchableText != nil {
		searchableText = truncateRunes(*rec.SearchableText, searchTextMaxChars)
	} else {
		existing, err := r.getTaskRow(ctx, db, rec.Meta.WorkspacePath, identityOfMeta(rec.Meta), rec.Meta.TaskID)
		if err != nil {
			return TaskMeta{}, err
		}
		if existing != nil {
			searchableText = existing.SearchableText
		}
	}
	metaJSON, err := serializeMetaJSON(rec.Meta)
	if err != nil {
		return TaskMeta{}, err
	}
	// cron automation 身份从 meta 投影到索引列（meta_json 里也保留一份）。
	unreadAt := any(nil)
	if rec.Meta.UnreadAt != nil {
		unreadAt = *rec.Meta.UnreadAt
	}
	lastUnreadAt := int64(0)
	if rec.Meta.UnreadAt != nil {
		lastUnreadAt = *rec.Meta.UnreadAt
	}
	writeUnread := 0
	if rec.WriteUnreadAt {
		writeUnread = 1
	}
	_, err = db.ExecContext(ctx, insertTaskSQL,
		workspaceKey(rec.Meta.WorkspacePath, deref(rec.Meta.WorkspaceIdentity)), rec.Meta.WorkspacePath,
		nullable(rec.Meta.WorkspaceIdentity), rec.Meta.TaskID, rec.Meta.Title,
		nullable(rec.Meta.Status), nullable(rec.Meta.Provider), rec.Meta.Mode,
		nullable(rec.Meta.Model), nullable(rec.Meta.MigrationSource), nullable(rec.Meta.ForkedFromTaskID),
		nullable(rec.Meta.CronAutomationID), nullable(rec.Meta.OffPeakTaskID),
		rec.Meta.CreatedAt, rec.Meta.UpdatedAt, unreadAt, lastUnreadAt,
		boolToInt(rec.Pinned), boolToInt(rec.Archived), boolToInt(rec.Deleted),
		boolToInt(rec.TitleOverridden), searchableText, metaJSON, writeUnread, writeUnread)
	if err != nil {
		return TaskMeta{}, err
	}
	persisted, err := r.getTaskRow(ctx, db, rec.Meta.WorkspacePath, identityOfMeta(rec.Meta), rec.Meta.TaskID)
	if err != nil {
		return TaskMeta{}, err
	}
	if persisted == nil {
		return TaskMeta{}, fmt.Errorf("task index 写入后缺少 task: %s", rec.Meta.TaskID)
	}
	return rowToMeta(*persisted), nil
}

// writeRecord is the internal write descriptor (TaskIndexWriteRecord).
type writeRecord struct {
	Meta            TaskMeta
	Pinned          bool
	Archived        bool
	Deleted         bool
	TitleOverridden bool
	// WriteUnreadAt: only the unread dedicated write path may modify unread_at;
	// every other metadata/snapshot write preserves the current CAS marker.
	WriteUnreadAt bool
	// SearchableText nil keeps the existing value; non-nil replaces it.
	SearchableText *string
}

func identityOf(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func identityOfMeta(meta TaskMeta) sql.NullString {
	return identityOf(deref(meta.WorkspaceIdentity))
}

// execer is satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// taskOrderNodeKey ports taskOrderNodeKey.
// task_group_view_node_orders.node_key 不能使用 "\u0000" 分隔；
// node:sqlite 读 TEXT 时会截断 NUL 后面的 taskId，导致 grouped 视图排序写入后查询匹配不上。
func taskOrderNodeKey(workspaceKeyValue, taskID string) string {
	out, err := json.Marshal([]string{workspaceKeyValue, taskID})
	if err != nil {
		// []string marshalling cannot fail.
		panic("taskindex: task order node key: " + err.Error())
	}
	return string(out)
}

// writeKey ports the TS writeKey: the per-row serialization key.
func writeKey(workspacePath, workspaceIdentity, taskID string) string {
	return workspaceKey(workspacePath, workspaceIdentity) + "\x00" + taskID
}

// taskNodeKey ports taskNodeKey (in-memory map key only, never persisted).
func taskNodeKey(workspacePath, workspaceIdentity, taskID string) string {
	return workspaceKey(workspacePath, workspaceIdentity) + "\x00" + taskID
}

// getTaskRow ports getTaskRow. It queries by the identity key, never by path
// alone, so two remotes that share a path cannot claim each other's rows.
func (r *Repo) getTaskRow(ctx context.Context, db execer, workspacePath string, identity sql.NullString, taskID string) (*TaskRow, error) {
	key := workspaceKey(workspacePath, trimOrEmpty(identity))
	q, err := db.QueryContext(ctx, "SELECT "+taskColumns+" FROM tasks WHERE workspace_key = ? AND task_id = ?", key, taskID)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	rows, err := scanTaskRows(q)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// scanTaskRows reads the standard task projection.
func scanTaskRows(rows *sql.Rows) ([]TaskRow, error) {
	var out []TaskRow
	for rows.Next() {
		var r TaskRow
		if err := rows.Scan(
			&r.WorkspaceKey, &r.WorkspacePath, &r.WorkspaceIdentity, &r.TaskID, &r.Title, &r.TaskStatus,
			&r.Provider, &r.Mode, &r.Model, &r.MigrationSource, &r.ForkedFromTaskID, &r.CronAutomationID,
			&r.OffPeakTaskID, &r.CreatedAt, &r.UpdatedAt, &r.UnreadAt, &r.LastUnreadAt, &r.Pinned,
			&r.Archived, &r.Deleted, &r.TitleOverridden, &r.SearchableText, &r.MetaJSON,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
