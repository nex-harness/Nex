package taskindex

// read.go — the read surface of TaskIndexRepo: the sidebar list, the per-task
// lookup, the deleted tombstones and the automation back-reference.

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ListFilter is the subset of listTaskMetas' parameters.
type ListFilter struct {
	// WorkspacePath empty means "every workspace" (listTaskMetas supports it;
	// the identity key still requires a concrete path whenever one is given).
	WorkspacePath     string
	WorkspaceIdentity string
	Provider          string
	Pinned            *bool
	Archived          *bool
	IncludeDeleted    bool
}

// ListTaskMetas ports listTaskMetas. Ordering is the list order the sidebar
// expects: most recently updated first, then most recently created, then id.
func (r *Repo) ListTaskMetas(ctx context.Context, filter ListFilter) ([]TaskMeta, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	where := []string{}
	var args []any
	if filter.WorkspacePath != "" {
		where = append(where, "workspace_key = ?")
		args = append(args, workspaceKey(filter.WorkspacePath, filter.WorkspaceIdentity))
	}
	if !filter.IncludeDeleted {
		where = append(where, "deleted = 0")
	}
	// 按请求指定的 runtime provider 过滤；迁移来源另存于 migration_source。
	if filter.Provider != "" {
		where = append(where, "provider = ?")
		args = append(args, filter.Provider)
	}
	if filter.Pinned != nil {
		where = append(where, "pinned = ?")
		args = append(args, boolToInt(*filter.Pinned))
	}
	if filter.Archived != nil {
		where = append(where, "archived = ?")
		args = append(args, boolToInt(*filter.Archived))
	}
	query := "SELECT " + taskColumns + " FROM tasks"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY updated_at DESC, created_at DESC, task_id DESC"
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	taskRows, err := scanTaskRows(rows)
	if err != nil {
		return nil, err
	}
	out := make([]TaskMeta, 0, len(taskRows))
	for _, row := range taskRows {
		out = append(out, rowToMeta(row))
	}
	return out, nil
}

// ListDeletedTaskIds ports listDeletedTaskIds.
//
// CLI session store 会继续保留会话内容；如果列表 join 只读取 active/pinned/archived，
// deleted task 会因"不在 archived 集合"被误判成普通 task，并在冷启动后重新出现。
func (r *Repo) ListDeletedTaskIds(ctx context.Context, workspacePath, workspaceIdentity, provider string) ([]string, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	query := "SELECT task_id FROM tasks WHERE workspace_key = ? AND deleted = 1"
	args := []any{workspaceKey(workspacePath, workspaceIdentity)}
	if provider != "" {
		query += " AND provider = ?"
		args = append(args, provider)
	}
	query += " ORDER BY task_id"
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListSessionsByAutomation ports listSessionsByAutomation: every cron session an
// automation produced, newest first.
func (r *Repo) ListSessionsByAutomation(ctx context.Context, automationID string) ([]TaskMeta, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		"SELECT "+taskColumns+" FROM tasks WHERE cron_automation_id = ? AND deleted = 0 ORDER BY created_at DESC, task_id DESC",
		automationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	taskRows, err := scanTaskRows(rows)
	if err != nil {
		return nil, err
	}
	out := make([]TaskMeta, 0, len(taskRows))
	for _, row := range taskRows {
		out = append(out, rowToMeta(row))
	}
	return out, nil
}

// GetTaskMeta ports getTaskMeta; a deleted row reads as absent.
func (r *Repo) GetTaskMeta(ctx context.Context, ref TargetRef) (*TaskMeta, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	row, err := r.getTaskRow(ctx, db, ref.WorkspacePath, identityOf(ref.WorkspaceIdentity), ref.TaskID)
	if err != nil {
		return nil, err
	}
	if row == nil || row.Deleted == 1 {
		return nil, nil
	}
	meta := rowToMeta(*row)
	return &meta, nil
}

// ListKind selects the sidebar bucket.
type ListKind string

const (
	ListKindActive   ListKind = "active"
	ListKindPinned   ListKind = "pinned"
	ListKindArchived ListKind = "archived"
)

// ListQuery is NexTaskListQuery's subset that the index answers.
type ListQuery struct {
	Kind            ListKind
	WorkspaceScopes []WorkspaceScope
	Provider        string
	Search          string
	SortBy          string
	Limit           *int
}

// WorkspaceScope is one scope of a multi-workspace list query.
type WorkspaceScope struct {
	WorkspacePath     string
	WorkspaceIdentity string
	// WorkspacePurpose "conversation" scopes are backing cwds, not projects; the
	// grouped bootstrap must not create a project group for them.
	WorkspacePurpose string
}

// ListItem is NexTaskListItem: a meta plus the search excerpts.
type ListItem struct {
	TaskMeta
	SearchSnippet  string
	SearchSnippets []string
}

// ListResult is NexTaskListResult.
type ListResult struct {
	Items   []ListItem
	Total   int
	HasMore bool
}

// QueryTaskList ports queryTaskList.
//
// The pinned/archived buckets are disjoint from the active one, and search
// matches title OR the indexed conversation body — the sidebar search dialog
// could never find message content before the searchable_text column existed.
func (r *Repo) QueryTaskList(ctx context.Context, query ListQuery) (ListResult, error) {
	db, err := r.database(ctx)
	if err != nil {
		return ListResult{}, err
	}
	keys := normalizeWorkspaceKeys(query.WorkspaceScopes)
	if len(keys) == 0 {
		return ListResult{Items: []ListItem{}, Total: 0, HasMore: false}, nil
	}
	search := strings.TrimSpace(query.Search)
	var searchLike any
	if search != "" {
		searchLike = "%" + strings.ToLower(search) + "%"
	}
	where := []string{"deleted = 0", "workspace_key IN (" + placeholders(len(keys)) + ")"}
	args := make([]any, 0, len(keys)+4)
	for _, k := range keys {
		args = append(args, k)
	}
	if query.Provider != "" {
		where = append(where, "provider = ?")
		args = append(args, query.Provider)
	}
	switch query.Kind {
	case ListKindPinned:
		where = append(where, "pinned = 1", "archived = 0")
	case ListKindArchived:
		where = append(where, "archived = 1")
	default:
		where = append(where, "pinned = 0", "archived = 0")
	}
	if searchLike != nil {
		where = append(where, "(LOWER(title) LIKE ? OR LOWER(searchable_text) LIKE ?)")
		args = append(args, searchLike, searchLike)
	}
	whereClause := strings.Join(where, " AND ")
	var total int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(1) FROM tasks WHERE "+whereClause, args...).Scan(&total); err != nil {
		return ListResult{}, err
	}
	orderBy := "updated_at DESC, created_at DESC, task_id DESC"
	if query.SortBy == "created" {
		orderBy = "created_at DESC, updated_at DESC, task_id DESC"
	}
	listArgs := append([]any{}, args...)
	if query.Limit != nil && *query.Limit > 0 {
		listArgs = append(listArgs, *query.Limit)
	}
	rows, err := db.QueryContext(ctx,
		"SELECT "+taskColumns+" FROM tasks WHERE "+whereClause+" ORDER BY "+orderBy+limitSuffix(query.Limit),
		listArgs...)
	if err != nil {
		return ListResult{}, err
	}
	defer rows.Close()
	taskRows, err := scanTaskRows(rows)
	if err != nil {
		return ListResult{}, err
	}
	purposeByKey := map[string]string{}
	for _, scope := range query.WorkspaceScopes {
		if scope.WorkspacePurpose != "" {
			purposeByKey[workspaceKey(scope.WorkspacePath, scope.WorkspaceIdentity)] = scope.WorkspacePurpose
		}
	}
	items := make([]ListItem, 0, len(taskRows))
	for _, row := range taskRows {
		meta := rowToMeta(row)
		item := ListItem{TaskMeta: meta}
		if search != "" {
			if snippets := buildSearchSnippets(row.SearchableText, search); len(snippets) > 0 {
				item.SearchSnippet = snippets[0]
				item.SearchSnippets = snippets
			}
		}
		_ = purposeByKey
		items = append(items, item)
	}
	return ListResult{Items: items, Total: total, HasMore: total > len(items)}, nil
}

func limitSuffix(limit *int) string {
	if limit != nil && *limit > 0 {
		return " LIMIT ?"
	}
	return ""
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// normalizeWorkspaceKeys ports normalizeWorkspaceKeys: dedupe, drop blanks, sort.
func normalizeWorkspaceKeys(scopes []WorkspaceScope) []string {
	seen := map[string]bool{}
	var out []string
	for _, scope := range scopes {
		key := workspaceKey(scope.WorkspacePath, scope.WorkspaceIdentity)
		if strings.TrimSpace(key) == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// HasGroupedWorkspaceBootstrapRun ports hasGroupedWorkspaceBootstrapRun.
func (r *Repo) HasGroupedWorkspaceBootstrapRun(ctx context.Context) (bool, error) {
	db, err := r.database(ctx)
	if err != nil {
		return false, err
	}
	var found int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM task_group_workspace_bootstraps LIMIT 1").Scan(&found); err != nil {
		return false, err
	}
	return found > 0, nil
}

// ArchiveStaleTasks ports archiveStaleTasks: everything untouched for N days,
// completed, not pinned and not unread goes to the archive bucket.
func (r *Repo) ArchiveStaleTasks(ctx context.Context, workspacePath, workspaceIdentity string, olderThanDays int, provider string) ([]TaskMeta, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	days := olderThanDays
	if days < 1 {
		days = 1
	}
	cutoff := r.now() - int64(days)*24*60*60*1000
	where := []string{
		"workspace_key = ?", "deleted = 0", "archived = 0", "pinned = 0",
		"unread_at IS NULL", "updated_at < ?", "task_status = 'completed'",
	}
	args := []any{workspaceKey(workspacePath, workspaceIdentity), cutoff}
	if provider != "" {
		where = append(where, "provider = ?")
		args = append(args, provider)
	}
	rows, err := db.QueryContext(ctx,
		"SELECT "+taskColumns+" FROM tasks WHERE "+strings.Join(where, " AND ")+
			" ORDER BY updated_at DESC, created_at DESC, task_id DESC", args...)
	if err != nil {
		return nil, err
	}
	taskRows, err := scanTaskRows(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(taskRows) == 0 {
		return nil, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, row := range taskRows {
		if _, err := tx.ExecContext(ctx,
			"UPDATE tasks SET archived = 1 WHERE workspace_key = ? AND task_id = ?",
			row.WorkspaceKey, row.TaskID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := make([]TaskMeta, 0, len(taskRows))
	for _, row := range taskRows {
		out = append(out, rowToMeta(row))
	}
	return out, nil
}

// IntegrityCheck is a test/diagnostic helper mirroring PRAGMA integrity_check.
func (r *Repo) IntegrityCheck(ctx context.Context) (string, error) {
	db, err := r.database(ctx)
	if err != nil {
		return "", err
	}
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return "", fmt.Errorf("integrity_check: %w", err)
	}
	return result, nil
}
