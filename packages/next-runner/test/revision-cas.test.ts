import { test } from "node:test";
import assert from "node:assert/strict";
import { commandAckSchema, conversationSnapshotSchema } from "@nex/shared/nex-protocol-v4";
import type { CommandEnvelope } from "@nex/shared/nex-protocol-v4";
import { ConversationPublisher } from "../src/v4/publisher.js";
import type { CompleteConversationWireFrame } from "../src/v4/envelopes.js";
import { FakeEngine } from "./helpers/fakeEngine.js";
import { createLogger } from "../src/logger.js";

/**
 * Revision CAS (docs/next/conversation-features.md decision 3) and the real availability /
 * inputRouting table, driven through the real command handler + projection.
 */

const WORKSPACE = "/workspace/demo";
const logger = createLogger("error");

function envelope(
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

async function waitFor(check: () => boolean, label: string): Promise<void> {
  const deadline = Date.now() + 3_000;
  while (Date.now() < deadline) {
    if (check()) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error(`timed out waiting for ${label}`);
}

interface Harness {
  engine: FakeEngine;
  publisher: ConversationPublisher;
  frames: CompleteConversationWireFrame[];
  sessionId: string;
  lastSnapshot: () => ReturnType<typeof snapshotOf>;
}

function snapshotOf(frames: CompleteConversationWireFrame[]) {
  const last = frames[frames.length - 1];
  return last && last.frame.payload.kind === "snapshot" ? last.frame.payload.snapshot : undefined;
}

async function start(): Promise<Harness> {
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const frames: CompleteConversationWireFrame[] = [];
  publisher.addConversationSink(WORKSPACE, (frame) => frames.push(frame));
  const created = await publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: envelope({ type: "createSession", payload: { workspaceId: WORKSPACE } }),
  });
  const sessionId = created.result?.type === "createSession" ? created.result.sessionId : "";
  publisher.subscribeConversation({ workspacePath: WORKSPACE, sessionId });
  await waitFor(() => frames.length > 0, "initial frame");
  return { engine, publisher, frames, sessionId, lastSnapshot: () => snapshotOf(frames) };
}

test("CAS: stale baseRevision is an ACK stale carrying the real revisionAtDecision", async () => {
  const h = await start();
  const before = h.lastSnapshot();
  assert.ok(before);
  const stale = await h.publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: envelope({
      type: "setFollowupMode",
      sessionId: h.sessionId,
      // One revision behind: the runner must answer stale, not apply the command.
      baseRevision: before.revision - 1,
      baseLogEpoch: before.logEpoch,
      payload: { mode: "guide" },
    }),
  });
  commandAckSchema.parse(stale);
  assert.equal(stale.status, "stale");
  assert.equal(stale.reasonCode, "proto.staleRevision");
  // revisionAtDecision is the runner's current revision — the value the client retries with.
  assert.equal(stale.revisionAtDecision, before.revision);
  // Nothing changed: still queue.
  assert.equal(h.lastSnapshot()?.config.followupMode, "queue");

  // Retrying with the reported revision converges.
  const retried = await h.publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: envelope({
      type: "setFollowupMode",
      sessionId: h.sessionId,
      baseRevision: stale.revisionAtDecision,
      baseLogEpoch: before.logEpoch,
      payload: { mode: "guide" },
    }),
  });
  commandAckSchema.parse(retried);
  assert.equal(retried.status, "accepted");
  await waitFor(() => h.lastSnapshot()?.config.followupMode === "guide", "guide applied");
  h.publisher.dispose();
});

test("CAS: a foreign baseLogEpoch on a row-targeting command is stale", async () => {
  const h = await start();
  const before = h.lastSnapshot();
  assert.ok(before);
  const stale = await h.publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: envelope({
      type: "retryTurn",
      sessionId: h.sessionId,
      baseRevision: before.revision,
      baseLogEpoch: "epoch-from-a-previous-runner-process",
      payload: { target: { rowId: 1, entityId: "pi:deadbeef" } },
    }),
  });
  commandAckSchema.parse(stale);
  assert.equal(stale.status, "stale");
  assert.equal(stale.reasonCode, "proto.staleLogEpoch");
  assert.equal(stale.revisionAtDecision, before.revision);
  h.publisher.dispose();
});

test("CAS: revision increments on every content change (rows, meta, config)", async () => {
  const h = await start();
  const created = h.lastSnapshot();
  assert.ok(created);
  assert.equal(created.revision, 0);

  const send = await h.publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: envelope({
      type: "sendText",
      commandId: "cmd-send",
      sessionId: h.sessionId,
      payload: { text: "hi" },
    }),
  });
  commandAckSchema.parse(send);
  // A user row (turnHeader + userInput) is a structural change.
  assert.equal(send.revisionAtDecision >= 1, true);

  h.engine.emit(h.sessionId, { type: "assistant_start", at: Date.now() });
  h.engine.emit(h.sessionId, { type: "text_delta", at: Date.now(), delta: "po" });
  h.engine.emit(h.sessionId, { type: "text_delta", at: Date.now(), delta: "ng" });
  h.engine.emit(h.sessionId, { type: "assistant_end", at: Date.now(), stopReason: "stop" });
  h.engine.emit(h.sessionId, { type: "settled", at: Date.now(), aborted: false });
  await waitFor(() => h.lastSnapshot()?.control.phase === "completedSuccess", "run completed");
  const done = h.lastSnapshot();
  assert.ok(done);
  conversationSnapshotSchema.parse(done);
  assert.equal(done.revision > created.revision, true);
  // seq tracks every publish; revision only content changes. Both grow here, but seq must
  // never lag revision (it is the snapshot watermark).
  assert.equal(done.seq >= done.revision, true);
  h.publisher.dispose();
});

test("availability: fork/compact are allowed when idle, and the goal flags use their guards", async () => {
  const h = await start();
  const idle = h.lastSnapshot();
  assert.ok(idle);
  conversationSnapshotSchema.parse(idle);
  // Batch 2d implements both; the lock only appears while a compaction runs/queues.
  assert.deepEqual(idle.availability.fork, { allowed: true });
  assert.deepEqual(idle.availability.compact, { allowed: true });
  // Batch 2c owns the queue: edit is available in every phase.
  assert.deepEqual(idle.availability.queueEdit, { allowed: true });
  assert.deepEqual(idle.availability.pauseGoal, {
    allowed: false,
    reasonCode: "noGoalToPause",
  });
  assert.deepEqual(idle.availability.resumeGoal, {
    allowed: false,
    reasonCode: "noGoalToResume",
  });
  // sendQueuedNow requires a run.
  assert.deepEqual(idle.availability.sendQueuedNow, {
    allowed: false,
    reasonCode: "sendQueuedNowRequiresRunning",
  });
  // Implemented by batch 2a.
  assert.deepEqual(idle.availability.switchModelConfig, { allowed: true });
  assert.deepEqual(idle.availability.setFollowupMode, { allowed: true });

  // While running, "send now" is available (it preempts the active run).
  await h.publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: envelope({
      type: "sendText",
      sessionId: h.sessionId,
      payload: { text: "run" },
    }),
  });
  await waitFor(() => h.lastSnapshot()?.control.phase === "running", "running");
  const running = h.lastSnapshot();
  assert.ok(running);
  conversationSnapshotSchema.parse(running);
  assert.deepEqual(running.availability.sendQueuedNow, { allowed: true });
  h.publisher.dispose();
});

test("inputRouting: idle → startNow, running → enqueue/guide by followupMode", async () => {
  const h = await start();
  assert.deepEqual(h.lastSnapshot()?.inputRouting, { mode: "startNow" });

  await h.publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: envelope({
      type: "sendText",
      sessionId: h.sessionId,
      payload: { text: "run" },
    }),
  });
  await waitFor(() => h.lastSnapshot()?.control.phase === "running", "running");
  assert.deepEqual(h.lastSnapshot()?.inputRouting, { mode: "enqueue" });

  const revision = h.lastSnapshot()?.revision ?? 0;
  const ack = await h.publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: envelope({
      type: "setFollowupMode",
      sessionId: h.sessionId,
      baseRevision: revision,
      payload: { mode: "guide" },
    }),
  });
  assert.equal(ack.status, "accepted");
  await waitFor(() => {
    const snapshot = h.lastSnapshot();
    return snapshot?.config.followupMode === "guide" && snapshot.inputRouting.mode === "guide";
  }, "guide routing");

  h.engine.emit(h.sessionId, { type: "settled", at: Date.now(), aborted: false });
  await waitFor(() => h.lastSnapshot()?.inputRouting.mode === "startNow", "back to startNow");
  h.publisher.dispose();
});
