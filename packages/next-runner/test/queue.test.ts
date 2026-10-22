import { test } from "node:test";
import assert from "node:assert/strict";
import {
  commandAckSchema,
  conversationSnapshotSchema,
  type CommandEnvelope,
  type ConversationSnapshot,
  type QueueItem,
} from "@nex/shared/nex-protocol-v4";
import { ConversationPublisher } from "../src/v4/publisher.js";
import type { CompleteConversationWireFrame } from "../src/v4/envelopes.js";
import {
  consumedGuideIds,
  moveQueueItem,
  nextDrainableItem,
  withQueuePositions,
} from "../src/v4/queueItem.js";
import { FakeEngine } from "./helpers/fakeEngine.js";
import { createLogger } from "../src/logger.js";

/**
 * Runner-owned queue (docs/next/conversation-features.md decision 4) driven through the real
 * command handler: the admission matrix, drain ordering, pause/resume, queue controls,
 * `sendQueuedNow`, the held-queue `choice`, and stale CAS. Every emitted ACK and snapshot is
 * validated against the protocol schemas.
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

async function waitFor(check: () => boolean, label: string, timeoutMs = 3_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
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
  snapshot: () => ConversationSnapshot;
  revision: () => number;
  send: (
    payload: Record<string, unknown>,
    extra?: Partial<CommandEnvelope>,
  ) => Promise<ReturnType<typeof commandAckSchema.parse>>;
  queueCommand: (
    type: "deleteQueueItem" | "editQueueItem" | "reorderQueueItem" | "sendQueuedNow",
    payload: Record<string, unknown>,
    baseRevision?: number,
  ) => Promise<ReturnType<typeof commandAckSchema.parse>>;
  setAutoDrain: (
    autoDrain: boolean,
    baseRevision?: number,
  ) => Promise<ReturnType<typeof commandAckSchema.parse>>;
}

function lastSnapshotOf(frames: CompleteConversationWireFrame[]): ConversationSnapshot {
  const last = frames[frames.length - 1];
  if (!last || last.frame.payload.kind !== "snapshot") {
    throw new Error("no snapshot frame yet");
  }
  return last.frame.payload.snapshot;
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

  // Snapshots are throttled (10/s), so a command's revision reaches the wire a beat after
  // its ACK. Waiting for the frame to carry the ACK's revision is exactly what the UI does
  // (it retries CAS against `revisionAtDecision`) and it keeps the assertions honest.
  const published = async (revision: number): Promise<void> => {
    await waitFor(
      () => lastSnapshotOf(frames).revision >= revision,
      `snapshot revision ${revision}`,
    );
  };

  const send: Harness["send"] = async (payload, extra = {}) => {
    const ack = commandAckSchema.parse(
      await publisher.sendCommand({
        workspacePath: WORKSPACE,
        envelope: envelope({
          type: "sendText",
          sessionId,
          payload,
          ...extra,
        }),
      }),
    );
    if (ack.status === "accepted" || ack.status === "noop") {
      await published(ack.revisionAtDecision);
    }
    return ack;
  };

  const queueCommand: Harness["queueCommand"] = async (type, payload, baseRevision) => {
    const ack = commandAckSchema.parse(
      await publisher.sendCommand({
        workspacePath: WORKSPACE,
        envelope: envelope({
          type,
          sessionId,
          payload,
          ...(baseRevision !== undefined ? { baseRevision } : {}),
        }),
      }),
    );
    if (ack.status === "accepted" || ack.status === "noop") {
      await published(ack.revisionAtDecision);
    }
    return ack;
  };

  const setAutoDrain: Harness["setAutoDrain"] = async (autoDrain, baseRevision) => {
    const ack = commandAckSchema.parse(
      await publisher.sendCommand({
        workspacePath: WORKSPACE,
        envelope: envelope({
          type: "setAutoDrain",
          sessionId,
          payload: { autoDrain },
          ...(baseRevision !== undefined ? { baseRevision } : {}),
        }),
      }),
    );
    if (ack.status === "accepted" || ack.status === "noop") {
      await published(ack.revisionAtDecision);
    }
    return ack;
  };

  const snapshot = (): ConversationSnapshot =>
    conversationSnapshotSchema.parse(lastSnapshotOf(frames));
  return {
    engine,
    publisher,
    frames,
    sessionId,
    snapshot,
    revision: () => snapshot().revision,
    send,
    queueCommand,
    setAutoDrain,
  };
}

/** Starts the first run so later sends land while busy. */
async function startBusy(h: Harness): Promise<void> {
  const ack = await h.send({ text: "first" });
  assert.equal(ack.status, "accepted");
  await waitFor(() => h.snapshot().control.phase === "running", "running");
}

/**
 * Waits until the throttled snapshot stream is quiet. Engine events published directly (not
 * through a command ACK) are the case where `h.revision()` would otherwise read a stale
 * frame and make a CAS command look stale for a reason the assertion is not about.
 */
async function settleFrames(h: Harness, quietMs = 250): Promise<void> {
  let count = h.frames.length;
  for (;;) {
    await new Promise((resolve) => setTimeout(resolve, quietMs));
    if (h.frames.length === count) return;
    count = h.frames.length;
  }
}

function submittedTexts(h: Harness): string[] {
  return h.engine.submitted.map((entry) => entry.text);
}

// ── admission matrix ──

test("queue admission: idle auto/queue/guide all start now and leave the queue empty", async () => {
  const h = await start();
  for (const requestedDelivery of [undefined, "queue", "guide"] as const) {
    const ack = await h.send({
      text: `text-${requestedDelivery ?? "auto"}`,
      ...(requestedDelivery ? { requestedDelivery } : {}),
    });
    assert.equal(ack.status, "accepted");
    assert.equal(ack.result?.type === "inputAccepted" ? ack.result.delivery : "", "startNow");
    assert.equal(h.snapshot().queue.items.length, 0);
    await h.engine.settle(h.sessionId);
    await waitFor(() => h.snapshot().control.phase === "completedSuccess", "settled");
  }
  assert.equal(h.snapshot().queue.autoDrain, true);
  h.publisher.dispose();
});

test("queue admission: busy + queue mode enqueues in order and ACKs queue", async () => {
  const h = await start();
  await startBusy(h);
  const ack = await h.send({ text: "second" });
  assert.equal(ack.result?.type === "inputAccepted" ? ack.result.delivery : "", "queue");
  const items = h.snapshot().queue.items;
  assert.equal(items.length, 1);
  assert.equal(items[0]?.text, "second");
  assert.equal(items[0]?.delivery.admitted, "queue");
  assert.equal(items[0]?.delivery.requested, "auto");
  assert.equal(items[0]?.dispatch.state, "queued");
  assert.equal(items[0]?.order.queuePosition, 0);
  assert.equal(items[0]?.sourceCommandId, ack.commandId);
  // Nothing new started: only the first prompt reached the engine.
  assert.deepEqual(submittedTexts(h), ["first"]);
  h.publisher.dispose();
});

test("queue admission: guide mode steers and keeps a pending guide item until pi consumes it", async () => {
  const h = await start();
  await h.setAutoDrain(true).catch(() => undefined);
  // Switch the session to guide mode through the real command.
  const ack = commandAckSchema.parse(
    await h.publisher.sendCommand({
      workspacePath: WORKSPACE,
      envelope: envelope({
        type: "setFollowupMode",
        sessionId: h.sessionId,
        baseRevision: h.revision(),
        payload: { mode: "guide" },
      }),
    }),
  );
  assert.equal(ack.status, "accepted");
  await startBusy(h);

  const guide = await h.send({ text: "steer me" });
  assert.equal(guide.result?.type === "inputAccepted" ? guide.result.delivery : "", "guide");
  assert.equal(h.engine.steered.length, 1);
  const item = h.snapshot().queue.items[0];
  assert.ok(item);
  assert.equal(item.delivery.admitted, "guide");
  assert.equal(item.steer.state, "guided");
  // The composer routes to guide while running in guide mode.
  assert.equal(h.snapshot().inputRouting.mode, "guide");

  // pi consumes it at the next tool-batch boundary.
  h.engine.consumeSteer(h.sessionId, "steer me");
  await waitFor(() => h.snapshot().queue.items.length === 0, "guide consumed");
  h.publisher.dispose();
});

test("queue admission: requestedDelivery overrides the session mode in both directions", async () => {
  const h = await start();
  await startBusy(h);
  // queue mode + explicit guide → steer
  const guide = await h.send({ text: "explicit guide", requestedDelivery: "guide" });
  assert.equal(guide.result?.type === "inputAccepted" ? guide.result.delivery : "", "guide");
  assert.equal(h.snapshot().queue.items[0]?.delivery.admitted, "guide");

  // queue mode + explicit queue → enqueue
  const queued = await h.send({ text: "explicit queue", requestedDelivery: "queue" });
  assert.equal(queued.result?.type === "inputAccepted" ? queued.result.delivery : "", "queue");
  assert.equal(h.snapshot().queue.items.length, 2);

  // busy + explicit startNow → abort, wait idle, start
  const startNow = await h.send({ text: "preempt", requestedDelivery: "startNow" });
  assert.equal(
    startNow.result?.type === "inputAccepted" ? startNow.result.delivery : "",
    "startNow",
  );
  assert.equal(h.engine.aborted.includes(h.sessionId), true);
  assert.deepEqual(submittedTexts(h), ["first", "preempt"]);
  h.publisher.dispose();
});

test("queue admission: empty text is rejected proto.invalidPayload", async () => {
  const h = await start();
  const ack = await h.send({ text: "   " });
  assert.equal(ack.status, "rejected");
  assert.equal(ack.reasonCode, "proto.invalidPayload");
  h.publisher.dispose();
});

// ── drain ──

test("queue drain: settle promotes the head only, in admission order, and removes it", async () => {
  const h = await start();
  await startBusy(h);
  await h.send({ text: "second" });
  await h.send({ text: "third" });
  assert.deepEqual(
    h.snapshot().queue.items.map((item) => item.text),
    ["second", "third"],
  );

  h.engine.settle(h.sessionId);
  await waitFor(() => submittedTexts(h).length === 2, "second promoted");
  assert.deepEqual(submittedTexts(h), ["first", "second"]);
  await waitFor(() => h.snapshot().queue.items.length === 1, "head removed");
  assert.deepEqual(
    h.snapshot().queue.items.map((item) => item.text),
    ["third"],
  );

  h.engine.settle(h.sessionId);
  await waitFor(() => submittedTexts(h).length === 3, "third promoted");
  assert.deepEqual(submittedTexts(h), ["first", "second", "third"]);
  await waitFor(() => h.snapshot().queue.items.length === 0, "queue drained");
  h.publisher.dispose();
});

test("queue drain: a promoted item is submitted with its original sourceCommandId", async () => {
  const h = await start();
  await startBusy(h);
  const ack = await h.send({
    text: "queued with lineage",
    modelSelection: { providerId: "faux", modelId: "faux-1" },
  });
  const item = h.snapshot().queue.items[0];
  assert.ok(item);
  assert.equal(item.sourceCommandId, ack.commandId);
  h.engine.settle(h.sessionId);
  await waitFor(() => submittedTexts(h).includes("queued with lineage"), "promoted");
  const promoted = h.engine.submitted.find((entry) => entry.text === "queued with lineage");
  assert.equal(promoted?.requestId, ack.commandId);
  h.publisher.dispose();
});

// ── drain in-flight guard ──

test("queue drain: a send that lands mid-promotion queues behind the promoted item", async () => {
  const h = await start();
  await startBusy(h);
  await h.send({ text: "head" });
  // Hold the promotion's submit so the admission below happens inside the drain window.
  h.engine.submitDelayMs = 120;
  h.engine.settle(h.sessionId);
  await waitFor(
    () => h.snapshot().queue.items.some((item) => item.dispatch.state === "promoting"),
    "head reserved",
  );
  const late = await h.send({ text: "late" });
  // Without the in-flight guard this would start a second run and throw inside pi.
  assert.equal(late.result?.type === "inputAccepted" ? late.result.delivery : "", "queue");
  h.engine.submitDelayMs = 0;
  await waitFor(() => submittedTexts(h).length === 2, "head promoted");
  assert.deepEqual(submittedTexts(h), ["first", "head"]);
  h.publisher.dispose();
});

// ── pause / resume ──

test("queue guide: an undelivered guide at settle is re-queued visibly, not dropped or leaked", async () => {
  const h = await start();
  const guideMode = commandAckSchema.parse(
    await h.publisher.sendCommand({
      workspacePath: WORKSPACE,
      envelope: envelope({
        type: "setFollowupMode",
        sessionId: h.sessionId,
        baseRevision: h.revision(),
        payload: { mode: "guide" },
      }),
    }),
  );
  assert.equal(guideMode.status, "accepted");
  await startBusy(h);
  await h.send({ text: "never delivered" });
  assert.equal(h.snapshot().queue.items[0]?.delivery.admitted, "guide");

  // The run ends without pi consuming the steer (the model never hit a tool-batch boundary):
  // autoDrain is on and the engine is idle, so demoting re-queues the text and the same
  // settle drains it as a visible run. Assert the outcome rather than the transient
  // `admitted:"queue"` snapshot, which the drain removes immediately.
  h.engine.settle(h.sessionId);
  await waitFor(() => submittedTexts(h).includes("never delivered"), "drained visibly");
  // Not dropped: it ran as runner-owned text, never via pi's own queue.
  assert.deepEqual(submittedTexts(h), ["first", "never delivered"]);
  assert.deepEqual(h.engine.steering.get(h.sessionId), []);
  h.publisher.dispose();
});

// ── pause / resume ──

test("queue pause: stop pauses (autoDrain=false, pauseReason=stopped) and resume drains", async () => {
  const h = await start();
  await startBusy(h);
  await h.send({ text: "held" });

  const stop = commandAckSchema.parse(
    await h.publisher.sendCommand({
      workspacePath: WORKSPACE,
      envelope: envelope({ type: "stop", sessionId: h.sessionId, payload: {} }),
    }),
  );
  assert.equal(stop.status, "accepted");
  await waitFor(() => !h.snapshot().queue.autoDrain, "paused");
  assert.equal(h.snapshot().queue.pauseReason, "stopped");
  assert.equal(h.snapshot().queue.items.length, 1);
  // Paused queue refuses to drain on settle.
  h.engine.settle(h.sessionId, { aborted: true });
  await new Promise((resolve) => setTimeout(resolve, 30));
  assert.deepEqual(submittedTexts(h), ["first"]);

  // Held queue: an idle completed session with items and autoDrain=false asks the client.
  await waitFor(() => h.snapshot().control.phase === "completedInterrupted", "idle");
  await settleFrames(h);
  assert.equal(h.snapshot().inputRouting.mode, "choice");
  assert.equal(h.snapshot().inputRouting.reasonCode, "heldQueueInputRequiresChoice");

  const resumed = await h.setAutoDrain(true, h.revision());
  assert.equal(resumed.status, "accepted");
  await waitFor(() => submittedTexts(h).length === 2, "drain on resume");
  assert.deepEqual(submittedTexts(h), ["first", "held"]);
  await waitFor(() => h.snapshot().queue.items.length === 0, "drained");
  assert.equal(h.snapshot().queue.autoDrain, true);
  assert.equal(h.snapshot().queue.pauseReason, undefined);
  h.publisher.dispose();
});

test("queue pause: a failed turn pauses with pauseReason=error", async () => {
  const h = await start();
  await startBusy(h);
  await h.send({ text: "after error" });
  // Provider error: assistant_end(error) then settled (not aborted).
  h.engine.emit(h.sessionId, {
    type: "assistant_end",
    at: Date.now(),
    stopReason: "error",
    errorMessage: "upstream 500",
  });
  h.engine.settle(h.sessionId);
  await waitFor(() => !h.snapshot().queue.autoDrain, "paused");
  assert.equal(h.snapshot().queue.pauseReason, "error");
  assert.equal(h.snapshot().queue.items.length, 1);
  // `choice` is deliberately limited to completed* phases (old CLI parity); an error phase
  // leaves the queue paused but the composer routes a new input as startNow.
  assert.equal(h.snapshot().control.phase, "error");
  assert.equal(h.snapshot().inputRouting.mode, "startNow");
  h.publisher.dispose();
});

test("queue pause: setAutoDrain(false) is a manual pause that survives without a run", async () => {
  const h = await start();
  await startBusy(h);
  await h.send({ text: "manual" });
  const ack = await h.setAutoDrain(false, h.revision());
  assert.equal(ack.status, "accepted");
  await waitFor(() => !h.snapshot().queue.autoDrain, "paused");
  assert.equal(h.snapshot().queue.pauseReason, "manual");
  h.engine.settle(h.sessionId);
  await new Promise((resolve) => setTimeout(resolve, 30));
  assert.deepEqual(submittedTexts(h), ["first"]);
  h.publisher.dispose();
});

// ── queue controls ──

test("queue controls: delete removes an item and a missing id is noop queue.itemMissing", async () => {
  const h = await start();
  await startBusy(h);
  await h.send({ text: "one" });
  await h.send({ text: "two" });
  const [first, second] = h.snapshot().queue.items;
  assert.ok(first && second);

  const deleted = await h.queueCommand(
    "deleteQueueItem",
    { queueItemId: first.queueItemId },
    h.revision(),
  );
  assert.equal(deleted.status, "accepted");
  await waitFor(() => h.snapshot().queue.items.length === 1, "deleted");
  assert.deepEqual(
    h.snapshot().queue.items.map((item) => item.text),
    ["two"],
  );
  // Positions are rewritten so the UI's drag anchors stay valid.
  assert.equal(h.snapshot().queue.items[0]?.order.queuePosition, 0);

  const missing = await h.queueCommand(
    "deleteQueueItem",
    { queueItemId: first.queueItemId },
    h.revision(),
  );
  assert.equal(missing.status, "noop");
  assert.equal(missing.reasonCode, "queue.itemMissing");
  h.publisher.dispose();
});

test("queue controls: reorder moves before an anchor, null means tail, unknown anchor noops", async () => {
  const h = await start();
  await startBusy(h);
  for (const text of ["a", "b", "c"]) await h.send({ text });
  const [a, b, c] = h.snapshot().queue.items;
  assert.ok(a && b && c);

  const moved = await h.queueCommand(
    "reorderQueueItem",
    { queueItemId: c.queueItemId, beforeQueueItemId: a.queueItemId },
    h.revision(),
  );
  assert.equal(moved.status, "accepted");
  await waitFor(() => h.snapshot().queue.items[0]?.text === "c", "c moved to head");
  assert.deepEqual(
    h.snapshot().queue.items.map((item) => item.text),
    ["c", "a", "b"],
  );

  // null anchor → tail
  const toTail = await h.queueCommand(
    "reorderQueueItem",
    { queueItemId: c.queueItemId, beforeQueueItemId: null },
    h.revision(),
  );
  assert.equal(toTail.status, "accepted");
  await waitFor(() => h.snapshot().queue.items[2]?.text === "c", "c at tail");
  assert.deepEqual(
    h.snapshot().queue.items.map((item) => item.text),
    ["a", "b", "c"],
  );

  const unknownAnchor = await h.queueCommand(
    "reorderQueueItem",
    { queueItemId: a.queueItemId, beforeQueueItemId: "qi-does-not-exist" },
    h.revision(),
  );
  assert.equal(unknownAnchor.status, "noop");
  h.publisher.dispose();
});

test("queue controls: edit rewrites text in place (protocol completeness)", async () => {
  const h = await start();
  await startBusy(h);
  await h.send({ text: "before" });
  const item = h.snapshot().queue.items[0];
  assert.ok(item);
  const ack = await h.queueCommand(
    "editQueueItem",
    { queueItemId: item.queueItemId, newText: "after" },
    h.revision(),
  );
  assert.equal(ack.status, "accepted");
  await waitFor(() => h.snapshot().queue.items[0]?.text === "after", "edited");
  // Drain promotes the edited text, not the original.
  h.engine.settle(h.sessionId);
  await waitFor(() => submittedTexts(h).includes("after"), "edited text promoted");
  assert.deepEqual(submittedTexts(h), ["first", "after"]);
  h.publisher.dispose();
});

test("queue controls: sendQueuedNow preempts and promotes the chosen item", async () => {
  const h = await start();
  await startBusy(h);
  await h.send({ text: "keep" });
  const late = await h.send({ text: "promote me" });
  const items = h.snapshot().queue.items;
  assert.equal(items.length, 2);
  const target = items[1];
  assert.ok(target);

  const ack = await h.queueCommand(
    "sendQueuedNow",
    { queueItemId: target.queueItemId },
    h.revision(),
  );
  assert.equal(ack.status, "accepted");
  await waitFor(() => submittedTexts(h).includes("promote me"), "promoted");
  // The active run was aborted and the promoted item restarted the session.
  assert.equal(h.engine.aborted.includes(h.sessionId), true);
  assert.deepEqual(submittedTexts(h).slice(0, 2), ["first", "promote me"]);
  // Only the promoted item left the queue; the other stays.
  await waitFor(() => h.snapshot().queue.items.length === 1, "promoted item removed");
  assert.deepEqual(
    h.snapshot().queue.items.map((item) => item.text),
    ["keep"],
  );
  assert.equal(late.status, "accepted");
  h.publisher.dispose();
});

// ── held queue choice ──

test("held choice: no disposition is rejected, stale confirmation is rejected, clear empties first", async () => {
  const h = await start();
  await startBusy(h);
  await h.send({ text: "held-a" });
  await h.send({ text: "held-b" });
  const items = h.snapshot().queue.items;
  const expectedIds = items.map((item) => item.queueItemId);

  // Pause the queue through stop so the session is completed + queue + !autoDrain.
  await h.publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: envelope({ type: "stop", sessionId: h.sessionId, payload: {} }),
  });
  h.engine.settle(h.sessionId, { aborted: true });
  await waitFor(() => h.snapshot().inputRouting.mode === "choice", "choice routing");

  const missingDisposition = await h.send({ text: "new input" });
  assert.equal(missingDisposition.status, "rejected");
  assert.equal(missingDisposition.reasonCode, "heldQueueDispositionRequired");

  const stale = await h.send({
    text: "new input",
    heldQueueDisposition: "keepQueueAndSend",
    expectedHeldQueueItemIds: [expectedIds[0] as string],
  });
  assert.equal(stale.status, "rejected");
  assert.equal(stale.reasonCode, "guard.heldQueueConfirmationStale");

  const cleared = await h.send({
    text: "new input",
    heldQueueDisposition: "clearQueueAndSend",
    expectedHeldQueueItemIds: expectedIds,
  });
  assert.equal(cleared.status, "accepted");
  assert.equal(cleared.result?.type === "inputAccepted" ? cleared.result.delivery : "", "startNow");
  await waitFor(() => h.snapshot().queue.items.length === 0, "cleared");
  assert.equal(h.snapshot().queue.autoDrain, true);
  assert.deepEqual(submittedTexts(h).includes("new input"), true);
  h.publisher.dispose();
});

test("held choice: keepQueueAndSend retains the items and starts the new input", async () => {
  const h = await start();
  await startBusy(h);
  await h.send({ text: "keep-a" });
  await h.publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: envelope({ type: "stop", sessionId: h.sessionId, payload: {} }),
  });
  h.engine.settle(h.sessionId, { aborted: true });
  await waitFor(() => h.snapshot().inputRouting.mode === "choice", "choice routing");

  const kept = await h.send({
    text: "kept send",
    heldQueueDisposition: "keepQueueAndSend",
    expectedHeldQueueItemIds: h.snapshot().queue.items.map((item) => item.queueItemId),
  });
  assert.equal(kept.status, "accepted");
  assert.equal(h.snapshot().queue.autoDrain, true);
  // autoDrain is re-armed but the new run is the kept input; the old item still waits.
  await waitFor(() => h.snapshot().queue.items.length === 1, "kept item");
  assert.deepEqual(submittedTexts(h).slice(0, 2), ["first", "kept send"]);
  h.publisher.dispose();
});

// ── CAS ──

test("queue CAS: stale baseRevision on a queue command is answered stale", async () => {
  const h = await start();
  await startBusy(h);
  await h.send({ text: "cas item" });
  const item = h.snapshot().queue.items[0];
  assert.ok(item);
  const revision = h.revision();
  const stale = await h.queueCommand(
    "deleteQueueItem",
    { queueItemId: item.queueItemId },
    revision - 1,
  );
  assert.equal(stale.status, "stale");
  assert.equal(stale.reasonCode, "proto.staleRevision");
  assert.equal(stale.revisionAtDecision, revision);
  // Nothing was deleted.
  assert.equal(h.snapshot().queue.items.length, 1);

  // Retrying against the reported revision converges.
  const retried = await h.queueCommand(
    "deleteQueueItem",
    { queueItemId: item.queueItemId },
    stale.revisionAtDecision,
  );
  assert.equal(retried.status, "accepted");
  await waitFor(() => h.snapshot().queue.items.length === 0, "deleted after retry");
  h.publisher.dispose();
});

// ── pure list helpers ──

test("queueItem helpers: positions, drain head, and move semantics", () => {
  const items = withQueuePositions([queuedItem("qi-x", 1), queuedItem("qi-y", 2)]);
  assert.deepEqual(
    items.map((item) => item.order.queuePosition),
    [0, 1],
  );
  assert.equal(nextDrainableItem(items)?.queueItemId, "qi-x");

  // y is already last: moving to the tail is a no-op (null), and a real move is not.
  assert.equal(moveQueueItem(items, "qi-y", null), null);
  const reordered = moveQueueItem(items, "qi-y", "qi-x");
  assert.ok(reordered);
  assert.deepEqual(
    withQueuePositions(reordered).map((item) => item.queueItemId),
    ["qi-y", "qi-x"],
  );
  // Already before its anchor → no-op.
  assert.equal(moveQueueItem(items, "qi-x", "qi-y"), null);

  // `steering` holds the texts pi still has queued; absence means pi already delivered it.
  const guides = [guideItem("qi-g1", 1), guideItem("qi-g2", 2)];
  assert.deepEqual(consumedGuideIds(guides, ["text-qi-g1", "text-qi-g2"]), []);
  assert.deepEqual(consumedGuideIds(guides, ["text-qi-g2"]), ["qi-g1"]);
  assert.deepEqual(consumedGuideIds(guides, []), ["qi-g1", "qi-g2"]);
  // Duplicate texts are matched one-to-one, so a second identical guide stays pending.
  const twin = [guideItem("qi-t1", 1), guideItem("qi-t2", 2)];
  assert.deepEqual(consumedGuideIds(twin, ["text-qi-t1"]), ["qi-t2"]);
});

test("queue snapshot: guide items never become the drain head", () => {
  const guide = guideItem("qi-guide", 1);
  const queued = queuedItem("qi-queue", 2);
  assert.equal(nextDrainableItem([guide, queued])?.queueItemId, "qi-queue");
  assert.equal(nextDrainableItem([guide]), undefined);
});

function guideItem(queueItemId: string, admissionSeq: number): QueueItem {
  return {
    sourceCommandId: `cmd-${queueItemId}`,
    queueItemId,
    clientId: "c",
    kind: "sendText",
    text: `text-${queueItemId}`,
    attachments: [],
    delivery: { requested: "auto", admitted: "guide" },
    order: { admissionSeq, queuePosition: 0 },
    steer: { state: "guided" },
    dispatch: { state: "queued" },
    admittedAt: 1_700_000_000_000,
  };
}

function queuedItem(queueItemId: string, admissionSeq: number): QueueItem {
  return {
    ...guideItem(queueItemId, admissionSeq),
    delivery: { requested: "auto", admitted: "queue" },
    steer: { state: "notRequested" },
  };
}
