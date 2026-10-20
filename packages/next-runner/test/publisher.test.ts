import { test } from "node:test";
import assert from "node:assert/strict";
import {
  commandAckSchema,
  conversationTopicWireFrameSchema,
  sessionsIndexTopicWireFrameSchema,
} from "@nex/shared/nex-protocol-v4";
import type { CommandEnvelope } from "@nex/shared/nex-protocol-v4";
import type {
  CompleteConversationWireFrame,
  CompleteSessionsIndexWireFrame,
} from "../src/v4/envelopes.js";
import { ConversationPublisher } from "../src/v4/publisher.js";
import { FakeEngine } from "./helpers/fakeEngine.js";
import { createLogger } from "../src/logger.js";

const WORKSPACE = "/workspace/demo";
const T0 = 1_700_000_000_000;
const logger = createLogger("error");

function testEnvelope(
  overrides: Partial<CommandEnvelope> & { type: CommandEnvelope["type"] },
): CommandEnvelope {
  return {
    commandId: "cmd-1",
    clientId: "client-1",
    sessionId: null,
    payload: {},
    issuedAt: Date.now(),
    ...overrides,
  };
}

function collectFrames(publisher: ConversationPublisher): CompleteConversationWireFrame[] {
  const frames: CompleteConversationWireFrame[] = [];
  publisher.addConversationSink(WORKSPACE, (frame) => frames.push(frame));
  return frames;
}

async function waitFor(check: () => boolean, label: string): Promise<void> {
  const deadline = Date.now() + 3000;
  while (Date.now() < deadline) {
    if (check()) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error(`timed out waiting for ${label}`);
}

test("subscribe emits initial frame after ACK; sendText produces online trail ending completedSuccess", async () => {
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const frames = collectFrames(publisher);

  const created = await publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: testEnvelope({
      type: "createSession",
      payload: { workspaceId: WORKSPACE },
    }),
  });
  commandAckSchema.parse(created);
  const sessionId = created.result?.type === "createSession" ? created.result.sessionId : "";

  const ack = publisher.subscribeConversation({ workspacePath: WORKSPACE, sessionId });
  assert.equal(ack.ack.mode, "snapshot");
  // Initial frame must not exist synchronously: it is emitted after the ACK is written.
  assert.equal(frames.length, 0);

  await waitFor(() => frames.length === 1, "initial frame");
  assert.equal(frames[0]?.deliveryKind, "initial");
  assert.equal(frames[0]?.subscriptionId, ack.ack.subscriptionId);

  const sent = await publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: testEnvelope({
      type: "sendText",
      commandId: "cmd-2",
      sessionId,
      payload: { text: "hi" },
    }),
  });
  commandAckSchema.parse(sent);
  assert.equal(sent.status, "accepted");
  assert.equal(sent.result?.type, "inputAccepted");

  // Drive a complete run through the fake engine (pi's real order: prompt → events).
  engine.emit(sessionId, { type: "assistant_start", at: T0 + 1 });
  engine.emit(sessionId, { type: "text_delta", at: T0 + 2, delta: "po" });
  engine.emit(sessionId, { type: "text_delta", at: T0 + 3, delta: "ng" });
  engine.emit(sessionId, { type: "assistant_end", at: T0 + 4, stopReason: "stop" });
  engine.emit(sessionId, { type: "settled", at: T0 + 5, aborted: false });

  await waitFor(
    () =>
      frames.some(
        (frame) =>
          frame.frame.payload.kind === "snapshot" &&
          frame.frame.payload.snapshot.control.phase === "completedSuccess",
      ),
    "completed snapshot",
  );

  for (const frame of frames) {
    conversationTopicWireFrameSchema.parse(frame);
    if (frame.frame.payload.kind === "snapshot") {
      assert.equal(frame.frame.fromSeq, 0);
      assert.equal(frame.frame.toSeq, frame.frame.payload.snapshot.seq);
    }
  }
  const online = frames.filter((frame) => frame.deliveryKind === "online");
  assert.ok(online.length >= 1);
  const last = frames[frames.length - 1];
  assert.equal(
    last?.frame.payload.kind === "snapshot" && last.frame.payload.snapshot.control.phase,
    "completedSuccess",
  );
  const text =
    last?.frame.payload.kind === "snapshot"
      ? last.frame.payload.snapshot.rows.window.find((row) => row.kind === "assistantText")
      : undefined;
  assert.equal(text?.kind === "assistantText" ? text.text : "", "pong");
  publisher.dispose();
});

test("duplicate commandId returns duplicate with the same result", async () => {
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const envelope = testEnvelope({
    type: "createSession",
    commandId: "cmd-dup",
    payload: { workspaceId: WORKSPACE },
  });
  const first = await publisher.sendCommand({ workspacePath: WORKSPACE, envelope });
  const second = await publisher.sendCommand({ workspacePath: WORKSPACE, envelope });
  commandAckSchema.parse(first);
  commandAckSchema.parse(second);
  assert.equal(second.status, "duplicate");
  assert.deepEqual(second.result, first.result);
  assert.equal(engine.sessions.size, 1);
  publisher.dispose();
});

test("resync emits a recovery frame; unsubscribe stops frames", async () => {
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const frames = collectFrames(publisher);
  const created = await publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: testEnvelope({ type: "createSession", payload: { workspaceId: WORKSPACE } }),
  });
  const sessionId = created.result?.type === "createSession" ? created.result.sessionId : "";
  const { ack } = publisher.subscribeConversation({ workspacePath: WORKSPACE, sessionId });
  await waitFor(() => frames.length === 1, "initial");
  publisher.resyncConversation({ workspacePath: WORKSPACE, subscriptionId: ack.subscriptionId });
  await waitFor(() => frames.length === 2, "recovery");
  assert.equal(frames[1]?.deliveryKind, "recovery");
  assert.equal(frames[1]?.logicalFrameOrdinal, 2);
  publisher.unsubscribeConversation(WORKSPACE, ack.subscriptionId);
  const before = frames.length;
  engine.emit(sessionId, { type: "text_delta", at: T0 + 10, delta: "ignored" });
  await new Promise((resolve) => setTimeout(resolve, 150));
  assert.equal(frames.length, before);
  publisher.dispose();
});

test("sessions index hides drafts and lists the session once sendText promotes it", async () => {
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const frames: CompleteSessionsIndexWireFrame[] = [];
  publisher.addSessionsIndexSink(WORKSPACE, (frame) => frames.push(frame));
  const { ack } = publisher.subscribeSessionsIndex({ workspacePath: WORKSPACE });
  assert.equal(ack.mode, "snapshot");
  await waitFor(() => frames.length === 1, "index initial");
  sessionsIndexTopicWireFrameSchema.parse(frames[0]!);
  assert.equal(frames[0]?.deliveryKind, "initial");
  assert.equal(
    frames[0]?.frame.payload.kind === "snapshot"
      ? frames[0].frame.payload.snapshot.sessions.length
      : -1,
    0,
  );

  const created = await publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: testEnvelope({ type: "createSession", payload: { workspaceId: WORKSPACE } }),
  });
  const sessionId = created.result?.type === "createSession" ? created.result.sessionId : "";
  assert.ok(sessionId);
  await waitFor(() => frames.length >= 2, "index update after create");
  const afterCreate = frames[frames.length - 1]!;
  sessionsIndexTopicWireFrameSchema.parse(afterCreate);
  // A draft prewarm must not surface in the sidebar until it has real input.
  assert.equal(
    afterCreate.frame.payload.kind === "snapshot"
      ? afterCreate.frame.payload.snapshot.sessions.length
      : -1,
    0,
  );

  await publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: testEnvelope({
      type: "sendText",
      commandId: "cmd-send",
      sessionId,
      payload: { text: "first message" },
    }),
  });
  await waitFor(() => frames.length >= 3, "index update after send");
  const latest = frames[frames.length - 1]!;
  sessionsIndexTopicWireFrameSchema.parse(latest);
  assert.equal(latest.deliveryKind, "online");
  assert.equal(
    latest.frame.payload.kind === "snapshot" ? latest.frame.payload.snapshot.sessions.length : -1,
    1,
  );
  publisher.dispose();
});

test("delete removes the session and adds a tombstone", async () => {
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const created = await publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: testEnvelope({ type: "createSession", payload: { workspaceId: WORKSPACE } }),
  });
  const sessionId = created.result?.type === "createSession" ? created.result.sessionId : "";
  const ack = await publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: testEnvelope({
      type: "deleteSession",
      commandId: "cmd-del",
      sessionId,
      payload: {},
    }),
  });
  commandAckSchema.parse(ack);
  assert.equal(ack.status, "accepted");
  assert.equal(await engine.listSessions(WORKSPACE).then((items) => items.length), 0);
  assert.deepEqual(await engine.listDeletedSessionIds(WORKSPACE), [sessionId]);
  publisher.dispose();
});

test("rowsRange is an empty page with the current epoch/seq", async () => {
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const created = await publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: testEnvelope({ type: "createSession", payload: { workspaceId: WORKSPACE } }),
  });
  const sessionId = created.result?.type === "createSession" ? created.result.sessionId : "";
  const page = publisher.rowsRange(WORKSPACE, sessionId);
  assert.deepEqual(page.rows, []);
  assert.equal(page.hasMore, false);
  assert.equal(typeof page.atLogEpoch, "string");
  const plans = publisher.plans(WORKSPACE, sessionId);
  assert.deepEqual(plans.plans, []);
  publisher.dispose();
});

test("queryConversationCommands reports unknown for unseen commands", async () => {
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const result = publisher.queryCommands({
    commands: [{ sessionId: "missing", commandId: "cmd-x" }],
  });
  assert.equal(result.results[0]?.result, "unknown");
  publisher.dispose();
});

test("prewarm: createSession without input then sendText works before any subscription", async () => {
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const created = await publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: testEnvelope({
      type: "createSession",
      commandId: "prewarm-create",
      payload: { workspaceId: WORKSPACE },
    }),
  });
  const sessionId = created.result?.type === "createSession" ? created.result.sessionId : "";
  assert.ok(sessionId);
  // No subscriber yet: the first sendText must still be accepted.
  const sent = await publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: testEnvelope({
      type: "sendText",
      commandId: "prewarm-send",
      sessionId,
      payload: { text: "first message" },
    }),
  });
  commandAckSchema.parse(sent);
  assert.equal(sent.status, "accepted");
  assert.equal(engine.submitted[0]?.text, "first message");

  // The late subscription must see the optimistic history once hydrated.
  const frames: CompleteConversationWireFrame[] = [];
  publisher.addConversationSink(WORKSPACE, (frame) => frames.push(frame));
  publisher.subscribeConversation({ workspacePath: WORKSPACE, sessionId });
  await waitFor(
    () => frames.some((frame) => frame.deliveryKind === "initial" && frame.frame.toSeq > 0),
    "initial frame with history",
  );
  publisher.dispose();
});
