import type {
  CommandAck,
  CommandEnvelope,
  ConversationRowTarget,
} from "@nex/shared/nex-protocol-v4";
import type { AttachmentRef } from "@nex/shared/nex-protocol-v4";
import type { Engine } from "../engine/types.js";
import { normalizeAdmittedRefs } from "../attachments/admitted.js";
import type { AttachmentRegistry } from "../attachments/registry.js";
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
/** Kept for protocol readers/tests; the runner now serves attachment edits (spec A). */
export const ATTACHMENTS_UNSUPPORTED = "guard.attachmentsUnsupported";

export interface RewindCommandContext {
  engine: Engine;
  sessions: SessionRuntimeStore;
  /** Ref authority for edited attachments (spec A); absent in tests without uploads. */
  attachments?: AttachmentRegistry | null;
}

interface RewindTarget {
  userEntryId: string;
  /** Text re-sent for a retry (empty for an edit: the new text replaces it). */
  userText: string;
  /**
   * Attachment refs of the rewritten prompt (spec A edit semantics): omitted = keep the
   * original turn's refs, `[]` = none, non-empty = replace with the new set.
   */
  attachments?: import("@nex/shared/nex-protocol-v4").AttachmentRef[];
}

export async function handleEditUserQuery(
  ctx: RewindCommandContext,
  workspacePath: string,
  envelope: CommandEnvelope,
): Promise<CommandAck> {
  return runRewindCommand(ctx, workspacePath, envelope, {
    attempt: async (runtime, payload) => {
      const newText = typeof payload.newText === "string" ? payload.newText.trim() : "";
      const rawAttachments = payload.attachments;
      if (newText.length === 0 && !(Array.isArray(rawAttachments) && rawAttachments.length > 0)) {
        return reject(runtime, envelope, "proto.invalidPayload", "newText must not be empty");
      }
      const target = payload.target as ConversationRowTarget | undefined;
      const userRow = userRowForTarget(runtime.projection, target);
      if (!userRow) return unresolved(runtime, envelope, "edit target is not a user message");
      // Edit semantics (old CLI `fork-edit-retry.ts`): the UI always sends its current full
      // list, so `[]` removes every attachment and a non-empty list replaces the set;
      // only an omitted field keeps the original turn's refs.
      const attachments = Array.isArray(rawAttachments)
        ? normalizeAdmittedRefs(rawAttachments as AttachmentRef[])
        : userRow.kind === "userInput" && userRow.attachments
          ? userRow.attachments
          : [];
      // Latest-realUser-only: exactly the rule that renders the row's edit entry.
      if (deriveRowActions(runtime.projection).get(userRow.rowId)?.canEdit !== true) {
        return reject(
          runtime,
          envelope,
          LATEST_QUERY_EDIT_ONLY,
          "only the latest user message can be edited",
        );
      }
      // Spec B: restore the turn's files first, then cut the conversation and re-prompt.
      // Ordering: preview ─▶ blocked? ─yes─▶ ACK blocked + preview
      //            ─no─▶ apply (journaled) ─▶ nex.rewind ─▶ branch cut ─▶ prompt
      const userEntryId = entryIdOf(userRow);
      if (payload.workspaceMode === "rewind") {
        const preview = await ctx.engine.fileRewindPreview(
          workspacePath,
          runtime.sessionId,
          userEntryId,
        );
        // Ignored (bash-touched) files are reported but do not block: they are not part of
        // the restored set. Unsafe files and an empty safe set do (guard per the old CLI).
        if (!preview.canApply) {
          const reasonCode =
            preview.unsafeFiles.length > 0
              ? "guard.workspaceRewindUnsafeFiles"
              : preview.ignoredFiles.length > 0
                ? "guard.workspaceRewindIgnoredFiles"
                : "guard.workspaceRewindUnavailable";
          const ack = acceptedAck(envelope.commandId, runtime.projection.revision, {
            type: "editUserQuery",
            disposition: "blocked",
            sessionId: runtime.sessionId,
            reasonCode,
            preview,
          });
          runtime.acceptedCommands.set(envelope.commandId, ack);
          return ack;
        }
        return {
          target: { userEntryId, userText: "", attachments },
          newText,
          rewindFiles: true,
        };
      }
      return {
        target: { userEntryId, userText: "", attachments },
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
        // Retry re-sends the canonical input, attachments included (spec A).
        ...(userRow.kind === "userInput" && userRow.attachments
          ? { attachments: userRow.attachments }
          : {}),
      };
    },
  });
}

type AttemptResult = { target: RewindTarget; newText: string; rewindFiles?: boolean } | CommandAck;

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
  // Ref authority: the rewritten prompt's refs are admitted before the branch cut, so
  // the new row (and a read against it) sees exactly the edited set (spec A).
  if (outcome.target.attachments && outcome.target.attachments.length > 0) {
    ctx.attachments?.addAdmitted(runtime.sessionId, outcome.target.attachments);
  }
  return applyRewind(
    ctx,
    workspacePath,
    runtime,
    envelope,
    outcome.target,
    outcome.newText,
    outcome.rewindFiles,
  );
}

async function applyRewind(
  ctx: RewindCommandContext,
  workspacePath: string,
  runtime: SessionRuntime,
  envelope: CommandEnvelope,
  target: RewindTarget,
  newText: string,
  rewindFiles = false,
): Promise<CommandAck> {
  const state = runtime.projection;
  const userRow = state.rows.find(
    (row) => row.kind === "userInput" && row.entityId === userEntityId(target.userEntryId),
  );
  const header = userRow ? turnHeaderRowForUserRow(state, userRow.rowId) : null;
  // Lineage (research §4): the rewritten turn keeps the command that started the original
  // prompt, so the UI's command attribution does not fork on an edit.
  const sourceCommandId = header ? sourceCommandOf(state, header.rowId) : envelope.commandId;
  const cut = () => {
    const current = runtime.projection;
    const liveUser = current.rows.find(
      (row) => row.kind === "userInput" && row.entityId === userEntityId(target.userEntryId),
    );
    const liveHeader = liveUser ? turnHeaderRowForUserRow(current, liveUser.rowId) : null;
    runtime.projection = bump({
      ...(liveHeader
        ? cutRowsFrom(current, { fromRowId: liveHeader.rowId, turnId: liveHeader.turnId })
        : current),
      promptLineage: {
        sourceCommandId: sourceCommandId ?? "",
        generation: current.promptLineage.generation,
      },
      lastError: null,
    });
    runtime.throttle.request();
  };

  if (rewindFiles) {
    // Two phases (spec B ordering): restore every file first (journaled, compensating);
    // only when all writes are down does the branch cut commit, so a failed restore can
    // never leave the conversation rewound without its files. A throw here compensates
    // every write and the command answers `blocked` with the live preview.
    const applied = await ctx.engine.applyFileRewind(
      workspacePath,
      runtime.sessionId,
      target.userEntryId,
      { sourceCommandId: sourceCommandId ?? "" },
    );
    if (!applied.applied) {
      runtime.throttle.request();
      const live = await ctx.engine.fileRewindPreview(
        workspacePath,
        runtime.sessionId,
        target.userEntryId,
      );
      const ack = acceptedAck(envelope.commandId, runtime.projection.revision, {
        type: "editUserQuery",
        disposition: "blocked",
        sessionId: runtime.sessionId,
        reasonCode: "guard.workspaceRewindApplyConflict",
        preview: live,
      });
      runtime.acceptedCommands.set(envelope.commandId, ack);
      return ack;
    }
    cut();
    try {
      await ctx.engine.rewind(runtime.sessionId, {
        target: { userEntryId: target.userEntryId, text: target.userText },
        // Branch cut only: the new text starts below, after the files are restored.
        newText: "",
      });
    } catch (error) {
      // Files stay restored (the user asked for them); the turn is reported as failed.
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
    // The re-prompt starts from the empty-text cut, whose single `cut` transition keeps the
    // live prompt lineage intact (no second tick moves the generation).
    await startRewindPrompt(ctx, runtime, target, newText);
    return rememberRewind(envelope, runtime, runtime.projection.revision);
  }

  cut();
  try {
    await ctx.engine.rewind(runtime.sessionId, {
      target: { userEntryId: target.userEntryId, text: target.userText },
      newText,
      attachments: target.attachments,
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
  return rememberRewind(envelope, runtime, runtime.projection.revision);
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

/** Prompts the new text after a cut-only rewind (the engine cut returns without starting). */
async function startRewindPrompt(
  ctx: RewindCommandContext,
  runtime: SessionRuntime,
  target: RewindTarget,
  newText: string,
): Promise<void> {
  try {
    await ctx.engine.rewind(runtime.sessionId, {
      target: { userEntryId: target.userEntryId, text: target.userText },
      newText,
      attachments: target.attachments,
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
  }
}

/** Stores the ACK for duplicate replay and builds it (edit ACK carries the disposition). */
function rememberRewind(
  envelope: CommandEnvelope,
  runtime: SessionRuntime,
  revision: number,
): CommandAck {
  const ack =
    envelope.type === "editUserQuery"
      ? acceptedAck(envelope.commandId, revision, {
          type: "editUserQuery",
          disposition: "rewind",
          sessionId: runtime.sessionId,
        })
      : acceptedAck(envelope.commandId, revision);
  runtime.acceptedCommands.set(envelope.commandId, ack);
  return ack;
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
