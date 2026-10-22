import type {
  CommandAck,
  CommandEnvelope,
  ConversationRowTarget,
  V4ConversationFileRewindPreviewResult,
} from "@nex/shared/nex-protocol-v4";
import type { Engine } from "../engine/types.js";
import { entryIdFromEntityId, userEntityId } from "../projection/entityIds.js";
import { deriveRowActions } from "../projection/rowActions.js";
import { cutRowsFrom, turnHeaderRowForUserRow } from "../projection/rewindProjection.js";
import { bump, type ProjectedRow, type ProjectionState } from "../projection/state.js";
import { acceptedAck, rejectedAck } from "./envelopes.js";
import { messageOf } from "./queueItem.js";
import type { SessionRuntime, SessionRuntimeStore } from "./sessionStore.js";

/**
 * `editUserQuery` / `retryTurn` — the shared rewind primitive
 * (docs/next/conversation-features.md decision 5, research §3/§4).
 *
 * Ordering is the whole design:
 * ```
 * guards (target / latest-only / compact lock) ─▶ cut live rows from the turn header
 *   ─▶ engine.rewind: abort+wait idle → branch(parent of user entry) → refreshContext
 *   ─▶ prompt(new text) in the SAME session, lineage = the original command
 * ```
 * "Rows after the cut disappear" needs no `row.removed` delta: snapshots are projections of
 * the row set, so dropping the rows is the removal. Restore agrees because `getBranch()`
 * walks from the branch leaf; rowIds stay monotonic because `nextRowId` is never lowered.
 */

export const LATEST_QUERY_EDIT_ONLY = "guard.latestQueryEditOnly";
export const LATEST_ASSISTANT_RETRY_ONLY = "guard.latestAssistantRetryOnly";
export const REWIND_TARGET_UNRESOLVED = "guard.rewindTargetUnresolved";
export const ATTACHMENTS_UNSUPPORTED = "guard.attachmentsUnsupported";

export interface RewindCommandContext {
  engine: Engine;
  sessions: SessionRuntimeStore;
}

interface RewindTarget {
  userEntryId: string;
  /** Text re-sent for a retry (empty for an edit: the new text replaces it). */
  userText: string;
}

export async function handleEditUserQuery(
  ctx: RewindCommandContext,
  workspacePath: string,
  envelope: CommandEnvelope,
): Promise<CommandAck> {
  return runRewindCommand(ctx, workspacePath, envelope, {
    attempt: (runtime, payload) => {
      const newText = typeof payload.newText === "string" ? payload.newText.trim() : "";
      if (newText.length === 0) {
        return reject(runtime, envelope, "proto.invalidPayload", "newText must not be empty");
      }
      // Attachments land in batch 3. Omitted (= keep) and `[]` (= none) are satisfiable
      // today; a non-empty list is refused instead of silently dropped.
      if (Array.isArray(payload.attachments) && payload.attachments.length > 0) {
        return reject(
          runtime,
          envelope,
          ATTACHMENTS_UNSUPPORTED,
          "editing attachments is not supported yet",
        );
      }
      const target = payload.target as ConversationRowTarget | undefined;
      const userRow = userRowForTarget(runtime.projection, target);
      if (!userRow) return unresolved(runtime, envelope, "edit target is not a user message");
      // Latest-realUser-only: exactly the rule that renders the row's edit entry.
      if (deriveRowActions(runtime.projection).get(userRow.rowId)?.canEdit !== true) {
        return reject(
          runtime,
          envelope,
          LATEST_QUERY_EDIT_ONLY,
          "only the latest user message can be edited",
        );
      }
      if (payload.workspaceMode === "rewind") {
        // No pi equivalent for file checkpoints (batch 3): answer `blocked` with a valid
        // synthetic preview so the UI can offer conversation-only rewind.
        const ack = acceptedAck(envelope.commandId, runtime.projection.revision, {
          type: "editUserQuery",
          disposition: "blocked",
          sessionId: runtime.sessionId,
          reasonCode: "guard.workspaceRewindUnavailable",
          preview: emptyPreview(),
        });
        runtime.acceptedCommands.set(envelope.commandId, ack);
        return ack;
      }
      return {
        target: { userEntryId: entryIdOf(userRow), userText: "" },
        newText,
      };
    },
  });
}

export async function handleRetryTurn(
  ctx: RewindCommandContext,
  workspacePath: string,
  envelope: CommandEnvelope,
): Promise<CommandAck> {
  return runRewindCommand(ctx, workspacePath, envelope, {
    attempt: async (runtime, payload) => {
      const target = payload.target as ConversationRowTarget | undefined;
      const entryId = entryIdFromEntityId(target?.entityId);
      if (!entryId) {
        return unresolved(runtime, envelope, "retry target has no pi entry id");
      }
      if (deriveRowActions(runtime.projection).get(target?.rowId ?? -1)?.canRetry !== true) {
        return reject(
          runtime,
          envelope,
          LATEST_ASSISTANT_RETRY_ONLY,
          "only the latest assistant reply can be retried",
        );
      }
      // Canonical user intent, read BEFORE the cut: the assistant entry's owning user
      // message on the active branch (pi has no assistant→user field).
      const cause = await ctx.engine.userMessageBefore(runtime.sessionId, entryId);
      if (!cause) {
        return unresolved(runtime, envelope, "retry target is not a stable assistant message");
      }
      const userRow = runtime.projection.rows.find(
        (row) => row.kind === "userInput" && row.entityId === userEntityId(cause.userEntryId),
      );
      if (!userRow) {
        return unresolved(runtime, envelope, "the reply's user message is not in the transcript");
      }
      return {
        target: { userEntryId: cause.userEntryId, userText: cause.text },
        newText: cause.text,
      };
    },
  });
}

type AttemptResult = { target: RewindTarget; newText: string } | CommandAck;

/** Guards shared by both commands, then the cut + engine rewind + lineage. */
async function runRewindCommand(
  ctx: RewindCommandContext,
  workspacePath: string,
  envelope: CommandEnvelope,
  spec: {
    attempt: (
      runtime: SessionRuntime,
      payload: Record<string, unknown>,
    ) => AttemptResult | Promise<AttemptResult>;
  },
): Promise<CommandAck> {
  const sessionId = envelope.sessionId;
  if (!sessionId) {
    return rejectedAck(
      envelope.commandId,
      0,
      "session_required",
      `${envelope.type} needs sessionId`,
    );
  }
  const runtime = ctx.sessions.ensure(workspacePath, sessionId);
  await runtime.ready;
  // A compaction (running OR queued) locks every rewind: the cut would abort it.
  const locked = compactionLocked(runtime);
  if (locked) {
    return reject(runtime, envelope, "compactOperationLock", "a compaction is running");
  }
  const outcome = await spec.attempt(runtime, (envelope.payload ?? {}) as Record<string, unknown>);
  if ("status" in outcome) return outcome;
  return applyRewind(ctx, runtime, envelope, outcome.target, outcome.newText);
}

async function applyRewind(
  ctx: RewindCommandContext,
  runtime: SessionRuntime,
  envelope: CommandEnvelope,
  target: RewindTarget,
  newText: string,
): Promise<CommandAck> {
  const state = runtime.projection;
  const userRow = state.rows.find(
    (row) => row.kind === "userInput" && row.entityId === userEntityId(target.userEntryId),
  );
  const header = userRow ? turnHeaderRowForUserRow(state, userRow.rowId) : null;
  // Lineage (research §4): the rewritten turn keeps the command that started the original
  // prompt, so the UI's command attribution does not fork on an edit.
  const sourceCommandId = header ? sourceCommandOf(state, header.rowId) : envelope.commandId;

  runtime.projection = bump({
    ...(header ? cutRowsFrom(state, { fromRowId: header.rowId, turnId: header.turnId }) : state),
    promptLineage: {
      sourceCommandId: sourceCommandId ?? "",
      generation: state.promptLineage.generation,
    },
    lastError: null,
  });
  runtime.throttle.request();

  try {
    await ctx.engine.rewind(runtime.sessionId, {
      target: { userEntryId: target.userEntryId, text: target.userText },
      newText,
    });
  } catch (error) {
    runtime.projection = bump({
      ...runtime.projection,
      lastError: {
        code: "rewind_failed",
        message: messageOf(error),
        at: Date.now(),
        recoverable: true,
        source: "runtime",
      },
    });
    runtime.throttle.request();
    return reject(runtime, envelope, "rewind_failed", messageOf(error));
  }
  runtime.throttle.request();
  const ack =
    envelope.type === "editUserQuery"
      ? acceptedAck(envelope.commandId, runtime.projection.revision, {
          type: "editUserQuery",
          disposition: "rewind",
          sessionId: runtime.sessionId,
        })
      : acceptedAck(envelope.commandId, runtime.projection.revision);
  runtime.acceptedCommands.set(envelope.commandId, ack);
  return ack;
}

/** `userInput` row for a target; the entityId is authoritative (a rowId may be stale). */
function userRowForTarget(
  projection: ProjectionState,
  target: ConversationRowTarget | undefined,
): ProjectedRow | undefined {
  if (!target) return undefined;
  const entryId = entryIdFromEntityId(target.entityId);
  if (entryId) {
    const byEntity = projection.rows.find(
      (row) => row.kind === "userInput" && row.entityId === userEntityId(entryId),
    );
    if (byEntity) return byEntity;
  }
  const byRow = projection.rows.find((row) => row.rowId === target.rowId);
  return byRow?.kind === "userInput" && byRow.entityId ? byRow : undefined;
}

function entryIdOf(row: ProjectedRow): string {
  const entryId = entryIdFromEntityId(row.entityId);
  if (!entryId) throw new Error("user row lost its entity id");
  return entryId;
}

function sourceCommandOf(state: ProjectionState, headerRowId: number): string | undefined {
  const row = state.rows.find((item) => item.rowId === headerRowId);
  return row?.kind === "turnHeader" ? row.sourceCommandId : undefined;
}

/** A rewind aborts the run; a compaction (running or queued) must not be interrupted. */
function compactionLocked(runtime: SessionRuntime): boolean {
  return (
    runtime.queue.compactQueued() || Object.keys(runtime.projection.compactOperations).length > 0
  );
}

function reject(
  runtime: SessionRuntime,
  envelope: CommandEnvelope,
  reasonCode: string,
  message: string,
): CommandAck {
  return rejectedAck(envelope.commandId, runtime.projection.revision, reasonCode, message);
}

function unresolved(
  runtime: SessionRuntime,
  envelope: CommandEnvelope,
  message: string,
): CommandAck {
  return reject(runtime, envelope, REWIND_TARGET_UNRESOLVED, message);
}

/** No file checkpoints yet: `canApply:false` with no file entries is a valid preview. */
function emptyPreview(): V4ConversationFileRewindPreviewResult {
  return { canApply: false, ignoredFiles: [], safeFiles: [], unsafeFiles: [] };
}
