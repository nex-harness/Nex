/**
 * P1 MCP status operations of the engine (docs/next/config-mcp-skills.md P1 item 5).
 *
 * Extracted from `piEngine.ts` to keep that file under the 400-line cap. These
 * methods only read/write the runtime-config manager and the open-session map;
 * the engine forwards to them so its surface stays unchanged.
 */
import { getAgentDir, SettingsManager, type ModelRuntime } from "@earendil-works/pi-coding-agent";
import type { McpServerStatus } from "../../config/applyRuntimeConfig.js";
import {
  mcpSessionWiring,
  type McpGeneration,
  type McpWiringServer,
} from "../../config/mcpConfig.js";
import type { PiEngineOptions } from "./piEngineOptions.js";
import type { OpenSession } from "./sessionLifecycle.js";
import { loadPiResources, type PiResourceCatalog } from "./piResources.js";
import { isSettledState, startMcpStatusProbe, STATUS_SETTLE_MS } from "./mcpStatusProbe.js";

/** Live MCP statuses of the newest session (P1). */
export interface McpStatusSnapshot {
  owned: boolean;
  statuses: Record<string, McpServerStatus>;
}

export interface McpStatusOpsDeps {
  options: PiEngineOptions;
  sessions: Map<string, OpenSession>;
  init: () => Promise<void>;
  settingsManager: () => SettingsManager | null;
  /** The engine's runtime of the current generation (initialized by `init`). */
  runtime: () => ModelRuntime;
}

export function createMcpStatusOps(deps: McpStatusOpsDeps) {
  /** Newest session that reported MCP statuses (P1). */
  let latestMcpSessionId: string | undefined;
  /** The hidden status session of one applied MCP list (see `mcpStatusProbe.ts`). */
  let probe: { key: string; session: ReturnType<typeof startMcpStatusProbe> } | undefined;
  let probeStopping: Promise<void> | undefined;

  return {
    /**
     * P1: a session's MCP statuses become the runner's live status set (the newest
     * session wins) and the facts the server reads from `configApplied`.
     */
    reportMcpStatus(workspacePath: string, sessionId: string, statuses: McpServerStatus[]): void {
      const manager = deps.options.runtimeConfig;
      if (!manager) return;
      latestMcpSessionId = sessionId;
      manager.mcpStatus.replace(statuses);
      const open = deps.sessions.get(sessionId);
      manager.latestMcpSession = {
        workspacePath,
        sessionId,
        generation: manager.appliedVersion,
        ...(open?.mcpTracker
          ? { sample: () => open.mcpTracker?.sample() }
          : { sample: () => undefined }),
      };
    },

    /**
     * P1: the live `mcp-sync` statuses.
     *
     * 修复依据：此前状态查询只读"最新会话"的采样；在第一个会话出现之前没有任何 tracker，
     * 于是给文档里的每个服务器返回 "connecting" 占位符 —— 设置页因此永远停在 connecting、
     * 工具数为 0，即使服务器本身可用。现在状态查询按生效列表（`McpWiring.key`）保证有一个
     * 隐藏的状态会话：它就是聊天会话用的同一个 pi `mcp` 扩展，只是不产生聊天记录；列表
     * 变化时旧会话按 `session_shutdown` 正常关闭（pi 因此关闭 stdio 子进程）。已经打开、
     * 用同一列表构建的聊天会话优先，省掉一次多余连接。
     *
     * `settleMs > 0` 时（Settings 的 "connect" 模式）等待仍处于 connecting 的服务器；会话
     * 里还在进行的那一轮不受影响（隐藏会话从不提示模型）。
     */
    async requestMcpStatus(settleMs = STATUS_SETTLE_MS): Promise<McpStatusSnapshot> {
      const manager = deps.options.runtimeConfig;
      if (!manager) return { owned: false, statuses: {} };
      const generation = manager.current.config.mcp;
      // A chat built from this very list is the cheapest probe: sample it first.
      const chat = newestSessionWith(deps.sessions, generation.key);
      chat?.mcpTracker?.sample();
      const sampled = () => merge(generation.servers, manager.mcpStatus.list(), false);
      if (chat?.mcpTracker && allSettled(sampled())) {
        return { owned: generation.owned, statuses: sampled() };
      }
      // No chat with this list (or one still connecting): the hidden status session
      // connects exactly what the next chat would, and is what Settings reads.
      const probed = await ensureProbe(generation, settleMs);
      if (probed) return { owned: generation.owned, statuses: probed };
      return {
        owned: generation.owned,
        statuses: chat?.mcpTracker ? sampled() : merge(generation.servers, [], true),
      };
    },

    /** Stops the hidden status session (config change, shutdown). */
    disposeStatusProbe(): Promise<void> {
      return stopProbe();
    },

    /** Synchronous view (facts): newest chat sample, placeholders when nothing reported yet. */
    refreshMcpStatus(): McpStatusSnapshot {
      const manager = deps.options.runtimeConfig;
      if (!manager) return { owned: false, statuses: {} };
      const newest = latestMcpSessionId ? deps.sessions.get(latestMcpSessionId) : undefined;
      if (newest?.mcpTracker) newest.mcpTracker.sample();
      const config = manager.current.config;
      return {
        owned: config.mcp.owned,
        statuses: merge(config.mcp.servers, manager.mcpStatus.list(), true),
      };
    },

    /**
     * P2: pi's skills and prompt templates as the next session would load them
     * (fallback discovery or the doc's bundles).
     */
    async piResources(workspacePath: string): Promise<PiResourceCatalog> {
      await deps.init();
      const resources = deps.options.runtimeConfig?.resources();
      return loadPiResources({
        workspacePath,
        settingsManager: deps.settingsManager() ?? SettingsManager.create(workspacePath),
        ...(resources?.skillPaths.length ? { additionalSkillPaths: resources.skillPaths } : {}),
        ...(resources?.promptPaths.length
          ? { additionalPromptTemplatePaths: resources.promptPaths }
          : {}),
      });
    },

    /** P2: pi `enableSkillCommands` of the applied generation (default true). */
    appliedConfig(): { skillsEnabled: boolean } {
      return { skillsEnabled: deps.options.runtimeConfig?.current.config.skillsEnabled !== false };
    },
  };

  /** Disposes the hidden status session (a newer MCP list, or shutdown). */
  async function stopProbe(): Promise<void> {
    const current = probe;
    probe = undefined;
    if (!current) return;
    const stopping = current.session.then((started) => started?.dispose()).catch(() => undefined);
    probeStopping = stopping.then(() => undefined);
    await probeStopping;
    probeStopping = undefined;
  }

  /**
   * Starts (or keeps) the hidden status session of a list and returns its statuses;
   * `undefined` when no workspace is declared or the session could not be built.
   */
  async function ensureProbe(
    generation: McpGeneration,
    settleMs: number,
  ): Promise<Record<string, McpServerStatus> | undefined> {
    if (!generation.owned || generation.servers.length === 0) {
      await stopProbe();
      return undefined;
    }
    await deps.init();
    if (probe?.key !== generation.key) await stopProbe();
    const workspacePath = deps.options.workspacePaths?.[0];
    if (!workspacePath) return undefined;
    if (!probe) {
      const started = startMcpStatusProbe({
        workspacePath,
        agentDir: getAgentDir(),
        mcp: mcpSessionWiring(generation),
        settingsManager: deps.settingsManager() ?? SettingsManager.create(workspacePath),
        modelRuntime: deps.runtime(),
        report: (statuses) => deps.options.runtimeConfig?.mcpStatus.replace(statuses),
        logger: deps.options.logger,
      }).catch((error: unknown) => {
        deps.options.logger.warn("mcp status probe failed", {
          message: error instanceof Error ? error.message : String(error),
        });
        return undefined;
      });
      probe = { key: generation.key, session: started };
    }
    const started = await probe.session;
    if (!started) return undefined;
    const statuses: Record<string, McpServerStatus> = {};
    for (const status of await started.statuses(settleMs)) statuses[status.name] = status;
    return Object.keys(statuses).length > 0 ? statuses : undefined;
  }
}

/**
 * The session that will answer a status request: a chat session built from this MCP
 * list, searched newest first (the engine's session map keeps insertion order).
 */
function newestSessionWith(
  sessions: Map<string, OpenSession>,
  key: string,
): OpenSession | undefined {
  for (const open of [...sessions.values()].reverse()) {
    if (open.mcpTracker && (open.mcpStartedWith?.key ?? "") === key) return open;
  }
  return undefined;
}

/** Every server has a state that a status request does not need to wait for. */
function allSettled(statuses: Record<string, McpServerStatus>): boolean {
  return Object.values(statuses).every((status) => isSettledState(status.state));
}

/**
 * The status map of the doc's servers: live samples win; `placeholders` fills a server
 * that has not reported yet with "connecting" (never a stale entry from another list).
 */
function merge(
  servers: ReadonlyArray<McpWiringServer>,
  live: readonly McpServerStatus[],
  placeholders: boolean,
): Record<string, McpServerStatus> {
  const byName = new Map(live.map((status) => [status.name, status]));
  const statuses: Record<string, McpServerStatus> = {};
  for (const server of servers) {
    const status = byName.get(server.name);
    if (status) statuses[server.name] = status;
    else if (placeholders) {
      statuses[server.name] = {
        name: server.name,
        transport: "url" in server.config ? "http" : "stdio",
        state: "connecting",
        toolCount: 0,
      };
    }
  }
  return statuses;
}
