// 从真实 Node agent 发出的模型请求里导出：
//   1. go/internal/tooldefs/tools.json  —— 默认会话（无 MCP/插件/技能）暴露给模型的 20 个工具定义，逐字、保持键序；
//   2. go/testdata/vectors/request-*.json —— 完整请求体 + 生成它所需的输入（环境事实、用户文本、模型、日期），
//      Go 侧用自己的 promptctx + tooldefs 重新拼出请求体，必须与 Node 发出的逐字段一致。
// 环境事实（cwd/platform/shell/osVersion/日期）从请求里带标签的行解析出来，不在这里手写。
import { mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { recordScenario } from "./record/record-agent-session.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../..");
const SCENARIOS = join(HERE, "record/scenarios");
const bodyDir = mkdtempSync(join(tmpdir(), "reqvec-"));
process.env.NEX_REC_BODY_DIR = bodyDir;
const out = mkdtempSync(join(tmpdir(), "reqvec-out-"));

const label = (text, key) => new RegExp(`^- ${key}: (.*)$`, "m").exec(text)?.[1];
const vectors = [];
let toolsJson;
for (const [scenario, wantRequests] of [["S02-chat.json", [0]]]) {
  rmSync(bodyDir, { recursive: true, force: true });
  mkdirSync(bodyDir, { recursive: true });
  await recordScenario(join(SCENARIOS, scenario), join(out, scenario), { quiet: true });
  for (const index of wantRequests) {
    const raw = readFileSync(join(bodyDir, `request-${index}.json`), "utf8");
    const { body, url } = JSON.parse(raw);
    const system = body.messages[0].content;
    const date = /Today's date is (\d{4}-\d{2}-\d{2})\./.exec(body.messages[1].content)?.[1];
    const env = {
      cwd: label(system, "Primary working directory"),
      platform: label(system, "Platform"),
      shell: label(system, "Shell"),
      osVersion: label(system, "OS Version"),
      isGitRepository: label(system, "Is a git repository") === "yes",
    };
    const powered = /You are powered by the model named ([^/]+)\/(.+)\.$/m.exec(system);
    // 工作目录是临时目录，随机；向量里换成固定占位，Go 测试用同一个值生成。
    const FIXED_CWD = "/tmp/nex-fixed/ws";
    const norm = (s) => (typeof s === "string" ? s.replaceAll(env.cwd, FIXED_CWD) : s);
    const bodyOut = JSON.parse(
      JSON.stringify(body, (k, v) => (typeof v === "string" ? norm(v) : v)),
    );
    toolsJson ??= JSON.stringify(body.tools, null, 1);
    vectors.push({
      scenario,
      requestIndex: index,
      url,
      env: { ...env, cwd: FIXED_CWD },
      date,
      providerId: powered?.[1],
      modelId: powered?.[2],
      userText: body.messages.at(-1).content,
      body: bodyOut,
    });
  }
}
const toolsDir = join(REPO, "go/internal/tooldefs");
writeFileSync(join(toolsDir, "tools.json"), toolsJson + "\n");
writeFileSync(
  join(REPO, "go/testdata/vectors/request.json"),
  JSON.stringify(vectors, null, 1) + "\n",
);
rmSync(bodyDir, { recursive: true, force: true });
rmSync(out, { recursive: true, force: true });
console.log(
  `tools: ${JSON.parse(toolsJson).length} (${toolsJson.length} bytes); request vectors: ${vectors.length}`,
);
