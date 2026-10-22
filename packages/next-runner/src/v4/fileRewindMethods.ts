import type {
  V4ConversationFileChangesParams,
  V4ConversationFileChangesResult,
  V4ConversationFileRewindPreviewParams,
  V4ConversationFileRewindPreviewResult,
} from "@nex/shared/nex-protocol-v4";
import type { Engine } from "../engine/types.js";
import type { Logger } from "../logger.js";
import type { SessionRuntimeStore } from "./sessionStore.js";

/**
 * `conversationFileChangesV4` / `conversationFileRewindPreviewV4` (spec B).
 *
 * Both are read-only queries. They resolve the target row to its pi user entry through the
 * hydrated projection (so a stale rowId is never trusted), and answer `{files:0}` / an empty
 * preview for turns without checkpoints — the UI then hides the summary and Undo.
 */
export type FileQueryContext = () => {
  engine: Engine;
  sessions: SessionRuntimeStore;
  logger: Logger;
};

export async function conversationFileChanges(
  ctx: FileQueryContext,
  workspacePath: string,
  params: V4ConversationFileChangesParams,
): Promise<V4ConversationFileChangesResult> {
  const { engine, sessions } = ctx();
  const runtime = sessions.ensure(workspacePath, params.sessionId);
  await runtime.ready;
  const userEntryId = await userEntryForTarget(
    runtime,
    params.target.entityId,
    params.target.rowId,
  );
  if (!userEntryId) return { files: 0, additions: 0, deletions: 0, items: [] };
  const changes = await engine.fileChanges(workspacePath, params.sessionId, userEntryId);
  return {
    files: changes.files,
    additions: changes.additions,
    deletions: changes.deletions,
    state: changes.state,
    items: changes.items,
  };
}

export async function conversationFileRewindPreview(
  ctx: FileQueryContext,
  workspacePath: string,
  params: V4ConversationFileRewindPreviewParams,
): Promise<V4ConversationFileRewindPreviewResult> {
  const { engine, sessions } = ctx();
  const runtime = sessions.ensure(workspacePath, params.sessionId);
  await runtime.ready;
  const userEntryId = await userEntryForTarget(
    runtime,
    params.target.entityId,
    params.target.rowId,
  );
  if (!userEntryId) return { canApply: false, ignoredFiles: [], safeFiles: [], unsafeFiles: [] };
  return engine.fileRewindPreview(workspacePath, params.sessionId, userEntryId);
}

/**
 * pi user entry id of a turn from any row target of that turn (`turnHeader`, `userInput`,
 * tool/assistant rows). Restored and live rows carry `pi:<userEntryId>` on the user row;
 * `turnHeader` rows carry `pi:turn:<entry>`.
 */
export async function userEntryForTarget(
  runtime: { projection: import("../projection/state.js").ProjectionState },
  entityId: string | undefined,
  rowId: number | undefined,
): Promise<string | undefined> {
  const rows = runtime.projection.rows;
  const target =
    (entityId ? rows.find((row) => row.entityId === entityId) : undefined) ??
    (rowId !== undefined ? rows.find((row) => row.rowId === rowId) : undefined);
  if (!target) return undefined;
  if (target.kind === "userInput") {
    return target.entityId?.startsWith("pi:") ? target.entityId.slice(3) : undefined;
  }
  if (target.kind === "turnHeader") {
    return target.entityId?.startsWith("pi:turn:")
      ? target.entityId.slice("pi:turn:".length)
      : undefined;
  }
  // Any other row of the turn: its user row is the turn's anchor.
  const user = rows.find((row) => row.turnId === target.turnId && row.kind === "userInput");
  if (!user || user.kind !== "userInput" || !user.entityId?.startsWith("pi:")) return undefined;
  return user.entityId.slice(3);
}
