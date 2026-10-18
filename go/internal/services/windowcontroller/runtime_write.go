package windowcontroller

import (
	"context"
	"errors"
	"fmt"
)

// runtime_write.go routes a controller write to the source that owns the target
// and then converges the projection onto it.
//
// The three rules that matter, all from windowHostControllerService.ts:
//
//   - A write NEVER touches the projection directly. It goes to the source's
//     reader, and the projection is refreshed afterwards. The projection is a
//     cache of the index, never a second write path — otherwise a local write and
//     a remote write could diverge and the sidebar would show a mutation the index
//     never accepted.
//   - open and resume are NOT writes. The controller's only job for them is to
//     verify that exactly ONE source owns the address; the conversation itself
//     then runs through that source's scoped facade. Accepting them as no-ops here
//     is what lets the sidebar open a task without the controller having to know
//     anything about conversations.
//   - delete-archived-batch re-verifies the source identity BEFORE the batch is
//     applied. Between the user confirming the dialog and the write landing, a
//     remote session can have been replaced; writing the old address's batch into
//     the NEW source's identity would delete rows the user never confirmed.

// MutateTask applies one mutation and returns the affected row's meta afterwards
// (nil when the row is gone, which is the normal answer for delete).
func (rt *Runtime) MutateTask(ctx context.Context, addr TaskAddress, mutation Mutation) (*taskMeta, error) {
	if !addr.Valid() {
		return nil, fmt.Errorf("%w: %+v", ErrNoSource, addr)
	}
	resolved, err := rt.resolveForWrite(addr)
	if err != nil {
		return nil, err
	}
	if err := rt.applyMutation(ctx, resolved, addr, mutation); err != nil {
		return nil, err
	}
	// open/resume carry no index write, so there is nothing to converge on; the
	// source is already materialized by resolveForWrite, which is the point.
	if mutation.Kind != MutOpen && mutation.Kind != MutResume {
		if err := rt.refreshSource(ctx, resolved, true); err != nil {
			return nil, err
		}
	}
	if row := rt.projection.findRow(addr); row != nil {
		meta := row.Meta
		return &meta, nil
	}
	return nil, nil
}

// DeleteArchivedTask conditionally removes one archived task and reports whether
// it was actually removed.
func (rt *Runtime) DeleteArchivedTask(ctx context.Context, addr TaskAddress) (bool, error) {
	resolved, err := rt.resolveForWrite(addr)
	if err != nil {
		return false, err
	}
	deleted, err := resolved.Reader.DeleteArchivedTask(ctx, addr)
	if err != nil {
		return false, err
	}
	// The delete is already persisted; a failed convergence must NOT be reported
	// as a failed delete. It is reported through the source-error channel and the
	// next workspace event re-converges the projection.
	if err := rt.refreshSource(ctx, resolved, true); err != nil {
		rt.report(resolved.Scope, "refresh", err)
	}
	return deleted, nil
}

// DeleteArchivedTasks conditionally removes a batch. It converges ONCE, after the
// whole batch, so the projection cannot show a half-applied batch.
func (rt *Runtime) DeleteArchivedTasks(ctx context.Context, addr TaskAddress, taskIDs []string) (ArchivedDeletionResult, error) {
	empty := ArchivedDeletionResult{DeletedTaskIDs: []string{}, SkippedTaskIDs: []string{}, FailedTaskIDs: []string{}}
	if len(taskIDs) == 0 {
		return empty, nil
	}
	resolved, err := rt.resolveForWrite(addr)
	if err != nil {
		return ArchivedDeletionResult{}, err
	}
	// 确认期间远端 session 可能已替换；不能将旧地址的批次写入同 identity 的新 source。
	if SourceKey(resolved.Scope) != SourceKey(addr.Scope()) {
		return ArchivedDeletionResult{}, errors.New("archived deletion batch source was replaced")
	}
	result, err := resolved.Reader.DeleteArchivedTasks(ctx, addr, taskIDs)
	if err != nil {
		return ArchivedDeletionResult{}, err
	}
	if err := rt.refreshSource(ctx, resolved, true); err != nil {
		rt.report(resolved.Scope, "refresh", err)
	}
	return result, nil
}

// resolveForWrite resolves the source for a write and refuses an offline one.
func (rt *Runtime) resolveForWrite(addr TaskAddress) (*ResolvedSource, error) {
	resolved := rt.resolveSource(addr.Scope())
	if resolved == nil || resolved.Reader == nil {
		return nil, ErrNoSource
	}
	if resolved.Availability != AvailabilityOnline {
		return nil, ErrSourceUnavailable
	}
	return resolved, nil
}

// applyMutation dispatches one mutation onto the source's reader.
func (rt *Runtime) applyMutation(ctx context.Context, resolved *ResolvedSource, addr TaskAddress, mutation Mutation) error {
	switch mutation.Kind {
	case MutPin:
		return resolved.Reader.SetPinned(ctx, addr, mutation.Pinned)
	case MutArchive:
		return resolved.Reader.SetArchived(ctx, addr, mutation.Archived)
	case MutDelete:
		return resolved.Reader.DeleteTask(ctx, addr)
	case MutDeleteArchived:
		_, err := resolved.Reader.DeleteArchivedTask(ctx, addr)
		return err
	case MutDeleteArchivedBat:
		_, err := resolved.Reader.DeleteArchivedTasks(ctx, addr, mutation.TaskIDs)
		return err
	case MutMarkRead:
		return resolved.Reader.SetUnread(ctx, addr, false, mutation.ExpectedUnreadAt)
	case MutMarkUnread:
		return resolved.Reader.SetUnread(ctx, addr, true, nil)
	case MutOpen, MutResume:
		// 打开/恢复的 Controller 职责是验证唯一 source；conversation 随后仍走 scoped facade。
		return nil
	default:
		return fmt.Errorf("unknown controller mutation %q", mutation.Kind)
	}
}

// ResolveTaskAddress resolves a sidebar (taskId, workspace) into the single
// source that owns it, materializing that source first.
//
// Materializing matters: a remote attachment used to be able to resolve an
// address without the controller ever having read that workspace, so the first
// write of an unread/unread-archive action failed with "no row". Here the source
// is refreshed (not forced) before the lookup, which is bounded by the same
// single-flight as every other read.
func (rt *Runtime) ResolveTaskAddress(ctx context.Context, addr TaskAddress, allowMissing bool) (TaskAddress, error) {
	resolved := rt.resolveSource(addr.Scope())
	if resolved != nil {
		if err := rt.refreshSource(ctx, resolved, false); err != nil {
			rt.report(resolved.Scope, "refresh", err)
		}
	}
	match := rt.projection.findAddress(addr)
	if match != nil {
		return *match, nil
	}
	if allowMissing && resolved != nil {
		// A conditional delete may legitimately target a row another client already
		// removed. The source identity is still verified above, so returning the
		// resolved scope is safe: the repo treats the missing row as a skip.
		return AddressOf(resolved.Scope, addr.TaskID), nil
	}
	return TaskAddress{}, fmt.Errorf("%w: taskId=%s", ErrNoSource, addr.TaskID)
}
