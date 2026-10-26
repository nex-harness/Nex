/**
 * P1 MCP status bridge (docs/next/config-mcp-skills.md P1 item 5).
 *
 * pi's own MCP extension owns the connections and already exposes what the
 * server needs: `/mcp` argument completions carry `describeState(server)`, and
 * the MCP tools are visible through `getAllTools()`. This extension opens no
 * connections; it samples those two facts and maps them onto the server's
 * status shape:
 *
 *   "connected · 1 tool"            → state "connected"
 *   "needs sign-in"                 → state "error", failureKind not_authenticated
 *   "failed: …"                     → state "error", failureKind server_unavailable
 *   "starting" (no connection yet)  → state "connecting"
 *   known server without a listing   → state "connecting"
 *
 * Tool names follow pi's `mcp__<namespace>__<tool>` rule (`-` becomes `_`), so a
 * server's tools are counted without reaching into pi's internals.
 */
import type {
  ExtensionAPI,
  ExtensionContext,
  ExtensionFactory,
  RegisteredCommand,
} from "@earendil-works/pi-coding-agent";
import type { McpServerStatus } from "../../config/applyRuntimeConfig.js";
import type { Logger } from "../../logger.js";

/** Command whose completions list the live MCP servers (`/mcp login <server>`). */
const MCP_COMMAND = "mcp";
/** How long to keep sampling after a session start (servers connect in the background). */
export const CONNECT_GRACE_MS = 12_000;
/** Delay before the first connection check (the session's own start runs first). */
const KICK_DELAY_MS = 2_000;
const SAMPLE_INTERVAL_MS = 250;

export function mcpNamespace(name: string): string {
  return name.replace(/-/gu, "_");
}

/** Maps pi's `describeState(server)` text onto the server's status vocabulary. */
export function statusFromCompletion(
  name: string,
  transport: "stdio" | "http",
  completion: string,
  tools: string[],
): McpServerStatus {
  const lower = completion.toLowerCase();
  const toolNames = tools.filter((tool) => tool.startsWith(`mcp__${mcpNamespace(name)}__`));
  const base = {
    name,
    transport,
    toolCount: toolNames.length,
    ...(toolNames.length > 0 ? { toolNames } : {}),
  };
  if (lower.includes("needs sign-in") || lower.includes("needs-auth")) {
    return { ...base, state: "error", failureKind: "not_authenticated", error: "sign-in required" };
  }
  if (lower.includes("failed")) {
    return {
      ...base,
      state: "error",
      failureKind: "server_unavailable",
      error: completion.replace(/^[^:]*:\s*failed:?\s*/iu, "").slice(0, 300) || "connection failed",
    };
  }
  if (lower.includes("connecting") || lower.includes("starting")) {
    return { ...base, state: "connecting" };
  }
  if (lower.includes("connected")) return { ...base, state: "connected" };
  if (lower.includes("disabled")) return { ...base, state: "disabled" };
  return { ...base, state: "connecting" };
}

export interface McpStatusTracker {
  extension: ExtensionFactory;
  /** Samples and reports the current state now (configApplied / status requests). */
  sample: () => void;
  /**
   * Starts the background connection check: a session's servers connect in the
   * background, but a draft in the composer is only sampled on its first turn.
   * Reconnects (through the `mcp` extension's own command) a server that is
   * enabled yet shows no tool under its namespace, then samples.
   */
  kick: () => void;
  /** Stops the sampler (a draft that adopts a newer MCP list). */
  dispose: () => void;
}

export interface McpStatusTrackerOptions {
  servers: ReadonlyArray<{ name: string; transport: "stdio" | "http" }>;
  report: (statuses: McpServerStatus[]) => void;
  logger: Logger;
  /** The session's extension runner (its `/mcp` command carries the handler). */
  commandRunner?: CommandRunner;
}

/** The slice of pi's ExtensionRunner the tracker reads. */
export interface CommandRunner {
  getRegisteredCommands(): Array<{ name: string; handler?: unknown; invocationName?: string }>;
}

export function createMcpStatusTracker(options: McpStatusTrackerOptions): McpStatusTracker {
  const names = options.servers.map((server) => server.name);
  const transports = new Map(options.servers.map((server) => [server.name, server.transport]));
  let pi: ExtensionAPI | undefined;
  const commandRunner = options.commandRunner;
  let lastJson = "";
  let deadline = 0;
  let timer: NodeJS.Timeout | undefined;

  const piCommand = (): RegisteredCommand | undefined => {
    const runner = commandRunner;
    const command = runner
      ?.getRegisteredCommands()
      .find((entry) => (entry.invocationName ?? entry.name) === MCP_COMMAND);
    return command ? (command as unknown as RegisteredCommand) : undefined;
  };

  /**
   * pi's own read-only state per server: the `/mcp reconnect <server>` argument
   * completions carry `describeState(server)` ("connected · 1 tool", "needs sign-in",
   * "failed: …", "starting") without touching the connection.
   *
   * 修复：此前每 250 ms 通过执行 `/mcp reconnect <name>` 读取状态，而 reconnect 会关闭并重开
   * 连接，导致服务器永远停留在 "connecting"、工具始终未注册（模型调用得到 "Tool not found"）。
   * 改为只读的参数补全：它直接返回 describeState，不触发任何连接操作。
   */
  const completions = async (): Promise<Map<string, string>> => {
    const found = new Map<string, string>();
    const complete = piCommand()?.getArgumentCompletions;
    if (typeof complete !== "function") return found;
    for (const name of names) {
      try {
        const items = (await complete(`reconnect ${name}`)) ?? [];
        const item = items.find((entry) => entry.label === name);
        if (item?.description) found.set(name, item.description);
      } catch {
        // A failing completion only means no state for this sample.
      }
    }
    return found;
  };

  const sample = async (): Promise<void> => {
    if (!pi) return;
    const found = await completions();
    let toolNames: string[] = [];
    try {
      toolNames = pi.getAllTools().map((tool) => tool.name);
    } catch {
      toolNames = [];
    }
    const statuses = names.map((name): McpServerStatus => {
      const status = statusFromCompletion(
        name,
        transports.get(name) ?? "stdio",
        found.get(name) ?? "",
        toolNames,
      );
      // A tool listed under this server's namespace proves the connection: pi
      // registers tools only after `listTools` succeeded.
      return status.state === "connecting" && status.toolCount > 0
        ? { ...status, state: "connected" }
        : status;
    });
    const encoded = JSON.stringify(statuses);
    if (encoded === lastJson) return;
    lastJson = encoded;
    options.report(statuses);
  };

  const ensureSampling = () => {
    deadline = Date.now() + CONNECT_GRACE_MS;
    if (timer) return;
    timer = setInterval(() => {
      void sample();
      if (Date.now() >= deadline && timer) {
        clearInterval(timer);
        timer = undefined;
      }
    }, SAMPLE_INTERVAL_MS);
    timer.unref?.();
  };

  const extension: ExtensionFactory = (api: ExtensionAPI) => {
    pi = api;
    api.on("session_start", (_event: unknown, ctx: ExtensionContext) => {
      void ctx;
      options.logger.debug("mcp status tracking started", { servers: names.length });
      ensureSampling();
      void sample();
    });
    api.on("session_shutdown", () => {
      if (timer) clearInterval(timer);
      timer = undefined;
    });
    api.on("before_agent_start", async () => {
      // The first prompt starts right after this (same tick): make sure the last
      // status is in before the run's tool loadout is fixed.
      await sample();
    });
    api.on("turn_start", () => {
      // A server may have become reachable between turns.
      ensureSampling();
      void sample();
    });
  };

  // pi connects every enabled server itself on `session_start`; the tracker only
  // observes. 修复：此前 kick 会对"还没有工具"的服务器执行 `/mcp reconnect`，在慢启动的
  // stdio 服务器上会打断正在进行的连接，使其反复回到 connecting。
  const kick = () => {
    setTimeout(() => {
      ensureSampling();
      void sample();
    }, KICK_DELAY_MS);
  };

  const dispose = () => {
    if (timer) clearInterval(timer);
    timer = undefined;
  };

  return { extension, sample: () => void sample(), kick, dispose };
}
