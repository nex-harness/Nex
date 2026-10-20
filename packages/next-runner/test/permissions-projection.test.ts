import { test } from "node:test";
import assert from "node:assert/strict";
import {
  commandAckSchema,
  conversationSnapshotSchema,
  sessionsIndexSnapshotSchema,
  sessionsIndexTopicWireFrameSchema,
} from "@nex/shared/nex-protocol-v4";
import type { EngineEvent, EngineSessionState } from "../src/engine/types.js";
import { applyEngineEvent } from "../src/projection/fold.js";
import { projectHistory } from "../src/projection/restore.js";
import { buildConversationSnapshot } from "../src/projection/snapshot.js";
import { createProjection } from "../src/projection/state.js";
import { buildSessionsIndexSnapshot } from "../src/v4/sessionsIndex.js";
import { buildSessionsIndexFrame } from "../src/v4/envelopes.js";
import { FakeEngine } from "./helpers/fakeEngine.js";
import { ConversationPublisher } from "../src/v4/publisher.js";
import { createLogger } from "../src/logger.js";

/**
 * Fold/publisher behavior for approvals. Every emitted snapshot, sessions-index frame and
 * ACK is parsed with the real schemas, like the rest of this package's tests.
 */

const SESSION_ID = "sess-perm";
const LOG_EPOCH = "epoch-perm";
const T0 = 1_700_000_000_000;
const WORKSPACE = "/workspace/demo";
const logger = createLogger("error");

function baseState() {
  return createProjection({
    sessionId: SESSION_ID,
    logEpoch: LOG_EPOCH,
    createdAt: T0,
    model: {
      providerId: "anthropic",
      modelId: "claude-sonnet-4-5",
      thought: "medium",
      thoughtLevels: ["off", "medium"],
    },
  });
}

function approvalRequest(
  interactionId: string,
  toolCallId: string,
  toolName: string,
  input: unknown,
): EngineEvent {
  return {
    type: "approval_request",
    at: T0 + 100,
    approval: {
      interactionId,
      toolCallId,
      toolName,
      input,
      summary: toolName === "write" ? "write perm-ok.txt" : toolName,
      createdAt: T0 + 100,
    },
  };
}

function runningThenPending(): { state: ReturnType<typeof baseState>; toolRowId: number } {
  let state = baseState();
  state = applyEngineEvent(state, { type: "user_input", at: T0, text: "create a file" });
  state = applyEngineEvent(state, {
    type: "tool_call_start",
    at: T0 + 50,
    toolCallId: "tc-1",
    toolName: "write",
    input: { path: "perm-ok.txt", content: "OK" },
  });
  state = applyEngineEvent(
    state,
    approvalRequest("perm-1", "tc-1", "write", {
      path: "perm-ok.txt",
      content: "OK",
    }),
  );
  const toolRowId = state.toolRows["tc-1"]!;
  return { state, toolRowId };
}

test("fold: running → pendingApproval emits the full UI contract", () => {
  const { state, toolRowId } = runningThenPending();
  const snapshot = conversationSnapshotSchema.parse(buildConversationSnapshot(state));
  assert.equal(snapshot.pendingInteractions.length, 1);
  const interaction = snapshot.pendingInteractions[0]!;
  assert.equal(interaction.kind, "permission");
  assert.equal(interaction.anchorRowId, toolRowId);
  assert.equal(interaction.interactionId, "perm-1");
  assert.ok(interaction.payload.kind === "permission");
  assert.equal(interaction.payload.toolCallId, "tc-1");
  assert.equal(interaction.payload.toolName, "Write");
  assert.equal(interaction.payload.freeText, true);
  assert.deepEqual(interaction.payload.detail, {
    file_path: "perm-ok.txt",
    content: "OK",
  });
  assert.deepEqual(
    interaction.payload.options.map((option) => option.optionId),
    ["allowOnce", "allowAlways", "deny"],
  );
  assert.deepEqual(
    interaction.payload.options.map((option) => option.response?.decision),
    ["allow", "allow", "deny"],
  );
  const row = snapshot.rows.window.find((item) => item.kind === "toolCall");
  assert.equal(row?.kind === "toolCall" ? row.status : "", "pendingApproval");
  // The UI flips the composer to yolo when this is set; the gate must never set it.
  assert.equal(snapshot.config.permissionGrant, undefined);
});

test("fold: allow settles the row back to running and drops the interaction", () => {
  let { state } = runningThenPending();
  state = applyEngineEvent(state, {
    type: "approval_settled",
    at: T0 + 110,
    interactionId: "perm-1",
    toolCallId: "tc-1",
    outcome: "allow",
  });
  const snapshot = conversationSnapshotSchema.parse(buildConversationSnapshot(state));
  assert.equal(snapshot.pendingInteractions.length, 0);
  const row = snapshot.rows.window.find((item) => item.kind === "toolCall");
  assert.equal(row?.kind === "toolCall" ? row.status : "", "running");
  assert.equal(row?.kind === "toolCall" ? row.approvalInteractionId : "x", undefined);
});

test("fold: deny cancels the row and the blocked tool result must not turn it into error", () => {
  let { state } = runningThenPending();
  state = applyEngineEvent(state, {
    type: "approval_settled",
    at: T0 + 110,
    interactionId: "perm-1",
    toolCallId: "tc-1",
    outcome: "deny",
  });
  // pi reports a blocked call as an error tool result; cancelled stays authoritative.
  state = applyEngineEvent(state, {
    type: "tool_call_end",
    at: T0 + 120,
    toolCallId: "tc-1",
    toolName: "write",
    text: "User denied write.",
    isError: true,
  });
  const snapshot = conversationSnapshotSchema.parse(buildConversationSnapshot(state));
  assert.equal(snapshot.pendingInteractions.length, 0);
  const row = snapshot.rows.window.find((item) => item.kind === "toolCall");
  assert.equal(row?.kind === "toolCall" ? row.status : "", "cancelled");
  assert.equal(row?.kind === "toolCall" ? row.error : "none", undefined);
});

test("fold: abort while pending cancels the row and clears the card", () => {
  let { state } = runningThenPending();
  state = applyEngineEvent(state, {
    type: "approval_settled",
    at: T0 + 110,
    interactionId: "perm-1",
    toolCallId: "tc-1",
    outcome: "aborted",
  });
  state = applyEngineEvent(state, { type: "settled", at: T0 + 120, aborted: true });
  const snapshot = conversationSnapshotSchema.parse(buildConversationSnapshot(state));
  assert.equal(snapshot.pendingInteractions.length, 0);
  assert.equal(snapshot.control.phase, "completedInterrupted");
  const row = snapshot.rows.window.find((item) => item.kind === "toolCall");
  assert.equal(row?.kind === "toolCall" ? row.status : "", "cancelled");
});

test("fold: a late duplicate tool_call_start never drops an on-screen approval", () => {
  let { state } = runningThenPending();
  state = applyEngineEvent(state, {
    type: "tool_call_start",
    at: T0 + 105,
    toolCallId: "tc-1",
    toolName: "write",
    input: { path: "perm-ok.txt", content: "OK" },
  });
  const snapshot = conversationSnapshotSchema.parse(buildConversationSnapshot(state));
  const row = snapshot.rows.window.find((item) => item.kind === "toolCall");
  assert.equal(row?.kind === "toolCall" ? row.status : "", "pendingApproval");
});

test("restore: a tool call without a result restores as cancelled, never success", () => {
  const session: EngineSessionState = {
    sessionId: SESSION_ID,
    workspacePath: WORKSPACE,
    title: "Create file",
    titleSource: "default",
    createdAt: T0,
    lastActivityAt: T0 + 200,
    phase: "completedInterrupted",
    model: null,
    usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    isRunning: false,
    mode: "edit",
    planEnabled: false,
  };
  const state = projectHistory(
    session,
    [
      { role: "user", at: T0, text: "create two files" },
      {
        role: "assistant",
        at: T0 + 10,
        stopReason: "toolUse",
        parts: [
          {
            type: "toolCall",
            id: "tc-done",
            name: "write",
            arguments: { path: "a.txt", content: "a" },
          },
          {
            type: "toolCall",
            id: "tc-pending",
            name: "write",
            arguments: { path: "b.txt", content: "b" },
          },
        ],
      },
      {
        role: "toolResult",
        at: T0 + 20,
        toolCallId: "tc-done",
        toolName: "write",
        text: "wrote a.txt",
        isError: false,
      },
    ],
    LOG_EPOCH,
  );
  const snapshot = conversationSnapshotSchema.parse(buildConversationSnapshot(state));
  const tools = snapshot.rows.window.filter((item) => item.kind === "toolCall");
  const done = tools.find((item) => item.kind === "toolCall" && item.toolCallId === "tc-done");
  const pending = tools.find(
    (item) => item.kind === "toolCall" && item.toolCallId === "tc-pending",
  );
  assert.equal(done?.kind === "toolCall" ? done.status : "", "success");
  assert.equal(pending?.kind === "toolCall" ? pending.status : "", "cancelled");
  // Pending approvals are not persisted: a restart loses the turn's card.
  assert.equal(snapshot.pendingInteractions.length, 0);
  assert.equal(snapshot.config.mode, "edit");
});

test("snapshot: config.mode echoes the session mode and plan toggle", () => {
  let state = baseState();
  state = applyEngineEvent(state, {
    type: "mode_changed",
    at: T0 + 10,
    mode: "yolo",
    planEnabled: true,
  });
  const snapshot = conversationSnapshotSchema.parse(buildConversationSnapshot(state));
  assert.equal(snapshot.config.mode, "yolo");
  assert.equal(snapshot.config.planEnabled, true);
});

test("sessions-index: a live approval surfaces the sidebar attention dot", () => {
  const { state } = runningThenPending();
  const sessions: EngineSessionState[] = [
    {
      sessionId: SESSION_ID,
      workspacePath: WORKSPACE,
      title: "Create file",
      titleSource: "default",
      createdAt: T0,
      lastActivityAt: T0 + 100,
      phase: "running",
      model: null,
      usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      isRunning: true,
    },
  ];
  const pending = new Map([
    [
      SESSION_ID,
      Object.values(state.pendingApprovals).map((approval) => ({
        interactionId: approval.interactionId,
        toolName: approval.toolName,
      })),
    ],
  ]);
  const snapshot = sessionsIndexSnapshotSchema.parse(
    buildSessionsIndexSnapshot(WORKSPACE, "si-epoch", sessions, pending),
  );
  const summary = snapshot.sessions[0]!;
  assert.equal(summary.pendingInteraction?.interactionId, "perm-1");
  assert.equal(summary.pendingInteraction?.kind, "permission");
  assert.equal(summary.pendingInteraction?.toolName, "write");
  assert.deepEqual(summary.pendingInteractionSummary, {
    permissionCount: 1,
    userInputCount: 0,
  });
  const wire = buildSessionsIndexFrame({
    topic: `sessions-index/${WORKSPACE}`,
    subscriptionId: "si-1",
    ordinal: 1,
    logicalFrameId: "si-1-lf-1",
    deliveryKind: "online",
    snapshot,
    toSeq: 2,
    sentAt: T0,
  });
  sessionsIndexTopicWireFrameSchema.parse(wire);
});

test("publisher: resolveInteraction ACKs accepted then noop, both schema-valid", async () => {
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  try {
    const { sessionId } = await engine.createSession(WORKSPACE, {});
    engine.approvals.set("perm-1", "allowOnce");
    const first = await publisher.sendCommand({
      workspacePath: WORKSPACE,
      envelope: {
        commandId: "cmd-resolve-1",
        clientId: "client-1",
        sessionId,
        type: "resolveInteraction",
        payload: { interactionId: "perm-1", answer: { optionId: "allowOnce" } },
        issuedAt: T0,
      },
    });
    commandAckSchema.parse(first);
    assert.equal(first.status, "accepted");
    assert.equal(first.result?.type, "resolveInteraction");
    assert.deepEqual(first.result?.type === "resolveInteraction" ? first.result.resolvedBy : null, {
      clientId: "client-1",
      optionId: "allowOnce",
    });

    const second = await publisher.sendCommand({
      workspacePath: WORKSPACE,
      envelope: {
        commandId: "cmd-resolve-2",
        clientId: "client-1",
        sessionId,
        type: "resolveInteraction",
        payload: { interactionId: "perm-1", answer: { optionId: "allowOnce" } },
        issuedAt: T0,
      },
    });
    commandAckSchema.parse(second);
    assert.equal(second.status, "noop");
    assert.equal(second.reasonCode, "already_resolved");

    const unknown = await publisher.sendCommand({
      workspacePath: WORKSPACE,
      envelope: {
        commandId: "cmd-resolve-3",
        clientId: "client-1",
        sessionId,
        type: "resolveInteraction",
        payload: { interactionId: "perm-nope", answer: { optionId: "deny" } },
        issuedAt: T0,
      },
    });
    commandAckSchema.parse(unknown);
    assert.equal(unknown.status, "noop");

    // A duplicate commandId replays the stored ACK.
    const replay = await publisher.sendCommand({
      workspacePath: WORKSPACE,
      envelope: {
        commandId: "cmd-resolve-1",
        clientId: "client-1",
        sessionId,
        type: "resolveInteraction",
        payload: { interactionId: "perm-1", answer: { optionId: "allowOnce" } },
        issuedAt: T0,
      },
    });
    commandAckSchema.parse(replay);
    assert.equal(replay.status, "duplicate");
    assert.equal(engine.resolvedApprovals.length, 1);
  } finally {
    publisher.dispose();
    await engine.dispose();
  }
});

test("publisher: createSession/sendText mode reaches the engine and snapshot.config", async () => {
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  try {
    const ack = await publisher.sendCommand({
      workspacePath: WORKSPACE,
      envelope: {
        commandId: "cmd-create-1",
        clientId: "client-1",
        sessionId: null,
        type: "createSession",
        payload: {
          workspaceId: WORKSPACE,
          firstInput: { text: "write a file", mode: "edit", planEnabled: false },
        },
        issuedAt: T0,
      },
    });
    commandAckSchema.parse(ack);
    assert.equal(ack.result?.type, "createSession");
    const sessionId = ack.result?.type === "createSession" ? ack.result.sessionId : "";
    assert.ok(sessionId);
    assert.deepEqual(engine.permissionMode(sessionId), { mode: "edit", planEnabled: false });

    await publisher.subscribeConversation({ workspacePath: WORKSPACE, sessionId });
    const runtime = publisher.sessions.ensure(WORKSPACE, sessionId);
    await runtime.ready;
    const snapshot = buildConversationSnapshot(runtime.projection);
    conversationSnapshotSchema.parse(snapshot);
    assert.equal(snapshot.config.mode, "edit");
    assert.equal(snapshot.config.planEnabled, false);

    await publisher.sendCommand({
      workspacePath: WORKSPACE,
      envelope: {
        commandId: "cmd-send-1",
        clientId: "client-1",
        sessionId,
        type: "sendText",
        payload: { text: "now use bash", mode: "build", planEnabled: true },
        issuedAt: T0,
      },
    });
    const next = buildConversationSnapshot(runtime.projection);
    conversationSnapshotSchema.parse(next);
    assert.equal(next.config.mode, "build");
    assert.equal(next.config.planEnabled, true);
  } finally {
    publisher.dispose();
    await engine.dispose();
  }
});
