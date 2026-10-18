package taskindex

// startup.go — the post-migration self-healing passes TaskIndexRepo.initialize
// runs on every open. They are idempotent by construction and deliberately not
// new migrations: they repair projections of data that is already in the file.

import (
	"context"
	"database/sql"
)

// runStartupRepairs ports the three initialize() repairs, in the same order.
func (r *Repo) runStartupRepairs(ctx context.Context) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	if err := r.backfillOffPeakTaskMarkers(ctx, db); err != nil {
		return err
	}
	if err := r.backfillOffPeakGroupMemberships(ctx); err != nil {
		return err
	}
	return r.cleanupDeletedTaskGroupingReferences(ctx, db)
}

// backfillOffPeakTaskMarkers ports backfillOffPeakTaskMarkers.
//
// 存量回填（幂等，每次 bootstrap 自愈）：打点上线前产生的 off-peak 会话行没有
// offPeakTaskId。off_peak_tasks 与 tasks 同库，按 session 绑定 join 只补投影列——
// rowToMeta 以列兜底即可生效，下次 syncTaskMeta 会自动回填 meta_json。
// 全新安装时 off_peak_tasks 可能尚未由 OffPeakTaskRepo 建表，需 guard。
func (r *Repo) backfillOffPeakTaskMarkers(ctx context.Context, db *sql.DB) error {
	var hasTable int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name='off_peak_tasks'").Scan(&hasTable); err != nil {
		return err
	}
	if hasTable == 0 {
		return nil
	}
	_, err := db.ExecContext(ctx, `UPDATE tasks SET off_peak_task_id = (
          SELECT o.off_peak_task_id FROM off_peak_tasks o
          WHERE o.session_id = tasks.task_id AND o.workspace_key = tasks.workspace_key
        )
        WHERE off_peak_task_id IS NULL
          AND EXISTS (
            SELECT 1 FROM off_peak_tasks o
            WHERE o.session_id = tasks.task_id AND o.workspace_key = tasks.workspace_key
          )`)
	return err
}

// backfillOffPeakGroupMemberships ports backfillOffPeakGroupMemberships.
//
// 成员关系回填（幂等）：历史回填只补了 off_peak_task_id 投影列，
// syncTaskMeta 的"首次获得标记"钩子对这些存量永远不会再触发（existing 已带标记），
// 必须在 bootstrap 里补一次系统分组归属。OR IGNORE 保证用户手动整理不被覆盖。
func (r *Repo) backfillOffPeakGroupMemberships(ctx context.Context) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT workspace_path, workspace_identity, task_id FROM tasks
         WHERE off_peak_task_id IS NOT NULL AND deleted = 0`)
	if err != nil {
		return err
	}
	type seed struct {
		workspacePath     string
		workspaceIdentity string
		taskID            string
	}
	var seeds []seed
	for rows.Next() {
		var s seed
		var identity sql.NullString
		if err := rows.Scan(&s.workspacePath, &identity, &s.taskID); err != nil {
			rows.Close()
			return err
		}
		s.workspaceIdentity = trimOrEmpty(identity)
		// 闲时任务暂不支持远程 workspace：远程存量行不归组。历史行的 workspace_identity
		// 列可能缺失，remote 判定必须看主键 workspace_key——否则 ensureSystemGroupMembership
		// 会用 workspacePath 重算出本地 key，把成员关系串写到同路径本地 workspace 上。
		if IsRemoteWorkspaceIdentity(workspaceKey(s.workspacePath, s.workspaceIdentity)) {
			continue
		}
		seeds = append(seeds, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, s := range seeds {
		if err := r.ensureOffPeakGroupMembership(ctx, db, s.workspacePath, s.workspaceIdentity, s.taskID); err != nil {
			return err
		}
	}
	return nil
}

// cleanupDeletedTaskGroupingReferences ports cleanupDeletedTaskGroupingReferences.
//
// 旧版本删除 task 时只写 tasks.deleted，membership/顶层顺序仍会被
// sessions-index 历史摘要重新投影；初始化时幂等收敛已经落盘的脏引用。
func (r *Repo) cleanupDeletedTaskGroupingReferences(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "SELECT workspace_key, task_id FROM tasks WHERE deleted = 1")
	if err != nil {
		return err
	}
	type deleted struct{ workspaceKey, taskID string }
	var rows2 []deleted
	for rows.Next() {
		var d deleted
		if err := rows.Scan(&d.workspaceKey, &d.taskID); err != nil {
			rows.Close()
			return err
		}
		rows2 = append(rows2, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(rows2) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, d := range rows2 {
		if err := deleteTaskGroupingReferences(ctx, tx, d.workspaceKey, d.taskID); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// deleteTaskGroupingReferencesReady ports deleteTaskGroupingReferencesReady.
// task_group_view_node_orders.node_key cannot contain "\u0000": node:sqlite
// truncates TEXT at the NUL and the grouped view then cannot match its own node,
// so the node key is the JSON pair form (taskOrderNodeKey).
func deleteTaskGroupingReferences(ctx context.Context, exec execer, workspaceKeyValue, taskID string) error {
	if _, err := exec.ExecContext(ctx,
		"DELETE FROM task_group_members WHERE workspace_key = ? AND task_id = ?", workspaceKeyValue, taskID); err != nil {
		return err
	}
	_, err := exec.ExecContext(ctx,
		`DELETE FROM task_group_view_node_orders
        WHERE node_type = 'task' AND (node_key = ? OR node_key = ?)`,
		taskOrderNodeKey(workspaceKeyValue, taskID), workspaceKeyValue)
	return err
}
