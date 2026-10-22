import { commandAckSchema, conversationSnapshotSchema } from "@nex/shared/nex-protocol-v4";
import type {
  CommandAck,
  CommandEnvelope,
  ConversationSnapshot,
} from "@nex/shared/nex-protocol-v4";
import { createLogger } from "../../src/logger.js";
import type { CompleteConversationWireFrame } from "../../src/v4/envelopes.js";
import { ConversationPublisher } from "../../src/v4/publisher.js";
import { FakeEngine } from "./fakeEngine.js";

/**
 * Shared in-process harness for conversation-feature tests: the real command handler +
 * projection over the `FakeEngine` double, every snapshot/ACK parsed with the real zod
 * schemas (docs/next/conversation-features.md acceptance).
 */
export const CONVERSATION_WORKSPACE = "/workspace/demo";

export interface ConversationHarness {
  engine: FakeEngine;
  publisher: ConversationPublisher;
  frames: CompleteConversationWireFrame[];
  sessionId: string;
  snapshot: () => ConversationSnapshot;
  /** A valid envelope with fresh ids; `baseRevision`/`baseLogEpoch` default to current. */
  envelope: (type: CommandEnvelope["type"], payload?: Record<string, unknown>) => CommandEnvelope;
  command: (
    type: CommandEnvelope["type"],
    payload?: Record<string, unknown>,
  ) => Promise<CommandAck>;
  send: (text: string, extra?: Record<string, unknown>) => Promise<CommandAck>;
  /** Emits one complete scripted turn with stable user/assistant entry ids. */
  runTurn: (
    text: string,
    reply: string,
    options?: { entry?: string; applyText?: boolean },
  ) => Promise<void>;
  /**
   * Sends text the way the real engine does: `submit` persists the user message and emits
   * `message_entry` (id assigned), so a scripted reply can follow without a second turn.
   */
  submitText: (text: string, entry: string) => Promise<void>;
  dispose: () => void;
}

export function snapshotOf(frames: CompleteConversationWireFrame[]): ConversationSnapshot {
  const last = frames[frames.length - 1];
  if (!last || last.frame.payload.kind !== "snapshot") throw new Error("no snapshot");
  return conversationSnapshotSchema.parse(last.frame.payload.snapshot);
}

export async function waitFor(
  check: () => boolean,
  label: string,
  timeoutMs = 3_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (check()) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error(`timed out waiting for ${label}`);
}

export async function startConversationHarness(): Promise<ConversationHarness> {
  const logger = createLogger("error");
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const frames: CompleteConversationWireFrame[] = [];
  publisher.addConversationSink(CONVERSATION_WORKSPACE, (frame) => frames.push(frame));
  const created = commandAckSchema.parse(
    await publisher.sendCommand({
      workspacePath: CONVERSATION_WORKSPACE,
      envelope: rawEnvelope({
        type: "createSession",
        payload: { workspaceId: CONVERSATION_WORKSPACE },
      }),
    }),
  );
  const sessionId = created.result?.type === "createSession" ? created.result.sessionId : "";
  publisher.subscribeConversation({ workspacePath: CONVERSATION_WORKSPACE, sessionId });
  await waitFor(() => frames.length > 0, "initial frame");
  let turnSeq = 0;
  const snapshot = () => snapshotOf(frames);
  /** Live projection rows (an engine event may not have reached a frame yet). */
  const liveRows = () =>
    (
      publisher as unknown as {
        sessions: {
          get(
            id: string,
          ):
            | { projection: { rows: Array<{ kind: string; text?: string; entityId?: string }> } }
            | undefined;
        };
      }
    ).sessions.get(sessionId)?.projection.rows ?? [];
  const envelope = (type: CommandEnvelope["type"], payload: Record<string, unknown> = {}) => {
    const current = snapshot();
    return rawEnvelope({
      type,
      sessionId,
      payload,
      baseRevision: current.revision,
      baseLogEpoch: current.logEpoch,
    });
  };
  const command = async (type: CommandEnvelope["type"], payload: Record<string, unknown> = {}) => {
    const ack = commandAckSchema.parse(
      await publisher.sendCommand({
        workspacePath: CONVERSATION_WORKSPACE,
        envelope: envelope(type, payload),
      }),
    );
    // The publisher throttles (10 fps): wait for the snapshot the ACK promised, so the
    // caller never reads a frame from before the command.
    if (ack.status === "accepted" || ack.status === "noop") {
      await waitFor(
        () => snapshot().revision >= ack.revisionAtDecision,
        `${type} snapshot at ACK revision`,
      );
    }
    return ack;
  };
  const send = (text: string, extra: Record<string, unknown> = {}) =>
    command("sendText", { text, ...extra });
  const runTurn = async (
    text: string,
    reply: string,
    options: { entry?: string; applyText?: boolean } = {},
  ) => {
    const at = Date.now();
    const entry = options.entry ?? `u-${turnSeq + 1}`;
    turnSeq += 1;
    engine.emit(sessionId, { type: "user_input", at, text });
    // pi assigns ids in persistence order, so the user entry id must land on the *newest*
    // waiting user row, after the row exists.
    await waitFor(() => {
      const last = liveRows()
        .filter((row) => row.kind === "userInput")
        .at(-1);
      return last?.text === text && !last.entityId;
    }, "user row without identity");
    engine.emit(sessionId, { type: "message_entry", at, entryId: entry, role: "user" });
    engine.emit(sessionId, { type: "assistant_start", at });
    engine.emit(sessionId, { type: "text_delta", at, delta: reply });
    engine.emit(sessionId, { type: "message_entry", at, entryId: `${entry}-a`, role: "assistant" });
    engine.emit(sessionId, { type: "assistant_end", at, stopReason: "stop" });
    engine.emit(sessionId, { type: "settled", at, aborted: false });
    await waitFor(
      () =>
        liveRows().some((row) => row.entityId === `pi:${entry}`) &&
        snapshot().control.phase !== "running",
      "turn settled",
    );
    // The frames must reflect the finished turn before the next command reads the
    // revision (the publisher only guarantees the throttled latest state).
    await waitFor(
      () => snapshot().rows.window.some((row) => row.entityId === `pi:${entry}`),
      "turn rows published",
    );
  };
  const submitText = async (text: string, entry: string) => {
    await waitFor(
      () =>
        liveRows()
          .filter((row) => row.kind === "userInput")
          .at(-1)?.text !== text,
      "pre-submit",
    );
    engine.emit(sessionId, { type: "user_input", at: Date.now(), text });
    await waitFor(() => {
      const last = liveRows()
        .filter((row) => row.kind === "userInput")
        .at(-1);
      return last?.text === text && !last.entityId;
    }, "submitted user row");
    engine.emit(sessionId, { type: "message_entry", at: Date.now(), entryId: entry, role: "user" });
    await waitFor(
      () => liveRows().some((row) => row.entityId === `pi:${entry}`),
      "submit identity",
    );
  };
  return {
    engine,
    publisher,
    frames,
    sessionId,
    snapshot,
    envelope,
    command,
    send,
    runTurn,
    submitText,
    dispose: () => publisher.dispose(),
  };
}

function rawEnvelope(
  overrides: Partial<CommandEnvelope> & { type: CommandEnvelope["type"] },
): CommandEnvelope {
  return {
    commandId: `cmd-${Math.random().toString(36).slice(2)}`,
    clientId: "client-1",
    sessionId: null,
    payload: {},
    issuedAt: Date.now(),
    ...overrides,
  };
}
