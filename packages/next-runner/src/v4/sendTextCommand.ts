import type { CommandAck, CommandEnvelope, ModelSelection } from "@nex/shared/nex-protocol-v4";
import { normalizeAdmittedRefs } from "../attachments/admitted.js";
import type { AttachmentRegistry } from "../attachments/registry.js";
import { normalizeMode } from "../engine/permissions/policy.js";
import { acceptedAck, inputAcceptedResult, rejectedAck } from "./envelopes.js";
import { QueueAdmissionError } from "./queue.js";
import type { SessionRuntime, SessionRuntimeStore } from "./sessionStore.js";

/**
 * `sendText` admission (docs/next/conversation-features.md decision 4). Extracted from
 * `CommandHandler` for the file budget; the authority is still the session's
 * `SessionQueue`, which decides startNow / queue / guide and owns the resulting item.
 *
 * The permission mode is echoed into the projection only for a `startNow`: a queued item
 * carries its own mode and applies it when promoted. Attachments (spec A) are registered
 * with ref authority before admission, so the queued item and the eventual row carry them
 * and a read of one of those refs passes the session's listing check.
 */
export interface SendTextCommandContext {
  sessions: SessionRuntimeStore;
  /** Engine mode readback, so createSession/approval paths stay consistent. */
  permissionMode: (sessionId: string) => { mode: string; planEnabled: boolean } | null;
  /** Ref authority (spec A); absent for tests that never send attachments. */
  attachments?: AttachmentRegistry | null;
}

export async function handleSendText(
  ctx: SendTextCommandContext,
  workspacePath: string,
  envelope: CommandEnvelope,
): Promise<CommandAck> {
  const sessionId = envelope.sessionId;
  if (!sessionId) {
    return rejectedAck(envelope.commandId, 0, "session_required", "sendText needs sessionId");
  }
  const payload = envelope.payload as {
    text?: string;
    attachments?: import("@nex/shared/nex-protocol-v4").AttachmentRef[];
    modelSelection?: ModelSelection;
    mode?: string;
    planEnabled?: boolean;
    requestedDelivery?: "startNow" | "queue" | "guide";
    heldQueueDisposition?: "clearQueueAndSend" | "keepQueueAndSend";
    expectedHeldQueueItemIds?: string[];
  };
  const attachments = normalizeAdmittedRefs(payload.attachments);
  if ((payload.text ?? "").trim().length === 0 && attachments.length === 0) {
    return rejectedAck(envelope.commandId, 0, "proto.invalidPayload", "input must not be empty");
  }
  try {
    const runtime = ctx.sessions.ensure(workspacePath, sessionId);
    // A restored session is opened asynchronously; submit must not race openSession.
    await runtime.ready;
    if (attachments.length > 0) ctx.attachments?.addAdmitted(sessionId, attachments);
    const admission = await runtime.queue.admitSendText(envelope, {
      text: payload.text ?? "",
      ...(attachments.length > 0 ? { attachments } : {}),
      ...(payload.requestedDelivery ? { requestedDelivery: payload.requestedDelivery } : {}),
      ...(payload.heldQueueDisposition
        ? { heldQueueDisposition: payload.heldQueueDisposition }
        : {}),
      ...(payload.expectedHeldQueueItemIds
        ? { expectedHeldQueueItemIds: payload.expectedHeldQueueItemIds }
        : {}),
      ...(payload.modelSelection ? { modelSelection: payload.modelSelection } : {}),
      ...(payload.mode ? { mode: payload.mode } : {}),
      ...(payload.planEnabled !== undefined ? { planEnabled: payload.planEnabled } : {}),
    });
    if (admission.delivery === "startNow") {
      applyModeToProjection(ctx, runtime, payload.mode, payload.planEnabled);
    }
    runtime.throttle.request();
    const ack = acceptedAck(
      envelope.commandId,
      runtime.projection.revision,
      inputAcceptedResult(envelope.commandId, admission.delivery),
    );
    runtime.acceptedCommands.set(envelope.commandId, ack);
    return ack;
  } catch (error) {
    const reasonCode = error instanceof QueueAdmissionError ? error.reasonCode : "send_failed";
    return rejectedAck(
      envelope.commandId,
      ctx.sessions.get(sessionId)?.projection.revision ?? 0,
      reasonCode,
      messageOf(error),
    );
  }
}

/** Mirrors the engine's mode into the projection (the gate is the authority). */
export function applyModeToProjection(
  ctx: Pick<SendTextCommandContext, "permissionMode">,
  runtime: SessionRuntime | undefined,
  mode?: string,
  planEnabled?: boolean,
): void {
  if (!runtime) return;
  const engineMode = ctx.permissionMode(runtime.sessionId);
  const nextMode = engineMode?.mode ?? normalizeMode(mode);
  const nextPlan = engineMode?.planEnabled ?? planEnabled ?? false;
  if (runtime.projection.mode === nextMode && runtime.projection.planEnabled === nextPlan) {
    return;
  }
  runtime.projection = {
    ...runtime.projection,
    seq: runtime.projection.seq + 1,
    revision: runtime.projection.revision + 1,
    mode: nextMode as SessionRuntime["projection"]["mode"],
    planEnabled: nextPlan,
    lastActivityAt: Date.now(),
  };
}

export function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
