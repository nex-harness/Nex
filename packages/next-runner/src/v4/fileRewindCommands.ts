import type {
  CommandAck,
  CommandEnvelope,
  ConversationRowTarget,
} from "@nex/shared/nex-protocol-v4";
import type { Engine } from "../engine/types.js";
import { deriveRowActions } from "../projection/rowActions.js";
import { markTurnReverted } from "../projection/turnCheckpointProjection.js";
import { acceptedAck, rejectedAck } from "./envelopes.js";
import { userEntryForTarget } from "./fileRewindMethods.js";
import type { SessionRuntime, SessionRuntimeStore } from "./sessionStore.js";

/**
 * `applyFileRewind {target}` (spec B): the Undo button on a turn's changed-files panel.
 *
 * ```
 * guards (canRewindFiles / not running / no compact lock) ─▶ resolve the turn's user entry
 *   ─▶ engine.applyFileRewind (journaled atomic restore, compensating on failure)
 *   ─▶ nex.rewind entry (engine) ─▶ header state="reverted"
 * ```
 *
 * The conversation is never cut here; only the files are restored. Any refusal answers
 * `rejected` with the guard code (the UI keeps the dialog open with the message).
 */
export const REWIND_FILES_UNAVAILABLE = "guard.workspaceRewindUnavailable";
export const REWIND_FILES_UNSAFE = "guard.workspaceRewindUnsafeFiles";
export const REWIND_FILES_IGNORED = "guard.workspaceRewindIgnoredFiles";
export const REWIND_FILES_APPLY_CONFLICT = "guard.workspaceRewindApplyConflict";

export interface FileRewindCommandContext {
  engine: Engine;
  sessions: SessionRuntimeStore;
}

export async function handleApplyFileRewind(
  ctx: FileRewindCommandContext,
  workspacePath: string,
  envelope: CommandEnvelope,
): Promise<CommandAck> {
  const sessionId = envelope.sessionId;
  if (!sessionId) {
    return rejectedAck(
      envelope.commandId,
      0,
      "session_required",
      "applyFileRewind needs sessionId",
    );
  }
  const runtime = ctx.sessions.ensure(workspacePath, sessionId);
  await runtime.ready;
  if (compactionLocked(runtime)) {
    return rejectedAck(
      envelope.commandId,
      runtime.projection.revision,
      "compactOperationLock",
      "a compaction is running",
    );
  }
  const payload = (envelope.payload ?? {}) as { target?: ConversationRowTarget };
  const userEntryId = await userEntryForTarget(
    runtime,
    payload.target?.entityId,
    payload.target?.rowId,
  );
  if (!userEntryId) {
    return rejectedAck(
      envelope.commandId,
      runtime.projection.revision,
      REWIND_FILES_UNAVAILABLE,
      "the target turn is not in the transcript",
    );
  }
  const userRow = runtime.projection.rows.find(
    (row) => row.kind === "userInput" && row.entityId === `pi:${userEntryId}`,
  );
  const headerRowId = userRow ? headerRowIdOf(runtime, userRow.turnId) : undefined;
  const headerActions =
    headerRowId === undefined ? undefined : deriveRowActions(runtime.projection).get(headerRowId);
  if (headerActions?.canRewindFiles !== true) {
    return rejectedAck(
      envelope.commandId,
      runtime.projection.revision,
      REWIND_FILES_UNAVAILABLE,
      "this turn cannot be undone now",
    );
  }
  const result = await ctx.engine.applyFileRewind(workspacePath, sessionId, userEntryId, {
    sourceCommandId: envelope.commandId,
  });
  if (!result.applied) {
    const reasonCode = result.preview.unsafeFiles.some(
      (file) => file.reason === "external_modified",
    )
      ? REWIND_FILES_UNSAFE
      : result.preview.ignoredFiles.length > 0
        ? REWIND_FILES_IGNORED
        : result.preview.safeFiles.length === 0
          ? REWIND_FILES_UNAVAILABLE
          : REWIND_FILES_APPLY_CONFLICT;
    return rejectedAck(
      envelope.commandId,
      runtime.projection.revision,
      reasonCode,
      result.response,
    );
  }
  runtime.projection = markTurnReverted(
    runtime.projection,
    userEntryId,
    result.preview.safeFiles.length,
  );
  runtime.throttle.request();
  const ack = acceptedAck(envelope.commandId, runtime.projection.revision, {
    type: "applyFileRewind",
    applied: true,
    preview: result.preview,
    response: result.response,
  });
  runtime.acceptedCommands.set(envelope.commandId, ack);
  return ack;
}

function headerRowIdOf(runtime: SessionRuntime, turnId: string): number | undefined {
  return runtime.projection.rows.find((row) => row.kind === "turnHeader" && row.turnId === turnId)
    ?.rowId;
}

function compactionLocked(runtime: SessionRuntime): boolean {
  return (
    runtime.queue.compactQueued() || Object.keys(runtime.projection.compactOperations).length > 0
  );
}
