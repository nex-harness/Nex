// worker 入口的解析。
// 发布产物（bundle / SEA）里没有 worker 文件，也没有 import.meta，所以 build.mjs 把 worker 的
// CJS bundle 内嵌为源码（见 embedded.ts），运行时以 eval 方式起 worker。
// dev / tsc 产物没有内嵌源码，退回与 host.js 同目录的 worker.js；测试可用
// setCodemodeWorkerUrl 显式指定。

import { embeddedCodemodeWorkerSource } from "./embedded.js";

let override: string | URL | undefined;

export function setCodemodeWorkerUrl(url: string | URL | undefined): void {
  override = url;
}

export function resolveCodemodeWorkerUrl(): string | URL | undefined {
  // undefined 让 CodemodeSandbox 使用相对 host.js 的默认 worker.js。
  return override;
}

/** 构建期内嵌了 worker 源码就用它（bundle/SEA）；否则退回按文件定位（dev/tsc 产物）。 */
export function resolveCodemodeWorkerSource(): string | undefined {
  return override === undefined ? embeddedCodemodeWorkerSource() : undefined;
}
