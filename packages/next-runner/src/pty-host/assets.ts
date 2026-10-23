// node-pty 原生资产安装：把平台预编译的 pty.node / ConPTY 文件放进
// `node_modules/node-pty/prebuilds/<platform>-<arch>/`，并修正 macOS 的 spawn-helper 权限。
//
// 来源与机制复用仓库现有实现（packages/desktop/scripts/node-pty-package-assets.mjs、
// packages/services/src/terminal/terminalService.ts:104-146）；这里只保留 runner bundle
// 需要的最小逻辑，不引入 @nex/services。
//
// 为什么不能只靠 `npm install`：node-pty@1.1.0 自身不含 linux 预编译产物，Linux 上
// `npm install --omit=dev` 会走 node-gyp 编译（需要 make/g++），在没有编译器的 runner
// 主机上终端会直接不可用。@lydell/node-pty-<platform>-<arch> 提供同一版本的预编译
// pty.node，bundle 时打包成 tar.gz，第一次使用时释放到 node-pty 的 prebuilds 目录。

import {
  accessSync,
  chmodSync,
  constants,
  copyFileSync,
  existsSync,
  mkdirSync,
  readdirSync,
} from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";

const require = createRequire(import.meta.url);

/** `darwin-arm64` / `linux-x64` / `win32-x64` … */
export function ptyPlatformKey(
  platform: NodeJS.Platform = process.platform,
  arch: string = process.arch,
): string {
  return `${platform}-${arch}`;
}

export function resolveNodePtyPackageRoot(): string | null {
  try {
    return dirname(require.resolve("node-pty/package.json"));
  } catch {
    return null;
  }
}

/**
 * 在已安装的 @lydell 平台包中定位 `<platform>/pty.node`。
 * 这些包通过 `exports` 只暴露 lib/index.js，所以从入口向上找 prebuilds，
 * 与 desktop 的 resolveSourceNodePtyPrebuildPath 保持一致。
 */
export function resolveLydellPrebuildDir(platformKey: string): string | null {
  const packageName = `@lydell/node-pty-${platformKey}`;
  try {
    let current = dirname(require.resolve(packageName));
    for (;;) {
      const candidate = join(current, "prebuilds", platformKey);
      if (existsSync(join(candidate, "pty.node"))) return candidate;
      const parent = dirname(current);
      if (parent === current) return null;
      current = parent;
    }
  } catch {
    return null;
  }
}

/** 从一个 prebuild 目录拷贝原生文件；只拷运行期需要的，不含 .pdb / winpty 旧后端。 */
const RUNTIME_FILES = [
  "pty.node",
  "spawn-helper",
  "conpty.node",
  "conpty.dll",
  "OpenConsole.exe",
  "conpty_console_list.node",
] as const;

export interface PtyAssetInstallResult {
  installed: boolean;
  platformKey: string;
  targetDir: string;
  copied: string[];
  /** 安装后实际的 pty.node 路径（未安装时为 null）。 */
  ptyNodePath: string | null;
}

/**
 * 幂等地把平台预编译产物安装到 node-pty 的 prebuilds 目录。
 * 已存在 pty.node 时只做 spawn-helper 权限修复，不重复拷贝。
 */
export function ensureNodePtyAssets(
  options: {
    platformKey?: string;
    /** 显式的预编译来源目录（bundle 释放在临时目录时传入）。 */
    prebuildSourceDir?: string;
    nodePtyRoot?: string | null;
    targetDir?: string;
  } = {},
): PtyAssetInstallResult {
  const platformKey = options.platformKey ?? ptyPlatformKey();
  const nodePtyRoot =
    options.nodePtyRoot === undefined ? resolveNodePtyPackageRoot() : options.nodePtyRoot;
  const result: PtyAssetInstallResult = {
    installed: false,
    platformKey,
    targetDir: "",
    copied: [],
    ptyNodePath: null,
  };
  if (!nodePtyRoot) return result;

  const targetDir = options.targetDir ?? join(nodePtyRoot, "prebuilds", platformKey);
  result.targetDir = targetDir;
  const targetPtyNode = join(targetDir, "pty.node");

  if (!existsSync(targetPtyNode)) {
    const sourceDir = options.prebuildSourceDir ?? resolveLydellPrebuildDir(platformKey);
    if (!sourceDir || !existsSync(join(sourceDir, "pty.node"))) return result;

    // 先写临时名再 rename：并发的多个 pty-host 同时安装时不会读到半拷贝的 .node。
    mkdirSync(targetDir, { recursive: true });
    for (const name of readdirSync(sourceDir)) {
      if (!(RUNTIME_FILES as readonly string[]).includes(name)) continue;
      copyFileSync(join(sourceDir, name), join(targetDir, name));
      result.copied.push(name);
    }
    result.installed = true;
  }

  if (existsSync(targetPtyNode)) {
    result.ptyNodePath = targetPtyNode;
    ensureSpawnHelperExecutable(targetDir);
  }
  return result;
}

/**
 * macOS: node-pty 启动伪终端前会 posix_spawnp 同目录的 spawn-helper；
 * 打包/解包丢失执行位时终端直接报 "posix_spawnp failed"。
 * 这里在每次 pty-host 启动时把 0644 修正为 0755（已可执行则不动）。
 */
export function ensureSpawnHelperExecutable(prebuildDir: string): void {
  if (process.platform !== "darwin") return;
  const helper = join(prebuildDir, "spawn-helper");
  if (!existsSync(helper)) return;
  try {
    accessSync(helper, constants.X_OK);
    return;
  } catch {
    // 权限漂移：继续走 chmod，失败时抛给调用方（终端不可用需要显式报错）。
  }
  chmodSync(helper, 0o755);
  accessSync(helper, constants.X_OK);
}
