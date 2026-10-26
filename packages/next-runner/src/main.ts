import { createLogger, parseLogLevel } from "./logger.js";
import {
  resolveConfig,
  resolveRunnerId,
  labelForWorkspace,
  runnerPlatform,
  runnerArch,
  seedAgentDirFromEnv,
} from "./config.js";
import { ChannelRegistry } from "./link/registry.js";
import { RunnerLink } from "./link/client.js";
import { PiEngine } from "./engine/pi/piEngine.js";
import { AttachmentRegistry } from "./attachments/registry.js";
import { ConversationPublisher } from "./v4/publisher.js";
import { registerChannels } from "./channels/register.js";
import { RuntimeConfigManager } from "./config/applyRuntimeConfig.js";
import { ConfigExecutor } from "./config/configAck.js";
import { registerConfigChannel } from "./channels/configChannel.js";
import { registerWorkspaceTools } from "./channels/workspaceTools.js";
import { registerE2EProvider } from "./extensions/e2eProvider.js";

async function main(): Promise<void> {
  // E2E only: seed the isolated agent dir before pi reads it.
  await seedAgentDirFromEnv(process.env);
  const config = resolveConfig(process.env);
  const logger = createLogger(parseLogLevel(config.logLevel), "runner");
  // No-op outside the E2E stack (marker file absent).
  registerE2EProvider(logger.child("config"));
  const runnerId = await resolveRunnerId(config.dataDir, config.runnerId);
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
    workspacePaths: config.workspaces,
  });
  registerConfigChannel(registry, { manager: runtimeConfig, logger: logger.child("config") });
  // Batch 1 workspace tools (docs/next/workspace-tools.md): file/git/watcher/
  // media/terminal/system channels on this runner, behind the same workspace
  // guard. Registered on the raw registry: the guard wraps calls per handler
  // inside (path containment is stricter than the workspacePath check).
  const workspaceTools = registerWorkspaceTools(registry, {
    logger: logger.child("tools"),
    workspacePaths: config.workspaces,
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
      workspaces: config.workspaces.map((path) => ({
        path,
        label: labelForWorkspace(path),
      })),
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
  });

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
    workspaces: config.workspaces,
    dataDir: config.dataDir,
  });
  link.start();
}

main().catch((error: unknown) => {
  process.stderr.write(
    `[runner] fatal: ${error instanceof Error ? (error.stack ?? error.message) : String(error)}\n`,
  );
  process.exit(1);
});
