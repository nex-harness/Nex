package taskindex

// writeapi.go — the write surface of TaskIndexRepo. Each entry point keeps its
// own transaction boundary and its own reconciliation rules; they share only
// writeRecord.

import (
	"context"
	"database/sql"
	"fmt"
)

// SyncTaskMeta ports syncTaskMeta: mirror an agent snapshot into the index.
func (r *Repo) SyncTaskMeta(ctx context.Context, meta TaskMeta, searchableText *string) (TaskMeta, error) {
	out, _, err := r.SyncTaskMetaWithGroupedAdmission(ctx, meta, searchableText, false)
	return out, err
}

// SyncTaskMetaAtGroupedTop ports syncTaskMetaAtGroupedTop: the first public
// appearance of a root task commits the row and its grouped top-level order in
// one transaction, so sessions-index can never expose an orderless node.
func (r *Repo) SyncTaskMetaAtGroupedTop(ctx context.Context, meta TaskMeta, searchableText *string) (TaskMeta, bool, error) {
	return r.SyncTaskMetaWithGroupedAdmission(ctx, meta, searchableText, true)
}

func (r *Repo) SyncTaskMetaWithGroupedAdmission(ctx context.Context, meta TaskMeta, searchableText *string, initializeGroupedAtTop bool) (TaskMeta, bool, error) {
	db, err := r.database(ctx)
	if err != nil {
		return TaskMeta{}, false, err
	}
	var persisted TaskMeta
	var initialized bool
	err = r.enqueueWrite(writeKey(meta.WorkspacePath, deref(meta.WorkspaceIdentity), meta.TaskID), func() error {
		exec := execer(db)
		if initializeGroupedAtTop {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			exec = tx
			defer func() {
				if err != nil {
					_ = tx.Rollback()
				}
			}()
			if err = r.syncTaskMetaInTx(ctx, exec, meta, searchableText, initializeGroupedAtTop, &persisted, &initialized); err != nil {
				return err
			}
			return tx.Commit()
		}
		return r.syncTaskMetaInTx(ctx, exec, meta, searchableText, false, &persisted, &initialized)
	})
	if err != nil {
		return TaskMeta{}, false, err
	}
	return persisted, initialized, nil
}

func (r *Repo) syncTaskMetaInTx(ctx context.Context, db execer, meta TaskMeta, searchableText *string, initializeGroupedAtTop bool, out *TaskMeta, initialized *bool) error {
	existing, err := r.getTaskRow(ctx, db, meta.WorkspacePath, identityOfMeta(meta), meta.TaskID)
	if err != nil {
		return err
	}
	var existingMeta *TaskMeta
	if existing != nil {
		m := rowToMeta(*existing)
		existingMeta = &m
	}
	titleOverridden := meta.TitleOverridden
	if existing != nil {
		titleOverridden = existing.TitleOverridden == 1
	}
	// snapshot 来源的 updatedAt 是 runtime sessionStore 里的"最后一次结构变更"时间，
	// 不一定包含 session.titleUpdated / turn.completed 这些事件触发的 Date.now() 增量。
	// 直接用 meta.UpdatedAt 覆盖会把刚走 ApplyAgentPatch 写入的时间戳冲回旧值，
	// 表现为新会话第一次 prompt 后又被压回列表底部。这里取与已有值的 max。
	updatedAt := meta.UpdatedAt
	if existingMeta != nil && existingMeta.UpdatedAt > updatedAt {
		updatedAt = existingMeta.UpdatedAt
	}
	preserveTerminal := shouldPreserveNewerTerminalStatus(existingMeta, &meta)

	next := meta
	// agent 只负责 session 核心标题，用户手动重命名属于 app 侧 task 状态。
	// 同步 agent snapshot 时保留已覆盖标题，避免后台状态刷新把用户标题冲掉。
	if titleOverridden && existingMeta != nil {
		next.Title = existingMeta.Title
	}
	next.TitleOverridden = titleOverridden
	// turn.completed 会先通过 ApplyAgentPatch 写入较新的 completed/error。
	// 随后到达的 protocol snapshot 可能仍带较旧 running；在这里降级 status 会让
	// 手机 replayable 切回 task 时把已完成任务恢复成"工作中"。
	if preserveTerminal && existingMeta != nil {
		next.Status = existingMeta.Status
		next.LastError = existingMeta.LastError
	}
	if !meta.HasTarget() && existingMeta != nil {
		next.Target = existingMeta.Target
		next.hasTarget = existingMeta.hasTarget
	}
	// 运行态 snapshot 的 meta 不带迁移/定时身份，必须用已存值兜底，否则后续 sync
	// 会把 Claude 导入来源和 cron/off-peak 归属冲掉。
	next.MigrationSource = meta.MigrationSource
	if next.MigrationSource == nil && existingMeta != nil {
		next.MigrationSource = existingMeta.MigrationSource
	}
	next.CronAutomationID = meta.CronAutomationID
	if next.CronAutomationID == nil && existingMeta != nil {
		next.CronAutomationID = existingMeta.CronAutomationID
	}
	next.OffPeakTaskID = meta.OffPeakTaskID
	if next.OffPeakTaskID == nil && existingMeta != nil {
		next.OffPeakTaskID = existingMeta.OffPeakTaskID
	}
	next.UpdatedAt = updatedAt
	next.UnreadAt = meta.UnreadAt
	if next.UnreadAt == nil && existingMeta != nil {
		next.UnreadAt = existingMeta.UnreadAt
	}

	rec := writeRecord{Meta: next, TitleOverridden: titleOverridden, SearchableText: searchableText}
	if existing != nil {
		rec.Pinned = existing.Pinned == 1
		rec.Archived = existing.Archived == 1
		rec.Deleted = existing.Deleted == 1
	}
	persisted, err := r.writeRecord(ctx, db, rec)
	if err != nil {
		return err
	}
	// cron session 首次获得 cronAutomationId 时归入固定 cron 分组。
	// 会话内 CronCreate 是给已有 task 补 cron 标记，不能只判断 !existing，否则左侧列表不会归入定时任务分组。
	if next.CronAutomationID != nil && (existingMeta == nil || existingMeta.CronAutomationID == nil) {
		if err := r.ensureCronGroupMembership(ctx, db, next.WorkspacePath, deref(next.WorkspaceIdentity), next.TaskID); err != nil {
			return err
		}
	}
	// 闲时会话首次获得 offPeakTaskId 时归入固定闲时系统分组（机制同 cron）。
	if next.OffPeakTaskID != nil && (existingMeta == nil || existingMeta.OffPeakTaskID == nil) {
		if err := r.ensureOffPeakGroupMembership(ctx, db, next.WorkspacePath, deref(next.WorkspaceIdentity), next.TaskID); err != nil {
			return err
		}
	}
	if initializeGroupedAtTop {
		ok, err := r.initializeGroupedTaskAtTopReady(ctx, db, next.WorkspacePath, deref(next.WorkspaceIdentity), next.TaskID)
		if err != nil {
			return err
		}
		*initialized = ok
	}
	*out = persisted
	return nil
}

// SeedTaskMetaIfMissing ports seedTaskMetaIfMissing: write the baseline meta only
// when the row does not exist. Existing product shell state (pin/archive/unread/
// manual title) is preserved — a remote workspace's first snapshot must be able
// to fill an empty tasks-index.sqlite without racing the full snapshot.
func (r *Repo) SeedTaskMetaIfMissing(ctx context.Context, meta TaskMeta) (TaskMeta, error) {
	db, err := r.database(ctx)
	if err != nil {
		return TaskMeta{}, err
	}
	var out TaskMeta
	err = r.enqueueWrite(writeKey(meta.WorkspacePath, deref(meta.WorkspaceIdentity), meta.TaskID), func() error {
		existing, err := r.getTaskRow(ctx, db, meta.WorkspacePath, identityOfMeta(meta), meta.TaskID)
		if err != nil {
			return err
		}
		if existing != nil {
			out = rowToMeta(*existing)
			return nil
		}
		persisted, err := r.writeRecord(ctx, db, writeRecord{Meta: meta, TitleOverridden: meta.TitleOverridden})
		if err != nil {
			return err
		}
		out = persisted
		return nil
	})
	return out, err
}

// StatePatch is TaskIndexStatePatch.
type StatePatch struct {
	Pinned          *bool
	Archived        *bool
	Deleted         *bool
	Title           *string
	TitleOverridden *bool
	UnreadAt        *int64
	// ClearUnreadAt distinguishes "unreadAt: undefined" (clear the marker) from
	// an absent key (keep it).
	ClearUnreadAt bool
	Model         *string
	Status        *string
	// ClearLastError distinguishes an explicit undefined from an absent key.
	ClearLastError bool
	LastError      *TaskLastError
	// TargetPresent distinguishes an explicit null/absent target from "keep".
	TargetPresent bool
	Target        *Goal
	UpdatedAt     *int64
}

// UpdateTaskState ports updateTaskState: the app-side state patch (pin, archive,
// delete, rename, model, status, unread).
func (r *Repo) UpdateTaskState(ctx context.Context, ref TargetRef, patch StatePatch) (TaskMeta, error) {
	db, err := r.database(ctx)
	if err != nil {
		return TaskMeta{}, err
	}
	var out TaskMeta
	err = r.enqueueWrite(writeKey(ref.WorkspacePath, ref.WorkspaceIdentity, ref.TaskID), func() error {
		deleting := patch.Deleted != nil && *patch.Deleted
		mutatingUnread := patch.UnreadAt != nil || patch.ClearUnreadAt
		// The delete marker and its grouping references must commit atomically:
		// a partial write leaves sessions-index content, task visibility and the
		// SQLite grouping permanently inconsistent.
		transactional := deleting || mutatingUnread
		var tx *sql.Tx
		exec := execer(db)
		if transactional {
			tx, err = db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			exec = tx
		}
		fail := func(err error) error {
			if tx != nil {
				_ = tx.Rollback()
			}
			return err
		}
		row, err := r.getTaskRow(ctx, exec, ref.WorkspacePath, identityOf(ref.WorkspaceIdentity), ref.TaskID)
		if err != nil {
			return fail(err)
		}
		if row == nil || row.Deleted == 1 {
			return fail(fmt.Errorf("task index 中不存在 task: %s", ref.TaskID))
		}
		current := rowToMeta(*row)
		// 毫秒时间戳可能让同一 task 的两个逻辑未读得到相同版本，且清除 unreadAt 后只看
		// 当前值会再次复用旧版本。必须在 SQLite 写锁内基于不会随清除重置的持久
		// watermark 分配严格递增 marker。
		lastUnreadAt := row.LastUnreadAt
		if row.UnreadAt.Valid && row.UnreadAt.Int64 > lastUnreadAt {
			lastUnreadAt = row.UnreadAt.Int64
		}
		if current.UnreadAt != nil && *current.UnreadAt > lastUnreadAt {
			lastUnreadAt = *current.UnreadAt
		}
		var unreadAt *int64
		switch {
		case patch.UnreadAt != nil:
			allocated := *patch.UnreadAt
			if allocated < lastUnreadAt+1 {
				allocated = lastUnreadAt + 1
			}
			unreadAt = &allocated
		case patch.ClearUnreadAt:
			unreadAt = nil
		default:
			unreadAt = current.UnreadAt
		}
		next := current
		if patch.Title != nil {
			next.Title = *patch.Title
		}
		if patch.TitleOverridden != nil {
			next.TitleOverridden = *patch.TitleOverridden
		}
		if patch.Model != nil {
			next.Model = patch.Model
		}
		if patch.UpdatedAt != nil {
			next.UpdatedAt = *patch.UpdatedAt
		}
		if patch.Status != nil {
			next.Status = patch.Status
		}
		if patch.ClearLastError {
			next.LastError = nil
		} else if patch.LastError != nil {
			next.LastError = patch.LastError
		}
		if patch.TargetPresent {
			next.Target = patch.Target
			next.hasTarget = true
		}
		next.UnreadAt = unreadAt

		rec := writeRecord{
			Meta:            next,
			Pinned:          row.Pinned == 1,
			Archived:        row.Archived == 1,
			Deleted:         row.Deleted == 1,
			TitleOverridden: row.TitleOverridden == 1,
			WriteUnreadAt:   mutatingUnread,
		}
		if patch.Pinned != nil {
			rec.Pinned = *patch.Pinned
		}
		if patch.Archived != nil {
			rec.Archived = *patch.Archived
		}
		if patch.Deleted != nil {
			rec.Deleted = *patch.Deleted
		}
		if patch.TitleOverridden != nil {
			rec.TitleOverridden = *patch.TitleOverridden
		}
		persisted, err := r.writeRecord(ctx, exec, rec)
		if err != nil {
			return fail(err)
		}
		if deleting {
			if err := deleteTaskGroupingReferences(ctx, exec, row.WorkspaceKey, row.TaskID); err != nil {
				return fail(err)
			}
		}
		if tx != nil {
			if err := tx.Commit(); err != nil {
				return fail(err)
			}
		}
		out = persisted
		return nil
	})
	if err != nil {
		return TaskMeta{}, err
	}
	return out, nil
}
