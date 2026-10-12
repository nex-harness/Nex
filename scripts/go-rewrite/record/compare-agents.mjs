// Node 对 Go 的实时差分：同一个场景分别驱动 Node agent 与另一个 agent，比较规范化后的输出。
// 与 replay-agent.mjs 的区别：不依赖已提交的录制，所以可以用"从未用于开发"的场景检验 Go 是否真的等价。
//
//   node compare-agents.mjs --agent <go-exe> --all            跑 scenarios/heldout 下全部场景
//   node compare-agents.mjs --agent <go-exe> --scenario <file> 单个场景，打印第一处差异的路径与两侧的值
//
// 先对 Node 自己录两次：两次不一致的场景是 Node 自身不稳定（重试抖动等），标记为 UNSTABLE，不计入差分结果。
import { readdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { recordScenario } from "./record-agent-session.mjs";
import { firstDiff } from "./replay-agent.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const HELDOUT = join(HERE, "scenarios/heldout");

export async function compareScenario(file, goAgent) {
  const tmp = mkdtempSync(join(tmpdir(), "cmp-"));
  try {
    const n1 = await recordScenario(file, join(tmp, "n1"), { quiet: true });
    const n2 = await recordScenario(file, join(tmp, "n2"), { quiet: true });
    const self =
      firstDiff(n1.normalizedOut, n2.normalizedOut) ??
      firstDiff(n1.modelRequestBodies, n2.modelRequestBodies);
    if (self) return { status: "UNSTABLE", detail: `node differs from itself at ${self.path}` };
    const g = await recordScenario(file, join(tmp, "g"), { quiet: true, agent: goAgent });
    // 模型实际收到的请求（含上一轮的工具结果文本）也必须一致：这是 Go 与 Node 行为等价最直接的证据。
    const reqDiff = firstDiff(n1.modelRequestBodies, g.modelRequestBodies);
    if (reqDiff)
      return {
        status: "DIFFERENT",
        detail: `model request ${reqDiff.path}\n      node: ${JSON.stringify(reqDiff.expected)?.slice(0, 300)}\n      go:   ${JSON.stringify(reqDiff.actual)?.slice(0, 300)}`,
      };
    const d = firstDiff(n1.normalizedOut, g.normalizedOut);
    if (!d) return { status: "SAME", entries: n1.normalizedOut.length };
    return {
      status: "DIFFERENT",
      detail: `${d.path}\n      node: ${JSON.stringify(d.expected)?.slice(0, 220)}\n      go:   ${JSON.stringify(d.actual)?.slice(0, 220)}`,
      node: n1.normalizedOut,
      go: g.normalizedOut,
    };
  } catch (error) {
    return { status: "ERROR", detail: String(error.message).slice(0, 300) };
  } finally {
    rmSync(tmp, { recursive: true, force: true });
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  const flag = (n) => {
    const i = args.indexOf(n);
    return i >= 0 ? args[i + 1] : undefined;
  };
  const exe = flag("--agent");
  if (!exe) {
    console.error("usage: compare-agents.mjs --agent <exe> (--all | --scenario <file>)");
    process.exit(2);
  }
  const goAgent = { command: resolve(exe), label: "go" };
  const files = args.includes("--all")
    ? readdirSync(HELDOUT)
        .filter((f) => f.endsWith(".json"))
        .sort()
        .map((f) => join(HELDOUT, f))
    : [resolve(flag("--scenario"))];
  const counts = { SAME: 0, DIFFERENT: 0, UNSTABLE: 0, ERROR: 0 };
  for (const f of files) {
    const r = await compareScenario(f, goAgent);
    counts[r.status]++;
    console.log(
      `${r.status.padEnd(9)} ${f.split("/").pop()}${r.status === "SAME" ? "" : "\n      " + r.detail}`,
    );
  }
  console.log(
    `same=${counts.SAME} different=${counts.DIFFERENT} unstable(node)=${counts.UNSTABLE} error=${counts.ERROR}`,
  );
  process.exit(counts.DIFFERENT + counts.ERROR ? 1 : 0);
}
