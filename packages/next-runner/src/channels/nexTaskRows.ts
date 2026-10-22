import type { NexTaskMeta, NexWorkspaceEvent, NexWorkspaceTaskListChanged } from "@nex/shared";
import type { EngineSessionState } from "../engine/types.js";

/**
 * Row projection + event diff for `nex-task` (docs/next/task-state.md). Kept pure and
 * separate from the channel so the reason mapping is directly testable.
 */

export interface TaskSnapshot {
  deleted: string[];
  /** Every non-draft session (archived/pinned included) so transitions stay diffable. */
  byId: Map<string, EngineSessionState>;
}

export type TaskEventReason = NexWorkspaceTaskListChanged["reason"];

/** Project list row: real (non-draft), live, neither pinned nor archived. */
export function isProjectRow(session: EngineSessionState): boolean {
  return session.phase !== "draft" && session.archived !== true && session.pinned !== true;
}

export function isTerminalPhase(phase: EngineSessionState["phase"]): boolean {
  return phase === "completedSuccess" || phase === "completedInterrupted" || phase === "error";
}

export function toNexTaskMeta(
  workspacePath: string,
  session: EngineSessionState,
  workspaceIdentity?: string,
): NexTaskMeta {
  return {
    taskId: session.sessionId,
    traceId: session.sessionId,
    title: session.title,
    ...(session.titleSource === "custom" ? { titleOverridden: true } : {}),
    workspacePath,
    ...(workspaceIdentity ? { workspaceIdentity } : {}),
    createdAt: session.createdAt,
    updatedAt: session.lastActivityAt,
    mode: "build",
    ...(session.model ? { model: session.model.modelId } : {}),
    ...(session.model ? { thoughtLevel: session.model.thought } : {}),
    ...(typeof session.unreadAt === "number" ? { unreadAt: session.unreadAt } : {}),
    status:
      session.phase === "error"
        ? "error"
        : session.phase === "running" || session.phase === "prewarming"
          ? "running"
          : "completed",
  };
}

function taskEvent(
  workspacePath: string,
  identity: string | undefined,
  taskId: string,
  reason: TaskEventReason,
  taskMeta?: NexTaskMeta,
): NexWorkspaceEvent {
  return {
    type: "workspace_task_list_changed",
    workspacePath,
    ...(identity ? { workspaceIdentity: identity } : {}),
    taskId,
    reason,
    ...(taskMeta ? { taskMeta } : {}),
  };
}

/**
 * One event per changed row, ordered: membership (archive/pin) → title → status →
 * activity/unread, then deletions. Membership wins over status/title so the UI's
 * membership refetch is driven by the strongest change in a multi-field mutation.
 */
export function diffTaskSnapshots(params: {
  workspacePath: string;
  workspaceIdentity?: string;
  previous: TaskSnapshot;
  next: TaskSnapshot;
  /** Sessions whose background completion wrote an unread marker in this refresh. */
  unreadSignalIds: ReadonlySet<string>;
}): NexWorkspaceEvent[] {
  const { workspacePath, workspaceIdentity, previous, next, unreadSignalIds } = params;
  const events: NexWorkspaceEvent[] = [];
  const emit = (taskId: string, reason: TaskEventReason, meta?: NexTaskMeta) => {
    events.push(taskEvent(workspacePath, workspaceIdentity, taskId, reason, meta));
  };

  for (const session of next.byId.values()) {
    const before = previous.byId.get(session.sessionId);
    const meta = toNexTaskMeta(workspacePath, session, workspaceIdentity);
    if (!before) {
      // An archived row appearing directly must not pop into the project list.
      if (session.archived !== true) emit(session.sessionId, "task_created", meta);
      continue;
    }
    if ((before.archived === true) !== (session.archived === true)) {
      emit(session.sessionId, session.archived ? "task_archived" : "task_unarchived", meta);
      continue;
    }
    if ((before.pinned === true) !== (session.pinned === true)) {
      emit(session.sessionId, session.pinned ? "task_pinned" : "task_unpinned", meta);
      continue;
    }
    if (before.title !== session.title) {
      emit(session.sessionId, "task_title_changed", meta);
      continue;
    }
    // Phase before unread: a background completion writes unreadAt and flips phase in the
    // same refresh, and the UI needs the status event (plus unreadSignal) for it.
    if (before.phase !== session.phase) {
      events.push({
        ...(taskEvent(
          workspacePath,
          workspaceIdentity,
          session.sessionId,
          "task_status_changed",
          meta,
        ) as NexWorkspaceTaskListChanged),
        // 只有后台终态才制造未读；前台收口（正在观看）不得挂红点。
        ...(unreadSignalIds.has(session.sessionId)
          ? { unreadSignal: "background_terminal" as const }
          : {}),
      });
      continue;
    }
    if (before.unreadAt !== session.unreadAt || before.lastActivityAt !== session.lastActivityAt) {
      emit(session.sessionId, "task_meta_changed", meta);
    }
  }
  for (const deletedId of next.deleted) {
    if (previous.deleted.includes(deletedId)) continue;
    emit(deletedId, "task_deleted");
  }
  return events;
}
