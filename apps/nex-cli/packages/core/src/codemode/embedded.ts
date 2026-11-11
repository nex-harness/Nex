// ============================================================
// 构建期内嵌的 worker 源码与 quickjs wasm
// ============================================================
// worker_threads 只能加载磁盘上的真实文件，而 CLI 以单文件 bundle / SEA 发布，
// 且 bundle 里 import.meta 为空，没法相对定位旁边的 worker.js 与 quickjs.wasm。
// 所以 build.mjs 把 worker 的 CJS bundle 与 wasm 字节以 define 常量内嵌进 nex.cjs，
// 运行时用 `new Worker(source, { eval: true })` 起沙箱，不依赖任何释放到磁盘的资产。
// 未经 esbuild define 的环境（tsc 产物、tsx 测试）取到 undefined，退回按文件定位。

declare const __CODEMODE_WORKER_SOURCE__: string | undefined;
declare const __CODEMODE_WASM_BASE64__: string | undefined;

export function embeddedCodemodeWorkerSource(): string | undefined {
  return typeof __CODEMODE_WORKER_SOURCE__ === "string" ? __CODEMODE_WORKER_SOURCE__ : undefined;
}

export function embeddedCodemodeWasmBase64(): string | undefined {
  return typeof __CODEMODE_WASM_BASE64__ === "string" ? __CODEMODE_WASM_BASE64__ : undefined;
}
