// runner buildId 的单一算法实现（docs/next/runner-update.md 决策 1）。
//
// 纯 Node ESM，供 scripts/bundle.mjs 直接 import；TypeScript 侧的
// src/update/buildId.ts 复刻同一算法，并由 test/self-update.test.ts 断言两者一致
// （bundle.mjs 是普通 node 脚本，无法 import .ts）。
//
// 算法：按固定顺序把三个文件的字节依次喂给 sha256，取前 16 位十六进制。
// 顺序固定、不含文件名，因此相同代码在任何平台上得到相同 id。

import { createHash } from "node:crypto";

/** 参与 buildId 的文件，顺序即哈希顺序。 */
export const BUILD_ID_FILES = ["runner.mjs", "pty-host.mjs", "lib/ptyHostAssets.mjs"];

/**
 * @param {ReadonlyArray<Buffer | Uint8Array>} files 按 BUILD_ID_FILES 顺序的字节
 * @returns {string} 16 位十六进制 buildId
 */
export function computeBuildId(files) {
  const hash = createHash("sha256");
  for (const file of files) hash.update(file);
  return hash.digest("hex").slice(0, 16);
}
