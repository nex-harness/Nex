#!/usr/bin/env node
// G1.8 的差分来源守卫：重新跑一遍 scripts/go-rewrite/gen-tool-vectors.mts（真实 TS
// handler），要求 go/testdata/vectors/tools-*.json 逐字节不变。
//
// 为什么需要它：Go 侧的向量回放只证明「Go 和已提交的 JSON 一致」，不能证明那份 JSON
// 真的来自 TypeScript。手工改向量能骗过回放，只有重新生成能骗不过。
//
// 脚本直接覆盖 go/testdata/vectors/ 下的产物（输出路径由生成器写死），比对哈希后
// 以非零码报错；成功时文件内容与提交态相同。tsx 缺失时退出 2，由 gate.sh 记为 SKIP。
//
//   node scripts/go-rewrite/check-tool-vector-drift.mjs
import { createHash } from "node:crypto";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../..");
const VECTOR_DIR = join(REPO, "go/testdata/vectors");

// G1.8 的三个 handler 差分向量必须存在；同脚本产出的其它 tools-*.json 一并比对。
const REQUIRED = ["tools-read.json", "tools-write.json", "tools-edit.json"];

function fail(message) {
  console.error(`check-tool-vector-drift: ${message}`);
  process.exit(1);
}

function hashDir() {
  const out = new Map();
  for (const name of readdirSync(VECTOR_DIR)) {
    if (!name.startsWith("tools-") || !name.endsWith(".json")) continue;
    out.set(
      name,
      createHash("sha256")
        .update(readFileSync(join(VECTOR_DIR, name)))
        .digest("hex"),
    );
  }
  return out;
}

const missing = REQUIRED.filter((f) => !existsSync(join(VECTOR_DIR, f)));
if (missing.length > 0) fail(`missing vectors: ${missing.join(", ")}`);

// 用仓库自带的 tsx，避免 npx 拉网络包；缺失时退出 2，让 gate.sh 记成 SKIP 而不是 FAIL。
const tsx = join(REPO, "node_modules/.bin/tsx");
if (!existsSync(tsx)) {
  console.error("check-tool-vector-drift: node_modules/.bin/tsx not found");
  process.exit(2);
}

const before = hashDir();
const gen = spawnSync(tsx, [join(HERE, "gen-tool-vectors.mts")], {
  cwd: join(REPO, "apps/nex-cli/packages/core"),
  encoding: "utf8",
});
if (gen.status !== 0) {
  process.stderr.write(gen.stdout ?? "");
  process.stderr.write(gen.stderr ?? "");
  fail("gen-tool-vectors.mts failed");
}

const after = hashDir();
const drifted = [];
const vanished = [];
for (const [name, hash] of before) {
  if (!after.has(name)) vanished.push(name);
  else if (after.get(name) !== hash) drifted.push(name);
}

if (vanished.length > 0 || drifted.length > 0) {
  fail(
    [
      vanished.length > 0 ? `vectors no longer produced: ${vanished.join(", ")}` : "",
      drifted.length > 0
        ? `committed vectors are not reproducible from the TypeScript handlers: ${drifted.join(", ")}`
        : "",
      "Regenerate and commit: (cd apps/nex-cli/packages/core && ../../../node_modules/.bin/tsx ../../../scripts/go-rewrite/gen-tool-vectors.mts)",
    ]
      .filter(Boolean)
      .join("\n"),
  );
}

console.log(`check-tool-vector-drift: ${before.size} tool vector files reproduce byte-identically`);
