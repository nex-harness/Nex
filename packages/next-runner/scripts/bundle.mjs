#!/usr/bin/env node
// 把 runner 打成一个可直接 `node dist/runner.mjs` 运行的单文件 ESM bundle，
// 用于部署到没有 monorepo / 没有 pnpm workspace 的机器（见 next/README.md
// "Deploy to a Linux host"）。
//
// 打包边界：
// - 工作区内部依赖（@nex/shared）内联，目标机器不需要 workspace 链接。
// - pi SDK（@earendil-works/*）保持 external：它自带几十个传递依赖和可选原生包，
//   bundle 进去既体积巨大又容易在非本机平台解析失败。改为在 dist/package.json
//   里固定版本，目标机器 `npm install --omit=dev` 即可。
// - ws 内联；它只在 CJS 里 require("events") 之类的内建模块，banner 里注入的
//   createRequire 就是为它准备的（纯 ESM 下否则报 "Dynamic require not supported"）。
//
// 终端（spec docs/next/workspace-tools.md 决策 3/4/6）：
// - 第二个入口 dist/pty-host.mjs，与 runner.mjs 同目录；runner 用
//   process.execPath 启动它（src/pty-host/launch.ts）。
// - node-pty 保持 external，并写进 dist/package.json 的运行时依赖。
// - node-pty@1.1.0 不含 Linux 预编译产物，`npm install --omit=dev` 在 Linux 上会
//   走 node-gyp（需要 make/g++）。因此把 @lydell/node-pty-<platform> 里的
//   prebuilds/<platform>/ 打成 dist/node-pty-assets.tar.gz，并生成
//   dist/lib/ptyHostAssets.mjs：pty-host 启动时（`--import`）就地释放到
//   node_modules/node-pty/prebuilds/<platform>/ 并修正 darwin spawn-helper 权限。
//   机制同 packages/desktop/scripts/node-pty-package-assets.mjs 与
//   packages/server/package.json。
// - pty-host 用的 @xterm/headless、@xterm/addon-serialize 是纯 JS，内联。
//
// 目标平台：`--platform linux-x64`（或 NEX_BUNDLE_PLATFORM），默认当前平台。
// 在 macOS 上为 Linux 构建时必须显式指定，否则资产包里装的是 darwin 预编译。

import { build } from "esbuild";
import { spawnSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const PACKAGE_ROOT = resolve(HERE, "..");
const OUT_DIR = resolve(PACKAGE_ROOT, "dist");
const OUT_FILE = resolve(OUT_DIR, "runner.mjs");
const PTY_HOST_OUT_FILE = resolve(OUT_DIR, "pty-host.mjs");
const PTY_ASSETS_ARCHIVE = resolve(OUT_DIR, "node-pty-assets.tar.gz");
const PTY_ASSETS_LOADER = resolve(OUT_DIR, "lib", "ptyHostAssets.mjs");

const require = createRequire(import.meta.url);
const pkg = JSON.parse(await readFile(resolve(PACKAGE_ROOT, "package.json"), "utf8"));

const PTY_PLATFORM_PACKAGES = Object.keys(pkg.dependencies ?? {}).filter((name) =>
  name.startsWith("@lydell/node-pty-"),
);
const PTY_NATIVE_PACKAGES = ["node-pty", ...PTY_PLATFORM_PACKAGES];

function parseTargetPlatform(argv) {
  const index = argv.indexOf("--platform");
  const value = index >= 0 ? argv[index + 1] : process.env.NEX_BUNDLE_PLATFORM;
  return value?.trim() || `${process.platform}-${process.arch}`;
}

const TARGET_PLATFORM = parseTargetPlatform(process.argv.slice(2));
if (!/^[a-z0-9]+-[a-z0-9]+$/.test(TARGET_PLATFORM)) {
  throw new Error(`invalid bundle platform '${TARGET_PLATFORM}' (expected <os>-<arch>)`);
}

// Keep every pi package external and pin exactly the versions this bundle was
// built against; the shipped package.json is what the target host installs.
const external = [
  ...Object.keys(pkg.dependencies ?? {}).filter((name) => name.startsWith("@earendil-works/")),
  ...PTY_NATIVE_PACKAGES,
];
// 目标机器只装 node-pty 本体；@lydell 平台包只是构建侧的预编译来源，
// 不进 dist/package.json，否则 npm 会在目标机额外拉取用不到的平台包。
const runtimeDependencies = Object.fromEntries(
  external
    .filter((name) => name.startsWith("@earendil-works/") || name === "node-pty")
    .map((name) => [name, pkg.dependencies[name]]),
);

const SHARED_BUILD_OPTIONS = {
  bundle: true,
  platform: "node",
  format: "esm",
  target: "node24",
  sourcemap: false,
  logLevel: "warning",
  external,
  banner: {
    js: [
      "import { createRequire as __nexCreateRequire } from 'node:module';",
      "const require = __nexCreateRequire(import.meta.url);",
    ].join("\n"),
  },
};

await rm(OUT_DIR, { recursive: true, force: true });
await mkdir(OUT_DIR, { recursive: true });

await build({
  ...SHARED_BUILD_OPTIONS,
  entryPoints: [resolve(PACKAGE_ROOT, "src/main.ts")],
  outfile: OUT_FILE,
});
// 终端宿主：独立入口，不会随 runner.mjs 一起加载 node-pty。
await build({
  ...SHARED_BUILD_OPTIONS,
  entryPoints: [resolve(PACKAGE_ROOT, "src/pty-host/ptyHost.ts")],
  outfile: PTY_HOST_OUT_FILE,
});

// --- node-pty 原生资产 ------------------------------------------------------

/** 从 @lydell 平台包解析 prebuilds 目录（其 package exports 不暴露 package.json）。 */
function resolveLydellPrebuildDir(platformKey) {
  const packageName = `@lydell/node-pty-${platformKey}`;
  let current = dirname(require.resolve(packageName));
  for (;;) {
    const candidate = join(current, "prebuilds", platformKey);
    if (existsSync(join(candidate, "pty.node"))) return candidate;
    const parent = dirname(current);
    if (parent === current) {
      throw new Error(`${packageName} has no prebuilds/${platformKey}/pty.node`);
    }
    current = parent;
  }
}

// 只收集运行期需要的文件（不含 .pdb；winpty 旧后端保留 pty.node + winpty 文件，
// 供老系统 ConPTY 不可用时回退）。
const PTY_RUNTIME_FILES = [
  "pty.node",
  "spawn-helper",
  "conpty.node",
  "conpty_console_list.node",
  "conpty/conpty.node",
  "conpty/conpty.dll",
  "conpty/OpenConsole.exe",
];

function collectPtyAssets(platformKey) {
  const sourceDir = resolveLydellPrebuildDir(platformKey);
  const files = PTY_RUNTIME_FILES.filter((name) => existsSync(join(sourceDir, name)));
  if (!files.includes("pty.node")) throw new Error(`missing pty.node in ${sourceDir}`);
  return { sourceDir, files };
}

function writePtyAssetsArchive({ sourceDir, files }) {
  // 直接调用系统 tar（macOS/Linux 自带），避免为构建再加一个归档依赖。
  // COPYFILE_DISABLE=1：macOS 的 bsdtar 会把 ._* AppleDouble 元数据文件写进归档，
  // 释放到 Linux 的 node-pty/prebuilds 里就是垃圾。
  const result = spawnSync("tar", ["-czf", PTY_ASSETS_ARCHIVE, "-C", sourceDir, ...files], {
    stdio: "inherit",
    env: { ...process.env, COPYFILE_DISABLE: "1" },
  });
  if (result.status !== 0) throw new Error(`tar failed with status ${String(result.status)}`);
}

const ASSET_LOADER_SOURCE = `// 由 scripts/bundle.mjs 生成：pty-host 启动时通过 \`--import\` 装载。
// 把同级目录的 node-pty-assets.tar.gz 释放到 node_modules/node-pty/prebuilds/<platform>/，
// 让 node-pty 的 require("prebuilds/<platform>/pty.node") 命中预编译产物；
// 目标机器不需要 make/g++ 就能启动终端。可重复执行（已就位则跳过拷贝）。
import { accessSync, chmodSync, constants, copyFileSync, existsSync, mkdirSync, readdirSync, renameSync, rmSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const PLATFORM_KEY = ${JSON.stringify(TARGET_PLATFORM)};
const ARCHIVE_NAME = "node-pty-assets.tar.gz";
const here = dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);

function repairSpawnHelper(dir) {
  if (!PLATFORM_KEY.startsWith("darwin-")) return;
  const helper = join(dir, "spawn-helper");
  if (!existsSync(helper)) return;
  try {
    accessSync(helper, constants.X_OK);
  } catch {
    // 打包/解包丢掉执行位：node-pty 启动伪终端前 posix_spawnp 会直接失败。
    chmodSync(helper, 0o755);
  }
}

function copyTree(from, to) {
  mkdirSync(to, { recursive: true });
  for (const entry of readdirSync(from, { withFileTypes: true })) {
    const source = join(from, entry.name);
    const destination = join(to, entry.name);
    if (entry.isDirectory()) {
      copyTree(source, destination);
      continue;
    }
    // 先写临时名再 rename，多个 pty-host 同时启动时不会读到半拷贝的 .node。
    const temporary = \`\${destination}.\${process.pid}.tmp\`;
    copyFileSync(source, temporary);
    renameSync(temporary, destination);
  }
}

function install() {
  let root;
  try {
    root = dirname(require.resolve("node-pty/package.json"));
  } catch {
    // node-pty 尚未安装：留给 npm install 与下一次启动。
    return;
  }
  const targetDir = join(root, "prebuilds", PLATFORM_KEY);
  if (!existsSync(join(targetDir, "pty.node"))) {
    const archive = join(here, "..", ARCHIVE_NAME);
    if (!existsSync(archive)) return;
    const staging = join(tmpdir(), \`nex-pty-assets-\${process.pid}-\${Date.now()}\`);
    mkdirSync(staging, { recursive: true });
    try {
      const result = spawnSync("tar", ["-xzf", archive, "-C", staging], { stdio: "ignore" });
      if (result.status !== 0) throw new Error(\`tar exited with \${String(result.status)}\`);
      copyTree(staging, targetDir);
    } finally {
      rmSync(staging, { recursive: true, force: true });
    }
  }
  repairSpawnHelper(targetDir);
}

install();
`;

const ptyAssets = collectPtyAssets(TARGET_PLATFORM);
writePtyAssetsArchive(ptyAssets);
await mkdir(dirname(PTY_ASSETS_LOADER), { recursive: true });
await writeFile(PTY_ASSETS_LOADER, ASSET_LOADER_SOURCE, "utf8");

// `npm install --omit=dev` on the target host reads this file; it must not
// advertise the monorepo-only devDependencies or the workspace protocol.
// node-pty 本体是普通依赖；@lydell 平台包只在构建侧使用，不进目标依赖。
const distPackage = {
  name: "nex-next-runner-bundle",
  version: pkg.version,
  private: true,
  type: "module",
  description: "Bundled nex-next agent runner (see next/README.md).",
  main: "runner.mjs",
  engines: pkg.engines,
  dependencies: runtimeDependencies,
  nexPtyTarget: TARGET_PLATFORM,
  nexPtyAssets: {
    archive: "node-pty-assets.tar.gz",
    loader: "lib/ptyHostAssets.mjs",
    files: ptyAssets.files,
  },
};
await writeFile(
  resolve(OUT_DIR, "package.json"),
  `${JSON.stringify(distPackage, null, 2)}\n`,
  "utf8",
);

const sizeOf = (path) => (existsSync(path) ? readFileSync(path).length : 0);
process.stdout.write(
  [
    `bundled ${OUT_FILE} (${sizeOf(OUT_FILE)} bytes)`,
    `bundled ${PTY_HOST_OUT_FILE} (${sizeOf(PTY_HOST_OUT_FILE)} bytes)`,
    `pty assets for ${TARGET_PLATFORM} -> ${PTY_ASSETS_ARCHIVE} (${sizeOf(PTY_ASSETS_ARCHIVE)} bytes): ${ptyAssets.files.join(", ")}`,
    `external packages: ${external.join(", ")}`,
  ].join("\n") + "\n",
);
