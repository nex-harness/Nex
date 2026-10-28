// pty-host 子进程的启动参数与打包产物路径约定。
//
// 打包后 dist/ 布局（scripts/bundle.mjs 生成）：
//   dist/runner.mjs      ← 现有 runner 入口
//   dist/pty-host.mjs    ← pty-host 子进程入口（process.execPath 启动）
//   dist/package.json    ← 含 node-pty external 依赖
//   dist/node-pty-assets.tar.gz / dist/lib/ptyHostAssets.mjs
// 开发态（tsx）直接运行 src/pty-host/ptyHost.ts。

import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

export interface PtyHostLaunchSpec {
  /** 传给 process.execPath 的绝对入口路径。 */
  entry: string;
  /** 入口形态：bundle 产物或 tsx 源码。 */
  kind: "bundle" | "source";
  /** 额外 argv：打包产物需要 `--import <loader>` 指向内联的 node-pty 安装器。 */
  args: string[];
  /** 子进程 cwd；保持与父进程一致，避免影响 node_modules 解析。 */
  cwd: string;
}

/**
 * 找出当前部署形态下的 pty-host 入口。
 * 顺序：显式覆盖（测试/调试）→ 同目录产物（bundle）→ 源码（tsx 开发态）。
 * 找不到时返回 null，由调用方给出明确错误，而不是静默地让终端不可用。
 */
export function resolvePtyHostLaunchSpec(
  moduleUrl: string = import.meta.url,
): PtyHostLaunchSpec | null {
  const override = process.env.NEX_PTY_HOST_ENTRY?.trim();
  if (override && existsSync(override)) {
    return { entry: override, kind: "bundle", args: [], cwd: process.cwd() };
  }

  const here = dirname(fileURLToPath(moduleUrl));
  if (!existsSync(here)) return null;
  const bundledEntry = join(here, "pty-host.mjs");
  if (existsSync(bundledEntry)) {
    const loader = join(here, "lib", "ptyHostAssets.mjs");
    // Windows：`--import C:\...` 会被 ESM loader 当成 `c:` 协议拒绝
    // （ERR_UNSUPPORTED_ESM_URL_SCHEME）。--import 只接受模块说明符，
    // 绝对路径必须转成 file:// URL；posix 两种形式都合法，统一转换。
    const loaderArg = existsSync(loader) ? pathToFileURL(loader).href : null;
    return {
      entry: bundledEntry,
      kind: "bundle",
      // 子进程启动时先安装 node-pty 原生资产（prebuild 拷贝 + spawn-helper chmod），
      // 再执行入口模块；这样第一次 spawn 前 pty.node 已经就位。
      args: loaderArg ? ["--import", loaderArg] : [],
      cwd: process.cwd(),
    };
  }

  const sourceEntry = resolve(here, "ptyHost.ts");
  if (existsSync(sourceEntry)) {
    // 开发态用 tsx 运行 TS 入口；tsx 由 devDependencies 提供。
    return { entry: sourceEntry, kind: "source", args: ["--import", "tsx"], cwd: process.cwd() };
  }

  return null;
}
