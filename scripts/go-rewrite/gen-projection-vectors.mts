/* eslint-disable max-lines -- 向量生成脚本：一条脚本驱动一组真实 TS 类并顺序产出全部向量，拆开会让用例与其期望值分居两个文件。 */
// 生成 Go 侧 product-projection 对照向量：用**真实的** ProductProjection
// （apps/nex-cli/packages/bootstrap，经 v4-replay 桶加载）消费一组 SessionEvent 序列，
// 记录每个事件产出的 raw delta、整段 coalesce 后的 delta 流，以及终态 snapshot。
//
// 事件序列的 payload 形状（字段、顺序、kind）逐字段抄自 core/src/runtime/methods/*.ts 与
// tool/executor/events.ts 的真实 emit 点；sequenceNumber 是投影的**输入**，取值对齐录制头部
// （go/testdata/traces/agent/S0x/out.normalized.json）里可见的 createdAtSeq / finalToSeq。
// 序列号如何分配由另一路工程师反推，本向量把它当输入。
//
// 运行：cd packages/shared && npx --yes tsx ../../scripts/go-rewrite/gen-projection-vectors.mts
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { ProductProjection } from "../../apps/nex-cli/packages/bootstrap/src/nex-protocol-v4/replay.js";
import { coalesceConversationDeltas } from "../../packages/shared/src/nex-protocol-v4/coalesce.js";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = join(HERE, "..", "..");
const OUT = join(REPO, "go/testdata/vectors/projection-scenarios.json");

const SESSION = "sess_proj_fixture";
const EPOCH = "epoch_proj_fixture";
const T0 = 1_700_000_000_000;
const WS = "/work/proj";

export interface RawEvent {
  id: string;
  sessionId: string;
  turnId?: string;
  type: string;
  timestampMs: number;
  traceId: string;
  sequenceNumber: number;
  payload: unknown;
}

let serial = 0;
interface EvOpts {
  turnId?: string | null;
  sequenceNumber?: number;
  timestampMs?: number;
}
function ev(type: string, payload: unknown, opts: EvOpts = {}): RawEvent {
  serial += 1;
  return {
    id: `ev_${String(serial).padStart(3, "0")}`,
    sessionId: SESSION,
    turnId: opts.turnId === null ? undefined : (opts.turnId ?? "turn_1"),
    type,
    timestampMs: opts.timestampMs ?? T0 + serial,
    traceId: `tr_${String(serial).padStart(3, "0")}`,
    sequenceNumber: opts.sequenceNumber ?? serial,
    payload,
  };
}

const BREAKDOWN = [
  { source: "system_prompt", chars: 6499 },
  { source: "meta_user_context", chars: 41 },
  { source: "system_tool_schemas", chars: 81936 },
  { source: "messages", chars: 37 },
];

function cacheHit(requestCount: number) {
  return {
    inputTokens: 100,
    cacheReadTokens: 0,
    cacheWriteTokens: 0,
    latestHitRate: 0,
    hitRate: 0,
    hitRateRequestCount: requestCount,
    totalInputTokens: 100 * requestCount,
    totalCacheReadTokens: 0,
    totalCacheWriteTokens: 0,
  };
}

function titleEvent(title: string, seq: number): RawEvent {
  // 会话标题事件：投影把 first_input / generated 归一为 generated。
  return ev(
    "session_title_updated",
    { previousTitle: "", source: "first_input", title },
    { sequenceNumber: seq, timestampMs: T0 + seq },
  );
}

function turnStarted(turnId: string, text: string, seq: number, turnNumber = 1): RawEvent {
  return ev(
    "turn_started",
    {
      turnNumber,
      input: text,
      messageId: turnId,
      inputId: "cmd-0001",
      foregroundExecutionId: "runtime_command_1",
      intent: {
        sourceCommandId: "cmd-0001",
        clientId: "rec-client",
        provenance: { sourceCommandId: "cmd-0001" },
      },
    },
    { turnId, sequenceNumber: seq, timestampMs: T0 + seq },
  );
}

function modelComplete(
  turnId: string,
  opts: {
    content: string;
    seq: number;
    cacheHitCount: number;
    toolCallCount?: number;
    breakdown?: boolean;
    fileChanges?: { additions: number; deletions: number; files: number };
  },
): RawEvent {
  return ev(
    "model_complete",
    {
      content: opts.content,
      querySource: "main_turn",
      stopReason: "stop",
      usage: { inputTokens: 100, outputTokens: 20, cacheReadTokens: 0, cacheWriteTokens: 0 },
      contextWindow: 100000,
      toolCallCount: opts.toolCallCount ?? 0,
      cacheHit: cacheHit(opts.cacheHitCount),
      ...(opts.breakdown === false ? {} : { contextUsageBreakdown: BREAKDOWN }),
      ...(opts.fileChanges ? { fileChanges: { ...opts.fileChanges, items: [] } } : {}),
    },
    { turnId, sequenceNumber: opts.seq, timestampMs: T0 + opts.seq },
  );
}

/** text_start 建行（seqStart），随后 text_delta 分块（8 字符一块），text_end 收口。 */
function streamText(
  turnId: string,
  assistantMessageId: string,
  text: string,
  seqStart: number,
): RawEvent[] {
  const chunks: string[] = [];
  for (let i = 0; i < text.length; i += 8) chunks.push(text.slice(i, i + 8));
  let seq = seqStart;
  const out: RawEvent[] = [
    ev(
      "model_streaming",
      { assistantMessageId, delta: "", done: false, kind: "text_start" },
      { turnId, sequenceNumber: seq, timestampMs: T0 + seq },
    ),
  ];
  for (const chunk of chunks) {
    seq += 1;
    out.push(
      ev(
        "model_streaming",
        { assistantMessageId, delta: chunk, done: false, kind: "text_delta" },
        { turnId, sequenceNumber: seq, timestampMs: T0 + seq },
      ),
    );
  }
  seq += 1;
  out.push(
    ev(
      "model_streaming",
      { assistantMessageId, delta: "", done: false, kind: "text_end" },
      { turnId, sequenceNumber: seq, timestampMs: T0 + seq },
    ),
  );
  return out;
}

function toolInputStreaming(
  turnId: string,
  assistantMessageId: string,
  toolCallId: string,
  toolName: string,
  input: unknown,
  inputText: string,
  seq: number,
): RawEvent[] {
  // tool_input_start 建空 inputText 行；tool_input_delta 累计 inputText；tool_call 定稿。
  const half = Math.floor(inputText.length / 2);
  return [
    ev(
      "model_streaming",
      {
        assistantMessageId,
        toolCallId,
        toolName,
        delta: "",
        done: false,
        kind: "tool_input_start",
      },
      { turnId, sequenceNumber: seq, timestampMs: T0 + seq },
    ),
    ev(
      "model_streaming",
      {
        assistantMessageId,
        toolCallId,
        toolName,
        delta: inputText.slice(0, half),
        done: false,
        kind: "tool_input_delta",
      },
      { turnId, sequenceNumber: seq, timestampMs: T0 + seq },
    ),
    ev(
      "model_streaming",
      {
        assistantMessageId,
        toolCallId,
        toolName,
        delta: inputText.slice(half),
        done: false,
        kind: "tool_input_delta",
      },
      { turnId, sequenceNumber: seq, timestampMs: T0 + seq },
    ),
    ev(
      "model_streaming",
      {
        assistantMessageId,
        toolCallId,
        toolName,
        delta: "",
        done: false,
        kind: "tool_call",
        input,
      },
      { turnId, sequenceNumber: seq, timestampMs: T0 + seq },
    ),
  ];
}

function toolScheduled(
  turnId: string,
  assistantMessageId: string,
  toolCallId: string,
  toolName: string,
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
      schedule: {
        parallelGroups: [[toolCallId]],
        executionOrder: [toolCallId],
      },
    },
    { turnId, sequenceNumber: seq, timestampMs: T0 + seq },
  );
}

function toolStarted(turnId: string, toolCallId: string, toolName: string, seq: number): RawEvent {
  return ev(
    "tool_call_started",
    { toolCallId, toolName, startedAt: new Date(T0 + seq).toISOString(), readOnly: true },
    { turnId, sequenceNumber: seq, timestampMs: T0 + seq },
  );
}

function toolResult(
  turnId: string,
  toolCallId: string,
  content: string,
  seq: number,
  success = true,
): RawEvent {
  return ev(
    "tool_call_result",
    {
      toolCallId,
      result: { success, content },
      duration: 3,
    },
    { turnId, sequenceNumber: seq, timestampMs: T0 + seq },
  );
}

function toolError(
  turnId: string,
  toolCallId: string,
  type: string,
  message: string,
  seq: number,
): RawEvent {
  return ev(
    "tool_call_error",
    { toolCallId, error: { type, message } },
    { turnId, sequenceNumber: seq, timestampMs: T0 + seq },
  );
}

function permissionRequested(
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
    { turnId, sequenceNumber: seq, timestampMs: T0 + seq },
  );
}

function permissionResolved(
  turnId: string,
  toolCallId: string,
  decision: "allow" | "deny",
  seq: number,
): RawEvent {
  return ev(
    "permission_resolved",
    { toolCallId, decision },
    { turnId, sequenceNumber: seq, timestampMs: T0 + seq },
  );
}

function turnComplete(
  turnId: string,
  seq: number,
  opts: { response: string; toolCallCount: number; historyRoundCount: number },
): RawEvent {
  return ev(
    "turn_complete",
    {
      response: opts.response,
      tokenCount: 240,
      toolCallCount: opts.toolCallCount,
      duration: 0,
      resultType: "success",
      historyRoundCount: opts.historyRoundCount,
      inputId: "cmd-0001",
    },
    { turnId, sequenceNumber: seq, timestampMs: T0 + seq },
  );
}

// ── S02 ────────────────────────────────────────────────────────────────
function s02Events(): RawEvent[] {
  const TURN = "msg_turn_s02";
  const AMSG = "msg_assist_s02";
  const prelude = [titleEvent("Say hello", 2)];
  const live = [
    turnStarted(TURN, "Say hello", 3),
    ...streamText(TURN, AMSG, "Hello from the mock model.", 8),
    modelComplete(TURN, { content: "Hello from the mock model.", seq: 14, cacheHitCount: 1 }),
    turnComplete(TURN, 17, {
      response: "Hello from the mock model.",
      toolCallCount: 0,
      historyRoundCount: 1,
    }),
  ];
  return [...prelude, ...live];
}

// ── S03：一个 Read 工具轮，然后正文 ─────────────────────────────────────
function s03Events(): RawEvent[] {
  const TURN = "msg_turn_s03";
  const AMSG1 = "msg_assist_s03a";
  const AMSG2 = "msg_assist_s03b";
  const TOOL = "call_read_1";
  const READ_INPUT = { file_path: `${WS}/hello.txt` };
  const prelude = [titleEvent("Read hello.txt", 2)];
  const live = [
    turnStarted(TURN, "Read hello.txt", 3),
    ...toolInputStreaming(
      TURN,
      AMSG1,
      TOOL,
      "Read",
      READ_INPUT,
      `{"file_path":"${WS}/hello.txt"}`,
      8,
    ),
    modelComplete(TURN, { content: "", seq: 13, cacheHitCount: 1, toolCallCount: 1 }),
    toolScheduled(TURN, AMSG1, TOOL, "Read", READ_INPUT, 16),
    toolStarted(TURN, TOOL, "Read", 17),
    toolResult(TURN, TOOL, "1\thi there\n2\t", 18),
    ...streamText(TURN, AMSG2, "The file says: hi there.", 27),
    modelComplete(TURN, { content: "The file says: hi there.", seq: 33, cacheHitCount: 2 }),
    turnComplete(TURN, 40, {
      response: "The file says: hi there.",
      toolCallCount: 1,
      historyRoundCount: 2,
    }),
    ev(
      "session_title_updated",
      {
        previousTitle: "Read hello.txt",
        source: "generated",
        title: "The file says: hi there",
      },
      { sequenceNumber: 41, timestampMs: T0 + 41 },
    ),
  ];
  return [...prelude, ...live];
}

// ── S04：Write + Edit 两个工具轮（含 pendingApproval / running / success）──
function s04Events(): RawEvent[] {
  const TURN = "msg_turn_s04";
  const AMSG1 = "msg_assist_s04a";
  const AMSG2 = "msg_assist_s04b";
  const AMSG3 = "msg_assist_s04c";
  const TOOL1 = "call_w1";
  const TOOL2 = "call_e1";
  const WRITE_INPUT = { file_path: `${WS}/notes.txt`, content: "alpha\nbeta\ngamma\n" };
  const WRITE_TEXT = JSON.stringify(WRITE_INPUT);
  const EDIT_INPUT = { file_path: `${WS}/notes.txt`, old_string: "beta", new_string: "BETA" };
  const EDIT_TEXT = JSON.stringify(EDIT_INPUT);
  const prelude = [titleEvent("Create notes.txt then edit it", 2)];
  const live = [
    turnStarted(TURN, "Create notes.txt then edit it", 3),
    ...toolInputStreaming(TURN, AMSG1, TOOL1, "Write", WRITE_INPUT, WRITE_TEXT, 8),
    modelComplete(TURN, { content: "", seq: 13, cacheHitCount: 1, toolCallCount: 1 }),
    toolScheduled(TURN, AMSG1, TOOL1, "Write", WRITE_INPUT, 16),
    permissionRequested(TURN, TOOL1, "Write", WRITE_INPUT, 17),
    permissionResolved(TURN, TOOL1, "allow", 18),
    toolStarted(TURN, TOOL1, "Write", 19),
    toolResult(
      TURN,
      TOOL1,
      `File created successfully at: ${WS}/notes.txt (file state is current in your context — no need to Read it back)`,
      20,
    ),
    ...toolInputStreaming(TURN, AMSG2, TOOL2, "Edit", EDIT_INPUT, EDIT_TEXT, 30),
    modelComplete(TURN, { content: "", seq: 35, cacheHitCount: 2, toolCallCount: 1 }),
    toolScheduled(TURN, AMSG2, TOOL2, "Edit", EDIT_INPUT, 38),
    permissionRequested(TURN, TOOL2, "Edit", EDIT_INPUT, 39),
    permissionResolved(TURN, TOOL2, "allow", 40),
    toolStarted(TURN, TOOL2, "Edit", 41),
    toolResult(
      TURN,
      TOOL2,
      `The file ${WS}/notes.txt has been updated successfully. (file state is current in your context — no need to Read it back)`,
      42,
    ),
    ...streamText(TURN, AMSG3, "Done: notes.txt created and edited.", 52),
    modelComplete(TURN, {
      content: "Done: notes.txt created and edited.",
      seq: 58,
      cacheHitCount: 3,
      fileChanges: { additions: 2, deletions: 1, files: 1 },
    }),
    turnComplete(TURN, 67, {
      response: "Done: notes.txt created and edited.",
      toolCallCount: 2,
      historyRoundCount: 3,
    }),
    ev(
      "session_title_updated",
      {
        previousTitle: "Create notes.txt then edit it",
        source: "generated",
        title: "Done: notes.txt created and edited",
      },
      { sequenceNumber: 68, timestampMs: T0 + 68 },
    ),
  ];
  return [...prelude, ...live];
}

// ── S05：Write 权限被拒（cancelled）──
function s05Events(): RawEvent[] {
  const TURN = "msg_turn_s05";
  const AMSG1 = "msg_assist_s05a";
  const AMSG2 = "msg_assist_s05b";
  const TOOL = "call_w1";
  const INPUT = { file_path: `${WS}/notes.txt`, content: "alpha\nbeta\ngamma\n" };
  const prelude = [titleEvent("Create notes.txt then edit it", 2)];
  const live = [
    turnStarted(TURN, "Create notes.txt then edit it", 3),
    ...toolInputStreaming(TURN, AMSG1, TOOL, "Write", INPUT, JSON.stringify(INPUT), 8),
    modelComplete(TURN, { content: "", seq: 13, cacheHitCount: 1, toolCallCount: 1 }),
    toolScheduled(TURN, AMSG1, TOOL, "Write", INPUT, 16),
    permissionRequested(TURN, TOOL, "Write", INPUT, 17),
    permissionResolved(TURN, TOOL, "deny", 18),
    ...streamText(TURN, AMSG2, "The write was denied, so I stopped.", 27),
    modelComplete(TURN, {
      content: "The write was denied, so I stopped.",
      seq: 33,
      cacheHitCount: 1,
    }),
    turnComplete(TURN, 42, {
      response: "The write was denied, so I stopped.",
      toolCallCount: 1,
      historyRoundCount: 1,
    }),
    ev(
      "session_title_updated",
      {
        previousTitle: "Create notes.txt then edit it",
        source: "generated",
        title: "The write was denied, so I stopped",
      },
      { sequenceNumber: 43, timestampMs: T0 + 43 },
    ),
  ];
  return [...prelude, ...live];
}

// ── S13：429 重试后成功 ────────────────────────────────────────────────
function s13Events(): RawEvent[] {
  const TURN = "msg_turn_s13";
  const AMSG = "msg_assist_s13";
  const REQ = "req_s13_1";
  const prelude = [titleEvent("Say hello", 2)];
  const networkBase = {
    timestamp: new Date(T0 + 6).toISOString(),
    traceId: "tr_net_s13",
    requestId: REQ,
    providerId: "mock",
    modelId: "mock-model",
    providerKind: "openai-compatible",
    transport: "sse",
    attempt: 1,
    maxAttempts: 11,
  };
  const live = [
    turnStarted(TURN, "Say hello", 3),
    // 首个 429：retry scheduled
    ev(
      "model_network_status",
      {
        ...networkBase,
        type: "model_retry_scheduled",
        delayMs: 100,
        nextAttempt: 2,
        reason: "rate_limited",
        message: "Rate limited",
        statusCode: 429,
      },
      { turnId: TURN, sequenceNumber: 6, timestampMs: T0 + 6 },
    ),
    ...streamText(TURN, AMSG, "Recovered after a retry.", 11),
    modelComplete(TURN, { content: "Recovered after a retry.", seq: 16, cacheHitCount: 1 }),
    turnComplete(TURN, 19, {
      response: "Recovered after a retry.",
      toolCallCount: 0,
      historyRoundCount: 1,
    }),
  ];
  return [...prelude, ...live];
}

export interface ScenarioVector {
  id: string;
  description: string;
  sourceTrace: string;
  prelude: Array<{ input: RawEvent; deltas: unknown[] }>;
  events: Array<{ input: RawEvent; deltas: unknown[] }>;
  coalesced: unknown[];
  snapshotBeforeLive: unknown;
  finalSnapshot: unknown;
}

function toInputEvent(e: RawEvent) {
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

function runScenario(
  id: string,
  description: string,
  sourceTrace: string,
  all: RawEvent[],
  preludeCount: number,
): ScenarioVector {
  const prelude = all.slice(0, preludeCount);
  const live = all.slice(preludeCount);
  const projection = new ProductProjection(SESSION, EPOCH);
  const preludeOut: Array<{ input: RawEvent; deltas: unknown[] }> = [];
  for (const e of prelude) {
    preludeOut.push({
      input: e,
      deltas: projection.applyEvent(toInputEvent(e) as never) as unknown[],
    });
  }
  const snapshotBeforeLive = projection.getSnapshot();
  const perEvent: Array<{ input: RawEvent; deltas: unknown[] }> = [];
  const acc: unknown[] = [];
  for (const e of live) {
    const deltas = projection.applyEvent(toInputEvent(e) as never) as unknown[];
    perEvent.push({ input: e, deltas });
    acc.push(...deltas);
  }
  return {
    id,
    description,
    sourceTrace,
    prelude: preludeOut,
    events: perEvent,
    coalesced: coalesceConversationDeltas(acc as never),
    snapshotBeforeLive,
    finalSnapshot: projection.getSnapshot(),
  };
}

const scenarios: ScenarioVector[] = [
  runScenario(
    "S02",
    "draft session + first input + one streamed text answer",
    "go/testdata/traces/agent/S02",
    s02Events(),
    1,
  ),
  runScenario(
    "S03",
    "one Read tool call then assistant text",
    "go/testdata/traces/agent/S03",
    s03Events(),
    1,
  ),
  runScenario(
    "S04",
    "Write + Edit with approvals and success",
    "go/testdata/traces/agent/S04",
    s04Events(),
    1,
  ),
  runScenario(
    "S05",
    "Write permission denied (cancelled)",
    "go/testdata/traces/agent/S05",
    s05Events(),
    1,
  ),
  runScenario(
    "S13",
    "rate-limited retry then success",
    "go/testdata/traces/agent/S13",
    s13Events(),
    1,
  ),
];

const payload = {
  generatedBy: "scripts/go-rewrite/gen-projection-vectors.mts",
  note: "raw per-event deltas from the real ProductProjection + coalesced stream + snapshots",
  session: SESSION,
  logEpoch: EPOCH,
  scenarios,
};

writeFileSync(OUT, JSON.stringify(payload, null, 2) + "\n");
console.log(`wrote ${OUT}: ${scenarios.length} scenarios`);
for (const s of scenarios) {
  console.log(
    `  ${s.id}: ${s.events.length} live events, ${s.events.reduce((n, e) => n + e.deltas.length, 0)} raw deltas, ${s.coalesced.length} coalesced`,
  );
}
