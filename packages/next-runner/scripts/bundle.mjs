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

import { build } from "esbuild";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const PACKAGE_ROOT = resolve(HERE, "..");
const OUT_DIR = resolve(PACKAGE_ROOT, "dist");
const OUT_FILE = resolve(OUT_DIR, "runner.mjs");

const pkg = JSON.parse(await readFile(resolve(PACKAGE_ROOT, "package.json"), "utf8"));

// Keep every pi package external and pin exactly the versions this bundle was
// built against; the shipped package.json is what the target host installs.
const external = Object.keys(pkg.dependencies ?? {}).filter((name) =>
  name.startsWith("@earendil-works/"),
);
const runtimeDependencies = Object.fromEntries(
  external.map((name) => [name, pkg.dependencies[name]]),
);

await mkdir(OUT_DIR, { recursive: true });
await build({
  entryPoints: [resolve(PACKAGE_ROOT, "src/main.ts")],
  outfile: OUT_FILE,
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
});

// `npm install --omit=dev` on the target host reads this file; it must not
// advertise the monorepo-only devDependencies or the workspace protocol.
const distPackage = {
  name: "nex-next-runner-bundle",
  version: pkg.version,
  private: true,
  type: "module",
  description: "Bundled nex-next agent runner (see next/README.md).",
  main: "runner.mjs",
  engines: pkg.engines,
  dependencies: runtimeDependencies,
};
await writeFile(
  resolve(OUT_DIR, "package.json"),
  `${JSON.stringify(distPackage, null, 2)}\n`,
  "utf8",
);

process.stdout.write(`bundled ${OUT_FILE}\nexternal packages: ${external.join(", ")}\n`);
