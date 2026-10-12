// 录制器的确定性与规范化强度测试。需要先构建 Node agent：
//   pnpm --filter @nex/cli build   （产物 apps/nex-cli/packages/cli/dist/nex.cjs）
import test from "node:test";
import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { recordScenario } from "./record-agent-session.mjs";
import { normalizeFrames } from "./normalize.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const AGENT = resolve(HERE, "../../../apps/nex-cli/packages/cli/dist/nex.cjs");
const haveAgent = existsSync(AGENT);
const scenario = (name) => join(HERE, "scenarios", name);
const out = () => mkdtempSync(join(tmpdir(), "rec-test-"));
const frames = (dir) => JSON.parse(readFileSync(join(dir, "out.normalized.json"), "utf8"));

test("normalizer replaces ids/times consistently and leaves semantics alone", () => {
  const raw = [
    {
      method: "x",
      params: {
        sessionId: "sess_aaaa-1",
        turnId: "turn_bbbb",
        at: 1791547201364,
        elapsedMs: 77,
        text: "keep me",
        seq: 5,
      },
    },
    { method: "y", params: { sessionId: "sess_aaaa-1", other: "sess_cccc" } },
  ];
  const n = normalizeFrames(raw, { WS: "/w", HOME: "/h" });
  assert.equal(n[0].params.sessionId, n[1].params.sessionId, "same id -> same placeholder");
  assert.notEqual(
    n[1].params.sessionId,
    n[1].params.other,
    "different ids -> different placeholders",
  );
  assert.equal(n[0].params.at, "<time>");
  assert.equal(n[0].params.elapsedMs, 0);
  assert.equal(n[0].params.text, "keep me");
  assert.equal(n[0].params.seq, 5, "seq is semantic and must survive");
});

test("normalizer does not hide semantic changes", () => {
  const base = [
    { method: "m", params: { kind: "assistantText", text: "hello", seq: 3, sessionId: "sess_1" } },
  ];
  const variants = [
    [
      {
        method: "m",
        params: { kind: "assistantText", text: "hellp", seq: 3, sessionId: "sess_1" },
      },
    ], // 文本
    [
      {
        method: "m",
        params: { kind: "assistantText", text: "hello", seq: 4, sessionId: "sess_1" },
      },
    ], // seq
    [
      {
        method: "m2",
        params: { kind: "assistantText", text: "hello", seq: 3, sessionId: "sess_1" },
      },
    ], // 方法名
    [
      {
        method: "m",
        params: { kind: "assistantText", text: "hello", seq: 3, sessionId: "sess_1", extra: 1 },
      },
    ], // 多一个字段
  ];
  const ref = JSON.stringify(normalizeFrames(base));
  for (const v of variants) assert.notEqual(JSON.stringify(normalizeFrames(v)), ref);
});

test("normalizer keeps the order of the main sequence", () => {
  const seq = ["a", "b", "c"].map((m) => ({ method: m, params: {} }));
  assert.deepEqual(
    normalizeFrames(seq).map((f) => f.method),
    ["a", "b", "c"],
  );
  assert.deepEqual(
    normalizeFrames([...seq].reverse()).map((f) => f.method),
    ["c", "b", "a"],
  );
});

test("session_title lane is order-insensitive relative to the main sequence but ordered within itself", () => {
  const t = (n) => ({ method: "v4/telemetry/event", params: { querySource: "session_title", n } });
  const m = { method: "state.updated", params: {} };
  assert.deepEqual(normalizeFrames([m, t(1), t(2)]), normalizeFrames([t(1), m, t(2)]));
  assert.notDeepEqual(normalizeFrames([m, t(1), t(2)]), normalizeFrames([m, t(2), t(1)]));
});

for (const name of ["S01-handshake.json", "S02-chat.json", "S03-tool-read.json"]) {
  test(
    `${name}: three recordings normalize to identical bytes`,
    { skip: !haveAgent && "build @nex/cli first", timeout: 120000 },
    async () => {
      const dirs = [out(), out(), out()];
      for (const d of dirs) await recordScenario(scenario(name), d, { agent: AGENT, quiet: true });
      const [a, b, c] = dirs.map((d) => JSON.stringify(frames(d)));
      assert.equal(a, b);
      assert.equal(b, c);
    },
  );
}

test(
  "S02 recording has the expected shape (guards against recording a failed run)",
  { skip: !haveAgent && "build @nex/cli first", timeout: 60000 },
  async () => {
    const d = out();
    const meta = await recordScenario(scenario("S02-chat.json"), d, { agent: AGENT, quiet: true });
    assert.equal(meta.exitCode, 0);
    assert.equal(meta.modelRequests, 1);
    const f = frames(d);
    const terminal = f.find(
      (x) => x.method === "v4/telemetry/event" && x.params.kind === "turn.terminal",
    );
    assert.equal(terminal.params.status, "success");
    const text = JSON.stringify(f.filter((x) => x.method?.startsWith("v4/conversation/")));
    assert.match(text, /Hello from the mock model\./);
    const ready = f.findIndex(
      (x) => x.method === "startup/storageState" && x.params.phase === "ready",
    );
    assert.ok(ready >= 0 && ready < f.findIndex((x) => x.method === "v4/conversation/frame"));
    assert.ok(
      f.some((x) => x.method === "v4/conversation/deltas(flattened)"),
      "flattened delta stream must be present",
    );
  },
);

test(
  "S03 recording shows a real tool round trip",
  { skip: !haveAgent && "build @nex/cli first", timeout: 60000 },
  async () => {
    const d = out();
    const meta = await recordScenario(scenario("S03-tool-read.json"), d, {
      agent: AGENT,
      quiet: true,
    });
    assert.ok(meta.modelRequests >= 2, "model must be called again after the tool result");
    const text = JSON.stringify(frames(d).filter((x) => x.method?.startsWith("v4/conversation/")));
    assert.match(text, /"toolName":"Read"/);
    assert.match(text, /hi there/);
  },
);

test("timing counters are zeroed but behavioural counters next to them survive", () => {
  const raw = [
    {
      method: "v4/telemetry/event",
      params: {
        kind: "tool.lifecycle",
        performance: {
          totalMs: 3,
          permissionWaitMs: 2,
          fsReadMs: 1,
          fsWriteMs: 4,
          patchMatchMs: 1,
          fileCount: 1,
          totalBytes: 17,
          hunkCount: 1,
          matchAttempts: 1,
        },
      },
    },
  ];
  const p = normalizeFrames(raw)[0].params.performance;
  assert.deepEqual(
    [p.totalMs, p.permissionWaitMs, p.fsReadMs, p.fsWriteMs, p.patchMatchMs],
    [0, 0, 0, 0, 0],
  );
  assert.deepEqual([p.fileCount, p.totalBytes, p.hunkCount, p.matchAttempts], [1, 17, 1, 1]);
});

test("a configured retry delay is behaviour, not timing", () => {
  const n = normalizeFrames([{ params: { delayMs: 1500 } }]);
  assert.equal(n[0].params.delayMs, 1500);
});

const deltaFrame = (from, to, deltas, sub = "s1") => ({
  method: "v4/conversation/frame",
  params: {
    logicalFrameOrdinal: from,
    frame: { subscriptionId: sub, fromSeq: from, toSeq: to, payload: { kind: "deltas", deltas } },
  },
});

test("conversation deltas: how they were batched into frames does not matter", () => {
  const d = (n) => ({ op: "row.appended", row: { rowId: n } });
  const oneFrame = [deltaFrame(2, 6, [d(1), d(2), d(3)])];
  const twoFrames = [deltaFrame(2, 4, [d(1)]), deltaFrame(4, 6, [d(2), d(3)])];
  assert.deepEqual(normalizeFrames(oneFrame), normalizeFrames(twoFrames));
});

test("conversation deltas: order, content and the final sequence still matter", () => {
  const d = (n) => ({ op: "row.appended", row: { rowId: n } });
  const ref = JSON.stringify(normalizeFrames([deltaFrame(2, 6, [d(1), d(2)])]));
  assert.notEqual(
    JSON.stringify(normalizeFrames([deltaFrame(2, 6, [d(2), d(1)])])),
    ref,
    "delta order",
  );
  assert.notEqual(
    JSON.stringify(normalizeFrames([deltaFrame(2, 6, [d(1), d(3)])])),
    ref,
    "delta content",
  );
  assert.notEqual(
    JSON.stringify(normalizeFrames([deltaFrame(2, 7, [d(1), d(2)])])),
    ref,
    "final toSeq",
  );
});

test("conversation deltas: a gap or overlap between frames is an error, not noise", () => {
  const d = { op: "state.updated" };
  assert.throws(
    () => normalizeFrames([deltaFrame(2, 4, [d]), deltaFrame(5, 6, [d])]),
    /not contiguous/,
  );
  assert.throws(
    () => normalizeFrames([deltaFrame(2, 5, [d]), deltaFrame(4, 6, [d])]),
    /not contiguous/,
  );
});
