/* eslint-disable max-lines -- 向量生成脚本：一条脚本驱动一组真实 TS 类并顺序产出全部向量，拆开会让用例与其期望值分居两个文件。 */
// 用真实的 SqliteSessionStore 仓库生成 Go 侧会话持久化对照向量。
//
// 目标：Go 实现写出的每一张表、每一列都必须与 Node 完全一致（列值逐列比较）。
// 为了让结果可复现，这里固定所有非确定输入：id、时钟、顺序、环境快照。
//
// 运行（仓库根目录）：
//   cd packages/shared && npx --yes tsx ../../scripts/go-rewrite/gen-store-vectors.mts
//
// 输出：go/testdata/vectors/store-scenarios.json
//   {
//     scenarios: [
//       {
//         name, clock, steps: [...scripted ops...],
//         tables: { <table>: [ { <col>: <value> }, ... ] }   // 按稳定顺序排列
//       }
//     ],
//     excludedColumns: [...],  // 明确排除的列（每次运行都不同/由 Node 内部生成）
//     notes: {...}
//   }
//
// 这份脚本只调用 TS 源码里真实存在的函数（repositories/*），不做任何“重写”；
// 因此它同时是 Node 行为的定义。
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { DatabaseSync } from "node:sqlite";

import { createSqliteSessionStore } from "../../apps/nex-cli/packages/adapters/src/storage/session-store/sqlite-session-store.ts";
import * as sessionRepository from "../../apps/nex-cli/packages/adapters/src/storage/session-store/repositories/sessions.ts";
import * as messageRepository from "../../apps/nex-cli/packages/adapters/src/storage/session-store/repositories/messages.ts";
import * as sessionEntryRepository from "../../apps/nex-cli/packages/adapters/src/storage/session-store/repositories/session-entries.ts";
import * as sessionInputRepository from "../../apps/nex-cli/packages/adapters/src/storage/session-store/repositories/session-inputs.ts";
import * as usageRepository from "../../apps/nex-cli/packages/adapters/src/storage/session-store/repositories/usage.ts";
import * as inputHistoryRepository from "../../apps/nex-cli/packages/adapters/src/storage/session-store/repositories/input-history.ts";
import * as todoRepository from "../../apps/nex-cli/packages/adapters/src/storage/session-store/repositories/todos.ts";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../..");
const OUT = join(REPO, "go/testdata/vectors/store-scenarios.json");

// ---- 确定性时钟 -------------------------------------------------------------
// 多个仓库直接调用 Date.now()（savePart 的 time_updated、saveSessionInput、
// updateSession、pruneUsage）。为了让向量逐列可复现，这里把全局 Date.now 固定成
// 一个常量。选在未来是因为 pruneUsage 会删除 started_at < now - 30 天的使用记录：
// 固定成过去会让 model_usage/turn_usage/tool_usage 被剪掉，无法校验列内容。
const NOW = 2_000_000_000_500;
const realDateNow = Date.now;
Date.now = () => NOW;

// ---- 固定输入 ---------------------------------------------------------------

const SESSION = "sess_00000000-0000-4000-8000-000000000001";
const TURN = "turn_00000000-0000-4000-8000-000000000002";
const TRACE = "trace-0000-0000-0000-000000000000";
const SPAN = "span-0000-0000-00";
const USER_MSG = "msg_test_user_1";
const ASSISTANT_MSG = "msg_test_assistant_1";
const TEXT_PART = "part_test_user_text_1";
const STEP_START = "part_test_step_start_1";
const STEP_TEXT = "part_test_step_text_1";
const STEP_FINISH = "part_test_step_finish_1";
const DIR = "/work/proj";
const PROJECT = "proj_work-proj";
const ENV = {
  cwd: DIR,
  platform: "linux",
  shell: "zsh",
  osVersion: "linux 6.8.0 x64",
  nodeVersion: "v24.0.0",
  isGitRepository: false,
  gitStatus: "not_repo",
};
const MODEL_SELECTION = {
  providerId: "mock",
  modelId: "mock-model",
  options: { reasoningLevel: "none" },
};
const TOOLS = { Read: true, Write: true };

// 固定时钟：每个写操作显式传入，避免 Date.now()。
// 这些值贴着 NOW（而不是过去的真实时间），否则 pruneUsage 会删掉使用记录。
const T = {
  sessionCreate: NOW - 20_000,
  inputAdmit: NOW - 19_000,
  userMessage: NOW - 18_000,
  modelStart: NOW - 17_000,
  firstToken: NOW - 16_000,
  assistantComplete: NOW - 15_000,
  turnComplete: NOW - 14_000,
  title: NOW - 13_000,
  toolStart: NOW - 12_000,
  toolFirstOutput: NOW - 11_000,
  toolComplete: NOW - 10_000,
};

// ---- 会话写入序列（与 Go 侧脚本完全相同的语义）------------------------------

interface Scenario {
  name: string;
  tables: Record<string, Array<Record<string, unknown>>>;
}

const scenarios: Scenario[] = [];

async function withStore(
  fn: (
    store: ReturnType<typeof createSqliteSessionStore>,
    dump: () => Record<string, Array<Record<string, unknown>>>,
  ) => Promise<void>,
): Promise<void> {
  const dir = mkdtempSync(join(tmpdir(), "nex-store-vec-"));
  const dbPath = join(dir, "db.sqlite");
  const store = createSqliteSessionStore({ dbPath });
  let closed = false;
  const closeOnce = () => {
    if (closed) return;
    closed = true;
    store.close();
  };
  try {
    // 写入完成后、关闭 store 的连接后再打开独立连接 dump，避免并发连接持锁。
    const dump = () => {
      closeOnce();
      const db = new DatabaseSync(dbPath);
      try {
        return dumpTables(db, WRITTEN_TABLES);
      } finally {
        db.close();
      }
    };
    await fn(store, dump);
  } finally {
    closeOnce();
    rmSync(dir, { recursive: true, force: true });
  }
}

function dumpTables(
  db: DatabaseSync,
  tables: readonly string[],
): Record<string, Array<Record<string, unknown>>> {
  const result: Record<string, Array<Record<string, unknown>>> = {};
  for (const table of tables) {
    const rows = db.prepare(`select * from ${table}`).all() as Array<Record<string, unknown>>;
    // input_history.id 内部用 crypto.randomUUID()，不可注入；归一成固定值。
    // 这是唯一被排除的列，Go 侧该列按前缀 `input_` 校验即可。
    result[table] = rows.map((row) =>
      table === "input_history" ? stableRow({ ...row, id: "<INPUT_HISTORY_ID>" }) : stableRow(row),
    );
  }
  return result;
}

function stableRow(row: Record<string, unknown>): Record<string, unknown> {
  // 按列名排序，保证不同 sqlite 驱动的列顺序不影响比较；同时保留 null。
  const out: Record<string, unknown> = {};
  for (const key of Object.keys(row).sort()) out[key] = row[key];
  return out;
}

const WRITTEN_TABLES = [
  "session",
  "message",
  "part",
  "session_entry",
  "session_input",
  "input_history",
  "model_usage",
  "turn_usage",
  "tool_usage",
  "todo",
] as const;

async function createSession(store: ReturnType<typeof createSqliteSessionStore>): Promise<void> {
  await store.createSession({
    id: SESSION,
    projectID: PROJECT,
    taskType: "interactive",
    traceID: TRACE,
    slug: SESSION,
    directory: DIR,
    path: DIR,
    title: "Say hello",
    titleSource: "first_input",
    version: "1.2.3",
    permission: { mode: "build" },
    time: { created: T.sessionCreate, updated: T.sessionCreate },
  });
}

async function persistSessionEntries(
  store: ReturnType<typeof createSqliteSessionStore>,
): Promise<void> {
  await store.saveSessionEntry({
    id: `${SESSION}:runtime-model-selection`,
    sessionID: SESSION,
    type: "runtime/model_selection",
    touchSession: false,
    time: { created: T.sessionCreate, updated: T.sessionCreate },
    data: MODEL_SELECTION,
  });
  await store.saveSessionEntry({
    id: `${SESSION}:runtime-execution-state`,
    sessionID: SESSION,
    type: "runtime/execution_state",
    touchSession: false,
    time: { created: T.sessionCreate, updated: T.sessionCreate },
    data: { mode: "build", planEnabled: false },
  });
}

function inputIntent(): Record<string, unknown> {
  return {
    sourceCommandId: "cmd-0001",
    queueItemId: "queue_cmd-0001",
    clientId: "vec-client",
    kind: "sendText",
    text: "Say hello",
    modelSelection: MODEL_SELECTION,
    mode: "build",
    planEnabled: false,
    admissionSeq: 1,
    admittedAt: T.inputAdmit,
    requestedDelivery: "startNow",
    admittedDelivery: "startNow",
    attachmentRefs: [],
  };
}

function conversationInputIntent(dispatch: "queued" | "drained"): Record<string, unknown> {
  const intent = inputIntent();
  return {
    sourceCommandId: intent.sourceCommandId,
    queueItemId: intent.queueItemId,
    clientId: intent.clientId,
    kind: intent.kind,
    text: intent.text,
    attachments: intent.attachmentRefs,
    modelSelection: intent.modelSelection,
    mode: intent.mode,
    planEnabled: intent.planEnabled,
    delivery: { requested: "startNow", admitted: "startNow" },
    order: { admissionSeq: 1 },
    steer: { state: "notRequested" },
    dispatch: { state: dispatch },
    admittedAt: T.inputAdmit,
  };
}

async function admitInput(store: ReturnType<typeof createSqliteSessionStore>): Promise<void> {
  await store.saveSessionInput({
    id: "queue_cmd-0001",
    sessionID: SESSION,
    kind: "sendText",
    delivery: "startNow",
    payload: {
      text: "Say hello",
      intent: inputIntent(),
      conversationInputIntent: conversationInputIntent("queued"),
      attachments: [],
      sourceCommandType: "createSession",
    },
  });
}

function userMessage(): Record<string, unknown> {
  return {
    id: USER_MSG,
    sessionID: SESSION,
    role: "user",
    time: { created: T.userMessage },
    agent: "nex-agent",
    modelSelection: MODEL_SELECTION,
    contextSnapshot: { envInfo: ENV },
    semantics: {
      origin: "real_user",
      kind: "user_prompt",
      uiVisibility: "visible",
      providerVisibility: "visible",
      transcriptVisibility: "visible",
    },
    anchor: { turnId: TURN, origin: "realUser", sourceCommandId: "cmd-0001" },
    tools: TOOLS,
    metadata: {
      inputIntent: inputIntent(),
      conversationInputIntent: conversationInputIntent("drained"),
      inputClientId: "vec-client",
    },
  };
}

function userParts(): Array<Record<string, unknown>> {
  return [
    {
      id: TEXT_PART,
      sessionID: SESSION,
      messageID: USER_MSG,
      type: "text",
      text: "Say hello",
      time: { start: T.userMessage, end: T.userMessage },
    },
  ];
}

function assistantMessage(): Record<string, unknown> {
  return {
    id: ASSISTANT_MSG,
    sessionID: SESSION,
    role: "assistant",
    parentID: USER_MSG,
    modelId: "mock-model",
    providerId: "mock",
    mode: "build",
    planEnabled: false,
    agent: "nex-agent",
    path: { cwd: DIR, root: DIR },
    time: { created: T.modelStart, completed: T.assistantComplete },
    cost: 0,
    tokens: { total: 120, input: 100, output: 20, reasoning: 0, cache: { read: 0, write: 0 } },
    finish: "stop",
    semantics: {
      origin: "agent_runtime",
      kind: "assistant_response",
      uiVisibility: "visible",
      providerVisibility: "visible",
      transcriptVisibility: "visible",
    },
    anchor: { turnId: TURN },
  };
}

function assistantParts(): Array<Record<string, unknown>> {
  return [
    { id: STEP_START, sessionID: SESSION, messageID: ASSISTANT_MSG, type: "step-start" },
    {
      id: STEP_TEXT,
      sessionID: SESSION,
      messageID: ASSISTANT_MSG,
      type: "text",
      text: "Hello from the mock model.",
      time: { start: T.modelStart, end: T.assistantComplete },
    },
    {
      id: STEP_FINISH,
      sessionID: SESSION,
      messageID: ASSISTANT_MSG,
      type: "step-finish",
      reason: "stop",
      cost: 0,
      tokens: { total: 120, input: 100, output: 20, reasoning: 0, cache: { read: 0, write: 0 } },
    },
  ];
}

function modelUsage(): Record<string, unknown> {
  return {
    id: `usage_model_main_turn_${ASSISTANT_MSG}_0`,
    logicalRequestId: ASSISTANT_MSG,
    attemptIndex: 0,
    sessionID: SESSION,
    turnID: TURN,
    traceID: TRACE,
    spanID: SPAN,
    assistantMessageID: ASSISTANT_MSG,
    parentUserMessageID: USER_MSG,
    querySource: "main_turn",
    providerId: "mock",
    modelId: "mock-model",
    reasoningLevel: "none",
    agent: "nex-agent",
    mode: "build",
    taskType: "interactive",
    status: "completed",
    startedAt: T.modelStart,
    firstTokenAt: T.firstToken,
    completedAt: T.assistantComplete,
    durationMs: T.assistantComplete - T.modelStart,
    timeToFirstTokenMs: T.firstToken - T.modelStart,
    finishReason: "stop",
    toolCallCount: 0,
    inputTokens: 100,
    outputTokens: 20,
    reasoningTokens: 0,
    cacheCreationInputTokens: 0,
    cacheReadInputTokens: 0,
    providerTotalTokens: 120,
    computedTotalTokens: 120,
    retryCount: 0,
    retryable: false,
    cancelledByUser: false,
    contextExceeded: false,
    rawUsage: {
      inputTokens: 100,
      outputTokens: 20,
      totalTokens: 120,
      cacheReadTokens: 0,
      reasoningTokens: 0,
    },
    providerMetadata: { rawFinishReason: "stop" },
  };
}

function turnUsage(status: "completed" | "error", retryable: boolean): Record<string, unknown> {
  return {
    sessionID: SESSION,
    turnID: TURN,
    traceID: TRACE,
    userMessageID: USER_MSG,
    status,
    startedAt: T.inputAdmit,
    firstModelStartAt: T.modelStart,
    firstTokenAt: T.firstToken,
    completedAt: T.turnComplete,
    durationMs: T.turnComplete - T.inputAdmit,
    timeToFirstTokenMs: T.firstToken - T.inputAdmit,
    modelRequestCount: 1,
    modelRetryCount: 0,
    toolCallCount: 0,
    toolErrorCount: 0,
    inputTokens: 100,
    outputTokens: 20,
    reasoningTokens: 0,
    cacheCreationInputTokens: 0,
    cacheReadInputTokens: 0,
    computedTotalTokens: 120,
    retryable,
    cancelledByUser: false,
    contextExceeded: false,
  };
}

function toolUsage(over: Record<string, unknown>): Record<string, unknown> {
  return {
    id: `usage_tool_${SESSION}_call_read_1`,
    sessionID: SESSION,
    turnID: TURN,
    traceID: TRACE,
    toolCallID: "call_read_1",
    toolName: "Read",
    sideEffectScope: "none",
    readOnly: true,
    destructive: false,
    approvalStatus: "none",
    status: "completed",
    startedAt: T.toolStart,
    firstOutputAt: T.toolFirstOutput,
    completedAt: T.toolComplete,
    durationMs: T.toolComplete - T.toolStart,
    timeToFirstOutputMs: T.toolFirstOutput - T.toolStart,
    exitCode: 0,
    outputBytes: 13,
    stdoutBytes: 0,
    stderrBytes: 0,
    truncated: false,
    retryCount: 0,
    retryable: false,
    cancelledByUser: false,
    ...over,
  };
}

// ---- 场景 1：文本轮（对应 S02）---------------------------------------------

await withStore(async (store, dump) => {
  await createSession(store);
  await persistSessionEntries(store);
  await admitInput(store);
  await store.promoteSessionInput({
    id: "queue_cmd-0001",
    sessionID: SESSION,
    message: userMessage() as never,
    parts: userParts() as never,
  });
  await store.saveMessage(assistantMessage() as never);
  for (const part of assistantParts()) await store.savePart(part as never);
  await store.recordModelUsage(modelUsage() as never);
  await store.upsertTurnUsage(turnUsage("completed", false) as never);
  await store.recordInputHistory({
    projectID: PROJECT,
    sessionID: SESSION,
    text: "Say hello",
    kind: "prompt",
    time: { created: T.inputAdmit },
  });
  await store.updateSession({
    id: SESSION,
    title: "Say hello",
    titleSource: "first_input",
    timeUpdated: T.title,
  });
  scenarios.push({ name: "text-turn", tables: dump() });
});

// ---- 场景 2：工具轮（Read，对应 S03）--------------------------------------

await withStore(async (store, dump) => {
  await createSession(store);
  await persistSessionEntries(store);
  await admitInput(store);
  await store.promoteSessionInput({
    id: "queue_cmd-0001",
    sessionID: SESSION,
    message: userMessage() as never,
    parts: userParts() as never,
  });
  const toolMsgId = "msg_test_assistant_tool_1";
  await store.saveMessage({
    id: toolMsgId,
    sessionID: SESSION,
    role: "assistant",
    parentID: USER_MSG,
    modelId: "mock-model",
    providerId: "mock",
    mode: "build",
    planEnabled: false,
    agent: "nex-agent",
    path: { cwd: DIR, root: DIR },
    time: { created: T.modelStart, completed: T.toolComplete },
    cost: 0,
    tokens: { total: 120, input: 100, output: 20, reasoning: 0, cache: { read: 0, write: 0 } },
    finish: "tool-calls",
    semantics: {
      origin: "agent_runtime",
      kind: "assistant_response",
      uiVisibility: "visible",
      providerVisibility: "visible",
      transcriptVisibility: "visible",
    },
    anchor: { turnId: TURN },
  } as never);
  await store.savePart({
    id: "part_test_tool_step_start_1",
    sessionID: SESSION,
    messageID: toolMsgId,
    type: "step-start",
  } as never);
  await store.savePart({
    id: "part_test_tool_read_1",
    sessionID: SESSION,
    messageID: toolMsgId,
    type: "tool",
    callID: "call_read_1",
    declarationIndex: 0,
    tool: "Read",
    state: {
      status: "completed",
      input: { file_path: "/work/proj/hello.txt" },
      output: "1\thi there\n2\t",
      title: "Read",
      metadata: {
        schemaVersion: 1,
        serialization: {
          truncated: false,
          originalBytes: 13,
          returnedBytes: 13,
          budgetStrategy: "truncate",
        },
      },
      time: { start: T.toolStart, end: T.toolComplete },
    },
  } as never);
  await store.savePart({
    id: "part_test_tool_step_finish_1",
    sessionID: SESSION,
    messageID: toolMsgId,
    type: "step-finish",
    reason: "tool-calls",
    cost: 0,
    tokens: { total: 120, input: 100, output: 20, reasoning: 0, cache: { read: 0, write: 0 } },
  } as never);
  await store.recordModelUsage({
    ...modelUsage(),
    id: `usage_model_main_turn_${toolMsgId}_0`,
    logicalRequestId: toolMsgId,
    assistantMessageID: toolMsgId,
    finishReason: "tool-calls",
  } as never);
  await store.upsertTurnUsage({ ...turnUsage("completed", false), toolCallCount: 1 } as never);
  await store.upsertToolUsage({
    ...toolUsage({}),
    outputBytes: 13,
  } as never);
  scenarios.push({ name: "tool-turn", tables: dump() });
});

// ---- 场景 3：重试（attemptIndex=1，rate_limited，对应 S13）-----------------

await withStore(async (store, dump) => {
  await createSession(store);
  await persistSessionEntries(store);
  await admitInput(store);
  await store.promoteSessionInput({
    id: "queue_cmd-0001",
    sessionID: SESSION,
    message: userMessage() as never,
    parts: userParts() as never,
  });
  await store.saveMessage(assistantMessage() as never);
  for (const part of assistantParts()) await store.savePart(part as never);
  // 第一次尝试失败（rate_limited），第二次成功；两条 model_usage 行。
  await store.recordModelUsage({
    ...modelUsage(),
    id: `usage_model_main_turn_${ASSISTANT_MSG}_0`,
    attemptIndex: 0,
    status: "error",
    errorType: "rate_limited",
    errorMessage: "Provider rate limited the model request.",
    retryable: true,
    retryCount: 1,
    completedAt: T.firstToken,
  } as never);
  await store.recordModelUsage({
    ...modelUsage(),
    id: `usage_model_main_turn_${ASSISTANT_MSG}_1`,
    attemptIndex: 1,
    retryCount: 1,
    retryable: true,
  } as never);
  await store.upsertTurnUsage({ ...turnUsage("completed", false), modelRequestCount: 1 } as never);
  scenarios.push({ name: "retry", tables: dump() });
});

// ---- 场景 4：拒绝（Write permission denied，对应 S05）----------------------

await withStore(async (store, dump) => {
  await createSession(store);
  await persistSessionEntries(store);
  await admitInput(store);
  await store.promoteSessionInput({
    id: "queue_cmd-0001",
    sessionID: SESSION,
    message: userMessage() as never,
    parts: userParts() as never,
  });
  const toolMsgId = "msg_test_assistant_denied_1";
  await store.saveMessage({
    id: toolMsgId,
    sessionID: SESSION,
    role: "assistant",
    parentID: USER_MSG,
    modelId: "mock-model",
    providerId: "mock",
    mode: "build",
    planEnabled: false,
    agent: "nex-agent",
    path: { cwd: DIR, root: DIR },
    time: { created: T.modelStart, completed: T.toolComplete },
    cost: 0,
    tokens: { total: 120, input: 100, output: 20, reasoning: 0, cache: { read: 0, write: 0 } },
    finish: "tool-calls",
    semantics: {
      origin: "agent_runtime",
      kind: "assistant_response",
      uiVisibility: "visible",
      providerVisibility: "visible",
      transcriptVisibility: "visible",
    },
    anchor: { turnId: TURN },
  } as never);
  await store.savePart({
    id: "part_test_denied_step_start_1",
    sessionID: SESSION,
    messageID: toolMsgId,
    type: "step-start",
  } as never);
  await store.savePart({
    id: "part_test_denied_write_1",
    sessionID: SESSION,
    messageID: toolMsgId,
    type: "tool",
    callID: "call_w1",
    declarationIndex: 0,
    tool: "Write",
    state: {
      status: "error",
      input: { file_path: "/work/proj/notes.txt", content: "alpha\nbeta\ngamma\n" },
      error:
        "The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). STOP what you are doing and wait for the user to tell you how to proceed.",
      metadata: {},
      time: { start: T.toolComplete, end: T.toolComplete },
    },
  } as never);
  await store.upsertToolUsage({
    ...toolUsage({
      id: `usage_tool_${SESSION}_call_w1`,
      toolCallID: "call_w1",
      toolName: "Write",
      sideEffectScope: "workspace",
      readOnly: false,
      status: "error",
      firstOutputAt: T.toolFirstOutput,
      durationMs: 0,
      timeToFirstOutputMs: 0,
      exitCode: null,
      outputBytes: 0,
      errorType: "permission_denied",
      errorMessage:
        "The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). STOP what you are doing and wait for the user to tell you how to proceed.",
    }),
  } as never);
  scenarios.push({ name: "denied", tables: dump() });
});

// ---- 场景 5：admit -> settle（cancelled）-----------------------------------

await withStore(async (store, dump) => {
  await createSession(store);
  await persistSessionEntries(store);
  await admitInput(store);
  await store.settleSessionInput({
    id: "queue_cmd-0001",
    sessionID: SESSION,
    status: "cancelled",
    reason: "user_cancelled",
  });
  scenarios.push({ name: "admit-settle-cancelled", tables: dump() });
});

// ---- 场景 6：queue edit/reorder（updateSessionInputs）----------------------

await withStore(async (store, dump) => {
  await createSession(store);
  await persistSessionEntries(store);
  await admitInput(store);
  await store.updateSessionInputs({
    sessionID: SESSION,
    updates: [
      {
        id: "queue_cmd-0001",
        text: "Edited hello",
        queuePosition: 2,
        intent: { ...inputIntent(), text: "Edited hello", queuePosition: 2 } as never,
      },
    ],
  });
  scenarios.push({ name: "queue-edit", tables: dump() });
});

const payload = {
  generatedBy: "scripts/go-rewrite/gen-store-vectors.mts",
  description:
    "Node SqliteSessionStore repository behaviour. Go must write identical rows for identical input.",
  fixed: {
    session: SESSION,
    turn: TURN,
    trace: TRACE,
    span: SPAN,
    directory: DIR,
    project: PROJECT,
    clock: T,
  },
  // 只有这一列被排除，因为 Node 运行时用 crypto.randomUUID() 生成、无法注入。
  excludedColumns: ["input_history.id"] as string[],
  tables: WRITTEN_TABLES,
  scenarios,
};

writeFileSync(OUT, JSON.stringify(payload, null, 2) + "\n", "utf8");
// 读回一次，保证 JSON 合法。
JSON.parse(readFileSync(OUT, "utf8"));
console.log(`wrote ${OUT}: ${scenarios.length} scenarios, tables=${WRITTEN_TABLES.join(",")}`);
