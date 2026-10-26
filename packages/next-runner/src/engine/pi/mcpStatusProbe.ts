/**
 * P1 MCP status probe (docs/next/config-mcp-skills.md P1 item 5).
 *
 * pi connects the MCP servers of a *session*: the `mcp` extension reads its list on
 * `session_start` and keeps the connections until `session_shutdown`. A settings page
 * that asks for live statuses before any chat exists therefore has nothing to read, and
 * the old callers answered with `connecting` placeholders forever.
 *
 * The probe is pi's own per-session mechanism without a chat: one hidden session per
 * applied MCP list, built with the generation's own `mcp` extension (the same one a
 * chat uses) and the status tracker, then disposed when the list changes and on
 * shutdown (which emits `session_shutdown`, so pi closes its stdio children). The
 * scanner below turns the tracker's samples into a settled answer for a status request
 * ("connect" mode waits briefly for a server that is still starting).
 */
import {
  DefaultResourceLoader,
  SessionManager,
  createAgentSession,
  type AgentSession,
  type ExtensionFactory,
} from "@earendil-works/pi-coding-agent";
import type { McpServerStatus } from "../../config/applyRuntimeConfig.js";
import type { McpWiring } from "../../config/mcpConfig.js";
import type { Logger } from "../../logger.js";
import { createMcpStatusTracker } from "./mcpStatusExtension.js";
import { disposePiSession } from "./disposePiSession.js";

/** A /mcp listing state that cannot change by waiting (settled for a status request). */
export function isSettledState(state: McpServerStatus["state"]): boolean {
  return state === "connected" || state === "error" || state === "disabled";
}

/** How long a status request waits for a server that is still starting. */
export const STATUS_SETTLE_MS = 8_000;
const POLL_MS = 150;

export interface McpProbeOptions {
  workspacePath: string;
  /** The session's agent dir: the one the chat sessions use (pi's own discovery). */
  agentDir: string;
  /** MCP extension + list identity of the applied generation. */
  mcp: McpWiring;
  /** Settings manager of the applied generation (chat sessions use the same one). */
  settingsManager: unknown;
  /** An initialized runtime; the probe session never prompts. */
  modelRuntime: unknown;
  report: (statuses: McpServerStatus[]) => void;
  logger: Logger;
}

export interface McpStatusProbe {
  /** The list the probe was built with (`McpWiring.key`). */
  readonly key: string;
  /** Samples now and resolves once every server has a settled state (or the timeout). */
  statuses(settleMs: number): Promise<McpServerStatus[]>;
  dispose(): Promise<void>;
}

/**
 * Starts the probe session and resolves once its servers connected (or the status is
 * settled). Never throws: a failed probe leaves the status set to the caller.
 */
export async function startMcpStatusProbe(
  options: McpProbeOptions,
): Promise<McpStatusProbe | undefined> {
  const latest: { statuses: McpServerStatus[] } = { statuses: [] };
  const tracker = createMcpStatusTracker({
    commandRunner: { getRegisteredCommands: () => runner.current?.getRegisteredCommands() ?? [] },
    servers: options.mcp.servers.map((server) => ({
      name: server.name,
      transport: "url" in server.config ? "http" : "stdio",
    })),
    report: (statuses) => {
      latest.statuses = statuses;
      options.report(statuses);
    },
    logger: options.logger,
  });
  const runner: { current?: AgentSession["extensionRunner"] } = {};
  const loader = new DefaultResourceLoader({
    cwd: options.workspacePath,
    agentDir: options.agentDir,
    settingsManager: options.settingsManager as never,
    extensionFactories: [options.mcp.extension, tracker.extension] as ExtensionFactory[],
  } as never);
  await loader.reload();
  const { session } = await createAgentSession({
    cwd: options.workspacePath,
    modelRuntime: options.modelRuntime as never,
    sessionManager: SessionManager.inMemory(options.workspacePath),
    resourceLoader: loader,
    // Everything else must be explicit: an omitted manager makes pi create a
    // session file under the agent dir (never here) and read the user's settings.
    settingsManager: options.settingsManager as never,
    agentDir: options.agentDir,
  } as never);
  runner.current = session.extensionRunner;
  await session.bindExtensions({});
  tracker.kick();
  let disposed = false;
  return {
    key: options.mcp.key,
    async statuses(settleMs: number): Promise<McpServerStatus[]> {
      const deadline = Date.now() + settleMs;
      for (;;) {
        tracker.sample();
        const statuses = latest.statuses;
        if (statuses.length > 0 && statuses.every((status) => isSettledState(status.state))) {
          return statuses;
        }
        if (Date.now() >= deadline) return statuses;
        await new Promise((resolve) => setTimeout(resolve, POLL_MS));
      }
    },
    async dispose(): Promise<void> {
      if (disposed) return;
      disposed = true;
      tracker.dispose();
      await disposePiSession(session, "quit");
    },
  };
}
