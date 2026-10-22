import { test } from "node:test";
import assert from "node:assert/strict";
import { conversationRowSchema, conversationSnapshotSchema } from "@nex/shared/nex-protocol-v4";
import { applyEngineEvent, applyUserInput } from "../src/projection/fold.js";
import { buildConversationSnapshot } from "../src/projection/snapshot.js";
import { deriveRowActions } from "../src/projection/rowActions.js";
import { createProjection, type ProjectionState } from "../src/projection/state.js";
import type { EngineEvent } from "../src/engine/types.js";

/**
 * Row action affordances (docs/next/conversation-features.md decision 1), mirroring
 * `materializeCommandRowActions` in the old CLI. Every snapshot is parsed with the real
 * schema so an illegal `actions` shape fails here instead of in the browser.
 */

const SESSION_ID = "sess-actions";
const LOG_EPOCH = "epoch-actions";
const T0 = 1_700_000_000_000;

function base(): ProjectionState {
  return createProjection({
    sessionId: SESSION_ID,
    logEpoch: LOG_EPOCH,
    createdAt: T0,
    model: {
      providerId: "anthropic",
      modelId: "claude-sonnet-4-5",
      thought: "high",
      thoughtLevels: ["low", "medium", "high"],
    },
  });
}

/** Drives one complete user→assistant turn through the live fold. */
function completeTurn(
  state: ProjectionState,
  text: string,
  userEntryId: string,
  assistantEntryId: string,
  at: number,
): ProjectionState {
  const events: EngineEvent[] = [
    { type: "user_input", at, text },
    { type: "message_entry", at: at + 1, entryId: userEntryId, role: "user" },
    { type: "assistant_start", at: at + 2, model: "claude-sonnet-4-5" },
    { type: "text_delta", at: at + 3, delta: `reply to ${text}` },
    { type: "assistant_end", at: at + 4, stopReason: "stop" },
    { type: "message_entry", at: at + 5, entryId: assistantEntryId, role: "assistant" },
    { type: "settled", at: at + 6, aborted: false },
  ];
  return events.reduce(applyEngineEvent, state);
}

test("actions: single completed turn exposes edit on the user row, retry+fork on the reply", () => {
  let state = completeTurn(base(), "hello", "e-user-1", "e-asst-1", T0);
  const snapshot = conversationSnapshotSchemaParse(state);
  const user = snapshot.rows.window.find((row) => row.kind === "userInput");
  const assistant = snapshot.rows.window.find((row) => row.kind === "assistantText");
  assert.equal(user?.entityId, "pi:e-user-1");
  assert.deepEqual(user?.actions, { canEdit: true, editDisposition: "rewind" });
  assert.equal(assistant?.entityId, "pi:e-asst-1:text:0");
  assert.deepEqual(assistant?.actions, { canRetry: true, canFork: true });

  // Every row carries productTurnId = turnId and visibility:"visible".
  for (const row of snapshot.rows.window) {
    assert.equal(row.productTurnId, row.turnId);
    assert.equal(row.visibility, "visible");
    conversationRowSchema.parse(row);
  }
  // Derived actions never leak into the projection state (they are computed per snapshot).
  assert.equal(
    state.rows.some((row) => row.actions !== undefined),
    false,
  );
});

test("actions: only the newest real-user row is editable; earlier turns lose edit", () => {
  let state = completeTurn(base(), "first", "e-user-1", "e-asst-1", T0);
  state = completeTurn(state, "second", "e-user-2", "e-asst-2", T0 + 100);
  const snapshot = conversationSnapshotSchemaParse(state);
  const users = snapshot.rows.window.filter((row) => row.kind === "userInput");
  assert.equal(users.length, 2);
  assert.equal(users[0]?.actions?.canEdit, undefined);
  assert.deepEqual(users[1]?.actions, { canEdit: true, editDisposition: "rewind" });
});

test("actions: only the newest assistant keeps retry; fork stays on the last text segment of each successful turn", () => {
  let state = completeTurn(base(), "first", "e-user-1", "e-asst-1", T0);
  state = completeTurn(state, "second", "e-user-2", "e-asst-2", T0 + 100);
  const snapshot = conversationSnapshotSchemaParse(state);
  const assistants = snapshot.rows.window.filter((row) => row.kind === "assistantText");
  assert.equal(assistants.length, 2);
  // Turn 1 is no longer the latest assistant → retry goes away, fork remains (its own turn
  // is completedSuccess and this row is that turn's last text segment).
  assert.equal(assistants[0]?.actions?.canRetry, undefined);
  assert.equal(assistants[0]?.actions?.canFork, true);
  assert.deepEqual(assistants[1]?.actions, { canRetry: true, canFork: true });
});

test("actions: while running, no retry/fork; the new user row is still editable", () => {
  let state = completeTurn(base(), "first", "e-user-1", "e-asst-1", T0);
  state = applyEngineEvent(state, { type: "user_input", at: T0 + 100, text: "second" });
  state = applyEngineEvent(state, {
    type: "message_entry",
    at: T0 + 101,
    entryId: "e-user-2",
    role: "user",
  });
  state = applyEngineEvent(state, { type: "assistant_start", at: T0 + 102 });
  state = applyEngineEvent(state, { type: "text_delta", at: T0 + 103, delta: "working" });
  const snapshot = conversationSnapshotSchemaParse(state);
  const users = snapshot.rows.window.filter((row) => row.kind === "userInput");
  assert.deepEqual(users.at(-1)?.actions, { canEdit: true, editDisposition: "rewind" });
  const assistants = snapshot.rows.window.filter((row) => row.kind === "assistantText");
  // The older complete reply is not the newest assistant any more, and its unstreaming
  // turn no longer qualifies as the fork target for retry purposes… fork still applies
  // per turn, but retry never does while a newer incomplete reply exists.
  assert.equal(
    assistants.some((row) => row.actions?.canRetry === true),
    false,
  );
  // The streaming reply cannot be forked or retried.
  const streaming = assistants.at(-1);
  assert.equal(streaming?.kind === "assistantText" ? streaming.state : "", "streaming");
  assert.equal(streaming?.actions, undefined);
});

test("actions: an interrupted turn yields no edit-after-abort fork, and retry needs a complete reply", () => {
  let state = base();
  state = applyUserInput(state, "hi", { at: T0, sourceCommandId: "" });
  state = applyEngineEvent(state, {
    type: "message_entry",
    at: T0 + 1,
    entryId: "e-user-1",
    role: "user",
  });
  state = applyEngineEvent(state, { type: "assistant_start", at: T0 + 2 });
  state = applyEngineEvent(state, { type: "text_delta", at: T0 + 3, delta: "partial" });
  state = applyEngineEvent(state, { type: "assistant_end", at: T0 + 4, stopReason: "aborted" });
  state = applyEngineEvent(state, {
    type: "message_entry",
    at: T0 + 5,
    entryId: "e-asst-1",
    role: "assistant",
  });
  state = applyEngineEvent(state, { type: "settled", at: T0 + 6, aborted: true });
  const snapshot = conversationSnapshotSchemaParse(state);
  const assistant = snapshot.rows.window.find((row) => row.kind === "assistantText");
  assert.equal(assistant?.kind === "assistantText" ? assistant.state : "", "interrupted");
  // No retry (not complete) and no fork (turn ended completedInterrupted).
  assert.equal(assistant?.actions, undefined);
  // Editing stays available: the user row is still the newest real input.
  const user = snapshot.rows.window.find((row) => row.kind === "userInput");
  assert.equal(user?.actions?.canEdit, true);
});

test("actions: rows without a resolved entityId expose no action (target could not be sent)", () => {
  // A restore/history-less stream can produce rows before `message_entry` arrives. The old
  // CLI required a resolvable message id; here the entityId *is* that check.
  let state = base();
  state = applyUserInput(state, "hi", { at: T0, sourceCommandId: "" });
  state = applyEngineEvent(state, { type: "assistant_start", at: T0 + 1 });
  state = applyEngineEvent(state, { type: "text_delta", at: T0 + 2, delta: "hello" });
  state = applyEngineEvent(state, { type: "assistant_end", at: T0 + 3, stopReason: "stop" });
  state = applyEngineEvent(state, { type: "settled", at: T0 + 4, aborted: false });
  const snapshot = conversationSnapshotSchemaParse(state);
  assert.equal(
    snapshot.rows.window.every((row) => row.entityId === undefined),
    true,
  );
  const actions = deriveRowActions(state);
  assert.equal(actions.size, 0);
});

test("actions: later text after a tool call is a distinct segment, and only the last one can fork", () => {
  let state = base();
  state = applyUserInput(state, "run it", { at: T0, sourceCommandId: "" });
  state = applyEngineEvent(state, {
    type: "message_entry",
    at: T0 + 1,
    entryId: "e-user-1",
    role: "user",
  });
  state = applyEngineEvent(state, { type: "assistant_start", at: T0 + 2 });
  state = applyEngineEvent(state, { type: "text_delta", at: T0 + 3, delta: "running" });
  state = applyEngineEvent(state, {
    type: "tool_call_start",
    at: T0 + 4,
    toolCallId: "tc-1",
    toolName: "bash",
    input: { command: "ls" },
  });
  state = applyEngineEvent(state, {
    type: "tool_call_end",
    at: T0 + 5,
    toolCallId: "tc-1",
    toolName: "bash",
    text: "a.txt",
    isError: false,
  });
  state = applyEngineEvent(state, { type: "text_delta", at: T0 + 6, delta: "done" });
  state = applyEngineEvent(state, { type: "assistant_end", at: T0 + 7, stopReason: "stop" });
  state = applyEngineEvent(state, {
    type: "message_entry",
    at: T0 + 8,
    entryId: "e-asst-1",
    role: "assistant",
  });
  state = applyEngineEvent(state, { type: "settled", at: T0 + 9, aborted: false });

  const snapshot = conversationSnapshotSchemaParse(state);
  const texts = snapshot.rows.window.filter((row) => row.kind === "assistantText");
  assert.equal(texts.length, 2);
  assert.deepEqual(
    texts.map((row) => row.entityId),
    ["pi:e-asst-1:text:0", "pi:e-asst-1:text:1"],
  );
  assert.equal(texts[0]?.actions?.canFork, undefined);
  assert.equal(texts[1]?.actions?.canFork, true);
  assert.equal(texts[1]?.actions?.canRetry, true);
  const tool = snapshot.rows.window.find((row) => row.kind === "toolCall");
  assert.equal(tool?.entityId, "pi:tool:tc-1");
  assert.equal(tool?.actions, undefined);
});

function conversationSnapshotSchemaParse(state: ProjectionState) {
  return conversationSnapshotSchema.parse(buildConversationSnapshot(state));
}
