// 扫描要提交的 fixture/trace，发现疑似凭据或个人信息就失败（phase-0 步骤 0.9 / 门禁 G0.10）。
//   node scripts/go-rewrite/scrub-check.mjs <dir> [<dir> ...]
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

// 允许名单：这些是 fixture 里刻意使用的假值，必须出现在这里才放行。
const ALLOW = [/sk-test-fake/g];
export const RULES = [
  ["api key", /\bsk-[A-Za-z0-9_-]{16,}\b/g],
  ["bearer token", /\bBearer\s+[A-Za-z0-9._~+/-]{20,}/g],
  ["jwt", /\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}/g],
  ["github token", /\bgh[pousr]_[A-Za-z0-9]{30,}/g],
  ["private key", /-----BEGIN [A-Z ]*PRIVATE KEY-----/g],
  ["aws key id", /\bAKIA[0-9A-Z]{16}\b/g],
  ["email", /\b[A-Za-z0-9._%+-]+@(?!example\.(?:com|org)\b)[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b/g],
  ["home path", /\/(?:home|Users)\/[A-Za-z0-9._-]+\//g],
  ["windows user path", /[A-Za-z]:\\Users\\[A-Za-z0-9._-]+\\/g],
];

export function scanText(text) {
  const hits = [];
  let cleaned = text;
  for (const allow of ALLOW) cleaned = cleaned.replace(allow, "");
  for (const [name, re] of RULES)
    for (const m of cleaned.matchAll(re)) hits.push({ rule: name, sample: m[0].slice(0, 40) });
  return hits;
}

function* walk(dir) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) yield* walk(p);
    else yield p;
  }
}

if (import.meta.url === `file://${process.argv[1]}`) {
  let bad = 0,
    files = 0;
  for (const root of process.argv.slice(2))
    for (const file of walk(root)) {
      if (/\.(sqlite|png|jpg|gz)$/.test(file)) continue;
      files++;
      const hits = scanText(readFileSync(file, "utf8"));
      for (const h of hits) {
        bad++;
        console.error(`${file}: ${h.rule}: ${h.sample}`);
      }
    }
  console.log(`scrub-check: ${files} files, ${bad} findings`);
  process.exit(bad ? 1 : 0);
}
