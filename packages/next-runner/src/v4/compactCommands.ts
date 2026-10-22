import type { CommandAck, CommandEnvelope } from "@nex/shared/nex-protocol-v4";
import { QueueAdmissionError } from "./queue.js";
import { acceptedAck, rejectedAck } from "./envelopes.js";
import { messageOf } from "./queueItem.js";
import type { SessionRuntimeStore } from "./sessionStore.js";

/**
 * `compact` command (docs/next/conversation-features.md decision 7, research §1).
 *
 * - not CAS (an input maintenance command; `sourceCommandId` is the idempotency boundary);
 * - running/queued compaction → `rejected compactOperationLock` (old CLI dedupe);
 * - busy (run streaming, a promotion in flight) → a queued `kind:"compact"` item and an
 *   `accepted` ACK; the drain runs it when idle, in the same FIFO as user text;
 * - idle → start now: the marker row is `running` before the ACK, so the UI shimmers.
 *
 * The runner mark, like the old CLI, is a prompt-free operation: `/compact` never becomes
 * a user row.
 */
export interface CompactCommandContext {
  sessions: SessionRuntimeStore;
}

export async function handleCompact(
  ctx: CompactCommandContext,
  workspacePath: string,
  envelope: CommandEnvelope,
): Promise<CommandAck> {
  const sessionId = envelope.sessionId;
  if (!sessionId) {
    return rejectedAck(envelope.commandId, 0, "session_required", "compact needs sessionId");
  }
  const runtime = ctx.sessions.ensure(workspacePath, sessionId);
  await runtime.ready;
  try {
    const admission = runtime.queue.admitCompact(envelope);
    if (admission.delivery === "startNow") {
      // Marker + engine call are the compact owner's; it never rejects for pi failures
      // (the terminal outcome lands on the marker row instead).
      void runtime.compact.runManual(admission.sourceCommandId);
    }
    runtime.throttle.request();
    const ack = acceptedAck(envelope.commandId, runtime.projection.revision);
    runtime.acceptedCommands.set(envelope.commandId, ack);
    return ack;
  } catch (error) {
    return rejectedAck(
      envelope.commandId,
      runtime.projection.revision,
      error instanceof QueueAdmissionError ? error.reasonCode : "compact_failed",
      messageOf(error),
    );
  }
}
