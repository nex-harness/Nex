import { spawn } from "node:child_process";
import { readFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  SOURCE_SERVER_REMOTE_RELEASE_TARGETS,
  beginSourceServerRemoteReleaseGeneration,
  collectSourceServerRemoteReleaseArchives,
} from "./remoteAssetRelease.js";

async function run(
  command: string,
  args: readonly string[],
  cwd: string,
  env: NodeJS.ProcessEnv,
): Promise<void> {
  await new Promise<void>((resolvePromise, rejectPromise) => {
    // Bug 原因：Windows 上 pnpm 只以 .cmd/.ps1 shim 存在，Node 不经 shell 无法解析，
    // spawn("pnpm") 直接 ENOENT；e2e onPrepare 自动 prepare:remote-assets 时因此 fatal。
    // 与 wdio.conf.ts runPnpmWorkspaceCommand 一致，仅 Windows 交给 shell 解析 shim；
    // 当前参数均无空格，无需额外转义。
    const child = spawn(command, [...args], {
      cwd,
      env,
      stdio: "inherit",
      shell: process.platform === "win32",
    });
    child.once("error", rejectPromise);
    child.once("exit", (code) => {
      if (code === 0) resolvePromise();
      else rejectPromise(new Error(`${command} ${args.join(" ")} exited with code ${code}`));
    });
  });
}

async function main(): Promise<void> {
  const linuxOnly = process.argv.slice(2).includes("--linux");
  const targets = linuxOnly
    ? SOURCE_SERVER_REMOTE_RELEASE_TARGETS.filter((target) => target.startsWith("linux-"))
    : SOURCE_SERVER_REMOTE_RELEASE_TARGETS;
  const env = { ...process.env };
  if (linuxOnly) {
    // Linux 开发入口与 dev:desktop 共用 production 默认值，避免远端模型配置退回 test。
    // 原全平台入口继续沿用通用构建器的环境选择规则。
    const nexEnv = env.NEX_ENV?.trim().toLowerCase() || "production";
    if (nexEnv !== "test" && nexEnv !== "production") {
      throw new Error(`Invalid NEX_ENV for prepare:remote-assets:linux: ${nexEnv}`);
    }
    env.NEX_ENV = nexEnv;
  }
  const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
  const repoRoot = resolve(packageRoot, "../..");
  const { version } = JSON.parse(await readFile(join(repoRoot, "package.json"), "utf8")) as {
    version: string;
  };
  // --out <根目录> 输出到 <根目录>/releases/<版本>/，与 NEX_REMOTE_ASSET_LOCAL_RELEASE_DIR 的目录结构一致；
  // 不带时沿用开发态的 packages/desktop/mock-cdn。
  const argv = process.argv.slice(2);
  const outIndex = argv.indexOf("--out");
  const outRoot = outIndex >= 0 ? argv[outIndex + 1] : undefined;
  if (outIndex >= 0 && (!outRoot || outRoot.startsWith("--"))) {
    throw new Error("--out requires a directory");
  }
  const releaseRoot = outRoot ? resolve(outRoot) : join(repoRoot, "packages/desktop/mock-cdn");
  const versionReleaseDir = join(releaseRoot, "releases", version);
  // --collect-only --dist <目录>：不构建，只把 <目录> 里已有的 nex-server-<平台>.tar.gz 收集成发布目录并生成
  // source-server-release.json。CI 的每台构建机只 stage 自己的平台，最后在 release 任务里汇总用。
  if (argv.includes("--collect-only")) {
    const distIndex = argv.indexOf("--dist");
    const distDir = distIndex >= 0 ? argv[distIndex + 1] : undefined;
    if (!distDir || distDir.startsWith("--"))
      throw new Error("--collect-only requires --dist <directory>");
    const collectedOnly = await collectSourceServerRemoteReleaseArchives({
      distReleaseDir: resolve(distDir),
      versionReleaseDir,
      version,
      targets,
    });
    for (const path of collectedOnly) console.log(`[source-server-release] ${path}`);
    return;
  }
  await beginSourceServerRemoteReleaseGeneration({ versionReleaseDir });
  await run("pnpm", ["--filter", "@nex/server-cli", "build"], repoRoot, env);
  for (const [index, target] of targets.entries()) {
    await run(
      "pnpm",
      [
        "--filter",
        "@nex/server-cli",
        "exec",
        "tsx",
        "src/packaging/stageCli.ts",
        "--target",
        target,
        ...(linuxOnly ? ["--skip-component-archives"] : []),
        ...(index === 0 ? [] : ["--skip-agent-build"]),
      ],
      repoRoot,
      env,
    );
  }
  const collected = await collectSourceServerRemoteReleaseArchives({
    distReleaseDir: join(packageRoot, "dist-release"),
    versionReleaseDir,
    version,
    targets,
  });
  for (const path of collected) console.log(`[source-server-release] ${path}`);
}

await main();
