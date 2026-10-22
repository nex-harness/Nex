import { createLogger, parseLogLevel } from "./logger.js";
import {
  resolveConfig,
  resolveRunnerId,
  labelForWorkspace,
  runnerPlatform,
  runnerArch,
} from "./config.js";
import { ChannelRegistry } from "./link/registry.js";
import { RunnerLink } from "./link/client.js";
import { PiEngine } from "./engine/pi/piEngine.js";
import { AttachmentRegistry } from "./attachments/registry.js";
import { ConversationPublisher } from "./v4/publisher.js";
import { registerChannels } from "./channels/register.js";

async function main(): Promise<void> {
  const config = resolveConfig(process.env);
  const logger = createLogger(parseLogLevel(config.logLevel), "runner");
  const runnerId = await resolveRunnerId(config.dataDir, config.runnerId);
  // One ref authority for the process (spec A): the engine feeds pi from it and the v4
  // runtime posts each session's listings into it, so a read and the sweep see one truth.
  const attachments = new AttachmentRegistry({ dataDir: config.dataDir });
  const engine = new PiEngine({
    dataDir: config.dataDir,
    logger: logger.child("engine"),
    attachments,
  });
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
  });

  const shutdown = async (signal: string): Promise<void> => {
    logger.info("runner shutting down", { signal });
    await link.stop();
    channels.disposed();
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
