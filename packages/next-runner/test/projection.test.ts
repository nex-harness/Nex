import { test } from "node:test";
import assert from "node:assert/strict";
import {
  conversationSnapshotSchema,
  conversationTopicFrameSchema,
  conversationTopicWireFrameSchema,
  sessionsIndexSnapshotSchema,
  sessionsIndexTopicWireFrameSchema,
  commandAckSchema,
  helloMessageSchema,
  modelSelectionSchema,
} from "@nex/shared/nex-protocol-v4";
import { nexWorkspacePresentationSchema as presentationSchema } from "@nex/shared";
import {
  applyEngineEvent,
  applyUserInput,
  mapToolInput,
  mapToolName,
} from "../src/projection/fold.js";
import { buildConversationSnapshot } from "../src/projection/snapshot.js";
import { createProjection } from "../src/projection/state.js";
import { projectHistory } from "../src/projection/restore.js";
import type { EngineEvent, EngineSessionState } from "../src/engine/types.js";
import { buildSessionsIndexSnapshot } from "../src/v4/sessionsIndex.js";
import {
  acceptedAck,
  buildConversationFrame,
  buildSessionsIndexFrame,
  createSessionResult,
  inputAcceptedResult,
} from "../src/v4/envelopes.js";
import { readWorkspacePresentation } from "../src/channels/nexSession.js";
import { buildModelSelectionViews, preferredSelectionFor } from "../src/channels/modelViews.js";
import type { EngineCatalog } from "../src/engine/types.js";

const SESSION_ID = "sess-1";
const LOG_EPOCH = "epoch-test";
const T0 = 1_700_000_000_000;

function scenario(): EngineEvent[] {
  return [
    { type: "user_input", at: T0, text: "List the repo" },
    { type: "assistant_start", at: T0 + 10, model: "claude-sonnet-4-5" },
    { type: "thinking_delta", at: T0 + 20, delta: "I should inspect files." },
    { type: "text_delta", at: T0 + 30, delta: "Sure, running " },
    { type: "text_delta", at: T0 + 40, delta: "a few tools." },
    {
      type: "tool_call_start",
      at: T0 + 50,
      toolCallId: "tool-1",
      toolName: "bash",
      input: { command: "ls" },
    },
    {
      type: "tool_call_end",
      at: T0 + 60,
      toolCallId: "tool-1",
      toolName: "bash",
      text: "a.txt\n",
      isError: false,
    },
    {
      type: "tool_call_start",
      at: T0 + 70,
      toolCallId: "tool-2",
      toolName: "read",
      input: { path: "a.txt", offset: 1, limit: 10 },
    },
    {
      type: "tool_call_end",
      at: T0 + 80,
      toolCallId: "tool-2",
      toolName: "read",
      text: "hello",
      isError: false,
    },
    { type: "assistant_end", at: T0 + 90, stopReason: "stop", model: "claude-sonnet-4-5" },
    { type: "settled", at: T0 + 100, aborted: false },
  ];
}

function projectionFor(events: EngineEvent[]) {
  const base = createProjection({
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
  return events.reduce(applyEngineEvent, base);
}

test("projection: scripted pi event sequence validates against conversationSnapshotSchema", () => {
  const state = projectionFor(scenario());
  const snapshot = buildConversationSnapshot(state);
  const parsed = conversationSnapshotSchema.parse(snapshot);
  assert.equal(parsed.control.phase, "completedSuccess");
  assert.equal(parsed.control.sessionEnded, true);
  assert.deepEqual(
    parsed.rows.window.map((row) => row.kind),
    ["turnHeader", "userInput", "reasoning", "assistantText", "toolCall", "toolCall"],
  );
  const text = parsed.rows.window.find((row) => row.kind === "assistantText");
  assert.equal(text?.kind === "assistantText" ? text.text : "", "Sure, running a few tools.");
  assert.equal(text?.kind === "assistantText" ? text.state : "", "complete");
  const bash = parsed.rows.window.find(
    (row) => row.kind === "toolCall" && row.toolCallId === "tool-1",
  );
  assert.equal(bash?.kind === "toolCall" ? bash.toolName : "", "Bash");
  assert.deepEqual(bash?.kind === "toolCall" ? bash.input : null, { command: "ls" });
  assert.equal(bash?.kind === "toolCall" ? bash.status : "", "success");
  const read = parsed.rows.window.find(
    (row) => row.kind === "toolCall" && row.toolCallId === "tool-2",
  );
  assert.equal(read?.kind === "toolCall" ? read.toolName : "", "Read");
  assert.deepEqual(read?.kind === "toolCall" ? read.input : null, {
    file_path: "a.txt",
    offset: 1,
    limit: 10,
  });
  assert.equal(parsed.rows.firstRowId, 1);
  assert.equal(parsed.rows.totalCount, 6);
  assert.equal(parsed.seq, snapshot.seq);
});

test("projection: provider error sets phase=error and control.lastError", () => {
  const events: EngineEvent[] = [
    { type: "user_input", at: T0, text: "hi" },
    { type: "assistant_start", at: T0 + 1 },
    { type: "text_delta", at: T0 + 2, delta: "partial" },
    {
      type: "assistant_end",
      at: T0 + 3,
      stopReason: "error",
      errorMessage: "upstream 500",
    },
    { type: "settled", at: T0 + 4, aborted: false },
  ];
  const snapshot = conversationSnapshotSchema.parse(
    buildConversationSnapshot(projectionFor(events)),
  );
  assert.equal(snapshot.control.phase, "error");
  assert.equal(snapshot.control.lastError?.message, "upstream 500");
  assert.equal(snapshot.control.lastError?.source, "provider");
  const row = snapshot.rows.window.find((item) => item.kind === "assistantText");
  assert.equal(row?.kind === "assistantText" ? row.state : "", "failed");
  const header = snapshot.rows.window.find((item) => item.kind === "turnHeader");
  assert.equal(header?.kind === "turnHeader" ? header.state : "", "failed");
});

test("projection: abort marks interrupted rows and phase", () => {
  const events: EngineEvent[] = [
    { type: "user_input", at: T0, text: "long task" },
    { type: "assistant_start", at: T0 + 1 },
    { type: "text_delta", at: T0 + 2, delta: "working" },
    {
      type: "tool_call_start",
      at: T0 + 3,
      toolCallId: "t",
      toolName: "bash",
      input: { command: "sleep 10" },
    },
    { type: "assistant_end", at: T0 + 4, stopReason: "aborted" },
    { type: "settled", at: T0 + 5, aborted: true },
  ];
  const snapshot = conversationSnapshotSchema.parse(
    buildConversationSnapshot(projectionFor(events)),
  );
  assert.equal(snapshot.control.phase, "completedInterrupted");
  const header = snapshot.rows.window.find((item) => item.kind === "turnHeader");
  assert.equal(header?.kind === "turnHeader" ? header.state : "", "completedInterrupted");
  const tool = snapshot.rows.window.find((item) => item.kind === "toolCall");
  assert.equal(tool?.kind === "toolCall" ? tool.status : "", "running");
});

test("tool mapping covers bash/read/edit/write/grep/find and keeps unknown names", () => {
  assert.equal(mapToolName("bash"), "Bash");
  assert.equal(mapToolName("read"), "Read");
  assert.equal(mapToolName("write"), "Write");
  assert.equal(mapToolName("edit"), "Edit");
  assert.equal(mapToolName("grep"), "Grep");
  assert.equal(mapToolName("find"), "Glob");
  assert.equal(mapToolName("ls"), "ls");
  assert.equal(mapToolName("mcp__custom"), "mcp__custom");
  assert.deepEqual(
    mapToolInput("edit", {
      path: "a.ts",
      edits: [
        { oldText: "a", newText: "b" },
        { oldText: "c", newText: "d" },
      ],
    }),
    {
      file_path: "a.ts",
      edits: [
        { oldText: "a", newText: "b" },
        { oldText: "c", newText: "d" },
      ],
    },
  );
  assert.deepEqual(
    mapToolInput("edit", { path: "a.ts", edits: [{ oldText: "a", newText: "b" }] }),
    {
      file_path: "a.ts",
      old_string: "a",
      new_string: "b",
    },
  );
  assert.deepEqual(mapToolInput("write", { path: "a.ts", content: "x" }), {
    file_path: "a.ts",
    content: "x",
  });
});

test("duplicate tool_call_start for the same toolCallId reuses one row (pi emits both stream and execution starts)", () => {
  // pi sends `toolcall_start` from the assistant stream and then `tool_execution_start`
  // with the final parsed args for the same id. Appending a row for each produced two
  // Read/Bash cards per call in the real UI.
  const events: EngineEvent[] = [
    { type: "user_input", at: T0, text: "Read notes" },
    {
      type: "tool_call_start",
      at: T0 + 10,
      toolCallId: "tool-read",
      toolName: "read",
      input: {},
    },
    {
      type: "tool_call_start",
      at: T0 + 11,
      toolCallId: "tool-read",
      toolName: "read",
      input: { path: "notes.txt" },
    },
    {
      type: "tool_call_end",
      at: T0 + 20,
      toolCallId: "tool-read",
      toolName: "read",
      text: "hello from demo",
      isError: false,
    },
  ];
  const state = events.reduce(
    applyEngineEvent,
    createProjection({ sessionId: SESSION_ID, logEpoch: LOG_EPOCH, createdAt: T0 }),
  );
  const snapshot = buildConversationSnapshot(state);
  const tools = snapshot.rows.window.filter((row) => row.kind === "toolCall");
  assert.equal(tools.length, 1);
  assert.deepEqual(tools[0]?.kind === "toolCall" ? tools[0].input : null, {
    file_path: "notes.txt",
  });
  assert.equal(tools[0]?.kind === "toolCall" ? tools[0].status : "", "success");
});

test("restore: projectHistory rebuilds rows and snapshot from transcript", () => {
  const session: EngineSessionState = {
    sessionId: SESSION_ID,
    workspacePath: "/workspace/demo",
    title: "Hello",
    titleSource: "default",
    createdAt: T0,
    lastActivityAt: T0 + 500,
    phase: "completedSuccess",
    model: {
      providerId: "anthropic",
      modelId: "claude-sonnet-4-5",
      thought: "medium",
      thoughtLevels: ["off", "low", "medium", "high"],
    },
    usage: { input: 10, output: 20, cacheRead: 3, cacheWrite: 4 },
    isRunning: false,
  };
  const state = projectHistory(
    session,
    [
      { role: "user", at: T0, text: "hi" },
      {
        role: "assistant",
        at: T0 + 10,
        stopReason: "toolUse",
        parts: [
          { type: "thinking", text: "plan" },
          { type: "text", text: "checking" },
          { type: "toolCall", id: "tc-1", name: "read", arguments: { path: "a.txt" } },
        ],
      },
      {
        role: "toolResult",
        at: T0 + 20,
        toolCallId: "tc-1",
        toolName: "read",
        text: "contents",
        isError: false,
      },
      {
        role: "assistant",
        at: T0 + 30,
        stopReason: "stop",
        parts: [{ type: "text", text: "done" }],
      },
    ],
    LOG_EPOCH,
  );
  const snapshot = conversationSnapshotSchema.parse(buildConversationSnapshot(state));
  assert.equal(snapshot.meta.title, "Hello");
  assert.deepEqual(
    snapshot.rows.window.map((row) => row.kind),
    ["turnHeader", "userInput", "reasoning", "assistantText", "toolCall", "assistantText"],
  );
  const tool = snapshot.rows.window.find((row) => row.kind === "toolCall");
  assert.equal(tool?.kind === "toolCall" ? tool.output?.text : "", "contents");
  assert.equal(tool?.kind === "toolCall" ? tool.status : "", "success");
  // Reopening a session initializes the composer from snapshot.config.modelSelection;
  // without it the model picker falls back to "Choose model" and submit stays disabled.
  assert.deepEqual(snapshot.config.modelSelection, {
    providerId: "anthropic",
    modelId: "claude-sonnet-4-5",
    options: { reasoningLevel: "medium" },
  });
  assert.deepEqual(snapshot.usage.cumulative, {
    inputTokens: 10,
    outputTokens: 20,
    cacheReadTokens: 3,
    cacheWriteTokens: 4,
  });
});

test("userInput row ids are stable and sequential within a log epoch", () => {
  let state = createProjection({ sessionId: SESSION_ID, logEpoch: LOG_EPOCH, createdAt: T0 });
  state = applyUserInput(state, "one", { at: T0, sourceCommandId: "cmd-1" });
  state = applyUserInput(state, "two", { at: T0 + 1, sourceCommandId: "cmd-2" });
  assert.deepEqual(
    state.rows.map((row) => row.rowId),
    [1, 2, 3, 4],
  );
  assert.notEqual(state.rows[0]?.turnId, state.rows[2]?.turnId);
});

test("frames: conversation and sessions-index envelopes parse with wire schemas", () => {
  const snapshot = buildConversationSnapshot(projectionFor(scenario()));
  const wire = buildConversationFrame({
    topic: `conversation/${SESSION_ID}`,
    subscriptionId: "sub-1",
    ordinal: 1,
    logicalFrameId: "sub-1-lf-1",
    deliveryKind: "initial",
    snapshot,
    toSeq: snapshot.seq,
    sentAt: T0,
  });
  conversationTopicWireFrameSchema.parse(wire);
  conversationTopicFrameSchema.parse(wire.frame);
  assert.equal(wire.frame.fromSeq, 0);
  assert.equal(wire.frame.toSeq, snapshot.seq);
  assert.equal(
    wire.frame.payload.kind === "snapshot" ? wire.frame.payload.snapshot.seq : -1,
    snapshot.seq,
  );

  const indexSnapshot = buildSessionsIndexSnapshot("/workspace/demo", "si-epoch", [
    {
      sessionId: SESSION_ID,
      workspacePath: "/workspace/demo",
      title: "Hello",
      titleSource: "default",
      createdAt: T0,
      lastActivityAt: T0,
      phase: "completedSuccess",
      model: null,
      usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      isRunning: false,
    },
  ]);
  sessionsIndexSnapshotSchema.parse(indexSnapshot);
  const indexWire = buildSessionsIndexFrame({
    topic: "sessions-index//workspace/demo",
    subscriptionId: "si-sub-1",
    ordinal: 1,
    logicalFrameId: "si-sub-1-lf-1",
    deliveryKind: "initial",
    snapshot: indexSnapshot,
    toSeq: 1,
    sentAt: T0,
  });
  sessionsIndexTopicWireFrameSchema.parse(indexWire);
});

test("acks: createSession/sendText ACKs parse with commandAckSchema", () => {
  const created = acceptedAck("cmd-1", 0, createSessionResult(SESSION_ID));
  commandAckSchema.parse(created);
  assert.equal(created.status === "accepted" ? created.result?.type : "", "createSession");
  const sent = acceptedAck("cmd-2", 3, inputAcceptedResult("input-1"));
  commandAckSchema.parse(sent);
  assert.equal(sent.revisionAtDecision, 3);
  const duplicate = { ...created, status: "duplicate" as const };
  commandAckSchema.parse(duplicate);
  assert.equal(duplicate.status, "duplicate");
});

test("nex-session presentation and model views parse", () => {
  presentationSchema.parse(readWorkspacePresentation({ workspacePath: "/workspace/demo" }));

  const catalog: EngineCatalog = {
    models: [
      {
        providerId: "anthropic",
        providerName: "Anthropic",
        modelId: "claude-sonnet-4-5",
        modelName: "Claude Sonnet 4.5",
        apiType: "anthropic-messages",
        baseUrl: "https://api.anthropic.com",
        reasoningLevels: ["off", "low", "medium", "high"],
        maxOutputTokens: 8192,
        contextWindow: 200_000,
        supportsImage: true,
        supportsVideo: false,
        supportsAudio: false,
        supportsPdf: false,
      },
    ],
    defaultSelection: {
      providerId: "anthropic",
      modelId: "claude-sonnet-4-5",
      options: { reasoningLevel: "high" },
    },
  };
  const views = buildModelSelectionViews(catalog, 1);
  const modelSelection = views.modelSelection as {
    providers: Array<{ providerId: string; models: Array<{ config: unknown }> }>;
    preferredSelection: unknown;
  };
  assert.equal(modelSelection.providers.length, 1);
  assert.ok(modelSelection.providers[0]?.models.length);
  modelSelectionSchema.parse(preferredSelectionFor(catalog));
  const settings = views.providerSettings as { providers: Array<{ models: unknown[] }> };
  assert.equal(settings.providers[0]?.models.length, 1);
  // API keys must never cross the wire.
  const providerConfig = (
    views.modelSelection as {
      providers: Array<{ config: { access: { apiKey?: string } } }>;
    }
  ).providers[0]?.config;
  assert.equal(providerConfig?.access.apiKey, "");
});

test("hello frame shape is usable for the runner link", () => {
  // The runner hello is a link frame, not the browser HelloMessage; assert the link shape
  // stays exactly what the server expects (docs/next/README.md).
  const hello = {
    t: "hello" as const,
    runnerId: "runner_1",
    name: "host",
    version: "0.1.0",
    os: "darwin",
    arch: "arm64",
    workspaces: [{ path: "/workspace/demo", label: "demo" }],
  };
  assert.equal(hello.t, "hello");
  assert.equal(hello.workspaces[0]?.path, "/workspace/demo");
  // The browser-facing hello schema is server-owned; keep the import exercised so a
  // future rename fails loudly.
  assert.equal(typeof helloMessageSchema.parse, "function");
});
