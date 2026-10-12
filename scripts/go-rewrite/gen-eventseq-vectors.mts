// 用真实的 Node agent 生成 Go eventstore 的序号对照向量。
//
// 背景：Node runtime 里每个 SessionEvent 都由 `InMemorySessionEventStore.append`
// 分配 `sequenceNumber`（apps/nex-cli/packages/contracts/src/events/in-memory-session-event-store.ts）。
// 序号只有两个可观测出口：
//   - 给「协议可见且非瞬态」的事件，经 v4 telemetry `eventSeq` / `computer-use/operation-event
//     sequenceNumber` / delta `createdAtSeq` 透出；
//   - 瞬态事件（model_streaming / model_network_status / streaming_tool_ledger_updated /
//     tool_call_progress）不进协议，只能在 debug 日志里看到。
//
// 本脚本以 NEX_RUNTIME_ENV=development 跑真实 Node agent + mock model 录制 S02/S03/S04/S05/S13，
// 从 agent 的 debug 日志恢复**每一次 append 的 (类型, 序号)**：
//   - `event_store.appended`        低频事件：直接给出 seq + type；
//   - `event_store.appended.summary` 瞬态事件被聚合：给出 (type, [first..last], count, payloadKinds)。
// 聚合块互相重叠（例如某次 model_network_status 的 5..14 包住 model_streaming 的 6..13），
// 因此按「块占用 [first..last] 内尚未被低频事件或更窄块占用的 seq」求解，并用 telemetry
// `eventSeq`（delta / network / turn / usage）与 computer-use operation-event 的 seq 交叉校验；
// 任一处不一致都直接让生成失败。
//
// 输出（go/testdata/vectors/agent-eventseq.json）每个场景一段：
//   { scenario, events: [{ type, seq }], transientTypes: [...] }
// Go 侧把 events 的 type 顺序喂给 eventstore.Append，必须得到完全相同的 seq。
import { writeFileSync, mkdirSync, readFileSync, readdirSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { recordScenario } from "./record/record-agent-session.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../..");
const SCENARIOS = join(HERE, "record/scenarios");
const TRACES = join(REPO, "go/testdata/traces/agent");
const OUT = join(REPO, "go/testdata/vectors/agent-eventseq.json");

const SCENARIO_FILES = [
  ["S02", "S02-chat.json"],
  ["S03", "S03-tool-read.json"],
  ["S04", "S04-write-edit.json"],
  ["S05", "S05-permission-denied.json"],
  ["S13", "S13-retry-429.json"],
];

// 与 contracts/src/events/session-event-retention.ts 的 TRANSIENT_SESSION_EVENT_TYPES 一致。
const TRANSIENT = new Set([
  "model_streaming",
  "tool_call_progress",
  "streaming_tool_ledger_updated",
  "model_network_status",
]);

// 瞬态聚合块内各 type 的因果顺序。实现在 runtime 里固定：
//   model_streaming: 每个 model step 依次 start → (text_start|reasoning_start|tool_input_start)
//     → *_delta* → (text_end|reasoning_end|tool_input_end) → (tool_call) → finish。
//   model_network_status: 每次物理请求 attempt 依次 started → (failed → retry_scheduled)* → completed。
//   streaming_tool_ledger_updated / tool_call_progress: 无法从聚合块区分内部顺序，
//     但都是同 type，序号连续，按占位展开即可（顺序对结果无影响）。

function fail(message: string): never {
  throw new Error(`eventseq vector generation failed: ${message}`);
}

// 从 telemetry trace 里取出所有 (eventSeq -> kind/status) 观测，作为聚合块展开的校验锚点。
function telemetryAnchors(id: string): Map<number, string> {
  const file = join(TRACES, id, "out.normalized.json");
  const anchors = new Map<number, string>();
  if (!existsSync(file)) return anchors;
  const frames = JSON.parse(readFileSync(file, "utf8"));
  for (const frame of frames) {
    if (frame.method === "v4/telemetry/event") {
      const p = frame.params;
      const tag = `${p.kind}:${p.status ?? ""}`;
      anchors.set(p.eventSeq, tag);
    } else if (frame.method === "computer-use/operation-event") {
      anchors.set(frame.params.sequenceNumber, `cu:${frame.params.kind}`);
    }
  }
  return anchors;
}

interface LoggedEvent {
  seq: number;
  type: string;
}

// 解析一次录制产生的 debug 日志，重建完整的 (seq, type) 有序列表。
function reconstruct(logPath: string): { events: LoggedEvent[] } {
  const lines = readFileSync(logPath, "utf8")
    .split("\n")
    .filter((l) => l.trim().length > 0)
    .map((l) => JSON.parse(l));

  // 收集：低频事件逐条给出精确 seq；瞬态事件被聚合，只给出 (type, [first..last], kinds)。
  type Summary = { first: number; last: number; type: string; kinds: Record<string, number> };
  const bySeq = new Map<number, string>();
  const summaries: Summary[] = [];
  for (const line of lines) {
    const c = line.context ?? {};
    if (line.event === "event_store.appended") {
      bySeq.set(c.sessionEventSequenceNumber, c.sessionEventType);
    } else if (line.event === "event_store.appended.summary") {
      summaries.push({
        first: c.firstSessionEventSequenceNumber,
        last: c.lastSessionEventSequenceNumber,
        type: c.sessionEventType,
        kinds: c.payloadKinds ?? {},
      });
    }
  }
  if (bySeq.size === 0 && summaries.length === 0) fail(`no event_store log lines in ${logPath}`);

  // 每个聚合块占用 [first..last] 中「尚未被低频事件或其它聚合块占用」的 seq。
  // 先处理范围更窄的块（块之间可以重叠，例如 model_network_status 5..14 包住 streaming 6..13）。
  summaries.sort((a, b) => a.last - a.first - (b.last - b.first));
  for (const s of summaries) {
    const want = Object.values(s.kinds).reduce((a, b) => a + b, 0);
    const free: number[] = [];
    for (let seq = s.first; seq <= s.last; seq++) {
      if (!bySeq.has(seq)) free.push(seq);
    }
    if (free.length !== want) {
      fail(`${s.type} ${s.first}..${s.last}: ${free.length} free seqs != ${want} events`);
    }
    const placed = expandSummary(s.type, s.kinds, free);
    for (const e of placed) bySeq.set(e.seq, e.type);
  }

  const maxSeq = Math.max(...bySeq.keys());
  const events: LoggedEvent[] = [];
  for (let seq = 1; seq <= maxSeq; seq++) {
    const type = bySeq.get(seq);
    if (type === undefined) fail(`sequence gap at ${seq}`);
    events.push({ seq, type });
  }
  return { events };
}

// 把一个聚合块展开成 (seq, type)：kinds 给出各 payload.kind 的计数，free 是这些事件
// 实际占用的 seq（升序）。运行时按固定因果顺序发射，这里按同一顺序回填：
//   model_streaming 每个 model step: start → (text_start|reasoning_start|tool_input_start)
//     → *_delta* → (text_end|reasoning_end|tool_input_end) → tool_call → finish。
//   model_network_status 每次 attempt: started → (failed → retry_scheduled)* → completed。
// 其它瞬态类型（streaming_tool_ledger_updated / tool_call_progress）同 type 连续，直接顺序回填。
function expandSummary(type: string, kinds: Record<string, number>, free: number[]): LoggedEvent[] {
  const out: LoggedEvent[] = [];
  let i = 0;
  const emit = (n: number): void => {
    for (let k = 0; k < n; k++) {
      if (i >= free.length) fail(`${type} expand overran free seqs`);
      out.push({ seq: free[i++], type });
    }
  };
  if (type === "model_network_status") {
    // 网络事件 payload 无 kind 字段，日志显示 <missing>；且 flush 边界会把一次 attempt 的
    // started/failed/retry_scheduled/completed 切到不同块里，无法从单个块恢复 kind 顺序。
    // 向量只关心 type == model_network_status，直接按计数回填即可。
    emit(Object.values(kinds).reduce((a, b) => a + b, 0));
    return out;
  }
  if (type === "model_streaming") {
    const order = [
      "start",
      "text_start",
      "reasoning_start",
      "tool_input_start",
      "text_delta",
      "reasoning_delta",
      "tool_input_delta",
      "text_end",
      "reasoning_end",
      "tool_input_end",
      "tool_call",
      "compact_stream_boundary",
      "finish",
      "error",
    ];
    for (const k of order) emit(kinds[k] ?? 0);
    for (const [k, c] of Object.entries(kinds)) {
      if (c > 0 && !order.includes(k)) fail(`unhandled model_streaming kind ${k} x${c}`);
    }
    return out;
  }
  emit(Object.values(kinds).reduce((a, b) => a + b, 0));
  return out;
}

// 交叉校验：每个 telemetry eventSeq 观测都必须能和展开后的事件对上。
// 只对「类型确定」的 kind 断言；tool.lifecycle 放宽为 tool_*（scheduled/started/result 共用）。
function validate(id: string, events: LoggedEvent[]): void {
  const anchors = telemetryAnchors(id);
  const bySeq = new Map(events.map((e) => [e.seq, e.type]));
  for (const [seq, tag] of anchors) {
    const type = bySeq.get(seq);
    if (type === undefined) fail(`${id}: telemetry seq ${seq} (${tag}) has no event`);
    const [kind] = tag.split(":");
    const expect = ((): string | null => {
      switch (kind) {
        case "turn.started":
          return "turn_started";
        case "turn.terminal":
          return "turn_complete";
        case "usage.delta":
          return "model_complete";
        case "stream.chunk":
          return "model_streaming";
        case "model.request.status":
          return "model_network_status";
        default:
          return null;
      }
    })();
    if (expect && type !== expect) {
      fail(`${id}: seq ${seq} expected ${expect} for ${tag}, got ${type}`);
    }
    if (kind === "tool.lifecycle" && !type?.startsWith("tool_call")) {
      fail(`${id}: seq ${seq} expected tool_call* for ${tag}, got ${type}`);
    }
  }
}

async function main(): Promise<void> {
  mkdirSync(dirname(OUT), { recursive: true });
  const outDir = join(tmpdir(), "eventseq-rec");
  const result: unknown[] = [];

  for (const [id, file] of SCENARIO_FILES) {
    let logFile: string;
    let sha: string | undefined;
    if (process.env.EVENTSEQ_LOG_DIR) {
      // 复用之前抓好的 debug 日志（离线重放，无需再跑 agent）。
      logFile = join(process.env.EVENTSEQ_LOG_DIR, `${id}.jsonl`);
    } else {
      process.env.KEEP_REC_DIR = "1";
      const before = new Set(readdirSync(tmpdir()).filter((n) => n.startsWith("nex-rec-")));
      await recordScenario(join(SCENARIOS, file), join(outDir, id), {
        quiet: true,
        extraEnv: { NEX_RUNTIME_ENV: "development" },
      });
      const fresh = readdirSync(tmpdir())
        .filter((n) => n.startsWith("nex-rec-"))
        .find((n) => !before.has(n));
      if (!fresh) fail(`${id}: no kept recording dir (KEEP_REC_DIR must stay set)`);
      const root = join(tmpdir(), fresh);
      const logDir = join(root, "home/.nex/cli/log");
      logFile = readdirSync(logDir).map((f) => join(logDir, f))[0];
      sha = JSON.parse(readFileSync(join(outDir, id, "meta.json"), "utf8")).normalizedOutSha256;
    }
    const { events } = reconstruct(logFile);
    validate(id, events);
    result.push({ scenario: id, events, normalizedOutSha256: sha, transientTypes: [...TRANSIENT] });
    console.log(`${id}: ${events.length} events (${logFile})`);
  }

  writeFileSync(
    OUT,
    JSON.stringify({ generatedBy: "gen-eventseq-vectors.mts", scenarios: result }, null, 1) + "\n",
  );
  console.log("wrote", OUT);
}

await main();
