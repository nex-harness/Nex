import type { AgentSession } from "@earendil-works/pi-coding-agent";
import type { EngineUnreadState } from "../types.js";
import type { SessionMetaEntry, SessionMetaStore } from "./metaStore.js";

/**
 * Sidebar task state (docs/next/task-state.md), split out of `PiEngine` so the engine
 * class stays inside the file-size budget. The metadata store remains the single writer;
 * every function here runs its check-and-write inside the store's workspace queue.
 */
export interface TaskStateHooks {
  metadata: SessionMetaStore;
  /** The live pi session, when one is open (its abort/dispose is part of a delete). */
  sessionFor: (sessionId: string) => AgentSession | undefined;
  /** Aborts and drops the in-process session; no-op when it is not open. */
  closeSession: (sessionId: string) => void;
  /** Removes the pi transcript file; a missing file is not an error. */
  removeSessionFile: (workspacePath: string, sessionId: string) => Promise<void>;
  sessionsChanged: (workspacePath: string) => void;
}

export interface TaskStateApi {
  setTaskState(
    workspacePath: string,
    sessionId: string,
    patch: { pinned?: boolean; archived?: boolean },
  ): Promise<void>;
  setTaskUnread(
    workspacePath: string,
    sessionId: string,
    unreadAt: number | null,
    expectedUnreadAt?: number,
  ): Promise<EngineUnreadState>;
  deleteTaskIfArchived(workspacePath: string, sessionId: string): Promise<boolean>;
}

/** Binds the three `Engine` task-state methods to one engine's metadata store. */
export function createTaskStateApi(hooks: TaskStateHooks): TaskStateApi {
  return {
    setTaskState: (workspacePath, sessionId, patch) =>
      setTaskStateImpl(hooks, workspacePath, sessionId, patch),
    setTaskUnread: (workspacePath, sessionId, unreadAt, expectedUnreadAt) =>
      setTaskUnreadImpl(hooks, workspacePath, sessionId, unreadAt, expectedUnreadAt),
    deleteTaskIfArchived: (workspacePath, sessionId) =>
      deleteTaskIfArchivedImpl(hooks, workspacePath, sessionId),
  };
}

export async function setTaskStateImpl(
  hooks: TaskStateHooks,
  workspacePath: string,
  sessionId: string,
  patch: { pinned?: boolean; archived?: boolean },
): Promise<void> {
  const now = Date.now();
  await hooks.metadata.upsert(workspacePath, sessionId, {
    ...(patch.pinned !== undefined
      ? { pinned: patch.pinned, ...(patch.pinned ? { pinnedAt: now } : {}) }
      : {}),
    ...(patch.archived !== undefined
      ? { archived: patch.archived, ...(patch.archived ? { archivedAt: now } : {}) }
      : {}),
  });
  hooks.sessionsChanged(workspacePath);
}

/**
 * Unread write/clear. The compare-and-clear runs inside the store's workspace queue: a
 * stale read request must not erase a completion that landed after the user looked at the
 * row (原 tasks-index 用同一写事务保证这一点). Repeated "mark unread" keeps the existing
 * marker, because the UI also writes it back after `unreadSignal`; two sides advancing the
 * timestamp would ping-pong events.
 */
export async function setTaskUnreadImpl(
  hooks: TaskStateHooks,
  workspacePath: string,
  sessionId: string,
  unreadAt: number | null,
  expectedUnreadAt?: number,
): Promise<EngineUnreadState> {
  let wrote = false;
  const result = await hooks.metadata.update<EngineUnreadState>(
    workspacePath,
    sessionId,
    (current) => {
      if (!current || current.deleted) {
        return { next: null, result: { cleared: false } };
      }
      if (
        unreadAt === null &&
        typeof expectedUnreadAt === "number" &&
        current.unreadAt !== expectedUnreadAt
      ) {
        return {
          next: null,
          result: {
            cleared: false,
            ...(typeof current.unreadAt === "number" ? { unreadAt: current.unreadAt } : {}),
          },
        };
      }
      if (typeof unreadAt === "number" && typeof current.unreadAt === "number") {
        return { next: null, result: { unreadAt: current.unreadAt, cleared: false } };
      }
      const next: SessionMetaEntry = { ...current };
      // null 清空字段；number 写入新标记（undefined 不会走到这里）。
      if (typeof unreadAt === "number") next.unreadAt = unreadAt;
      else delete next.unreadAt;
      wrote = true;
      return {
        next,
        result: {
          cleared: unreadAt === null,
          ...(typeof next.unreadAt === "number" ? { unreadAt: next.unreadAt } : {}),
        },
      };
    },
  );
  if (wrote) hooks.sessionsChanged(workspacePath);
  return result;
}

/**
 * Soft delete (docs/next/task-state.md): one tombstone path for the v4 `deleteSession`
 * command and `nex-task.deleteTask`. The transcript file is removed; the metadata row
 * keeps the tombstone so a later pi restore cannot resurrect the row.
 */
export async function deleteTaskImpl(
  hooks: TaskStateHooks,
  workspacePath: string,
  sessionId: string,
): Promise<void> {
  if (hooks.sessionFor(sessionId)) hooks.closeSession(sessionId);
  await hooks.removeSessionFile(workspacePath, sessionId);
  await hooks.metadata.markDeleted(workspacePath, sessionId);
  hooks.sessionsChanged(workspacePath);
}

/**
 * Conditional tombstone used by the archived view's delete confirm. The archiving check
 * and the tombstone write share one queued read-modify-write, so an unarchive that lands
 * while the dialog is open makes the delete a no-op instead of silently winning.
 */
export async function deleteTaskIfArchivedImpl(
  hooks: TaskStateHooks,
  workspacePath: string,
  sessionId: string,
): Promise<boolean> {
  const deleted = await hooks.metadata.update<boolean>(workspacePath, sessionId, (current) => {
    if (!current || current.deleted || current.archived !== true) {
      return { next: null, result: false };
    }
    return { next: { ...current, deleted: true, lastActivityAt: Date.now() }, result: true };
  });
  if (!deleted) return false;

  if (hooks.sessionFor(sessionId)) hooks.closeSession(sessionId);
  await hooks.removeSessionFile(workspacePath, sessionId);
  hooks.sessionsChanged(workspacePath);
  return true;
}
