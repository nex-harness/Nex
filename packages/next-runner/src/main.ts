import { createLogger, parseLogLevel } from "./logger.js";
import {
  resolveConfig,
  resolveRunnerId,
  runnerPlatform,
  runnerArch,
  seedAgentDirFromEnv,
} from "./config.js";
import { ChannelRegistry } from "./link/registry.js";
import { RunnerLink } from "./link/client.js";
import { RUNNER_CAPABILITIES } from "./link/frames.js";
import { PiEngine } from "./engine/pi/piEngine.js";
import { AttachmentRegistry } from "./attachments/registry.js";
import { ConversationPublisher } from "./v4/publisher.js";
import { registerChannels } from "./channels/register.js";
import { RuntimeConfigManager } from "./config/applyRuntimeConfig.js";
import { ConfigExecutor } from "./config/configAck.js";
import { registerConfigChannel } from "./channels/configChannel.js";
import { registerWorkspaceTools } from "./channels/workspaceTools.js";
import { registerE2EProvider } from "./extensions/e2eProvider.js";
import { WorkspaceRoots } from "./workspaces/workspaceRoots.js";
import { publishWorkspaceFrames } from "./workspaces/workspaceFrames.js";
import { bundleDir, currentBuildId } from "./update/buildId.js";
import { rollbackPendingUpdate } from "./update/rollback.js";
import { SelfUpdater } from "./update/selfUpdate.js";

async function main(): Promise<void> {
  // E2E only: seed the isolated agent dir before pi reads it.
  await seedAgentDirFromEnv(process.env);
  const config = resolveConfig(process.env);
  const logger = createLogger(parseLogLevel(config.logLevel), "runner");
  // 自更新回滚必须先于一切业务初始化（docs/next/runner-update.md 决策 5）：
  // 上一次更新后崩溃的进程在这里把 .prev/ 恢复回来，避免带着坏代码继续跑。
  const installDir = bundleDir();
  await rollbackPendingUpdate({ installDir, logger: logger.child("update") });
  // 运行中的 runner 不读 build.json，而是对自己安装目录里的三个产物重算哈希；
  // 从源码（tsx）启动时文件不存在，报告 "dev" 并禁用自更新。
  const buildId = await currentBuildId(installDir);
  // No-op outside the E2E stack (marker file absent).
  registerE2EProvider(logger.child("config"));
  const runnerId = await resolveRunnerId(config.dataDir, config.runnerId);
  // Served folders are runner-owned (docs/next/runner-folders.md decision 1):
  // env roots + persisted folders, read before any channel is built so the guard,
  // the file-tools root check and the hello all share one live owner.
  const roots = new WorkspaceRoots({
    envRoots: config.workspaces,
    dataDir: config.dataDir,
    logger: logger.child("workspaces"),
  });
  await roots.load();
  // One ref authority for the process (spec A): the engine feeds pi from it and the v4
  // runtime posts each session's listings into it, so a read and the sweep see one truth.
  const attachments = new AttachmentRegistry({ dataDir: config.dataDir });
  // Server-owned config (docs/next/config-mcp-skills.md decision 4-5): the
  // engine reads the latest generation for new sessions; the link pushes it.
  const runtimeConfig = new RuntimeConfigManager({
    dataDir: config.dataDir,
    logger: logger.child("config"),
  });
  const engine = new PiEngine({
    dataDir: config.dataDir,
    logger: logger.child("engine"),
    attachments,
    runtimeConfig,
    // P1: the hidden MCP status session opens in the first served workspace.
    // It stays on the env roots: the probe is a startup-time status session, and a
    // folder added later must not move it.
    workspacePaths: config.workspaces,
  });
  // P1: an MCP status change becomes a fact right away (the settings list polls
  // the runner's `mcp-sync.listWorkspaceMcpServerStatuses` for live state).
  await engine.init();
  const publisher = new ConversationPublisher({
    engine,
    logger: logger.child("v4"),
    attachments,
  });
  const registry = new ChannelRegistry();
  const channels = registerChannels(registry, {
    engine,
    publisher,
    logger,
    roots,
    runnerId,
  });
  registerConfigChannel(registry, { manager: runtimeConfig, logger: logger.child("config") });
  // Batch 1 workspace tools (docs/next/workspace-tools.md): file/git/watcher/
  // media/terminal/system channels on this runner, behind the same workspace
  // guard. Registered on the raw registry: the guard wraps calls per handler
  // inside (path containment is stricter than the workspacePath check).
  const workspaceTools = registerWorkspaceTools(registry, {
    logger: logger.child("tools"),
    roots,
  });
  // `configApplied.facts` for the server's model-selection view: server-owned
  // mode reports the applied runtime, fallback mode reports pi's own catalog
  // (decision 2), so the composer works in both modes without a forward.
  const configExecutor = new ConfigExecutor({
    manager: runtimeConfig,
    catalog: () => engine.catalog(),
    providerConfigured: (providerId) =>
      engine.requireRuntime().getProviderAuthStatus(providerId).configured,
    logger: logger.child("config"),
  });

  const link = new RunnerLink({
    url: config.serverUrl,
    token: config.token,
    logger: logger.child("link"),
    registry,
    hello: {
      runnerId,
      name: config.name,
      version: config.version,
      os: runnerPlatform,
      arch: runnerArch,
      // 决策 2：buildId 与 capabilities 让服务端/UI 判断这台机器支持哪些能力、
      // 是否需要推送更新；旧 runner 没有这两个字段，服务端按 legacy 处理。
      buildId,
      capabilities: [...RUNNER_CAPABILITIES],
      // Read at (re)connect time so a folder added while the link was down is
      // reported by the next hello (docs/next/runner-folders.md decision 1).
      workspaces: () => roots.list().map((folder) => ({ path: folder.path, label: folder.label })),
    },
    config: {
      lastAppliedVersion: () => runtimeConfig.appliedVersion,
      apply: async (frame) => {
        const result = await configExecutor.apply(frame);
        // New sessions adopt the applied generation; running sessions keep theirs.
        if (result.ok) {
          engine.adoptRuntimeConfig();
          // P1: warm the status set so a status query right after the push does
          // not wait for the next session. A new MCP list replaces the hidden
          // status session (the old one closes its stdio children on shutdown).
          void engine.requestMcpStatus(0);
        }
        return result;
      },
      applied: (result) => {
        link.sendConfigApplied(result);
      },
    },
    // 决策 5：拉取式自更新。这两个回调只会在 link.start() 之后触发，
    // 那时下面的 updater 已完成构造。
    update: {
      handle: (targetBuildId) => {
        void updater.handleUpdate(targetBuildId);
      },
      welcomed: () => {
        // 第一次 welcome 说明新 build 已连上服务端，可以清掉待定标记。
        void updater.markStartupComplete();
      },
    },
  });

  const updater = new SelfUpdater({
    installDir,
    buildId,
    serverUrl: config.serverUrl,
    token: config.token,
    logger: logger.child("update"),
    sendStatus: (status) => link.sendUpdateStatus(status),
    stopLink: () => link.stop(),
  });

  // Push the full served list after an add/remove (docs/next/runner-folders.md
  // decision 3). Only while connected: a reconnect's hello already carries it.
  publishWorkspaceFrames(roots, link);

  const shutdown = async (signal: string): Promise<void> => {
    logger.info("runner shutting down", { signal });
    await link.stop();
    await runtimeConfig.dispose();
    channels.disposed();
    await workspaceTools.dispose();
    publisher.dispose();
    await engine.dispose();
    process.exit(0);
  };
  process.on("SIGINT", () => void shutdown("SIGINT"));
  process.on("SIGTERM", () => void shutdown("SIGTERM"));

  logger.info("runner starting", {
    runnerId,
    serverUrl: config.serverUrl,
    workspaces: roots.list().map((folder) => folder.path),
    dataDir: config.dataDir,
    buildId,
  });
  link.start();
}

main().catch((error: unknown) => {
  process.stderr.write(
    `[runner] fatal: ${error instanceof Error ? (error.stack ?? error.message) : String(error)}\n`,
  );
  process.exit(1);
});
