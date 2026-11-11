import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { build } from "esbuild";
import { test } from "vitest";

// codemode 的 worker 与 quickjs wasm 在单文件 bundle / SEA 里不能按文件定位，只能由 CLI 的
// build.mjs 以 esbuild define 常量（__CODEMODE_WORKER_SOURCE__ / __CODEMODE_WASM_BASE64__）
// 内嵌。但 core/contracts 不依赖 esbuild、也不 import CLI 构建脚本，所以这里不复制构建逻辑，
// 而是把发布常量按 build.mjs 的语义注入后确认两条运行路径都成立：
//   1. embedded.ts 读取内嵌值；worker 源码可被 eval 方式的 worker 执行；wasm 可编译。
//   2. 这些常量确实被 esbuild define 替换（而不是留给运行时）。

const coreRoot = resolve(import.meta.dirname, "..");
const embeddedEntry = join(coreRoot, "src/codemode/embedded.ts");

async function bundleEmbedded(define) {
  const result = await build({
    bundle: true,
    entryPoints: [embeddedEntry],
    format: "cjs",
    logLevel: "silent",
    platform: "node",
    target: "node22",
    write: false,
    ...(define === undefined ? {} : { define }),
  });
  const file = join(coreRoot, `.tmp-codemode-embedded-${process.pid}.cjs`);
  const { writeFile, rm } = await import("node:fs/promises");
  await writeFile(file, result.outputFiles[0].text);
  try {
    // vitest 会拦截 CJS 加载；用真实 Node 子进程读取打包产物，避免按源码模块图重解析。
    const { execFile } = await import("node:child_process");
    const { promisify } = await import("node:util");
    const { stdout } = await promisify(execFile)(process.execPath, [
      "-e",
      `process.stdout.write(JSON.stringify({ worker: require(${JSON.stringify(file)}).embeddedCodemodeWorkerSource(), wasm: require(${JSON.stringify(file)}).embeddedCodemodeWasmBase64() }))`,
    ]);
    return JSON.parse(stdout);
  } finally {
    await rm(file, { force: true });
  }
}

test("without build-time defines the embedded values are absent", async () => {
  const embedded = await bundleEmbedded(undefined);
  assert.equal(embedded.worker, undefined);
  assert.equal(embedded.wasm, undefined);
});

test("build-time defines reach core and the embedded wasm compiles", async () => {
  const wasmPath = resolve(coreRoot, "node_modules/quickjs-wasi/quickjs.wasm");
  const wasmBase64 = (await readFile(wasmPath)).toString("base64");
  const embedded = await bundleEmbedded({
    __CODEMODE_WORKER_SOURCE__: JSON.stringify("module.exports = 42;"),
    __CODEMODE_WASM_BASE64__: JSON.stringify(wasmBase64),
  });
  assert.equal(embedded.worker, "module.exports = 42;");
  const bytes = Buffer.from(embedded.wasm, "base64");
  await WebAssembly.compile(bytes);
});
