// 验证发布形态：把沙箱与 build.mjs 同一组内嵌常量打成单文件，在没有 node_modules / dist 的
// 目录里运行，确认 worker 来自内嵌源码、wasm 来自内嵌字节。
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { build } from "esbuild";
import { buildCodemodeEmbeds } from "./build.mjs";

const cliDirectory = resolve(import.meta.dirname, "..");
const coreDist = resolve(cliDirectory, "../core/dist/codemode");
const work = await mkdtemp(join(tmpdir(), "nex-codemode-embed-"));
try {
  const entry = join(work, "entry.mjs");
  await writeFile(
    entry,
    `import { CodemodeSandbox } from ${JSON.stringify(resolve(coreDist, "runtime/host.js"))};
import { resolveCodemodeWorkerSource } from ${JSON.stringify(resolve(coreDist, "worker-url.js"))};
async function main() {
const source = resolveCodemodeWorkerSource();
if (typeof source !== "string") throw new Error("embedded worker source missing");
const box = new CodemodeSandbox({
  workerSource: source,
  tools: [{ name: "add", execute: (a) => a.a + a.b }],
  timeoutMs: 5000,
});
const ok = await box.execute("return [await tools.add({a:20,b:22}), typeof process, typeof require];");
const spin = await box.execute("while(true){}", { timeoutMs: 300 });
const next = await box.execute("return 7;");
await box.close();
console.log(JSON.stringify({ ok: ok.ok && ok.value, spin: !spin.ok && spin.error.kind, next: next.ok && next.value }));
}
main().catch((error) => { console.error(error); process.exit(1); });
`,
  );
  const outfile = join(work, "bundle.cjs");
  await build({
    entryPoints: [entry],
    bundle: true,
    format: "cjs",
    platform: "node",
    target: "node22",
    outfile,
    logLevel: "silent",
    define: await buildCodemodeEmbeds({ cliDirectory }),
    // import.meta 在 CJS bundle 里为空，与 nex.cjs 同样的约束。
    banner: { js: "" },
  });
  const run = spawnSync(process.execPath, [outfile], { cwd: work, encoding: "utf8" });
  if (run.status !== 0) {
    console.error(run.stdout, run.stderr);
    process.exit(1);
  }
  const result = JSON.parse(run.stdout.trim().split("\n").pop());
  const expected = { ok: [42, "undefined", "undefined"], spin: "timeout", next: 7 };
  if (JSON.stringify(result) !== JSON.stringify(expected)) {
    console.error("unexpected result", result);
    process.exit(1);
  }
  console.log("codemode embed OK:", JSON.stringify(result));
} finally {
  await rm(work, { recursive: true, force: true });
}
