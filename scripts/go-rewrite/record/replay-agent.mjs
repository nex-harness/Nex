// 回放：用同一份场景驱动任意 agent，把规范化后的输出与已提交的 trace 对比。
//
//   node replay-agent.mjs --all [--agent node|<path-to-exe>] [--arg ...]
//   node replay-agent.mjs --scenario S02 --agent ./go/dist/nex-agent
//
// 退出码 0 = 全部一致；1 = 有差异（打印第一个不同帧的路径）；2 = 用法错误。
// 注意：本工具驱动的是"场景脚本"（host 视角的请求序列），而不是把录制的入站帧原样重放。
// 原因：入站帧里带有 agent 在运行时分配的 sessionId，不能静态重放（见 decisions.md D11）。
import { readdirSync, readFileSync, existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { recordScenario } from "./record-agent-session.mjs";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../../..");
const TRACES = join(REPO, "go/testdata/traces/agent");
const SCENARIOS = join(HERE, "scenarios");

/** 第一个不同处的 JSON 路径与两边的值，没有差异返回 null。 */
export function firstDiff(expected, actual, path = "$") {
  if (
    typeof expected !== typeof actual ||
    Array.isArray(expected) !== Array.isArray(actual) ||
    expected === null ||
    actual === null
  ) {
    return expected === actual ? null : { path, expected, actual };
  }
  if (typeof expected !== "object") return expected === actual ? null : { path, expected, actual };
  if (Array.isArray(expected)) {
    if (expected.length !== actual.length)
      return { path: `${path}.length`, expected: expected.length, actual: actual.length };
    for (let i = 0; i < expected.length; i++) {
      const d = firstDiff(expected[i], actual[i], `${path}[${i}]`);
      if (d) return d;
    }
    return null;
  }
  const keys = [...new Set([...Object.keys(expected), ...Object.keys(actual)])].sort();
  for (const k of keys) {
    if (!(k in expected)) return { path: `${path}.${k}`, expected: undefined, actual: actual[k] };
    if (!(k in actual)) return { path: `${path}.${k}`, expected: expected[k], actual: undefined };
    const d = firstDiff(expected[k], actual[k], `${path}.${k}`);
    if (d) return d;
  }
  return null;
}

export function describeFrame(frames, path) {
  const m = /^\$\[(\d+)\]/.exec(path);
  if (!m) return "";
  const f = frames[Number(m[1])];
  return f ? ` (frame ${m[1]}: ${f.method ?? (f.error ? "error" : "response")})` : "";
}

// 先按文件名前缀找 S01…S13，再按 JSON 内的 id 找 heldout 下的场景（H-…）。
function findScenarioFile(id) {
  const named = readdirSync(SCENARIOS).find((f) => f.startsWith(id + "-") && f.endsWith(".json"));
  if (named) return join(SCENARIOS, named);
  const heldout = join(SCENARIOS, "heldout");
  for (const f of readdirSync(heldout).filter((n) => n.endsWith(".json"))) {
    const file = join(heldout, f);
    if (JSON.parse(readFileSync(file, "utf8")).id === id) return file;
  }
  return undefined;
}

export async function replayScenario(id, agent) {
  const scenarioPath = findScenarioFile(id);
  if (!scenarioPath) throw new Error(`no scenario file for ${id}`);
  const traceDir = join(TRACES, id);
  if (!existsSync(join(traceDir, "out.normalized.json")))
    throw new Error(`no recorded trace for ${id} in ${traceDir}`);
  const expected = JSON.parse(readFileSync(join(traceDir, "out.normalized.json"), "utf8"));
  const out = mkdtempSync(join(tmpdir(), "replay-"));
  let result;
  try {
    result = await recordScenario(scenarioPath, out, { agent, quiet: true });
  } catch (error) {
    return { id, ok: false, reason: `scenario did not complete: ${error.message.slice(0, 300)}` };
  }
  const diff = firstDiff(expected, result.normalizedOut);
  if (!diff) return { id, ok: true };
  return {
    id,
    ok: false,
    reason: `first difference at ${diff.path}${describeFrame(expected, diff.path)}`,
    diff,
    counts: { expected: expected.length, actual: result.normalizedOut.length },
  };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  const flag = (n) => {
    const i = args.indexOf(n);
    return i >= 0 ? args[i + 1] : undefined;
  };
  const agentArg = flag("--agent") ?? "node";
  const extra = args.flatMap((a, i) => (args[i - 1] === "--arg" ? [a] : []));
  const agent =
    agentArg === "node"
      ? join(REPO, "apps/nex-cli/packages/cli/dist/nex.cjs")
      : {
          command: resolve(agentArg),
          args: extra.length ? extra : ["app-server", "--stdio"],
          label: "exe",
        };
  const ids = args.includes("--all")
    ? readdirSync(TRACES).sort()
    : [flag("--scenario")].filter(Boolean);
  if (ids.length === 0) {
    console.error("usage: replay-agent.mjs (--all | --scenario S02) [--agent node|<exe>]");
    process.exit(2);
  }
  let failed = 0;
  for (const id of ids) {
    const r = await replayScenario(id, agent);
    if (r.ok) console.log(`PASS ${id}`);
    else {
      failed++;
      console.log(`FAIL ${id}: ${r.reason}`);
      if (r.diff)
        console.log(
          `     expected: ${JSON.stringify(r.diff.expected)?.slice(0, 160)}\n     actual:   ${JSON.stringify(r.diff.actual)?.slice(0, 160)}`,
        );
    }
  }
  console.log(`${ids.length - failed}/${ids.length} passed`);
  process.exit(failed ? 1 : 0);
}
