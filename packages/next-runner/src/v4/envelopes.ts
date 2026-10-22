import type {
  CommandAck,
  CommandEnvelope,
  CommandResult,
  ConversationSnapshot,
  ConversationTopicFrame,
  ConversationTopicWireFrame,
  ModelSelection,
  SessionsIndexTopicFrame,
  SessionsIndexSnapshot,
  SessionsIndexTopicWireFrame,
} from "@nex/shared/nex-protocol-v4";
import type { EnginePhase } from "../engine/types.js";

export const V4_WIRE_VERSION = 3 as const;

/** This runner only emits complete frames; fragments are a later optimization. */
export type CompleteConversationWireFrame = Extract<
  ConversationTopicWireFrame,
  { kind: "complete" }
>;
export type CompleteSessionsIndexWireFrame = Extract<
  SessionsIndexTopicWireFrame,
  { kind: "complete" }
>;

/** Per-subscription monotonically increasing logical frame ordinal. */
export class FrameOrdinal {
  private value = 0;

  next(subscriptionId: string): { ordinal: number; logicalFrameId: string } {
    this.value += 1;
    return {
      ordinal: this.value,
      logicalFrameId: `${subscriptionId}-lf-${this.value}`,
    };
  }
}

export function conversationTopic(sessionId: string): string {
  return `conversation/${sessionId}`;
}

export function sessionsIndexTopic(workspaceId: string): string {
  return `sessions-index/${workspaceId}`;
}

export function buildConversationFrame(input: {
  topic: string;
  subscriptionId: string;
  ordinal: number;
  logicalFrameId: string;
  deliveryKind: "initial" | "online" | "recovery";
  snapshot: ConversationSnapshot;
  toSeq: number;
  sentAt: number;
}): CompleteConversationWireFrame {
  const frame: ConversationTopicFrame = {
    topic: input.topic,
    subscriptionId: input.subscriptionId,
    fromSeq: 0,
    toSeq: input.toSeq,
    sentAt: input.sentAt,
    payload: { kind: "snapshot", snapshot: input.snapshot },
  };
  return {
    wireVersion: V4_WIRE_VERSION,
    kind: "complete",
    deliveryKind: input.deliveryKind,
    logicalFrameId: input.logicalFrameId,
    logicalFrameOrdinal: input.ordinal,
    topic: input.topic,
    subscriptionId: input.subscriptionId,
    frame,
  };
}

export function buildSessionsIndexFrame(input: {
  topic: string;
  subscriptionId: string;
  ordinal: number;
  logicalFrameId: string;
  deliveryKind: "initial" | "online" | "recovery";
  snapshot: SessionsIndexSnapshot;
  toSeq: number;
  sentAt: number;
}): CompleteSessionsIndexWireFrame {
  const frame: SessionsIndexTopicFrame = {
    topic: input.topic,
    subscriptionId: input.subscriptionId,
    fromSeq: 0,
    toSeq: input.toSeq,
    sentAt: input.sentAt,
    payload: { kind: "snapshot", snapshot: input.snapshot },
  };
  return {
    wireVersion: V4_WIRE_VERSION,
    kind: "complete",
    deliveryKind: input.deliveryKind,
    logicalFrameId: input.logicalFrameId,
    logicalFrameOrdinal: input.ordinal,
    topic: input.topic,
    subscriptionId: input.subscriptionId,
    frame,
  };
}

/** Command ACK shapes used by this runner (see docs/next/channels-research.md §6.6). */
export function acceptedAck(
  commandId: string,
  revisionAtDecision: number,
  result?: CommandResult,
): CommandAck {
  return {
    commandId,
    status: "accepted",
    revisionAtDecision,
    ...(result ? { result } : {}),
  };
}

export function duplicateAck(
  commandId: string,
  revisionAtDecision: number,
  result?: CommandResult,
): CommandAck {
  return {
    commandId,
    status: "duplicate",
    revisionAtDecision,
    ...(result ? { result } : {}),
  };
}

export function rejectedAck(
  commandId: string,
  revisionAtDecision: number,
  reasonCode: string,
  message: string,
): CommandAck {
  return { commandId, status: "rejected", reasonCode, message, revisionAtDecision };
}

export function createSessionResult(sessionId: string): CommandResult {
  return { type: "createSession", sessionId };
}

export function inputAcceptedResult(
  inputId: string,
  delivery: "startNow" | "queue" | "guide" = "startNow",
): CommandResult {
  return { type: "inputAccepted", delivery, inputId };
}

export function resolveInteractionResult(clientId: string, optionId?: string): CommandResult {
  return {
    type: "resolveInteraction",
    resolvedBy: { clientId, ...(optionId ? { optionId } : {}) },
  };
}

/** CAS mismatch: the client's base revision/epoch is behind the runner's projection. */
export function staleAck(
  commandId: string,
  revisionAtDecision: number,
  reasonCode: string,
): CommandAck {
  return { commandId, status: "stale", reasonCode, revisionAtDecision };
}

export function noopAck(
  commandId: string,
  revisionAtDecision: number,
  reasonCode: string,
  result?: CommandResult,
): CommandAck {
  return {
    commandId,
    status: "noop",
    reasonCode,
    revisionAtDecision,
    ...(result ? { result } : {}),
  };
}

export type { CommandEnvelope, ModelSelection, EnginePhase };
