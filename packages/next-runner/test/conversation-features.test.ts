import { test } from "node:test";
import assert from "node:assert/strict";
import { commandAckSchema, conversationSnapshotSchema } from "@nex/shared/nex-protocol-v4";
import { projectHistory } from "../src/projection/restore.js";
import { createProjection } from "../src/projection/state.js";
import { applyEngineEvent } from "../src/projection/fold.js";
import { buildConversationSnapshot } from "../src/projection/snapshot.js";
import {
  CONVERSATION_WORKSPACE,
  snapshotOf,
  startConversationHarness,
  waitFor,
} from "./helpers/conversationHarness.js";

/**
 * Batch 2d through the real command handler + projection over the engine double
 * (docs/next/conversation-features.md decisions 5-7). Every ACK/snapshot is parsed with the
 * real zod schemas.
 */

function compactMarkers(h: Awaited<ReturnType<typeof startConversationHarness>>) {
  return h
    .snapshot()
    .rows.window.flatMap((row) =>
      row.kind === "timelineMarker" && row.marker.type === "compact" ? [row.marker] : [],
    );
}

test("compact (idle): running marker → success, tokens, origin manual, lineage id", async () => {
  const h = await startConversationHarness();
  try {
    const ack = await h.command("compact");
    commandAckSchema.parse(ack);
    assert.equal(ack.status, "accepted");
    assert.equal(h.engine.conversation.compacts.length, 1);
    assert.equal(h.engine.conversation.compacts[0]?.sourceCommandId, ack.commandId);
    await waitFor(() => compactMarkers(h)[0]?.status === "success", "compact marker success");
    const [marker] = compactMarkers(h);
    assert.deepEqual(marker, {
      type: "compact",
      origin: "manual",
      status: "success",
      tokensBefore: 100,
      tokensAfter: 20,
    });
    const row = h.snapshot().rows.window.find((item) => item.kind === "timelineMarker");
    assert.equal(row?.kind === "timelineMarker" ? row.sourceCommandId : undefined, ack.commandId);
    assert.equal(row?.kind === "timelineMarker" ? row.lane : undefined, "assistantWork");
    // Idle again: no compact work, no lock.
    assert.deepEqual(h.snapshot().availability.compact, { allowed: true });
    assert.deepEqual(h.snapshot().availability.fork, { allowed: true });
  } finally {
    h.dispose();
  }
});

test("compact: noop / cancelled / failed map to their marker statuses", async () => {
  for (const status of ["noop", "cancelled", "failed"] as const) {
    const h = await startConversationHarness();
    try {
      h.engine.conversation.compactOutcome = { status, origin: "manual" };
      const ack = await h.command("compact");
      assert.equal(ack.status, "accepted");
      await waitFor(() => compactMarkers(h)[0]?.status === status, `marker ${status}`);
      assert.equal(compactMarkers(h)[0]?.status, status);
    } finally {
      h.dispose();
    }
  }
});

test("compact: a second compact while one runs is rejected compactOperationLock", async () => {
  const h = await startConversationHarness();
  try {
    let release: () => void = () => {};
    h.engine.conversation.compactOutcome = { status: "success", origin: "manual" };
    const pending = new Promise<void>((resolve) => {
      release = resolve;
    });
    const originalCompact = h.engine.compact.bind(h.engine);
    h.engine.compact = async (sessionId, sourceCommandId) => {
      await pending;
      return originalCompact(sessionId, sourceCommandId);
    };
    const first = await h.command("compact");
    assert.equal(first.status, "accepted");
    await waitFor(() => compactMarkers(h)[0]?.status === "running", "marker running");
    // While the marker is open the action is locked and a second command is refused.
    assert.deepEqual(h.snapshot().availability.compact, {
      allowed: false,
      reasonCode: "compactOperationLock",
    });
    const second = await h.command("compact");
    assert.equal(second.status, "rejected");
    assert.equal(second.reasonCode, "compactOperationLock");
    release();
    await waitFor(() => compactMarkers(h)[0]?.status === "success", "marker success");
    assert.equal(h.engine.conversation.compacts.length, 1);
  } finally {
    h.dispose();
  }
});

test("compact while busy is queued as a compact item and drains after the run", async () => {
  const h = await startConversationHarness();
  try {
    h.engine.emit(h.sessionId, { type: "user_input", at: Date.now(), text: "long prompt" });
    h.engine.emit(h.sessionId, { type: "assistant_start", at: Date.now() });
    h.engine.streaming.add(h.sessionId);
    await waitFor(() => h.snapshot().control.phase === "running", "running");
    const ack = await h.command("compact");
    assert.equal(ack.status, "accepted");
    const queue = h.snapshot().queue.items;
    assert.equal(queue.length, 1);
    assert.equal(queue[0]?.kind, "compact");
    assert.equal(queue[0]?.text, "/compact");
    // Locked while queued.
    assert.equal(h.snapshot().availability.compact.allowed, false);
    // Finish the run: the drain promotes the compact item and runs it.
    h.engine.streaming.delete(h.sessionId);
    h.engine.emit(h.sessionId, { type: "text_delta", at: Date.now(), delta: "done" });
    h.engine.emit(h.sessionId, { type: "assistant_end", at: Date.now(), stopReason: "stop" });
    h.engine.emit(h.sessionId, { type: "settled", at: Date.now(), aborted: false });
    await waitFor(
      () => h.engine.conversation.compacts.length === 1 && h.snapshot().queue.items.length === 0,
      "queued compact drained",
    );
    await waitFor(() => compactMarkers(h)[0]?.status === "success", "marker success");
  } finally {
    h.dispose();
  }
});

test("compact: a failed queued compaction pauses the queue instead of running it", async () => {
  const h = await startConversationHarness();
  try {
    h.engine.emit(h.sessionId, { type: "user_input", at: Date.now(), text: "long prompt" });
    h.engine.emit(h.sessionId, { type: "assistant_start", at: Date.now() });
    h.engine.streaming.add(h.sessionId);
    h.engine.conversation.compactOutcome = { status: "failed", origin: "manual" };
    await h.command("compact");
    await h.send("queued prompt");
    assert.equal(h.snapshot().queue.items.length, 2);
    h.engine.streaming.delete(h.sessionId);
    h.engine.emit(h.sessionId, { type: "assistant_end", at: Date.now(), stopReason: "stop" });
    h.engine.emit(h.sessionId, { type: "settled", at: Date.now(), aborted: false });
    await waitFor(() => compactMarkers(h)[0]?.status === "failed", "compact failed");
    // The queued text is held (not replayed), with the paused banner state.
    await waitFor(() => h.snapshot().queue.autoDrain === false, "queue paused");
    assert.equal(h.snapshot().queue.pauseReason, "error");
    assert.equal(h.snapshot().queue.items.length, 1);
    assert.equal(h.snapshot().queue.items[0]?.text, "queued prompt");
  } finally {
    h.dispose();
  }
});

test("editUserQuery: rewinds the latest turn, removes old rows, re-prompts, keeps lineage", async () => {
  const h = await startConversationHarness();
  try {
    // The originals were started by sendText commands; the edit must keep that lineage.
    await h.submitText("first question", "u1");
    await h.runTurn("first question", "first answer", { entry: "u1", applyText: false });
    await h.runTurn("second question", "second answer", { entry: "u2" });
    const before = h.snapshot();
    const userRow = before.rows.window.find((row) => row.entityId === "pi:u2");
    assert.equal(userRow?.kind, "userInput");
    assert.equal(userRow?.actions?.canEdit, true);
    const rowIdsBefore = before.rows.window.map((row) => row.rowId);

    const ack = await h.command("editUserQuery", {
      target: { rowId: userRow?.rowId, entityId: "pi:u2" },
      newText: "edited question",
    });
    commandAckSchema.parse(ack);
    assert.equal(ack.status, "accepted");
    assert.equal(ack.result?.type, "editUserQuery");
    assert.equal(ack.result?.type === "editUserQuery" ? ack.result.disposition : "", "rewind");
    assert.equal(ack.result?.type === "editUserQuery" ? ack.result.sessionId : "", h.sessionId);
    assert.equal(h.engine.conversation.rewinds.length, 1);
    assert.equal(h.engine.conversation.rewinds[0]?.request.target.userEntryId, "u2");
    assert.equal(h.engine.conversation.rewinds[0]?.request.newText, "edited question");

    // pi emits the re-prompted turn's events; asynchronously subscribe to catch its frames.
    await waitFor(
      () =>
        h
          .snapshot()
          .rows.window.some((row) => row.kind === "userInput" && row.text === "edited question"),
      "rewritten user row",
    );
    const after = h.snapshot();
    conversationSnapshotSchema.parse(after);
    assert.equal(
      after.rows.window.some((row) => row.kind === "assistantText" && row.text === "second answer"),
      false,
    );
    assert.equal(
      after.rows.window.some((row) => row.kind === "assistantText" && row.text === "first answer"),
      true,
    );
    // New user row with a NEW row id (monotonic, never reused), same command lineage.
    const newUserRow = after.rows.window.find(
      (row) => row.kind === "userInput" && row.text === "edited question",
    );
    assert.ok(newUserRow);
    // Lineage = the ORIGINAL command that started the rewritten turn, not this edit (the
    // scripted turns had no command, so no lineage is carried over).
    assert.equal(
      newUserRow.kind === "userInput" ? (newUserRow.sourceCommandId ?? null) : "x",
      null,
    );
    const header = after.rows.window.find((row) => row.turnId === newUserRow.turnId);
    assert.equal(header?.kind === "turnHeader" ? (header.sourceCommandId ?? null) : "x", null);
    assert.equal(rowIdsBefore.includes(newUserRow.rowId), false);
    // Nothing rest of the transcript lost: first turn's two rows survive.
    assert.equal(
      after.rows.window.some((row) => row.kind === "userInput" && row.text === "first question"),
      true,
    );
  } finally {
    h.dispose();
  }
});

test("editUserQuery: guards (latest-only, empty text, attachments, stray target)", async () => {
  const h = await startConversationHarness();
  try {
    await h.runTurn("q1", "a1", { entry: "u1" });
    await h.runTurn("q2", "a2", { entry: "u2" });
    const rows = h.snapshot().rows.window;
    const older = rows.find((row) => row.entityId === "pi:u1");
    const newer = rows.find((row) => row.entityId === "pi:u2");

    const olderAck = await h.command("editUserQuery", {
      target: { rowId: older?.rowId, entityId: "pi:u1" },
      newText: "nope",
    });
    assert.equal(olderAck.status, "rejected");
    assert.equal(olderAck.reasonCode, "guard.latestQueryEditOnly");

    const empty = await h.command("editUserQuery", {
      target: { rowId: newer?.rowId, entityId: "pi:u2" },
      newText: "   ",
    });
    assert.equal(empty.status, "rejected");
    assert.equal(empty.reasonCode, "proto.invalidPayload");

    const withAttachment = await h.command("editUserQuery", {
      target: { rowId: newer?.rowId, entityId: "pi:u2" },
      newText: "text",
      attachments: [{ id: "a1", name: "x.png", mimeType: "image/png", size: 1, kind: "file" }],
    });
    assert.equal(withAttachment.status, "rejected");
    assert.equal(withAttachment.reasonCode, "guard.attachmentsUnsupported");

    const stray = await h.command("editUserQuery", {
      target: { rowId: 999, entityId: "pi:missing" },
      newText: "text",
    });
    assert.equal(stray.status, "rejected");
    assert.equal(stray.reasonCode, "guard.rewindTargetUnresolved");
    assert.equal(h.engine.conversation.rewinds.length, 0);
  } finally {
    h.dispose();
  }
});

test("editUserQuery workspaceMode=rewind: blocked with guard + valid synthetic preview", async () => {
  const h = await startConversationHarness();
  try {
    await h.runTurn("q1", "a1", { entry: "u1" });
    const user = h.snapshot().rows.window.find((row) => row.entityId === "pi:u1");
    const ack = await h.command("editUserQuery", {
      target: { rowId: user?.rowId, entityId: "pi:u1" },
      newText: "edited",
      workspaceMode: "rewind",
    });
    assert.equal(ack.status, "accepted");
    const result = ack.result;
    assert.equal(result?.type, "editUserQuery");
    if (result?.type !== "editUserQuery") throw new Error("unexpected ACK shape");
    assert.equal(result.disposition, "blocked");
    assert.equal(result.reasonCode, "guard.workspaceRewindUnavailable");
    assert.deepEqual(result.preview, {
      canApply: false,
      ignoredFiles: [],
      safeFiles: [],
      unsafeFiles: [],
    });
    // Nothing was rewound or removed.
    assert.equal(h.engine.conversation.rewinds.length, 0);
    assert.equal(
      h.snapshot().rows.window.some((row) => row.kind === "assistantText" && row.text === "a1"),
      true,
    );
  } finally {
    h.dispose();
  }
});

test("retryTurn: re-runs the original user text and drops the old reply", async () => {
  const h = await startConversationHarness();
  try {
    await h.runTurn("original question", "old answer", { entry: "u1" });
    const reply = h
      .snapshot()
      .rows.window.find((row) => row.kind === "assistantText" && row.text === "old answer");
    assert.equal(reply?.actions?.canRetry, true);
    h.engine.conversation.userCauses.set("u1-a", {
      userEntryId: "u1",
      text: "original question",
    });
    const ack = await h.command("retryTurn", {
      target: { rowId: reply?.rowId, entityId: "pi:u1-a:text:0" },
    });
    commandAckSchema.parse(ack);
    assert.equal(ack.status, "accepted");
    assert.equal(h.engine.conversation.rewinds[0]?.request.target.userEntryId, "u1");
    assert.equal(h.engine.conversation.rewinds[0]?.request.newText, "original question");
    const after = h.snapshot();
    assert.equal(
      after.rows.window.some((row) => row.kind === "assistantText" && row.text === "old answer"),
      false,
    );
    assert.equal(
      after.rows.window.some((row) => row.kind === "userInput" && row.text === "original question"),
      true,
    );
  } finally {
    h.dispose();
  }
});

test("retryTurn: rejects a non-latest assistant reply", async () => {
  const h = await startConversationHarness();
  try {
    await h.runTurn("q1", "a1", { entry: "u1" });
    await h.runTurn("q2", "a2", { entry: "u2" });
    const olderReply = h
      .snapshot()
      .rows.window.find((row) => row.kind === "assistantText" && row.text === "a1");
    h.engine.conversation.userCauses.set("u1-a", { userEntryId: "u1", text: "q1" });
    const ack = await h.command("retryTurn", {
      target: { rowId: olderReply?.rowId, entityId: "pi:u1-a:text:0" },
    });
    assert.equal(ack.status, "rejected");
    assert.equal(ack.reasonCode, "guard.latestAssistantRetryOnly");
  } finally {
    h.dispose();
  }
});

test("forkAssistant: creates a child session, ACK sessionId, parent rows untouched", async () => {
  const h = await startConversationHarness();
  try {
    await h.runTurn("fork source", "fork answer", { entry: "u1" });
    const parentRowsBefore = JSON.stringify(h.snapshot().rows.window);
    const reply = h
      .snapshot()
      .rows.window.find((row) => row.kind === "assistantText" && row.text === "fork answer");
    assert.equal(reply?.actions?.canFork, true);
    const ack = await h.command("forkAssistant", {
      target: { rowId: reply?.rowId, entityId: "pi:u1-a:text:0" },
    });
    commandAckSchema.parse(ack);
    assert.equal(ack.status, "accepted");
    assert.equal(ack.result?.type, "forkAssistant");
    const childId = ack.result?.type === "forkAssistant" ? ack.result.sessionId : "";
    assert.ok(childId && childId !== h.sessionId);
    assert.equal(h.engine.conversation.forks.length, 1);
    assert.equal(h.engine.conversation.forks[0]?.request.targetEntryId, "u1-a");
    // Parent: no row changes at all (a fork only adds a session).
    assert.equal(JSON.stringify(h.snapshot().rows.window), parentRowsBefore);
    const child = await h.engine.listSessions(CONVERSATION_WORKSPACE);
    const summary = child.find((item) => item.sessionId === childId);
    assert.equal(summary?.titleSource, "generated");
    assert.match(summary?.title ?? "", /^Fork of /);
  } finally {
    h.dispose();
  }
});

test("forkAssistant: refuses an unstable target and a running compaction", async () => {
  const h = await startConversationHarness();
  try {
    await h.runTurn("q1", "a1", { entry: "u1" });
    const reply = h
      .snapshot()
      .rows.window.find((row) => row.kind === "assistantText" && row.text === "a1");
    const user = h.snapshot().rows.window.find((row) => row.entityId === "pi:u1");
    const userFork = await h.command("forkAssistant", {
      target: { rowId: user?.rowId, entityId: "pi:u1" },
    });
    assert.equal(userFork.status, "rejected");
    assert.equal(userFork.reasonCode, "guard.forkTargetNotStable");

    // An open compact marker locks fork (same guard id as the ACK).
    const originalCompact = h.engine.compact.bind(h.engine);
    let release: () => void = () => {};
    h.engine.compact = async (sessionId, id) => {
      await new Promise<void>((resolve) => {
        release = resolve;
      });
      return originalCompact(sessionId, id);
    };
    await h.command("compact");
    await waitFor(() => h.snapshot().availability.fork.allowed === false, "fork locked");
    const locked = await h.command("forkAssistant", {
      target: { rowId: reply?.rowId, entityId: "pi:u1-a:text:0" },
    });
    assert.equal(locked.status, "rejected");
    assert.equal(locked.reasonCode, "compactOperationLock");
    release();
    await waitFor(() => h.snapshot().availability.fork.allowed === true, "fork unlocked");
  } finally {
    h.dispose();
  }
});

test("compact marker lifecycle (fold): auto start/end, cancelled without result → noop", () => {
  const base = createProjection({ sessionId: "s", logEpoch: "e", createdAt: 1 });
  const running = applyEngineEvent(
    { ...base, phase: "running" },
    { type: "compact_start", at: 10, reason: "threshold" },
  );
  assert.equal(Object.keys(running.compactOperations).length, 1);
  const snapshot = buildConversationSnapshot(running);
  conversationSnapshotSchema.parse(snapshot);
  // Lane `assistantWork`: the marker joins the running turn's work, so find it by type.
  const autoMarker = snapshot.rows.window.find(
    (row) => row.kind === "timelineMarker" && row.marker.type === "compact",
  );
  assert.equal(
    autoMarker?.kind === "timelineMarker" && autoMarker.marker.type === "compact"
      ? autoMarker.marker.origin
      : "",
    "auto",
  );
  assert.deepEqual(
    snapshot.control.activeWorks.map((work) => work.kind),
    ["primaryTurn", "compact"],
  );

  const done = applyEngineEvent(running, {
    type: "compact_end",
    at: 20,
    reason: "threshold",
    result: { tokensBefore: 50, tokensAfter: 10 },
    aborted: false,
  });
  assert.equal(Object.keys(done.compactOperations).length, 0);
  const doneSnapshot = buildConversationSnapshot(done);
  conversationSnapshotSchema.parse(doneSnapshot);
  const marker = doneSnapshot.rows.window[0];
  assert.equal(
    marker?.kind === "timelineMarker" && marker.marker.type === "compact"
      ? marker.marker.status
      : "",
    "success",
  );

  // A declined automatic attempt (no start, no result) never shows a cancelled card.
  const declined = applyEngineEvent(base, {
    type: "compact_end",
    at: 30,
    reason: "overflow",
    aborted: true,
  });
  const declinedMarker = buildConversationSnapshot(declined).rows.window.find(
    (row) => row.kind === "timelineMarker",
  );
  assert.equal(
    declinedMarker?.kind === "timelineMarker" && declinedMarker.marker.type === "compact"
      ? declinedMarker.marker.status
      : "",
    "noop",
  );
});

test("restore projects a forkNotice from the nex.fork custom entry", () => {
  const session = {
    sessionId: "s",
    workspacePath: CONVERSATION_WORKSPACE,
    title: "Fork of parent",
    titleSource: "generated" as const,
    createdAt: 1,
    lastActivityAt: 2,
    phase: "completedSuccess" as const,
    model: null,
    usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    isRunning: false,
  };
  const state = projectHistory(
    session,
    [
      {
        role: "runnerMarker",
        kind: "forkNotice",
        at: 2,
        entryId: "f1",
        parentSessionId: "parent-1",
        parentRowId: 7,
      },
      { role: "user", at: 3, text: "hello", entryId: "u1" },
      {
        role: "assistant",
        at: 4,
        parts: [{ type: "text", text: "hi" }],
        stopReason: "stop",
        entryId: "a1",
      },
    ],
    "epoch",
  );
  const snapshot = buildConversationSnapshot(state);
  conversationSnapshotSchema.parse(snapshot);
  const notice = snapshot.rows.window.find(
    (row) => row.kind === "timelineMarker" && row.marker.type === "forkNotice",
  );
  assert.ok(notice);
  assert.deepEqual(notice.kind === "timelineMarker" ? notice.marker : {}, {
    type: "forkNotice",
    parentSessionId: "parent-1",
    parentRowId: 7,
  });
  assert.equal(notice.entityId, "pi:marker:f1");
});

test("queue: compactQueued/availability and snapshot schema accept a queued compact item", async () => {
  const h = await startConversationHarness();
  try {
    h.engine.emit(h.sessionId, { type: "user_input", at: Date.now(), text: "busy" });
    h.engine.emit(h.sessionId, { type: "assistant_start", at: Date.now() });
    h.engine.streaming.add(h.sessionId);
    await h.command("compact");
    // The item is queued (not run): no manual compaction reached the engine yet.
    assert.equal(h.engine.conversation.compacts.length, 0);
    // A second compact while one is queued is rejected up front (old CLI dedupe).
    const second = await h.command("compact");
    assert.equal(second.reasonCode, "compactOperationLock");
    const snapshot = snapshotOf(h.frames);
    assert.equal(snapshot.availability.compact.allowed, false);
    assert.equal(snapshot.availability.fork.allowed, false);
    // Editing is refused while a compaction is pending (it would abort it) — the lock is
    // checked before the target, so even a stale target gets it.
    const edit = await h.command("editUserQuery", {
      target: { rowId: 1, entityId: "pi:any" },
      newText: "x",
    });
    assert.equal(edit.reasonCode, "compactOperationLock");
    // Resume the queue by ending the turn as a completed one: the drain runs the compact.
    h.engine.streaming.delete(h.sessionId);
    h.engine.emit(h.sessionId, { type: "assistant_end", at: Date.now(), stopReason: "stop" });
    h.engine.emit(h.sessionId, { type: "settled", at: Date.now(), aborted: false });
    await waitFor(() => h.engine.conversation.compacts.length === 1, "queued compact ran");
  } finally {
    h.dispose();
  }
});
