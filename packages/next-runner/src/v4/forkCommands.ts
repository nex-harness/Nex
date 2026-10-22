import type { CommandAck, CommandEnvelope } from "@nex/shared/nex-protocol-v4";
import type { Engine } from "../engine/types.js";
import { entryIdFromEntityId } from "../projection/entityIds.js";
import { deriveRowActions } from "../projection/rowActions.js";
import { acceptedAck, rejectedAck } from "./envelopes.js";
import { messageOf } from "./queueItem.js";
import type { SessionRuntimeStore } from "./sessionStore.js";

/**
 * `forkAssistant` (docs/next/conversation-features.md decision 6, research §2).
 *
 * Guards (old CLI `product-projection.ts:884-935` semantics): target is the last text
 * segment of a `completedSuccess` turn (exactly the `canFork` rule the UI renders from),
 * a compaction is not running, and the target is on the parent's active branch. Then the
 * engine copies the conversation up to that entry into a new session file and registers
 * the child; the ACK is `{type:"forkAssistant", sessionId}` and the pane switches to it.
 * The parent projection is not touched — a fork only adds a session.
 */
export interface ForkCommandContext {
  engine: Engine;
  sessions: SessionRuntimeStore;
}

export async function handleForkAssistant(
  ctx: ForkCommandContext,
  workspacePath: string,
  envelope: CommandEnvelope,
): Promise<CommandAck> {
  const sessionId = envelope.sessionId;
  if (!sessionId) {
    return rejectedAck(envelope.commandId, 0, "session_required", "forkAssistant needs sessionId");
  }
  const runtime = ctx.sessions.ensure(workspacePath, sessionId);
  await runtime.ready;
  const payload = envelope.payload as { target?: { rowId?: number; entityId?: string } };
  const target = payload.target;
  const entryId = entryIdFromEntityId(target?.entityId);
  if (!target || target.rowId === undefined || !entryId) {
    return rejectedAck(
      envelope.commandId,
      runtime.projection.revision,
      "guard.forkTargetNotStable",
      "fork target has no pi entry id",
    );
  }
  if (Object.keys(runtime.projection.compactOperations).length > 0) {
    return rejectedAck(
      envelope.commandId,
      runtime.projection.revision,
      "compactOperationLock",
      "a compaction is running",
    );
  }
  // The fork row's own affordance is the stable-target rule: last text segment of a
  // successful turn with a resolved entry id.
  const canFork = deriveRowActions(runtime.projection).get(target.rowId)?.canFork === true;
  if (!canFork) {
    return rejectedAck(
      envelope.commandId,
      runtime.projection.revision,
      "guard.forkTargetNotStable",
      "the reply is not a stable completed turn",
    );
  }
  const parentTitle = runtime.projection.title.trim();
  const title = `Fork of ${parentTitle || "untitled"}`;
  try {
    const child = await ctx.engine.forkAssistant(workspacePath, sessionId, {
      targetEntryId: entryId,
      parentSessionId: sessionId,
      parentRowId: target.rowId,
      title,
    });
    const ack = acceptedAck(envelope.commandId, runtime.projection.revision, {
      type: "forkAssistant",
      sessionId: child.sessionId,
    });
    runtime.acceptedCommands.set(envelope.commandId, ack);
    return ack;
  } catch (error) {
    return rejectedAck(
      envelope.commandId,
      runtime.projection.revision,
      "fork_failed",
      messageOf(error),
    );
  }
}
