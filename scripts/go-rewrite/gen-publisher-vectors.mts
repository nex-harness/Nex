/* eslint-disable max-lines -- 向量生成脚本：一条脚本驱动一组真实 TS 类并顺序产出全部向量，拆开会让用例与其期望值分居两个文件。 */
// 用真实的 TypeScript ConversationTopicPublisher 生成 Go 对照向量。
//
// 目标：把 publisher 的权威账本（保留窗 + subscribe 裁决 + 每订阅者 flush 管线）
// 逐字节钉住。投影（SessionEvent -> delta）由 Agent B 负责，本生成器用一份
// 脚本化假投影替换它：假投影把「本事件产出的 delta」原样交回，于是 publisher
// 的其余全部行为都是真实的。
//
// 产出 go/testdata/vectors/publisher.json，Go 测试读取并要求逐字节一致。
//
// 归一化：与 record/normalize.mjs 同口径地抹掉运行相关的值（时间戳、subscriptionId
// 里的 logEpoch），但保留 seq、logEpoch 关系、帧的 fromSeq/toSeq/ordinal 与
// delta 内容——那些是语义。
import { writeFileSync } from "node:fs";
import {
  ConversationTopicPublisher,
  ProjectionPayloadTooLargeError,
} from "../../apps/nex-cli/packages/bootstrap/src/nex-protocol-v4/conversation-topic-publisher.ts";
import { coalesceConversationDeltas } from "../../packages/shared/src/nex-protocol-v4/coalesce.ts";
import {
  filterConversationDeltasForProfile,
  filterConversationRowsForProfile,
} from "../../packages/shared/src/nex-protocol-v4/profiles.ts";
import { encodeConversationDeltasForLegacy } from "../../apps/nex-cli/packages/bootstrap/src/nex-protocol-v4/conversation-workflow-run-deltas.ts";
import { clampWorkflowRunsForLegacy } from "../../packages/shared/src/nex-protocol-v4/workflow-runs-legacy.ts";
import {
  DELIVERY_PROFILES,
  PROTOCOL_V4_LIMITS,
} from "../../packages/shared/src/nex-protocol-v4/core.ts";

const OUT = new URL("../../go/testdata/vectors/publisher.json", import.meta.url);

// 固定的 logEpoch：向量里所有 subscriptionId / logEpoch 都归一化到它，
// Go 用同一个常量构造 publisher 再比对，于是「sub-<epoch>-<n>」完全可比。
const EPOCH = "epochepoch0";

// ── 归一化：把 sentAt / logEpoch 等运行相关量稳定化 ──
const NORM_TIME = 1_700_000_000_000;
function normalizeFrame(frame: any, subscriptionId: string): any {
  const out = structuredClone(frame);
  out.sentAt = NORM_TIME;
  // subscriptionId 形如 sub-<logEpoch>-<n>；把 epoch 段换成占位符，n 保留。
  out.subscriptionId = subscriptionId;
  if (out.topic) out.topic = out.topic.replace(/conversation\/.+$/, "conversation/<session>");
  if (out.payload?.kind === "snapshot" && out.payload.snapshot) {
    out.payload.snapshot.sessionId = "<session>";
    out.payload.snapshot.logEpoch = EPOCH;
    out.payload.snapshot.rows.window = out.payload.snapshot.rows.window.map((row: any) => {
      if (row && typeof row === "object") row.rowId = row.rowId; // rowIds 是语义，保留
      return row;
    });
  }
  return out;
}

// 归一化 subscriptionId：sub-<epoch>-<n> -> sub-<epoch>-<n>，epoch 段统一。
function normalizeSubId(id: string): string {
  return id.replace(/^sub-[^-]+-(\d+)$/, "sub-<epoch>-$1");
}

// ── 假投影：把脚本给定的 delta 交回 publisher ──
class ScriptedProjection {
  snapshot: any;
  private seq = 0;
  private revision = 0;
  constructor(
    sessionId: string,
    logEpoch: string,
    private workflowRuns: any = undefined,
  ) {
    this.snapshot = initialSnapshot(sessionId, logEpoch);
  }
  getSnapshot() {
    return this.snapshot;
  }
  establishedStreamingAppend(): null {
    return null; // 强制走 applyEventAtomically 路径
  }
  applyEventAtomically(_event: any, predicate: (snapshot: any, produced: any[]) => boolean) {
    // 与真实实现同义：候选快照上 applyEvent 再判定是否接收。
    const produced: any[] = this.nextDeltas;
    const candidate = this.applyDeltas(this.snapshot, produced);
    if (!predicate(candidate, produced)) return null;
    this.snapshot = candidate;
    return produced;
  }
  applyEvent(_event: any): any[] {
    const produced: any[] = this.nextDeltas;
    this.snapshot = this.applyDeltas(this.snapshot, produced);
    return produced;
  }
  /** 由脚本在每次 ingest 前设置。 */
  nextDeltas: any[] = [];
  /** ingest 前推进 seq/revision。 */
  advance(seq: number, revision?: number) {
    this.snapshot.seq = seq;
    if (revision !== undefined) this.snapshot.revision = revision;
  }
  private applyDeltas(snapshot: any, deltas: any[]): any {
    // 只支持 publisher 会校验的少量 op，用于生成合法快照。
    let s = structuredClone(snapshot);
    for (const d of deltas) {
      if (d.op === "state.updated") {
        s = { ...s, ...d.patch };
        if (d.patch.workflowRuns !== undefined) s.workflowRuns = d.patch.workflowRuns;
        if (d.patch.revision !== undefined) s.revision = d.patch.revision;
        if (this.workflowRuns !== undefined) s.workflowRuns = this.workflowRuns;
      } else if (d.op === "row.appended") {
        s.rows = {
          ...s.rows,
          window: [...s.rows.window, d.row],
          totalCount: s.rows.window.length + 1,
          firstRowId: s.rows.firstRowId ?? d.row.rowId,
        };
      } else if (d.op === "row.upserted") {
        const win = s.rows.window.map((r: any) => (r.rowId === d.row.rowId ? d.row : r));
        s.rows = { ...s.rows, window: win };
      } else if (d.op === "row.removed") {
        const win = s.rows.window.filter((r: any) => r.rowId < d.fromRowId);
        s.rows = {
          ...s.rows,
          window: win,
          totalCount: win.length,
          firstRowId: win[0]?.rowId ?? null,
        };
      } else if (d.op === "row.delta") {
        // 流式追加：在匹配行上拼文本，路径决定字段。
        const win = s.rows.window.map((r: any) => {
          if (r.rowId !== d.rowId) return r;
          const field =
            d.path === "text"
              ? "text"
              : d.path === "inputText"
                ? "inputText"
                : d.path === "summaryText"
                  ? "summaryText"
                  : null;
          if (field) return { ...r, [field]: (r[field] ?? "") + d.append };
          if (d.path === "output.text")
            return { ...r, output: { ...r.output, text: (r.output?.text ?? "") + d.append } };
          return r;
        });
        s.rows = { ...s.rows, window: win };
      }
    }
    return s;
  }
  // 供完整度用：真实 publisher 构造与 rehydrate 会调这些。
  beginHydrationReplay() {}
  completeHydrationReplay() {
    return [];
  }
  applyHydrationEvent() {
    return [];
  }
}

function initialSnapshot(sessionId: string, logEpoch: string): any {
  return {
    protocolVersion: 1,
    sessionId,
    logEpoch,
    seq: 0,
    revision: 0,
    control: {
      phase: "draft",
      sessionEnded: false,
      canStop: false,
      stopState: "idle",
      stopTargetKind: "unknown",
      activeWorks: [],
      lastError: null,
      apiRetry: null,
    },
    availability: {
      fork: { allowed: false, reasonCode: "forkTargetNotStable" },
      compact: { allowed: false, reasonCode: "idleCannotCompact" },
      switchModelConfig: { allowed: true },
      setFollowupMode: { allowed: true },
      queueEdit: { allowed: true },
      sendQueuedNow: { allowed: false, reasonCode: "sendQueuedNowRequiresRunning" },
      pauseGoal: { allowed: false, reasonCode: "noGoalToPause" },
      resumeGoal: { allowed: false, reasonCode: "noGoalToResume" },
    },
    inputRouting: { mode: "startNow" },
    meta: { title: "t", titleSource: "generated" },
    config: {
      provider: "mock",
      model: "mock-model",
      thought: "none",
      thoughtLevels: ["none"],
      followupMode: "queue",
      mode: "build",
      modelSelection: {
        providerId: "mock",
        modelId: "mock-model",
        options: { reasoningLevel: "none" },
      },
      planEnabled: false,
    },
    modelTransition: null,
    usage: {
      contextWindow: null,
      cumulative: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 },
    },
    queue: { items: [], autoDrain: true },
    pendingInteractions: [],
    pendingCommands: [],
    backgroundWorks: [],
    subagents: { revision: 0, childSessionIds: [], running: [], endedTotal: 0 },
    goal: null,
    plan: null,
    workspaceHookAdmission: null,
    rows: { window: [], totalCount: 0, firstRowId: null },
  };
}

// 替换 publisher 的 projection（TS private 只存在于编译期）。
function withFakeProjection(pub: ConversationTopicPublisher, proj: ScriptedProjection): void {
  (pub as any).projection = proj;
  // 重新测量 wireSnapshotBytesUpperBound（构造时已测量真实快照）。
  (pub as any).wireSnapshotBytesUpperBound = (pub as any).measureWireSnapshotBytes(
    (pub as any).getWireSnapshot(),
  );
}

// ── 一个脚本化的 publisher 运行 ──
interface Step {
  seq: number;
  revision?: number;
  deltas: any[];
}
interface FrameRecord {
  deliveryKind: string;
  logicalFrameId: string;
  logicalFrameOrdinal: number;
  frame: any;
}
interface RunResult {
  acks: any[];
  frames: FrameRecord[];
  flushNulls: boolean[];
}

let nowValue = NORM_TIME;
function runScenario(opts: {
  sessionId: string;
  logEpoch: string;
  workflowRunDeltas: boolean;
  steps: Step[];
  workflowRuns?: any;
  retention?: number;
  subscriberBufferMaxOps?: number;
  subscriberBufferMaxBytes?: number;
  script: Array<
    | { op: "subscribe"; connectionId: string; deliveryProfile?: string; base?: any; mode: string }
    | { op: "flush"; sub: number }
    | { op: "resync"; sub: number; base: any; forceSnapshot?: boolean }
    | { op: "unsubscribe"; sub: number; connectionId?: string }
    | { op: "ingest"; step: number }
    | {
        op: "resubscribe";
        connectionId: string;
        deliveryProfile?: string;
        base?: any;
        mode: string;
      }
  >;
}): RunResult {
  const proj = new ScriptedProjection(opts.sessionId, opts.logEpoch, opts.workflowRuns);
  const pub = new ConversationTopicPublisher(opts.sessionId, opts.logEpoch, {
    now: () => nowValue,
    retention: opts.retention,
    subscriberBufferMaxOps: opts.subscriberBufferMaxOps,
    subscriberBufferMaxBytes: opts.subscriberBufferMaxBytes,
  });
  withFakeProjection(pub, proj);

  const acks: any[] = [];
  const frames: FrameRecord[] = [];
  const flushNulls: boolean[] = [];
  const subIds: string[] = [];

  const emit = (reservation: any) => {
    if (!reservation) return;
    frames.push({
      deliveryKind: reservation.deliveryKind,
      logicalFrameId: normalizeSubId(reservation.logicalFrameId),
      logicalFrameOrdinal: reservation.logicalFrameOrdinal,
      frame: normalizeFrame(reservation.frame, normalizeSubId(reservation.frame.subscriptionId)),
    });
    reservation.commit();
  };
  const emitResult = (result: any, slot = subIds.length) => {
    acks.push({
      ack: {
        subscriptionId: normalizeSubId(result.ack.subscriptionId),
        mode: result.ack.mode,
        logEpoch: result.ack.logEpoch === opts.logEpoch ? EPOCH : result.ack.logEpoch,
      },
    });
    const reservation = (result as any).reservation;
    subIds[slot] = reservation ? (reservation.frame?.subscriptionId ?? "") : "";
    if (reservation) emit(reservation);
  };

  for (const action of opts.script) {
    switch (action.op) {
      case "ingest": {
        const step = opts.steps[action.step];
        proj.advance(step.seq, step.revision);
        proj.nextDeltas = step.deltas;
        pub.ingest({ sequenceNumber: step.seq, type: "synthetic", payload: {} } as any);
        break;
      }
      case "subscribe":
      case "resubscribe": {
        const result = pub.subscribeReserved({
          connectionId: action.connectionId,
          base: action.base,
          deliveryProfile: action.deliveryProfile as any,
          workflowRunDeltas: opts.workflowRunDeltas,
        });
        // 记录本条 subscribe 落入的 sub 槽位，Go 依此索引订阅。
        action.sub = subIds.length;
        emitResult(result);
        break;
      }
      case "flush": {
        const subId = subIds[action.sub];
        if (!subId) {
          flushNulls.push(true);
          break;
        }
        const reservation = pub.reserveFlush(subId);
        if (!reservation) {
          flushNulls.push(true);
          break;
        }
        flushNulls.push(false);
        frames.push({
          deliveryKind: reservation.deliveryKind,
          logicalFrameId: normalizeSubId(reservation.logicalFrameId),
          logicalFrameOrdinal: reservation.logicalFrameOrdinal,
          frame: normalizeFrame(
            reservation.frame,
            normalizeSubId(reservation.frame.subscriptionId),
          ),
        });
        reservation.commit();
        break;
      }
      case "resync": {
        const subId = subIds[action.sub];
        const result = pub.resyncReserved(subId, {
          base: action.base,
          forceSnapshot: action.forceSnapshot,
        });
        if (result) emitResult(result, action.sub);
        break;
      }
      case "unsubscribe": {
        const subId = subIds[action.sub];
        pub.unsubscribe(subId, action.connectionId);
        break;
      }
    }
  }
  return { acks, frames, flushNulls };
}

// ── coalesce 向量：随机 delta 序列 + 真实 coalesce() ──
function makeRng(seed: number): () => number {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 0x1_0000_0000;
  };
}

function randomDelta(rng: () => number, pick: (n: number) => number): any {
  const ops = [
    "row.appended",
    "row.upserted",
    "row.removed",
    "row.delta",
    "state.updated",
    "workflowRun.updated",
    "workflowRun.removed",
  ];
  const op = ops[pick(ops.length)];
  switch (op) {
    case "row.appended":
    case "row.upserted":
      return { op, row: { rowId: pick(4) + 1, kind: "assistantText", text: `t${pick(9)}` } };
    case "row.removed":
      return { op, fromRowId: pick(4) + 1 };
    case "row.delta":
      return {
        op,
        rowId: pick(4) + 1,
        path: ["text", "inputText", "output.text", "summaryText"][pick(4)],
        append: `a${pick(9)}`,
      };
    case "state.updated": {
      const patch: any = { revision: pick(5) };
      if (pick(2) === 0)
        patch.control = {
          phase: "draft",
          sessionEnded: false,
          canStop: false,
          stopState: "idle",
          stopTargetKind: "unknown",
          activeWorks: [],
          lastError: null,
          apiRetry: null,
        };
      if (pick(3) === 0)
        patch.workflowRuns = {
          revision: pick(5),
          runs: [
            {
              runId: `r${pick(3)}`,
              status: "running",
              usage: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 },
              actors: [],
              nodes: [],
              lastEventSequence: 0,
            },
          ],
        };
      return { op, patch };
    }
    case "workflowRun.updated": {
      const runId = `r${pick(3)}`;
      const d: any = { op, runId, revision: pick(5) };
      if (pick(2) === 0)
        d.run = {
          runId,
          status: "running",
          usage: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 },
          lastEventSequence: pick(9),
        };
      if (pick(3) === 0)
        d.actors = [
          {
            siteId: `s${pick(3)}`,
            ordinal: pick(4),
            status: pick(2) === 0 ? "completed" : "running",
          },
        ];
      if (pick(4) === 0) d.removedActors = [{ siteId: `s${pick(3)}`, ordinal: pick(4) }];
      if (pick(4) === 0) d.cleared = ["resultPreview"];
      return d;
    }
    case "workflowRun.removed":
      return { op, runId: `r${pick(3)}`, revision: pick(5) };
  }
}

// ── 主流程 ──
const vectors: any = {
  limits: {
    subscriberBufferMaxOps: PROTOCOL_V4_LIMITS.subscriberBufferMaxOps,
    subscriberBufferMaxBytes: PROTOCOL_V4_LIMITS.subscriberBufferMaxBytes,
    eventRetentionPerSession: PROTOCOL_V4_LIMITS.eventRetentionPerSession,
    snapshotTailWindowRows: PROTOCOL_V4_LIMITS.snapshotTailWindowRows,
  },
  deliveryProfiles: {
    continuous: { flushWindowMs: DELIVERY_PROFILES.continuous.flushWindowMs },
    replayable: { flushWindowMs: DELIVERY_PROFILES.replayable.flushWindowMs },
  },
  coalesceCases: [] as any[],
  filterCases: [] as any[],
  legacyEncodeCases: [] as any[],
  clampCases: [] as any[],
  scenarios: [] as any[],
};

// coalesce 向量
let rng = makeRng(0x5eed);
for (let i = 0; i < 60; i++) {
  const n = 1 + Math.floor(rng() * 14);
  const deltas: any[] = [];
  for (let j = 0; j < n; j++) deltas.push(randomDelta(rng, (m) => Math.floor(rng() * m)));
  const result = coalesceConversationDeltas(deltas as any);
  vectors.coalesceCases.push({ name: `c${i}`, input: deltas, output: result });
}
// 规则针对性用例：相邻拼接、state 合并、upserted 吞 row.delta、屏障、run 合并。
vectors.coalesceCases.push(
  {
    name: "adjacent-row-delta",
    input: [
      { op: "row.delta", rowId: 1, path: "text", append: "a" },
      { op: "row.delta", rowId: 1, path: "text", append: "b" },
      { op: "row.delta", rowId: 2, path: "text", append: "c" },
    ],
    output: coalesceConversationDeltas([
      { op: "row.delta", rowId: 1, path: "text", append: "a" },
      { op: "row.delta", rowId: 1, path: "text", append: "b" },
      { op: "row.delta", rowId: 2, path: "text", append: "c" },
    ] as any),
  },
  {
    name: "upserted-swallows-delta",
    input: [
      { op: "row.delta", rowId: 1, path: "text", append: "a" },
      { op: "row.upserted", row: { rowId: 1, kind: "assistantText", text: "z" } },
    ],
    output: coalesceConversationDeltas([
      { op: "row.delta", rowId: 1, path: "text", append: "a" },
      { op: "row.upserted", row: { rowId: 1, kind: "assistantText", text: "z" } },
    ] as any),
  },
  {
    name: "barrier-blocks-swallow",
    input: [
      { op: "row.delta", rowId: 1, path: "text", append: "a" },
      { op: "row.removed", fromRowId: 1 },
      { op: "row.upserted", row: { rowId: 1, kind: "assistantText", text: "z" } },
    ],
    output: coalesceConversationDeltas([
      { op: "row.delta", rowId: 1, path: "text", append: "a" },
      { op: "row.removed", fromRowId: 1 },
      { op: "row.upserted", row: { rowId: 1, kind: "assistantText", text: "z" } },
    ] as any),
  },
  {
    name: "rule3-stops-at-prior-upserted",
    input: [
      { op: "row.delta", rowId: 1, path: "text", append: "a" },
      { op: "row.upserted", row: { rowId: 1, kind: "assistantText", text: "mid" } },
      { op: "row.delta", rowId: 1, path: "text", append: "b" },
      { op: "row.upserted", row: { rowId: 1, kind: "assistantText", text: "final" } },
    ],
    output: coalesceConversationDeltas([
      { op: "row.delta", rowId: 1, path: "text", append: "a" },
      { op: "row.upserted", row: { rowId: 1, kind: "assistantText", text: "mid" } },
      { op: "row.delta", rowId: 1, path: "text", append: "b" },
      { op: "row.upserted", row: { rowId: 1, kind: "assistantText", text: "final" } },
    ] as any),
  },
  {
    name: "run-merge-into-latest",
    input: [
      {
        op: "workflowRun.updated",
        runId: "r1",
        revision: 1,
        run: {
          runId: "r1",
          status: "running",
          usage: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 },
          lastEventSequence: 1,
        },
      },
      { op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "x" } },
      {
        op: "workflowRun.updated",
        runId: "r1",
        revision: 2,
        actors: [{ siteId: "s1", ordinal: 0, status: "running" }],
      },
    ],
    output: coalesceConversationDeltas([
      {
        op: "workflowRun.updated",
        runId: "r1",
        revision: 1,
        run: {
          runId: "r1",
          status: "running",
          usage: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 },
          lastEventSequence: 1,
        },
      },
      { op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "x" } },
      {
        op: "workflowRun.updated",
        runId: "r1",
        revision: 2,
        actors: [{ siteId: "s1", ordinal: 0, status: "running" }],
      },
    ] as any),
  },
);

// filter 向量
for (const profileName of ["continuous", "replayable"] as const) {
  const deltas = [
    { op: "row.delta", rowId: 1, path: "text", append: "a" },
    { op: "row.delta", rowId: 1, path: "inputText", append: "b" },
    { op: "row.delta", rowId: 1, path: "output.text", append: "c" },
    { op: "row.delta", rowId: 1, path: "summaryText", append: "d" },
    { op: "state.updated", patch: { revision: 1 } },
  ];
  vectors.filterCases.push({
    profile: profileName,
    input: deltas,
    output: filterConversationDeltasForProfile(deltas as any, DELIVERY_PROFILES[profileName]),
  });
}

// legacy encode 向量
vectors.legacyEncodeCases.push({
  name: "folds-last",
  input: [
    { op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "x" } },
    {
      op: "workflowRun.updated",
      runId: "r1",
      revision: 1,
      run: {
        runId: "r1",
        status: "running",
        usage: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 },
        lastEventSequence: 1,
      },
    },
    { op: "state.updated", patch: { revision: 2 } },
    { op: "workflowRun.removed", runId: "r2", revision: 3 },
  ],
  workflowRuns: {
    revision: 9,
    runs: [
      {
        runId: "r1",
        status: "completed",
        usage: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 },
        actors: [],
        nodes: [],
        lastEventSequence: 1,
      },
    ],
  },
});
// 无 workflowRun.* 增量：原样返回（走 lastIndex<0 分支）。
vectors.legacyEncodeCases.push({
  name: "passthrough-no-run-delta",
  input: [
    { op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "x" } },
    { op: "state.updated", patch: { revision: 2 } },
  ],
  workflowRuns: { revision: 9, runs: [] },
});
// 用真实函数填充 output
for (const c of vectors.legacyEncodeCases) {
  c.output = encodeConversationDeltasForLegacy(c.input as any, c.workflowRuns as any);
}

// clamp 向量：actors 超 1024，含 live/completed。
const bigActors = Array.from({ length: 1200 }, (_, i) => ({
  siteId: `s${i}`,
  ordinal: i,
  status: i % 3 === 0 ? "completed" : "running",
}));
vectors.clampCases.push({
  name: "clamps-actors",
  input: {
    revision: 3,
    runs: [
      {
        runId: "r1",
        status: "running",
        usage: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 },
        actors: bigActors,
        nodes: [],
        lastEventSequence: 1,
      },
    ],
  },
});
for (const c of vectors.clampCases) {
  c.output = clampWorkflowRunsForLegacy(c.input as any);
}

// 场景向量
const scenarios: any[] = [];
scenarios.push({
  name: "snapshot-then-deltas",
  script: [
    { op: "subscribe", connectionId: "c1", deliveryProfile: "continuous", mode: "snapshot" },
    { op: "ingest", step: 0 },
    { op: "flush", sub: 0 },
  ],
  steps: [
    {
      seq: 1,
      revision: 1,
      deltas: [{ op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "hi" } }],
    },
  ],
});
scenarios.push({
  name: "two-subscribers-profiles",
  script: [
    { op: "subscribe", connectionId: "a", deliveryProfile: "continuous", mode: "snapshot" },
    { op: "subscribe", connectionId: "b", deliveryProfile: "replayable", mode: "snapshot" },
    { op: "ingest", step: 0 },
    { op: "flush", sub: 0 },
    { op: "flush", sub: 1 },
  ],
  steps: [
    {
      seq: 1,
      revision: 1,
      deltas: [
        { op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "" } },
        { op: "row.delta", rowId: 1, path: "text", append: "hello" },
        { op: "row.delta", rowId: 1, path: "inputText", append: "tool" },
      ],
    },
  ],
});
scenarios.push({
  name: "resume-from-base",
  script: [
    { op: "ingest", step: 0 },
    { op: "ingest", step: 1 },
    {
      op: "subscribe",
      connectionId: "a",
      deliveryProfile: "continuous",
      base: { logEpoch: "__EPOCH__", seq: 1 },
      mode: "resume",
    },
  ],
  steps: [
    {
      seq: 1,
      revision: 1,
      deltas: [{ op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "a" } }],
    },
    {
      seq: 2,
      revision: 2,
      deltas: [{ op: "row.appended", row: { rowId: 2, kind: "assistantText", text: "b" } }],
    },
  ],
});
scenarios.push({
  name: "resume-base-equals-current",
  script: [
    { op: "ingest", step: 0 },
    {
      op: "subscribe",
      connectionId: "a",
      deliveryProfile: "continuous",
      base: { logEpoch: "__EPOCH__", seq: 1 },
      mode: "resume",
    },
  ],
  steps: [
    {
      seq: 1,
      revision: 1,
      deltas: [{ op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "a" } }],
    },
  ],
});
scenarios.push({
  name: "resume-base-too-old",
  script: [
    { op: "ingest", step: 0 },
    { op: "ingest", step: 1 },
    { op: "ingest", step: 2 },
    {
      op: "subscribe",
      connectionId: "a",
      deliveryProfile: "continuous",
      base: { logEpoch: "__EPOCH__", seq: 0 },
      mode: "snapshot",
    },
  ],
  retention: 2,
  steps: [
    {
      seq: 1,
      revision: 1,
      deltas: [{ op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "a" } }],
    },
    {
      seq: 2,
      revision: 2,
      deltas: [{ op: "row.appended", row: { rowId: 2, kind: "assistantText", text: "b" } }],
    },
    {
      seq: 3,
      revision: 3,
      deltas: [{ op: "row.appended", row: { rowId: 3, kind: "assistantText", text: "c" } }],
    },
  ],
});
scenarios.push({
  name: "resume-wrong-epoch",
  script: [
    { op: "ingest", step: 0 },
    {
      op: "subscribe",
      connectionId: "a",
      deliveryProfile: "continuous",
      base: { logEpoch: "other", seq: 1 },
      mode: "snapshot",
    },
  ],
  steps: [
    {
      seq: 1,
      revision: 1,
      deltas: [{ op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "a" } }],
    },
  ],
});
scenarios.push({
  name: "overflow-byte-budget",
  script: [
    { op: "subscribe", connectionId: "a", deliveryProfile: "continuous", mode: "snapshot" },
    { op: "ingest", step: 0 },
    { op: "flush", sub: 0 },
  ],
  subscriberBufferMaxBytes: 40,
  steps: [
    {
      seq: 1,
      revision: 1,
      deltas: [
        {
          op: "row.appended",
          row: {
            rowId: 1,
            kind: "assistantText",
            text: "this payload exceeds the tiny byte budget",
          },
        },
      ],
    },
  ],
});
scenarios.push({
  name: "html-characters-in-text",
  script: [
    { op: "subscribe", connectionId: "a", deliveryProfile: "continuous", mode: "snapshot" },
    { op: "ingest", step: 0 },
    { op: "flush", sub: 0 },
  ],
  steps: [
    {
      seq: 1,
      revision: 1,
      deltas: [
        {
          op: "row.appended",
          row: {
            rowId: 1,
            kind: "assistantText",
            text: "<b>&amp;</b> \u2028 \u2029 😀 \\ \" ' \b\t",
          },
        },
        // 相邻同 (rowId, path) 的两条 row.delta 会被 coalesce 拼接并重新序列化，
        // 于是 append 里的反斜杠/控制字符必须由 Go 的 JSON 编码器逐字节复现。
        { op: "row.delta", rowId: 1, path: "text", append: "<i>a</i>\n\\p" },
        { op: "row.delta", rowId: 1, path: "text", append: "\t\\q\b" },
        { op: "row.delta", rowId: 1, path: "text", append: "\u0001\u001f" },
      ],
    },
  ],
});
scenarios.push({
  name: "resume-base-future",
  script: [
    { op: "ingest", step: 0 },
    { op: "ingest", step: 1 },
    {
      op: "subscribe",
      connectionId: "a",
      deliveryProfile: "continuous",
      base: { logEpoch: "__EPOCH__", seq: 99 },
      mode: "snapshot",
    },
  ],
  steps: [
    {
      seq: 1,
      revision: 1,
      deltas: [{ op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "a" } }],
    },
    {
      seq: 2,
      revision: 2,
      deltas: [{ op: "row.appended", row: { rowId: 2, kind: "assistantText", text: "b" } }],
    },
  ],
});
// base == currentSeq + 1（相邻合法上界）：必须 snapshot。
scenarios.push({
  name: "resume-base-one-past-current",
  script: [
    { op: "ingest", step: 0 },
    { op: "ingest", step: 1 },
    {
      op: "subscribe",
      connectionId: "a",
      deliveryProfile: "continuous",
      base: { logEpoch: "__EPOCH__", seq: 3 },
      mode: "snapshot",
    },
  ],
  steps: [
    {
      seq: 1,
      revision: 1,
      deltas: [{ op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "a" } }],
    },
    {
      seq: 2,
      revision: 2,
      deltas: [{ op: "row.appended", row: { rowId: 2, kind: "assistantText", text: "b" } }],
    },
  ],
});
// 数值格式化：小数字段（非整数）必须走 JS Number::toString。
scenarios.push({
  name: "number-formatting",
  script: [
    { op: "subscribe", connectionId: "a", deliveryProfile: "continuous", mode: "snapshot" },
    { op: "ingest", step: 0 },
    { op: "flush", sub: 0 },
  ],
  steps: [
    {
      seq: 1,
      revision: 1,
      deltas: [
        {
          op: "state.updated",
          patch: {
            revision: 7,
            control: {
              phase: "draft",
              sessionEnded: false,
              canStop: false,
              stopState: "idle",
              stopTargetKind: "unknown",
              activeWorks: [],
              lastError: null,
              apiRetry: null,
              waitMs: 0.1,
              ratio: 1234.5,
            },
          },
        },
      ],
    },
  ],
});
scenarios.push({
  name: "overflow-resync-required",
  script: [
    { op: "subscribe", connectionId: "a", deliveryProfile: "continuous", mode: "snapshot" },
    { op: "ingest", step: 0 },
    { op: "ingest", step: 1 },
    { op: "flush", sub: 0 },
  ],
  subscriberBufferMaxOps: 2,
  steps: [
    {
      seq: 1,
      revision: 1,
      deltas: [
        { op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "a" } },
        { op: "row.appended", row: { rowId: 2, kind: "assistantText", text: "b" } },
        { op: "row.appended", row: { rowId: 3, kind: "assistantText", text: "c" } },
      ],
    },
    {
      seq: 2,
      revision: 2,
      deltas: [{ op: "row.appended", row: { rowId: 4, kind: "assistantText", text: "d" } }],
    },
  ],
});
scenarios.push({
  name: "resubscribe-replaces",
  script: [
    { op: "subscribe", connectionId: "a", deliveryProfile: "continuous", mode: "snapshot" },
    { op: "ingest", step: 0 },
    { op: "resubscribe", connectionId: "a", deliveryProfile: "continuous", mode: "snapshot" },
    { op: "ingest", step: 1 },
    // 旧订阅（槽 0）已被替换：其后再 flush 必须得不到帧。
    { op: "flush", sub: 0 },
    { op: "flush", sub: 1 },
  ],
  steps: [
    {
      seq: 1,
      revision: 1,
      deltas: [{ op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "a" } }],
    },
    {
      seq: 2,
      revision: 2,
      deltas: [{ op: "row.appended", row: { rowId: 2, kind: "assistantText", text: "b" } }],
    },
  ],
});
scenarios.push({
  name: "resync-recovery-snapshot",
  script: [
    { op: "subscribe", connectionId: "a", deliveryProfile: "continuous", mode: "snapshot" },
    { op: "ingest", step: 0 },
    { op: "resync", sub: 0, base: null, forceSnapshot: true },
  ],
  steps: [
    {
      seq: 1,
      revision: 1,
      deltas: [{ op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "a" } }],
    },
  ],
});
scenarios.push({
  name: "resync-resume-from-base",
  script: [
    { op: "ingest", step: 0 },
    { op: "ingest", step: 1 },
    { op: "subscribe", connectionId: "a", deliveryProfile: "continuous", mode: "snapshot" },
    { op: "resync", sub: 0, base: { logEpoch: "__EPOCH__", seq: 1 } },
  ],
  steps: [
    {
      seq: 1,
      revision: 1,
      deltas: [{ op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "a" } }],
    },
    {
      seq: 2,
      revision: 2,
      deltas: [{ op: "row.appended", row: { rowId: 2, kind: "assistantText", text: "b" } }],
    },
  ],
});
scenarios.push({
  name: "unsubscribe",
  script: [
    { op: "subscribe", connectionId: "a", deliveryProfile: "continuous", mode: "snapshot" },
    { op: "ingest", step: 0 },
    { op: "unsubscribe", sub: 0 },
    { op: "ingest", step: 1 },
    { op: "flush", sub: 0 },
  ],
  steps: [
    {
      seq: 1,
      revision: 1,
      deltas: [{ op: "row.appended", row: { rowId: 1, kind: "assistantText", text: "a" } }],
    },
    {
      seq: 2,
      revision: 2,
      deltas: [{ op: "row.appended", row: { rowId: 2, kind: "assistantText", text: "b" } }],
    },
  ],
});

for (const s of scenarios) {
  const rawSteps = s.steps.map((st: any) => ({
    seq: st.seq,
    revision: st.revision,
    deltas: st.deltas,
  }));
  // 替换 __EPOCH__ 占位符
  const script = s.script.map((a: any) => {
    if (a.base && a.base.logEpoch === "__EPOCH__")
      return { ...a, base: { logEpoch: EPOCH, seq: a.base.seq } };
    return a;
  });
  const result = runScenario({
    sessionId: "sess-vector",
    logEpoch: EPOCH,
    workflowRunDeltas: true,
    steps: rawSteps,
    workflowRuns: undefined,
    retention: s.retention,
    subscriberBufferMaxOps: s.subscriberBufferMaxOps,
    subscriberBufferMaxBytes: s.subscriberBufferMaxBytes,
    script,
  });
  vectors.scenarios.push({
    name: s.name,
    ackCount: result.acks.length,
    acks: result.acks,
    frames: result.frames,
    flushNulls: result.flushNulls,
  });
  // Go 驱动脚本：动作 + 步骤原样导出（base 里的 epoch 已替换）。
  writeFileSync(
    new URL(`../../go/testdata/vectors/publisher-scripts/${s.name}.json`, import.meta.url),
    JSON.stringify(
      {
        name: s.name,
        logEpoch: EPOCH,
        workflowRunDeltas: true,
        sessionId: "sess-vector",
        retention: s.retention,
        subscriberBufferMaxOps: s.subscriberBufferMaxOps,
        subscriberBufferMaxBytes: s.subscriberBufferMaxBytes,
        steps: s.steps,
        actions: script,
      },
      null,
      1,
    ),
  );
}

writeFileSync(OUT, JSON.stringify(vectors, null, 1));
console.log(`wrote ${OUT}`);
console.log(`coalesceCases=${vectors.coalesceCases.length} scenarios=${vectors.scenarios.length}`);

// 额外导出给 Go 的 S02 形状向量：publisher 空会话 snapshot 帧的 ACK 与帧。
export const EPOCH_PLACEHOLDER = EPOCH;
