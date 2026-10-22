import type { CommandAck, CommandEnvelope } from "@nex/shared/nex-protocol-v4";
import { acceptedAck, noopAck, rejectedAck } from "./envelopes.js";
import type { SessionQueue } from "./queue.js";
import type { SessionRuntimeStore } from "./sessionStore.js";

/**
 * `deleteQueueItem` / `editQueueItem` / `reorderQueueItem` / `sendQueuedNow` / `setAutoDrain`
 * (docs/next/conversation-features.md decision 4, research §5). Extracted from
 * `CommandHandler` for the file budget; ownership is unchanged — every handler only reads a
 * `SessionQueue` and returns an ACK, and CAS already ran in the dispatcher.
 *
 * Error semantics match the old CLI: an id that vanished between snapshot and click is
 * `noop queue.itemMissing` (a concurrent drain or another client's delete), never a failure.
 */
export interface QueueCommandContext {
  sessions: SessionRuntimeStore;
}

export async function handleDeleteQueueItem(
  ctx: QueueCommandContext,
  workspacePath: string,
  envelope: CommandEnvelope,
): Promise<CommandAck> {
  return withQueue(ctx, workspacePath, envelope, (queue) => {
    const { queueItemId } = envelope.payload as { queueItemId?: string };
    return queueItemId !== undefined && queue.delete(queueItemId);
  });
}

/** Protocol-only: the UI restores the composer instead of dispatching this (research §5). */
export async function handleEditQueueItem(
  ctx: QueueCommandContext,
  workspacePath: string,
  envelope: CommandEnvelope,
): Promise<CommandAck> {
  const missing = missingSession(envelope);
  if (missing) return missing;
  const runtime = ctx.sessions.ensure(workspacePath, envelope.sessionId as string);
  await runtime.ready;
  const { queueItemId, newText } = envelope.payload as {
    queueItemId?: string;
    newText?: string;
  };
  if (!queueItemId || typeof newText !== "string") {
    return rejectedAck(
      envelope.commandId,
      runtime.projection.revision,
      "invalid_payload",
      "queueItemId and newText are required",
    );
  }
  const result = runtime.queue.edit(queueItemId, newText);
  runtime.throttle.request();
  const revision = runtime.projection.revision;
  if (result === "edited") return acceptedAck(envelope.commandId, revision);
  if (result === "notEditable") {
    return rejectedAck(
      envelope.commandId,
      revision,
      "guard.queueItemNotEditable",
      "queue item is not editable",
    );
  }
  return noopAck(envelope.commandId, revision, "queue.itemMissing");
}

/** `beforeQueueItemId: null` = tail; an unknown item or anchor is a no-op, not an error. */
export async function handleReorderQueueItem(
  ctx: QueueCommandContext,
  workspacePath: string,
  envelope: CommandEnvelope,
): Promise<CommandAck> {
  return withQueue(ctx, workspacePath, envelope, (queue) => {
    const { queueItemId, beforeQueueItemId } = envelope.payload as {
      queueItemId?: string;
      beforeQueueItemId?: string | null;
    };
    return queueItemId !== undefined && queue.reorder(queueItemId, beforeQueueItemId ?? null);
  });
}

/** Abort the active run, wait for idle, then reserve → promote the chosen item. */
export async function handleSendQueuedNow(
  ctx: QueueCommandContext,
  workspacePath: string,
  envelope: CommandEnvelope,
): Promise<CommandAck> {
  const missing = missingSession(envelope);
  if (missing) return missing;
  const runtime = ctx.sessions.ensure(workspacePath, envelope.sessionId as string);
  await runtime.ready;
  const { queueItemId } = envelope.payload as { queueItemId?: string };
  const result = queueItemId ? await runtime.queue.sendQueuedNow(queueItemId) : "missing";
  runtime.throttle.request();
  const revision = runtime.projection.revision;
  if (result === "promoted") return acceptedAck(envelope.commandId, revision);
  if (result === "busy") {
    return rejectedAck(
      envelope.commandId,
      revision,
      "guard.queueItemReserved",
      "queue item is already being promoted",
    );
  }
  return noopAck(envelope.commandId, revision, "queue.itemMissing");
}

/** `setAutoDrain(true)` re-arms a paused queue and drains immediately when idle. */
export async function handleSetAutoDrain(
  ctx: QueueCommandContext,
  workspacePath: string,
  envelope: CommandEnvelope,
): Promise<CommandAck> {
  const missing = missingSession(envelope);
  if (missing) return missing;
  const runtime = ctx.sessions.ensure(workspacePath, envelope.sessionId as string);
  await runtime.ready;
  const { autoDrain } = envelope.payload as { autoDrain?: boolean };
  if (typeof autoDrain !== "boolean") {
    return rejectedAck(
      envelope.commandId,
      runtime.projection.revision,
      "invalid_payload",
      "autoDrain must be a boolean",
    );
  }
  await runtime.queue.setAutoDrain(autoDrain);
  runtime.throttle.request();
  const revision = runtime.projection.revision;
  return acceptedAck(envelope.commandId, revision);
}

async function withQueue(
  ctx: QueueCommandContext,
  workspacePath: string,
  envelope: CommandEnvelope,
  operation: (queue: SessionQueue, revision: number) => boolean,
): Promise<CommandAck> {
  const missing = missingSession(envelope);
  if (missing) return missing;
  const runtime = ctx.sessions.ensure(workspacePath, envelope.sessionId as string);
  await runtime.ready;
  const changed = operation(runtime.queue, runtime.projection.revision);
  runtime.throttle.request();
  const revision = runtime.projection.revision;
  return changed
    ? acceptedAck(envelope.commandId, revision)
    : noopAck(envelope.commandId, revision, "queue.itemMissing");
}

function missingSession(envelope: CommandEnvelope): CommandAck | undefined {
  return envelope.sessionId
    ? undefined
    : rejectedAck(
        envelope.commandId,
        0,
        "session_required",
        `${String(envelope.type)} needs sessionId`,
      );
}
