// 规范化：把一次录制里"每次运行必然不同"的值替换成按首次出现顺序编号的占位符，
// 让两次录制可以逐字节比较。刻意不动 seq / logEpoch 之间的关系、事件顺序、文本内容——
// 那些是语义，不是噪声。
//
// 规则来自对同一场景录两次后的字段级 diff（见 record-agent-session.test.mjs 里的断言），不是猜的。
const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/g;
// 带前缀的 id：sess_<uuid>、turn_<uuid>、msg_<base36>_<uuid>、sub-<base36>-<base36>-N、logEpoch <base36>-<base36>
const TIME_BASE36 = /\b(?:mv|mw|mx|my|mz|n[a-z0-9])[a-z0-9]{5,8}\b/g; // Date.now().toString(36) 的前缀段（2026 年）
const HEX64 = /\b[0-9a-f]{64}\b/g;
const EPOCH_MS = (n) => Number.isInteger(n) && n >= 1.6e12 && n < 1.9e12;

// 这些键的数值是运行耗时/计时器读数，每次不同，统一抹成 0。
const TIMING_KEYS = new Set([
  "elapsedMs",
  "durationMs",
  "activeMs",
  "initialFrameEncodeMs",
  "hostPrepareMs",
  "cliRequestMs",
  "taskMetaReadMs",
  "cliBootstrapMs",
  "ttftMs",
  "latencyMs",
  "snapshotEncodeMs",
  "totalMs",
  // tool.lifecycle.performance 里的测量计时器；同一对象里的 fileCount/totalBytes/hunkCount 是行为，不动。
  "permissionWaitMs",
  "fsReadMs",
  "fsWriteMs",
  "patchMatchMs",
]);

// 并发通道：这些帧由与主对话流并发运行的后台任务产生，它们相对主序列的位置在 Node agent 自身就不稳定。
// 证据：S03 重复录制 5 次出现 1 次 `state.updated` 与 session_title 的 model.request.status 互换相邻位置，
// 帧的集合完全相同，只有顺序不同。因此它们不属于"顺序契约"：规范化时抽出来，作为自己的有序子序列附在末尾，
// 主序列保持确定。新增一条并发通道必须有同样级别的证据（见 record-agent-session.test.mjs 的稳定性测试）。
function laneOf(frame) {
  const p = frame.params;
  if (frame.method === "v4/telemetry/event" && p && p.querySource === "session_title")
    return "title";
  return null;
}

export function normalizeFrames(frames, { WS, HOME } = {}) {
  const seen = new Map();
  const placeholder = (kind, value) => {
    const key = `${kind}:${value}`;
    if (!seen.has(key))
      seen.set(
        key,
        `<${kind}${[...seen.keys()].filter((k) => k.startsWith(kind + ":")).length + 1}>`,
      );
    return seen.get(key);
  };
  const scrubString = (s, keyHint) => {
    let out = s;
    if (WS) out = out.split(WS).join("<WS>");
    if (HOME) out = out.split(HOME).join("<HOME>");
    out = out.replace(/\/tmp\/nex-rec-[A-Za-z0-9]+-[A-Za-z0-9]{6}/g, "<TMP>");
    out = out.replace(HEX64, (m) => placeholder("hash", m));
    // 先整体处理带前缀的 id，保证 "sess_<uuid>" 整体映射为同一个占位符。
    out = out.replace(/\b(sess|turn|msg|call)_[A-Za-z0-9_-]+/g, (m) => placeholder("id", m));
    out = out.replace(UUID, (m) => placeholder("uuid", m));
    out = out.replace(
      /\bsub-[a-z0-9]+-[a-z0-9]+-(\d+)/g,
      (m, n) => `${placeholder("sub", m.replace(/-\d+$/, ""))}-${n}`,
    );
    if (keyHint === "logEpoch" || /^[a-z0-9]{6,9}-[a-z0-9]{6,9}$/.test(out))
      out = out.replace(/^[a-z0-9]{6,9}-[a-z0-9]{6,9}$/, (m) => placeholder("epoch", m));
    out = out.replace(/-lf-(\d+)$/, "-lf-$1");
    return out;
  };
  const walk = (value, key) => {
    if (typeof value === "string") return scrubString(value, key);
    if (typeof value === "number") {
      if (TIMING_KEYS.has(key)) return 0;
      if (EPOCH_MS(value)) return "<time>";
      return value;
    }
    if (Array.isArray(value)) return value.map((v) => walk(v, key));
    if (value && typeof value === "object")
      return Object.fromEntries(
        Object.entries(value).map(([k, v]) => [scrubString(k, undefined), walk(v, k)]),
      );
    return value;
  };
  const main = [],
    lanes = new Map(),
    convSnapshots = [],
    convDeltas = [],
    lastSeq = new Map();
  for (const frame of frames) {
    const payload =
      frame.method === "v4/conversation/frame" ? frame.params?.frame?.payload : undefined;
    if (payload?.kind === "deltas") {
      // 分帧边界是计时噪声，但"帧首尾相接"是协议不变量：每个 delta frame 的 fromSeq 必须等于同订阅上一帧的 toSeq。
      // 破坏它就是丢/重 delta，所以这里直接抛错而不是抹平。
      const f = frame.params.frame;
      const prev = lastSeq.get(f.subscriptionId);
      if (prev !== undefined && f.fromSeq !== prev)
        throw new Error(
          `conversation frames are not contiguous: fromSeq ${f.fromSeq} after toSeq ${prev}`,
        );
      lastSeq.set(f.subscriptionId, f.toSeq);
      convDeltas.push(...payload.deltas);
      continue;
    }
    if (payload?.kind === "snapshot") {
      lastSeq.set(frame.params.frame.subscriptionId, frame.params.frame.toSeq);
      convSnapshots.push(frame);
      continue;
    }
    const lane = laneOf(frame);
    if (lane === null) main.push(frame);
    else (lanes.get(lane) ?? lanes.set(lane, []).get(lane)).push(frame);
  }
  // 会话 delta 流：agent 用定时器（flushWindowMs=30ms）把 delta 攒成 frame，分帧边界取决于时序（见 core.ts）。
  // 因此比较的是"摊平后的 delta 序列"，而不是 frame 的切法；frame 之间的 fromSeq/toSeq/ordinal/sentAt 也不比较
  //（wire 层的切片与序号由 Go 的 wire 向量测试单独覆盖）。delta 之间的相对顺序仍然严格比较。
  const finalSeq = [...lastSeq.values()];
  const deltaStream = convDeltas.length
    ? [{ method: "v4/conversation/deltas(flattened)", finalToSeq: finalSeq, deltas: convDeltas }]
    : [];
  // 占位符按"首次出现顺序"编号，所以必须在固定顺序（先主序列后各通道）上规范化，编号才稳定。
  const ordered = [
    ...main,
    ...convSnapshots,
    ...deltaStream,
    ...[...lanes.keys()].sort().flatMap((k) => lanes.get(k)),
  ];
  return ordered.map((f) => walk(f, undefined));
}
