/* eslint-disable max-lines -- 向量生成脚本：一条脚本驱动一组真实 TS 类并顺序产出全部向量，拆开会让用例与其期望值分居两个文件。 */
// 生成 Go 侧 `emitters` 包的对照向量：用**真实的** TypeScript 代码消费每个场景的
// 有序 SessionEvent 流，记录它产出的非会话帧。
//
// 覆盖四类帧：
//   1. v4/telemetry/event —— 真实 ConversationTelemetryFactNormalizer
//      (bootstrap/src/nex-protocol-v4/conversation-telemetry-facts.ts)；
//   2. computer-use/operation-event —— 真实 mapComputerUseOperationEvent
//      (bootstrap/src/nex-protocol/computer-use-operation-event.ts)；
//   3. process/mcpTelemetry（session_startup）/ state.updated（prompt_completed）/
//      session/requestRuntimePreferences —— 形状抄自录制 trace，Go 用显式构造器产出；
//   4. contextUsageBreakdown —— 真实 buildContextUsageSnapshot
//      (core/src/runtime/methods/context-usage.ts)，输入是真实 ContextBuilder sections +
//      真实 ModelToolContract 列表 + 消息列表。
//
// 事件序列的 seq 取自 go/testdata/vectors/agent-eventseq.json（真实 Node agent 的 append 序号），
// payload 形状抄自 core/src/runtime/methods/*.ts 与 tool/executor/events.ts 的 emit 点。
//
// 运行：cd packages/shared && npx --yes tsx ../../scripts/go-rewrite/gen-emitter-vectors.mts
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { ConversationTelemetryFactNormalizer } from "../../apps/nex-cli/packages/bootstrap/src/nex-protocol-v4/conversation-telemetry-facts.js";
import { mapComputerUseOperationEvent } from "../../apps/nex-cli/packages/bootstrap/src/nex-protocol/computer-use-operation-event.js";
import { createContextBuilder } from "../../apps/nex-cli/packages/core/src/context/builder.ts";
import * as contextUsage from "../../apps/nex-cli/packages/core/src/runtime/methods/context-usage.ts";
const { buildContextUsageSnapshot, buildContextUsageBreakdownFromSnapshot } = contextUsage;
import { createToolRegistry } from "../../apps/nex-cli/packages/core/src/tool/registry.ts";
import { registerBuiltInTools } from "../../apps/nex-cli/packages/core/src/tool/handlers/index.ts";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = join(HERE, "..", "..");
const OUT = join(REPO, "go/testdata/vectors/emitters.json");
const OUT_CONTEXT = join(REPO, "go/testdata/vectors/emitters-context.json");

// ── SessionEvent builder ───────────────────────────────────────────────
const SESSION = "sess_emit_fixture";
const T0 = 1_700_000_000_000;
const WS = "/work/proj";
const TURN = {
  S02: "msg_turn_s02",
  S03: "msg_turn_s03",
  S04: "msg_turn_s04",
  S05: "msg_turn_s05",
  S13: "msg_turn_s13",
} as const;

interface Env {
  cwd: string;
  platform: string;
  shell: string;
  osVersion: string;
  nodeVersion: string;
  isGitRepository: boolean;
  gitStatus: string;
}

let serial = 0;
interface EvOpts {
  turnId?: string | null;
  sequenceNumber: number;
  timestampMs?: number;
  /** assistantMessageId 等额外字段直接挂在 payload 上，这里不参与。 */
}
interface RawEvent {
  id: string;
  sessionId: string;
  turnId?: string;
  type: string;
  timestampMs: number;
  traceId: string;
  sequenceNumber: number;
  payload: unknown;
}

function ev(type: string, payload: unknown, opts: EvOpts): RawEvent {
  serial += 1;
  return {
    id: `ev_${String(serial).padStart(3, "0")}`,
    sessionId: SESSION,
    turnId: opts.turnId === null ? undefined : (opts.turnId ?? "turn_1"),
    type,
    timestampMs: opts.timestampMs ?? T0 + opts.sequenceNumber,
    traceId: `tr_${String(serial).padStart(3, "0")}`,
    sequenceNumber: opts.sequenceNumber,
    payload,
  };
}

// ── 场景事件序列（与 agent-eventseq.json 的 seq 对齐）──────────────────
function networkBase(turnId: string, requestId: string, seq: number) {
  return {
    timestamp: new Date(T0 + seq).toISOString(),
    traceId: "tr_net",
    requestId,
    providerId: "mock",
    modelId: "mock-model",
    baseURL: "http://127.0.0.1:1/v1",
    providerKind: "openai-compatible",
    transport: "sse",
    querySource: "main_turn",
    queryId: "cmd-0001",
    attempt: 1,
    maxAttempts: 11,
  };
}

function textStream(
  turnId: string,
  assistantMessageId: string,
  text: string,
  startSeq: number,
  firstSeq: number,
): RawEvent[] {
  // start(seq) → text_start → 8 字符一块的 text_delta → text_end。
  // 记录里 streaming 事件占用 seq 8..13（start, start, delta×4, end, finish）= 8 个，
  // 与 eventseq 的连续 model_streaming 块一致；这里 startSeq 是块的第一个 seq。
  const out: RawEvent[] = [];
  let seq = startSeq;
  const push = (p: unknown) => out.push(ev("model_streaming", p, { turnId, sequenceNumber: seq }));
  // `start` 与 `text_start` 与第一个 delta 共用 seq 是投影的输入约定（见 gen-projection）；
  // 但 eventseq 里每块 model_streaming 占独立连续 seq，故这里每个事件各自 +1。
  push({ assistantMessageId, delta: "", done: false, kind: "start" });
  seq += 1;
  push({ assistantMessageId, delta: "", done: false, kind: "text_start" });
  for (let i = 0; i < text.length; i += 8) {
    seq += 1;
    push({ assistantMessageId, delta: text.slice(i, i + 8), done: false, kind: "text_delta" });
  }
  seq += 1;
  push({ assistantMessageId, delta: "", done: false, kind: "text_end" });
  seq += 1;
  push({ assistantMessageId, delta: "", done: false, kind: "finish" });
  void firstSeq;
  return out;
}

function s02Events(): RawEvent[] {
  const turn = TURN.S02;
  const amsg = "msg_assist_s02";
  const out: RawEvent[] = [];
  out.push(
    ev(
      "turn_started",
      {
        turnNumber: 1,
        input: "Say hello",
        messageId: turn,
        inputId: "cmd-0001",
        foregroundExecutionId: "runtime_command_1",
        intent: {
          sourceCommandId: "cmd-0001",
          clientId: "rec-client",
          provenance: { sourceCommandId: "cmd-0001" },
        },
      },
      { turnId: turn, sequenceNumber: 2 },
    ),
  );
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s02_1", 5), type: "model_request_started" },
      { turnId: turn, sequenceNumber: 5 },
    ),
  );
  out.push(...textStream(turn, amsg, "Hello from the mock model.", 6, 8));
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s02_1", 14), type: "model_request_completed", durationMs: 0 },
      { turnId: turn, sequenceNumber: 14 },
    ),
  );
  out.push(
    ev(
      "model_complete",
      {
        content: "Hello from the mock model.",
        querySource: "main_turn",
        stopReason: "stop",
        usage: { inputTokens: 100, outputTokens: 20, cacheReadTokens: 0, cacheWriteTokens: 0 },
        contextWindow: 100000,
        toolCallCount: 0,
      },
      { turnId: turn, sequenceNumber: 15 },
    ),
  );
  out.push(
    ev(
      "turn_complete",
      {
        response: "Hello from the mock model.",
        tokenCount: 120,
        toolCallCount: 0,
        duration: 0,
        resultType: "success",
        historyRoundCount: 1,
        inputId: "cmd-0001",
      },
      { turnId: turn, sequenceNumber: 16 },
    ),
  );
  return out;
}

function toolInputChunks(
  turnId: string,
  assistantMessageId: string,
  toolCallId: string,
  toolName: string,
  inputText: string,
  startSeq: number,
): RawEvent[] {
  // tool_input_start → tool_input_delta×n → tool_call：投影侧同 seq，但真实 store 每 part 一个 seq。
  const half = Math.floor(inputText.length / 2);
  let seq = startSeq;
  const out: RawEvent[] = [];
  const push = (p: unknown) => out.push(ev("model_streaming", p, { turnId, sequenceNumber: seq }));
  push({ assistantMessageId, toolCallId, toolName, delta: "", done: false, kind: "start" });
  seq += 1;
  push({
    assistantMessageId,
    toolCallId,
    toolName,
    delta: "",
    done: false,
    kind: "tool_input_start",
  });
  seq += 1;
  push({
    assistantMessageId,
    toolCallId,
    toolName,
    delta: inputText.slice(0, half),
    done: false,
    kind: "tool_input_delta",
  });
  seq += 1;
  push({
    assistantMessageId,
    toolCallId,
    toolName,
    delta: inputText.slice(half),
    done: false,
    kind: "tool_input_delta",
  });
  seq += 1;
  push({
    assistantMessageId,
    toolCallId,
    toolName,
    delta: "",
    done: false,
    kind: "tool_input_end",
  });
  seq += 1;
  push({
    assistantMessageId,
    toolCallId,
    toolName,
    delta: "",
    done: false,
    kind: "tool_call",
    input: {},
  });
  seq += 1;
  push({ assistantMessageId, toolCallId, toolName, delta: "", done: false, kind: "finish" });
  return out;
}

function s13Events(): RawEvent[] {
  const turn = TURN.S13;
  const amsg = "msg_assist_s13";
  const out: RawEvent[] = [];
  out.push(
    ev(
      "turn_started",
      {
        turnNumber: 1,
        input: "Say hello",
        messageId: turn,
        inputId: "cmd-0001",
        foregroundExecutionId: "runtime_command_1",
      },
      { turnId: turn, sequenceNumber: 2 },
    ),
  );
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s13_1", 5), type: "model_request_started", attempt: 1 },
      { turnId: turn, sequenceNumber: 5 },
    ),
  );
  out.push(
    ev(
      "model_network_status",
      {
        ...networkBase(turn, "req_s13_1", 6),
        type: "model_request_failed",
        attempt: 1,
        reason: "rate_limited",
        retryable: true,
        message: "Rate limited",
        statusCode: 429,
        durationMs: 0,
      },
      { turnId: turn, sequenceNumber: 6 },
    ),
  );
  out.push(
    ev(
      "model_network_status",
      {
        ...networkBase(turn, "req_s13_1", 7),
        type: "model_retry_scheduled",
        attempt: 1,
        delayMs: 0,
        nextAttempt: 2,
        reason: "rate_limited",
        message: "Rate limited",
        statusCode: 429,
      },
      { turnId: turn, sequenceNumber: 7 },
    ),
  );
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s13_2", 8), type: "model_request_started", attempt: 2 },
      { turnId: turn, sequenceNumber: 8 },
    ),
  );
  out.push(...textStream(turn, amsg, "Recovered after a retry.", 9, 11));
  out.push(
    ev(
      "model_network_status",
      {
        ...networkBase(turn, "req_s13_2", 16),
        type: "model_request_completed",
        attempt: 2,
        durationMs: 0,
      },
      { turnId: turn, sequenceNumber: 16 },
    ),
  );
  out.push(
    ev(
      "model_complete",
      {
        content: "Recovered after a retry.",
        querySource: "main_turn",
        stopReason: "stop",
        usage: { inputTokens: 100, outputTokens: 20, cacheReadTokens: 0, cacheWriteTokens: 0 },
        contextWindow: 100000,
        toolCallCount: 0,
      },
      { turnId: turn, sequenceNumber: 17 },
    ),
  );
  out.push(
    ev(
      "turn_complete",
      {
        response: "Recovered after a retry.",
        tokenCount: 120,
        toolCallCount: 0,
        duration: 0,
        resultType: "success",
        historyRoundCount: 1,
        inputId: "cmd-0001",
      },
      { turnId: turn, sequenceNumber: 18 },
    ),
  );
  return out;
}

// ── S03 / S04 / S05：工具轮 ────────────────────────────────────────────
function toolScheduledEv(
  turnId: string,
  toolCallId: string,
  toolName: string,
  assistantMessageId: string,
  input: unknown,
  seq: number,
): RawEvent {
  return ev(
    "tool_call_scheduled",
    {
      toolCallId,
      assistantMessageId,
      toolName,
      input,
      parallelGroupIndex: 0,
      canRunParallel: false,
      schedule: { parallelGroups: [[toolCallId]], executionOrder: [toolCallId] },
    },
    { turnId, sequenceNumber: seq },
  );
}
function toolStartedEv(
  turnId: string,
  toolCallId: string,
  toolName: string,
  seq: number,
): RawEvent {
  return ev(
    "tool_call_started",
    {
      toolCallId,
      toolName,
      startedAt: new Date(T0 + seq).toISOString(),
      readOnly: true,
    },
    { turnId, sequenceNumber: seq },
  );
}
function permissionRequestedEv(
  turnId: string,
  toolCallId: string,
  toolName: string,
  input: unknown,
  seq: number,
): RawEvent {
  return ev(
    "permission_requested",
    {
      toolCallId,
      toolName,
      riskLevel: "medium",
      reason: `Tool ${toolName} requires approval`,
      input,
    },
    { turnId, sequenceNumber: seq },
  );
}
function permissionResolvedEv(
  turnId: string,
  toolCallId: string,
  decision: "allow" | "deny",
  seq: number,
): RawEvent {
  return ev("permission_resolved", { toolCallId, decision }, { turnId, sequenceNumber: seq });
}
function toolResultEv(
  turnId: string,
  toolCallId: string,
  content: string,
  seq: number,
  perf?: unknown,
): RawEvent {
  return ev(
    "tool_call_result",
    {
      toolCallId,
      result: { success: true, content, ...(perf ? { perf } : {}) },
      duration: 0,
    },
    { turnId, sequenceNumber: seq },
  );
}

function s03Events(): RawEvent[] {
  const turn = TURN.S03;
  const a1 = "msg_assist_s03a";
  const a2 = "msg_assist_s03b";
  const tool = "call_read_1";
  const input = { file_path: `${WS}/hello.txt` };
  const inputText = JSON.stringify(input);
  const out: RawEvent[] = [];
  out.push(
    ev(
      "turn_started",
      {
        turnNumber: 1,
        input: "Read hello.txt",
        messageId: turn,
        inputId: "cmd-0001",
        foregroundExecutionId: "runtime_command_1",
      },
      { turnId: turn, sequenceNumber: 2 },
    ),
  );
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s03_1", 5), type: "model_request_started" },
      { turnId: turn, sequenceNumber: 5 },
    ),
  );
  out.push(...toolInputChunks(turn, a1, tool, "Read", inputText, 6));
  out.push(toolScheduledEv(turn, tool, "Read", a1, input, 12));
  out.push(toolStartedEv(turn, tool, "Read", 16));
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s03_1", 17), type: "model_request_completed", durationMs: 0 },
      { turnId: turn, sequenceNumber: 17 },
    ),
  );
  out.push(
    ev(
      "model_complete",
      {
        content: "",
        querySource: "main_turn",
        stopReason: "stop",
        usage: { inputTokens: 100, outputTokens: 20, cacheReadTokens: 0, cacheWriteTokens: 0 },
        contextWindow: 100000,
        toolCallCount: 1,
      },
      { turnId: turn, sequenceNumber: 18 },
    ),
  );
  out.push(toolResultEv(turn, tool, "1\thi there\n2\t", 19, { totalMs: 0 }));
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s03_2", 24), type: "model_request_started" },
      { turnId: turn, sequenceNumber: 24 },
    ),
  );
  out.push(...textStream(turn, a2, "The file says: hi there.", 25, 27));
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s03_2", 32), type: "model_request_completed", durationMs: 0 },
      { turnId: turn, sequenceNumber: 32 },
    ),
  );
  out.push(
    ev(
      "model_complete",
      {
        content: "The file says: hi there.",
        querySource: "main_turn",
        stopReason: "stop",
        usage: { inputTokens: 100, outputTokens: 20, cacheReadTokens: 0, cacheWriteTokens: 0 },
        contextWindow: 100000,
        toolCallCount: 0,
      },
      { turnId: turn, sequenceNumber: 33 },
    ),
  );
  out.push(
    ev(
      "turn_complete",
      {
        response: "The file says: hi there.",
        tokenCount: 240,
        toolCallCount: 1,
        duration: 0,
        resultType: "success",
        historyRoundCount: 2,
        inputId: "cmd-0001",
      },
      { turnId: turn, sequenceNumber: 34 },
    ),
  );
  out.push(
    ev(
      "model_network_status",
      {
        ...networkBase(turn, "req_s03_title", 36),
        type: "model_request_started",
        transport: "http",
        querySource: "session_title",
      },
      { turnId: turn, sequenceNumber: 36 },
    ),
  );
  out.push(
    ev(
      "model_network_status",
      {
        ...networkBase(turn, "req_s03_title", 37),
        type: "model_request_completed",
        transport: "http",
        querySource: "session_title",
        durationMs: 0,
      },
      { turnId: turn, sequenceNumber: 37 },
    ),
  );
  out.push(
    ev(
      "model_complete",
      {
        content: "The file says: hi there",
        querySource: "session_title",
        stopReason: "stop",
        usage: { inputTokens: 64, outputTokens: 8, cacheReadTokens: 0, cacheWriteTokens: 0 },
        contextWindow: 100000,
        toolCallCount: 0,
      },
      { turnId: turn, sequenceNumber: 38 },
    ),
  );
  return out;
}

function writePerf() {
  return {
    totalMs: 0,
    permissionWaitMs: 0,
    detail: {
      kind: "filesystem",
      filesystem: {
        readMs: 0,
        writeMs: 0,
        fileCount: 1,
        totalBytes: 17,
        maxFileBytes: 17,
        workspaceKind: "local",
      },
    },
  };
}
function editPerf() {
  return {
    totalMs: 0,
    permissionWaitMs: 0,
    detail: {
      kind: "patch",
      filesystem: {
        readMs: 0,
        writeMs: 0,
        fileCount: 1,
        totalBytes: 17,
        maxFileBytes: 17,
        workspaceKind: "local",
      },
      patch: { matchMs: 0, hunkCount: 1, matchAttempts: 1 },
    },
  };
}

function s04Events(): RawEvent[] {
  const turn = TURN.S04;
  const a1 = "msg_assist_s04a";
  const a2 = "msg_assist_s04b";
  const a3 = "msg_assist_s04c";
  const t1 = "call_w1";
  const t2 = "call_e1";
  const win = { file_path: `${WS}/notes.txt`, content: "alpha\nbeta\ngamma\n" };
  const ein = { file_path: `${WS}/notes.txt`, old_string: "beta", new_string: "BETA" };
  const out: RawEvent[] = [];
  out.push(
    ev(
      "turn_started",
      {
        turnNumber: 1,
        input: "Create notes.txt then edit it",
        messageId: turn,
        inputId: "cmd-0001",
        foregroundExecutionId: "runtime_command_1",
      },
      { turnId: turn, sequenceNumber: 2 },
    ),
  );
  // 轮次 1：Write
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s04_1", 5), type: "model_request_started" },
      { turnId: turn, sequenceNumber: 5 },
    ),
  );
  out.push(...toolInputChunks(turn, a1, t1, "Write", JSON.stringify(win), 6));
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s04_1", 12), type: "model_request_completed", durationMs: 0 },
      { turnId: turn, sequenceNumber: 12 },
    ),
  );
  out.push(
    ev(
      "model_complete",
      {
        content: "",
        querySource: "main_turn",
        stopReason: "stop",
        usage: { inputTokens: 100, outputTokens: 20, cacheReadTokens: 0, cacheWriteTokens: 0 },
        contextWindow: 100000,
        toolCallCount: 1,
      },
      { turnId: turn, sequenceNumber: 13 },
    ),
  );
  out.push(toolScheduledEv(turn, t1, "Write", a1, win, 15));
  out.push(permissionRequestedEv(turn, t1, "Write", win, 18));
  out.push(permissionResolvedEv(turn, t1, "allow", 19));
  out.push(toolStartedEv(turn, t1, "Write", 20));
  out.push(
    toolResultEv(turn, t1, `File created successfully at: ${WS}/notes.txt`, 21, writePerf()),
  );
  // 轮次 2：Edit
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s04_2", 27), type: "model_request_started" },
      { turnId: turn, sequenceNumber: 27 },
    ),
  );
  out.push(...toolInputChunks(turn, a2, t2, "Edit", JSON.stringify(ein), 28));
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s04_2", 34), type: "model_request_completed", durationMs: 0 },
      { turnId: turn, sequenceNumber: 34 },
    ),
  );
  out.push(
    ev(
      "model_complete",
      {
        content: "",
        querySource: "main_turn",
        stopReason: "stop",
        usage: { inputTokens: 100, outputTokens: 20, cacheReadTokens: 0, cacheWriteTokens: 0 },
        contextWindow: 100000,
        toolCallCount: 1,
      },
      { turnId: turn, sequenceNumber: 35 },
    ),
  );
  out.push(toolScheduledEv(turn, t2, "Edit", a2, ein, 37));
  out.push(permissionRequestedEv(turn, t2, "Edit", ein, 40));
  out.push(permissionResolvedEv(turn, t2, "allow", 41));
  out.push(toolStartedEv(turn, t2, "Edit", 42));
  out.push(
    toolResultEv(
      turn,
      t2,
      `The file ${WS}/notes.txt has been updated successfully.`,
      43,
      editPerf(),
    ),
  );
  // 轮次 3：正文
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s04_3", 49), type: "model_request_started" },
      { turnId: turn, sequenceNumber: 49 },
    ),
  );
  out.push(...textStream(turn, a3, "Done: notes.txt created and edited.", 50, 52));
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s04_3", 59), type: "model_request_completed", durationMs: 0 },
      { turnId: turn, sequenceNumber: 59 },
    ),
  );
  out.push(
    ev(
      "model_complete",
      {
        content: "Done: notes.txt created and edited.",
        querySource: "main_turn",
        stopReason: "stop",
        usage: { inputTokens: 100, outputTokens: 20, cacheReadTokens: 0, cacheWriteTokens: 0 },
        contextWindow: 100000,
        toolCallCount: 0,
      },
      { turnId: turn, sequenceNumber: 60 },
    ),
  );
  out.push(
    ev(
      "turn_complete",
      {
        response: "Done: notes.txt created and edited.",
        tokenCount: 360,
        toolCallCount: 2,
        duration: 0,
        resultType: "success",
        historyRoundCount: 3,
        inputId: "cmd-0001",
      },
      { turnId: turn, sequenceNumber: 61 },
    ),
  );
  out.push(
    ev(
      "model_network_status",
      {
        ...networkBase(turn, "req_s04_title", 63),
        type: "model_request_started",
        transport: "http",
        querySource: "session_title",
      },
      { turnId: turn, sequenceNumber: 63 },
    ),
  );
  out.push(
    ev(
      "model_network_status",
      {
        ...networkBase(turn, "req_s04_title", 64),
        type: "model_request_completed",
        transport: "http",
        querySource: "session_title",
        durationMs: 0,
      },
      { turnId: turn, sequenceNumber: 64 },
    ),
  );
  out.push(
    ev(
      "model_complete",
      {
        content: "Done: notes.txt created and edited",
        querySource: "session_title",
        stopReason: "stop",
        usage: { inputTokens: 64, outputTokens: 8, cacheReadTokens: 0, cacheWriteTokens: 0 },
        contextWindow: 100000,
        toolCallCount: 0,
      },
      { turnId: turn, sequenceNumber: 65 },
    ),
  );
  return out;
}

function s05Events(): RawEvent[] {
  const turn = TURN.S05;
  const a1 = "msg_assist_s05a";
  const a2 = "msg_assist_s05b";
  const t1 = "call_w1";
  const win = { file_path: `${WS}/notes.txt`, content: "alpha\nbeta\ngamma\n" };
  const out: RawEvent[] = [];
  out.push(
    ev(
      "turn_started",
      {
        turnNumber: 1,
        input: "Create notes.txt then edit it",
        messageId: turn,
        inputId: "cmd-0001",
        foregroundExecutionId: "runtime_command_1",
      },
      { turnId: turn, sequenceNumber: 2 },
    ),
  );
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s05_1", 5), type: "model_request_started" },
      { turnId: turn, sequenceNumber: 5 },
    ),
  );
  out.push(...toolInputChunks(turn, a1, t1, "Write", JSON.stringify(win), 6));
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s05_1", 12), type: "model_request_completed", durationMs: 0 },
      { turnId: turn, sequenceNumber: 12 },
    ),
  );
  out.push(
    ev(
      "model_complete",
      {
        content: "",
        querySource: "main_turn",
        stopReason: "stop",
        usage: { inputTokens: 100, outputTokens: 20, cacheReadTokens: 0, cacheWriteTokens: 0 },
        contextWindow: 100000,
        toolCallCount: 1,
      },
      { turnId: turn, sequenceNumber: 13 },
    ),
  );
  out.push(toolScheduledEv(turn, t1, "Write", a1, win, 15));
  out.push(permissionRequestedEv(turn, t1, "Write", win, 18));
  out.push(permissionResolvedEv(turn, t1, "deny", 19));
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s05_2", 24), type: "model_request_started" },
      { turnId: turn, sequenceNumber: 24 },
    ),
  );
  out.push(...textStream(turn, a2, "The write was denied, so I stopped.", 25, 27));
  out.push(
    ev(
      "model_network_status",
      { ...networkBase(turn, "req_s05_2", 34), type: "model_request_completed", durationMs: 0 },
      { turnId: turn, sequenceNumber: 34 },
    ),
  );
  out.push(
    ev(
      "model_complete",
      {
        content: "The write was denied, so I stopped.",
        querySource: "main_turn",
        stopReason: "stop",
        usage: { inputTokens: 100, outputTokens: 20, cacheReadTokens: 0, cacheWriteTokens: 0 },
        contextWindow: 100000,
        toolCallCount: 0,
      },
      { turnId: turn, sequenceNumber: 35 },
    ),
  );
  out.push(
    ev(
      "turn_complete",
      {
        response: "The write was denied, so I stopped.",
        tokenCount: 240,
        toolCallCount: 1,
        duration: 0,
        resultType: "success",
        historyRoundCount: 1,
        inputId: "cmd-0001",
      },
      { turnId: turn, sequenceNumber: 36 },
    ),
  );
  out.push(
    ev(
      "model_network_status",
      {
        ...networkBase(turn, "req_s05_title", 38),
        type: "model_request_started",
        transport: "http",
        querySource: "session_title",
      },
      { turnId: turn, sequenceNumber: 38 },
    ),
  );
  out.push(
    ev(
      "model_network_status",
      {
        ...networkBase(turn, "req_s05_title", 39),
        type: "model_request_completed",
        transport: "http",
        querySource: "session_title",
        durationMs: 0,
      },
      { turnId: turn, sequenceNumber: 39 },
    ),
  );
  out.push(
    ev(
      "model_complete",
      {
        content: "The write was denied, so I stopped",
        querySource: "session_title",
        stopReason: "stop",
        usage: { inputTokens: 64, outputTokens: 8, cacheReadTokens: 0, cacheWriteTokens: 0 },
        contextWindow: 100000,
        toolCallCount: 0,
      },
      { turnId: turn, sequenceNumber: 40 },
    ),
  );
  return out;
}

// ── 运行 normalizer / mapper ───────────────────────────────────────────
function toSessionEvent(e: RawEvent) {
  return {
    id: e.id,
    sessionId: e.sessionId,
    turnId: e.turnId,
    type: e.type,
    timestamp: new Date(e.timestampMs),
    traceId: e.traceId,
    sequenceNumber: e.sequenceNumber,
    payload: e.payload,
  };
}

interface TelemetryVector {
  input: RawEvent;
  fact: unknown;
}
interface OperationVector {
  input: RawEvent;
  event: unknown;
}

function runScenario(
  id: string,
  events: RawEvent[],
  metadata: { modelName?: string; modelProvider?: string; memoryEnabled?: boolean },
) {
  const normalizer = new ConversationTelemetryFactNormalizer();
  const telemetry: TelemetryVector[] = [];
  const operations: OperationVector[] = [];
  let factOrdinal = 0;
  for (const e of events) {
    const se = toSessionEvent(e) as never;
    const fact = normalizer.normalize(SESSION, se, metadata);
    if (fact) {
      telemetry.push({ input: e, fact });
      factOrdinal += 1;
    }
    const op = mapComputerUseOperationEvent(se);
    if (op) operations.push({ input: e, event: op });
  }
  return { id, factCount: factOrdinal, telemetry, operations };
}

// ── Context usage ─────────────────────────────────────────────────────
const EMPTY_ENV: Env = {
  cwd: "/work/proj",
  platform: "linux",
  shell: "zsh",
  osVersion: "linux 6.8.0 x64",
  nodeVersion: "v24.0.0",
  isGitRepository: false,
  gitStatus: "not_repo",
};

const GUIDANCE_TOOLS = [
  "Agent",
  "AskUserQuestion",
  "Bash",
  "CronCreate",
  "CronDelete",
  "CronList",
  "CronUpdate",
  "Edit",
  "EnterPlanMode",
  "ExitPlanMode",
  "Read",
  "Skill",
  "TaskOutput",
  "TaskStop",
  "TodoRead",
  "TodoWrite",
  "WebFetch",
  "Write",
  "SendMessage",
  "ReadSessionContext",
];

// 20 个默认工具的真实 ModelToolContract（与录制 S02 请求一致：动态工作流关闭、内嵌搜索开启）。
function defaultToolContracts() {
  const reg = createToolRegistry();
  registerBuiltInTools(reg as never, {
    includeSkill: true,
    includeSendMessage: true,
    includeAgent: true,
    includeAutomation: true,
    includeOffPeak: false,
    includeDynamicWorkflow: false,
    embeddedSearchEnabled: true,
  });
  const all = reg.toContracts() as Array<Record<string, unknown>>;
  const byName = new Map(all.map((c) => [c.name as string, c]));
  return GUIDANCE_TOOLS.map((n) => byName.get(n)!);
}

function messageChars(messages: unknown[]): number {
  // 复刻 buildMessageRoleBreakdown 的 stringifyForEstimation（JSON.stringify，undefined 丢弃）。
  let total = 0;
  for (const m of messages) {
    const content = JSON.stringify({
      role: (m as never)["role"],
      content: (m as never)["content"],
      toolCalls: (m as never)["toolCalls"],
      toolCallId: (m as never)["toolCallId"],
      toolName: (m as never)["toolName"],
    });
    total += content.length;
  }
  return total;
}

function makeRuntime(tools: unknown[], env: Env) {
  const config: Record<string, unknown> = {
    workingDirectory: env.cwd,
    envInfo: env,
    model: { providerId: "mock", modelId: "mock-model" },
    currentDate: "2026-10-09",
    guidanceToolNames: GUIDANCE_TOOLS,
  };
  const built = createContextBuilder(config as never).build();
  // buildContextUsageSnapshot 是混入 AgentRuntimeInternal 的方法，依赖 this 上的若干
  // 辅助方法（estimatedMetric / sumMetrics / buildToolUsageDetail / buildMessageRoleBreakdown 等）。
  // 这些函数都定义在同一模块里，直接全部挂到 stub 上即可复现真实行为。
  const rt: Record<string, unknown> = {
    latestContextBuildResult: { sections: (built as never as { sections: unknown[] }).sections },
    tools,
  };
  for (const [k, v] of Object.entries(contextUsage)) {
    if (
      typeof v === "function" &&
      k !== "buildContextUsageSnapshot" &&
      k !== "buildContextUsageBreakdownFromSnapshot"
    ) {
      rt[k] = v;
    }
  }
  return rt;
}

function buildUsage(scenarioId: string, messages: unknown[], env: Env) {
  const tools = defaultToolContracts();
  const rt = makeRuntime(tools, env);
  const snapshot = buildContextUsageSnapshot.call(
    rt as never,
    { model: { providerId: "mock", modelId: "mock-model" }, messages, tools } as never,
  );
  const breakdown = buildContextUsageBreakdownFromSnapshot(snapshot as never);
  const categories = (snapshot as Record<string, unknown>)["categories"];
  const toolDetails = (snapshot as Record<string, unknown>)["systemTools"];
  // 供 Go 复算的输入：真实 section 列表 + 消息字符串（stringifyForEstimation 的输入）。
  const sectionInputs = (rt["latestContextBuildResult"] as { sections: unknown[] }).sections.map(
    (s) => {
      const sec = s as Record<string, unknown>;
      return {
        name: sec["name"],
        source: sec["source"],
        injectionTarget: sec["injectionTarget"],
        cacheHint: sec["cacheHint"],
        chars: sec["chars"],
        tokens: sec["tokens"],
      };
    },
  );
  const messageInputs = messages.map((m) => {
    const msg = m as Record<string, unknown>;
    const content = JSON.stringify({
      role: msg["role"],
      content: msg["content"],
      toolCalls: msg["toolCalls"],
      toolCallId: msg["toolCallId"],
      toolName: msg["toolName"],
    });
    return { role: msg["role"], content, raw: m };
  });
  return {
    scenarioId,
    breakdown,
    categories,
    systemTools: toolDetails,
    totalChars: (snapshot as Record<string, unknown>)["totalChars"],
    sectionInputs,
    messageInputs,
  };
}
// 消息列表：buildMessageRoleBreakdown 计入的问答消息（不含 system，且 meta user context 提醒被过滤）。
// 录制里的 breakdown 取自该轮**第一个** model_complete，此刻只有用户输入一条可见消息。
const MSG_S02 = [
  {
    role: "user",
    content: "<system-reminder>\n# currentDate\nToday's date is 2026-10-09.\n</system-reminder>",
  },
  { role: "user", content: "Say hello" },
];
const MSG_S03 = [
  {
    role: "user",
    content: "<system-reminder>\n# currentDate\nToday's date is 2026-10-09.\n</system-reminder>",
  },
  { role: "user", content: "Read hello.txt" },
];
const MSG_S04 = [
  {
    role: "user",
    content: "<system-reminder>\n# currentDate\nToday's date is 2026-10-09.\n</system-reminder>",
  },
  { role: "user", content: "Create notes.txt then edit it" },
];

const scenarios = [
  runScenario("S02", s02Events(), {
    modelName: "mock-model",
    modelProvider: "mock",
    memoryEnabled: false,
  }),
  runScenario("S03", s03Events(), {
    modelName: "mock-model",
    modelProvider: "mock",
    memoryEnabled: false,
  }),
  runScenario("S04", s04Events(), {
    modelName: "mock-model",
    modelProvider: "mock",
    memoryEnabled: false,
  }),
  runScenario("S05", s05Events(), {
    modelName: "mock-model",
    modelProvider: "mock",
    memoryEnabled: false,
  }),
  runScenario("S13", s13Events(), {
    modelName: "mock-model",
    modelProvider: "mock",
    memoryEnabled: false,
  }),
];

const contextCases = [
  buildUsage("S02", MSG_S02, EMPTY_ENV),
  buildUsage("S03", MSG_S03, EMPTY_ENV),
  buildUsage("S04", MSG_S04, EMPTY_ENV),
];

// 20 个真实工具契约作为数据向量，供 Go 复算 system_tool_schemas。
const toolContractDump = defaultToolContracts().map((c) => {
  const pick = [
    "name",
    "description",
    "capability",
    "inputSchema",
    "outputSchema",
    "readOnly",
    "destructive",
    "sideEffectScope",
    "permission",
    "resultBudget",
  ];
  const out: Record<string, unknown> = {};
  for (const k of pick) if ((c as never)[k] !== undefined) out[k] = (c as never)[k];
  return out;
});

const SESSION_ID = SESSION;
writeFileSync(
  OUT,
  JSON.stringify(
    {
      generatedBy: "scripts/go-rewrite/gen-emitter-vectors.mts",
      note: "non-conversation frames: v4 telemetry facts + computer-use operation events, from the real normalizers",
      session: SESSION_ID,
      scenarios,
    },
    null,
    2,
  ) + "\n",
);

writeFileSync(
  OUT_CONTEXT,
  JSON.stringify(
    {
      generatedBy: "scripts/go-rewrite/gen-emitter-vectors.mts",
      note: "contextUsageBreakdown from the real buildContextUsageSnapshot; tool contracts are data",
      session: SESSION_ID,
      toolContracts: toolContractDump,
      cases: contextCases,
    },
    null,
    2,
  ) + "\n",
);

console.log(`wrote ${OUT}`);
for (const s of scenarios)
  console.log(`  ${s.id}: ${s.telemetry.length} facts, ${s.operations.length} operation events`);
console.log(`wrote ${OUT_CONTEXT}`);
for (const c of contextCases) console.log(`  ${c.scenarioId}: ${JSON.stringify(c.breakdown)}`);
