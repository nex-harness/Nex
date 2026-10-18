package taskindex

// groups.go — the system group memberships and the grouped top-level order.
// Both are INSERT OR IGNORE by design: the user dragging a task out of the cron
// or off-peak group must not be undone by the next bootstrap.

import (
	"context"
	"database/sql"
)

const groupedTaskOrderStepValue = groupedTaskOrderStep

// ensureCronGroupMembership ports ensureCronGroupMembership: called once, the
// first time a session acquires a cronAutomationId.
func (r *Repo) ensureCronGroupMembership(ctx context.Context, db execer, workspacePath, identity, taskID string) error {
	return r.ensureSystemGroupMembership(ctx, db, workspacePath, identity, taskID,
		CronDefaultGroupID, "cron", "blue")
}

// ensureOffPeakGroupMembership ports ensureOffPeakGroupMembership. Same shape as
// cron; invoked on the first offPeakTaskId, or by the bootstrap backfill.
func (r *Repo) ensureOffPeakGroupMembership(ctx context.Context, db execer, workspacePath, identity, taskID string) error {
	// 闲时任务暂不支持远程 workspace：远程会话即使带标记也不归入闲时系统分组。
	if IsRemoteWorkspaceIdentity(workspaceKey(workspacePath, identity)) {
		return nil
	}
	return r.ensureSystemGroupMembership(ctx, db, workspacePath, identity, taskID,
		OffPeakDefaultGroupID, "off-peak", "purple")
}

// ensureSystemGroupMembership ports ensureSystemGroupMembership. Every insert is
// OR IGNORE: if the task already has a membership (the user grouped it
// manually) it is left alone.
func (r *Repo) ensureSystemGroupMembership(ctx context.Context, db execer, workspacePath, identity, taskID, groupID, title, color string) error {
	now := r.now()
	if _, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO task_groups (group_id, title, color, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?)`, groupID, title, color, now, now); err != nil {
		return err
	}
	// 分组视图排序：不存在才插入（OR IGNORE），避免每次新建系统分组 session 都把该组顺序打乱。
	if _, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO task_group_view_node_orders (node_type, node_key, sort_order, created_at, updated_at)
        VALUES ('group', ?, ?, ?, ?)`,
		groupID, r.getNextGroupedTopSortOrder(ctx, db), now, now); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO task_group_members (
          group_id, workspace_key, workspace_path, workspace_identity, task_id,
          sort_order, added_at, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?)`,
		groupID, workspaceKey(workspacePath, identity), workspacePath, nullOrNil(identity), taskID, now, now, now)
	return err
}

// getNextGroupedTopSortOrder ports getNextGroupedTopSortOrder: new top-level
// nodes land above everything existing, so a freshly visible task appears at the
// top instead of the bottom.
func (r *Repo) getNextGroupedTopSortOrder(ctx context.Context, db execer) int {
	var minOrder sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT MIN(sort_order) FROM task_group_view_node_orders").Scan(&minOrder); err != nil {
		return groupedTaskOrderStepValue*2 - groupedTaskOrderStepValue
	}
	base := int64(groupedTaskOrderStepValue * 2)
	if minOrder.Valid {
		base = minOrder.Int64
	}
	return int(base - groupedTaskOrderStepValue)
}

// upsertGroupedTopOrder ports upsertGroupedTopOrder.
func (r *Repo) upsertGroupedTopOrder(ctx context.Context, db execer, nodeType, nodeKey string, sortOrder, now int64) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO task_group_view_node_orders (node_type, node_key, sort_order, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(node_type, node_key) DO UPDATE SET
          sort_order = excluded.sort_order,
          updated_at = excluded.updated_at`,
		nodeType, nodeKey, sortOrder, now, now)
	return err
}

// initializeGroupedTaskAtTopReady ports initializeGroupedTaskAtTopReady: the
// top-level order may only be initialized the first time a task node appears.
//
// session 可见与首标题缺行都可能并发触发完整 snapshot 回源；顶层顺序只能在第一次
// 出现时初始化。重复回源若再次分配最小 sort_order，较慢完成的旧任务会越过之后创建
// 的新任务，使最终顺序依赖异步完成时序。
func (r *Repo) initializeGroupedTaskAtTopReady(ctx context.Context, db execer, workspacePath, identity, taskID string) (bool, error) {
	row, err := r.getTaskRow(ctx, db, workspacePath, identityOf(identity), taskID)
	if err != nil {
		return false, err
	}
	if row == nil || row.Deleted == 1 || row.Archived == 1 || row.Pinned == 1 {
		return false, nil
	}
	var found int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM task_group_members WHERE workspace_key = ? AND task_id = ? LIMIT 1`,
		workspaceKey(workspacePath, identity), taskID).Scan(&found); err != nil {
		return false, err
	}
	nodeKey := taskOrderNodeKey(workspaceKey(workspacePath, identity), taskID)
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM task_group_view_node_orders WHERE node_type = 'task' AND node_key = ? LIMIT 1`,
		nodeKey).Scan(&found); err != nil {
		return false, err
	}
	if found > 0 {
		return false, nil
	}
	now := r.now()
	if err := r.upsertGroupedTopOrder(ctx, db, "task", nodeKey, int64(r.getNextGroupedTopSortOrder(ctx, db)), now); err != nil {
		return false, err
	}
	return true, nil
}

// InitializeGroupedTaskAtTop ports initializeGroupedTaskAtTop.
func (r *Repo) InitializeGroupedTaskAtTop(ctx context.Context, ref TargetRef) (bool, error) {
	db, err := r.database(ctx)
	if err != nil {
		return false, err
	}
	return r.initializeGroupedTaskAtTopReady(ctx, db, ref.WorkspacePath, ref.WorkspaceIdentity, ref.TaskID)
}

func nullOrNil(identity string) any {
	if identity == "" {
		return nil
	}
	return identity
}

// GroupedTopOrder returns the grouped top-level sort order of a task node and
// whether one exists. It is the read side of initializeGroupedTaskAtTopReady and
// exists so callers (and tests) can observe the order the sidebar will render.
func (r *Repo) GroupedTopOrder(ctx context.Context, ref TargetRef) (int64, bool, error) {
	db, err := r.database(ctx)
	if err != nil {
		return 0, false, err
	}
	var order int64
	err = db.QueryRowContext(ctx,
		"SELECT sort_order FROM task_group_view_node_orders WHERE node_type = 'task' AND node_key = ?",
		taskOrderNodeKey(workspaceKey(ref.WorkspacePath, ref.WorkspaceIdentity), ref.TaskID)).Scan(&order)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return order, true, nil
}
