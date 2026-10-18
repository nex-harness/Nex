package taskindex

// patches.go — the two patch shapes. They are deliberately different types: the
// app-side patch may move shell state (pin/archive/delete/unread), the agent-side
// patch may not.

import (
	"context"
	"errors"
)

// AgentPatch is the restricted patch the agent side may write
// (Pick<TaskIndexStatePatch, "title" | "status" | "lastError" | "target" | "updatedAt">).
// It can never touch pin/archive/delete: those are product shell state.
type AgentPatch struct {
	Title          *string
	Status         *string
	ClearLastError bool
	LastError      *TaskLastError
	TargetPresent  bool
	Target         *Goal
	UpdatedAt      *int64
}

// ApplyAgentPatch ports applyAgentPatch. A missing or deleted row returns
// (nil, nil): the caller then re-reads the full snapshot from the agent.
func (r *Repo) ApplyAgentPatch(ctx context.Context, ref TargetRef, patch AgentPatch) (*TaskMeta, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	var out *TaskMeta
	err = r.enqueueWrite(writeKey(ref.WorkspacePath, ref.WorkspaceIdentity, ref.TaskID), func() error {
		row, err := r.getTaskRow(ctx, db, ref.WorkspacePath, identityOf(ref.WorkspaceIdentity), ref.TaskID)
		if err != nil {
			return err
		}
		if row == nil || row.Deleted == 1 {
			return nil
		}
		current := rowToMeta(*row)
		canAcceptAgentTitle := row.TitleOverridden != 1
		next := current
		if canAcceptAgentTitle && patch.Title != nil && *patch.Title != "" {
			next.Title = *patch.Title
		}
		next.TitleOverridden = row.TitleOverridden == 1
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
		persisted, err := r.writeRecord(ctx, db, writeRecord{
			Meta:            next,
			Pinned:          row.Pinned == 1,
			Archived:        row.Archived == 1,
			Deleted:         row.Deleted == 1,
			TitleOverridden: row.TitleOverridden == 1,
		})
		if err != nil {
			return err
		}
		out = &persisted
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteArchivedTask ports deleteArchivedTask: the archive confirmation may sit
// open while the other end restores the task, so the archive check and the
// tombstone must be in the same transaction — never read-then-delete.
func (r *Repo) DeleteArchivedTask(ctx context.Context, ref TargetRef) (*TaskMeta, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	var out *TaskMeta
	err = r.enqueueWrite(writeKey(ref.WorkspacePath, ref.WorkspaceIdentity, ref.TaskID), func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		fail := func(err error) error {
			_ = tx.Rollback()
			return err
		}
		row, err := r.getTaskRow(ctx, tx, ref.WorkspacePath, identityOf(ref.WorkspaceIdentity), ref.TaskID)
		if err != nil {
			return fail(err)
		}
		// 已删除/不存在也跳过，避免重试从 CLI seed 后复活。
		if row == nil || row.Deleted == 1 || row.Archived != 1 {
			return tx.Commit()
		}
		current := rowToMeta(*row)
		persisted, err := r.writeRecord(ctx, tx, writeRecord{
			Meta:            current,
			Pinned:          row.Pinned == 1,
			Archived:        true,
			Deleted:         true,
			TitleOverridden: row.TitleOverridden == 1,
		})
		if err != nil {
			return fail(err)
		}
		if err := deleteTaskGroupingReferences(ctx, tx, row.WorkspaceKey, row.TaskID); err != nil {
			return fail(err)
		}
		if err := tx.Commit(); err != nil {
			return fail(err)
		}
		out = &persisted
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ClearTaskUnreadIfMatches ports clearTaskUnreadIfMatches: clearing is
// conditional on the marker the client saw, so a newer unread is not lost.
func (r *Repo) ClearTaskUnreadIfMatches(ctx context.Context, ref TargetRef, expectedUnreadAt int64) (TaskMeta, bool, error) {
	db, err := r.database(ctx)
	if err != nil {
		return TaskMeta{}, false, err
	}
	var (
		out     TaskMeta
		cleared bool
	)
	err = r.enqueueWrite(writeKey(ref.WorkspacePath, ref.WorkspaceIdentity, ref.TaskID), func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		fail := func(err error) error {
			_ = tx.Rollback()
			return err
		}
		row, err := r.getTaskRow(ctx, tx, ref.WorkspacePath, identityOf(ref.WorkspaceIdentity), ref.TaskID)
		if err != nil {
			return fail(err)
		}
		if row == nil || row.Deleted == 1 || !row.UnreadAt.Valid || row.UnreadAt.Int64 != expectedUnreadAt {
			return tx.Commit()
		}
		current := rowToMeta(*row)
		// ClearUnreadAt must reach writeRecord: the upsert only assigns the new
		// unread_at when the write is flagged, and the meta carries the cleared
		// value. rowToMeta re-reads the column afterwards, so a stale pointer here
		// would silently keep the marker.
		current.UnreadAt = nil
		persisted, err := r.writeRecord(ctx, tx, writeRecord{
			Meta:            current,
			Pinned:          row.Pinned == 1,
			Archived:        row.Archived == 1,
			Deleted:         row.Deleted == 1,
			TitleOverridden: row.TitleOverridden == 1,
			WriteUnreadAt:   true,
		})
		if err != nil {
			return fail(err)
		}
		if err := tx.Commit(); err != nil {
			return fail(err)
		}
		out = persisted
		cleared = true
		return nil
	})
	if err != nil {
		return TaskMeta{}, false, err
	}
	return out, cleared, nil
}

// ErrTaskNotFound is returned by UpdateTaskState when the row is gone.
var ErrTaskNotFound = errors.New("task index 中不存在 task")
