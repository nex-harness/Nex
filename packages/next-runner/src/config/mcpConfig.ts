/**
 * MCP servers of one server-owned config generation (docs/next/config-mcp-skills.md
 * P1, research §B "MCP").
 *
 * The server pushes `doc.mcp[]` (effective list, secret references already
 * replaced by their values) and `doc.mcpOwned`. Each new session gets:
 *
 *   - `mcpOwned`: one `createMcpExtension({ loadConfig })` loading exactly the
 *     pushed servers (per-OS variants resolved for this runner);
 *   - not owned (no MCP entry in the doc): pi's built-in MCP extension with its
 *     own discovery (`~/.pi/agent/mcp.json`, trusted `.pi/mcp.json`), i.e.
 *     today's behavior — this is what keeps a RemoteBuilder runner usable.
 *
 * The runner verifies what pi accepts (`validateMcpServerConfig`), so a doc
 * written directly by an older server still cannot break a session.
 */

import {
  createMcpExtension,
  type ExtensionFactory,
  type InlineExtension,
  type LoadedMcpConfig,
  type McpServerConfig,
} from "@earendil-works/pi-coding-agent";
import type { Logger } from "../logger.js";

export interface WireMcpServer {
  name: string;
  enabled: boolean;
  exposure?: string;
  timeout?: number;
  transport: WireMcpTransport;
}

export interface WireMcpTransport {
  kind: "stdio" | "http";
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  cwd?: string;
  variants?: Record<string, { command: string; args?: string[]; env?: Record<string, string> }>;
  url?: string;
  headers?: Record<string, string>;
  oauth?: Record<string, unknown>;
  authProvider?: string;
}

export interface McpGeneration {
  /** True when the doc defines any MCP server (the server owns the list). */
  readonly owned: boolean;
  readonly servers: ReadonlyArray<{ name: string; config: McpServerConfig }>;
  readonly issues: ReadonlyArray<{ scope: string; path: string; code: string; message: string }>;
  /**
   * Identity of the effective list (name + resolved pi config), `""` when not owned.
   * pi reads the list once at `session_start`, so two sessions with the same key were
   * built from the same MCP config whatever a later push reports as its version.
   */
  readonly key: string;
}

/** Node's platform name for the doc's variant keys (running on macOS ⇒ darwin). */
export function docOSVariant(platform: NodeJS.Platform = process.platform): string {
  switch (platform) {
    case "darwin":
      return "darwin";
    case "win32":
      return "win32";
    default:
      return "linux";
  }
}

/**
 * Builds the generation from a validated doc. Unusable entries are issues, never
 * a failed apply: the rest of the servers still load.
 */
export function buildMcpGeneration(
  raw: unknown,
  options: { os?: string; logger?: Logger } = {},
): McpGeneration {
  if (!Array.isArray(raw) || raw.length === 0) {
    return Object.freeze({
      owned: false,
      servers: Object.freeze([]),
      issues: Object.freeze([]),
      key: "",
    });
  }
  const os = options.os ?? docOSVariant();
  const issues: McpGeneration["issues"][number][] = [];
  const servers: Array<{ name: string; config: McpServerConfig }> = [];
  const namespaces = new Map<string, string>();
  for (const [index, entry] of raw.entries()) {
    const path = `mcp.${index}`;
    if (!isRecord(entry) || typeof entry["name"] !== "string") {
      issues.push({
        scope: "mcp",
        path,
        code: "mcp-invalid",
        message: "server entry must carry a name",
      });
      continue;
    }
    const server = entry as unknown as WireMcpServer;
    if (!server.enabled) {
      // Disabled servers stay in the doc; nothing connects for them.
      continue;
    }
    const config = toPiConfig(server, os, issues);
    if (!config) continue;
    const namespace = server.name.replace(/-/gu, "_");
    const clash = namespaces.get(namespace);
    if (clash) {
      issues.push({
        scope: "mcp",
        path,
        code: "mcp-namespace-clash",
        message: `${server.name} conflicts with ${clash}`,
      });
      continue;
    }
    const validated = validateEntry(server.name, config);
    if (typeof validated === "string") {
      issues.push({ scope: "mcp", path, code: "mcp-config-invalid", message: validated });
      continue;
    }
    namespaces.set(namespace, server.name);
    servers.push({ name: server.name, config: validated });
  }
  options.logger?.debug("mcp generation built", { servers: servers.length, issues: issues.length });
  return Object.freeze({
    owned: true,
    servers: Object.freeze(servers),
    issues: Object.freeze(issues),
    key: mcpListKey(servers),
  });
}

function toPiConfig(
  server: WireMcpServer,
  os: string,
  issues: McpGeneration["issues"][number][],
): Record<string, unknown> | null {
  const transport = server.transport;
  if (!isRecord(transport)) {
    issues.push({
      scope: "mcp",
      path: server.name,
      code: "mcp-transport-missing",
      message: "transport is required",
    });
    return null;
  }
  const common: Record<string, unknown> = {};
  if (server.exposure) common["exposure"] = server.exposure;
  if (typeof server.timeout === "number" && server.timeout > 0) common["timeout"] = server.timeout;
  if (transport.kind === "http") {
    const config: Record<string, unknown> = { type: "http", url: transport.url ?? "", ...common };
    if (transport.headers && Object.keys(transport.headers).length > 0)
      config["headers"] = { ...transport.headers };
    if (isRecord(transport.oauth) && Object.keys(transport.oauth).length > 0) {
      config["oauth"] = { ...transport.oauth };
    }
    if (transport.authProvider) config["auth"] = { provider: transport.authProvider };
    return config;
  }
  const variant = transport.variants?.[os];
  const command = variant?.command ?? transport.command ?? "";
  const config: Record<string, unknown> = { type: "stdio", command, ...common };
  const args = variant?.args ?? transport.args;
  if (args && args.length > 0) config["args"] = [...args];
  const env = { ...transport.env, ...variant?.env };
  if (Object.keys(env).length > 0) config["env"] = env;
  if (transport.cwd) config["cwd"] = transport.cwd;
  return config;
}

/**
 * The loadConfig closure of one session's MCP extension. It ignores pi's
 * context: the generation is frozen for the session.
 */
export function mcpLoaderFor(generation: McpGeneration): () => LoadedMcpConfig {
  return () => {
    return {
      servers: generation.servers.map((server) => ({
        name: server.name,
        config: server.config,
        source: "nex-server",
        scope: "global" as const,
      })),
      errors: generation.issues.map((issue) => `${issue.path}: ${issue.message}`),
    };
  };
}

/** Stable identity of an effective server list (JSON keeps key order of the config). */
export function mcpListKey(
  servers: ReadonlyArray<{ name: string; config: McpServerConfig }>,
): string {
  return JSON.stringify(servers.map((server) => [server.name, server.config]));
}

/** pi's built-in MCP extension name (default `mcp.json` discovery). */
export const BUILTIN_MCP = "builtin:mcp";

/** Session wiring: the extension entry and the generation it was built from. */
/** One server of an effective list (name plus the pi config it loads). */
export type McpWiringServer = McpGeneration["servers"][number];

export interface McpWiring {
  owned: boolean;
  extension: InlineExtension;
  servers: McpGeneration["servers"];
  /** `McpGeneration.key`: the list this wiring loads at `session_start`. */
  key: string;
  factory?: ExtensionFactory;
}

export interface McpSessionWiring {
  /**
   * Inline extension for the session's `extensionFactories`. The string
   * `"builtin:mcp"` is pi's own MCP extension (default `mcp.json` discovery).
   */
  extension: InlineExtension;
  /** True when the extension is the server-owned one (status tracking applies). */
  owned: boolean;
  /** The server-owned factory (undefined when not owned). */
  factory?: ExtensionFactory;
}

/**
 * The MCP extension of one session: the server-owned extension when the doc has
 * servers, otherwise pi's built-in MCP support (`builtin:mcp`, so the resource
 * loader keeps its default discovery).
 */
export function mcpSessionWiring(generation: McpGeneration): McpWiring {
  if (generation.owned) {
    const factory = createMcpExtension({ loadConfig: mcpLoaderFor(generation) });
    return {
      extension: factory,
      owned: true,
      factory,
      servers: generation.servers,
      key: generation.key,
    };
  }
  return {
    extension: BUILTIN_MCP as unknown as InlineExtension,
    owned: false,
    servers: generation.servers,
    key: generation.key,
  };
}

/**
 * pi's own `validateMcpServerConfig` is not part of the SDK entry point; this is
 * the same rule set (name, transport, exposure, timeout, oauth URLs) so a pushed
 * entry pi would reject is reported instead of failing the session.
 */
export function validateEntry(
  name: string,
  raw: Record<string, unknown>,
): McpServerConfig | string {
  if (!/^[A-Za-z0-9_-]+$/u.test(name)) return `invalid server name "${name}"`;
  const type = raw["type"];
  const exposure = raw["exposure"];
  if (
    exposure !== undefined &&
    !["codemode", "deferred", "direct", "hidden"].includes(String(exposure))
  ) {
    return `server "${name}": exposure must be codemode, deferred, direct or hidden`;
  }
  const timeout = raw["timeout"];
  if (timeout !== undefined && (typeof timeout !== "number" || !(timeout > 0))) {
    return `server "${name}": timeout must be a positive number of seconds`;
  }
  if (type === "http") {
    const url = raw["url"];
    if (
      typeof url !== "string" ||
      !URL.canParse(url) ||
      !/^https?:$/u.test(new URL(url).protocol)
    ) {
      return `server "${name}": url must be an http or https URL`;
    }
    const headers = raw["headers"];
    if (headers !== undefined && !isStringRecord(headers))
      return `server "${name}": headers must map names to strings`;
    return raw as unknown as McpServerConfig;
  }
  if (typeof raw["command"] !== "string" || !raw["command"].trim()) {
    return `server "${name}": command is required`;
  }
  const args = raw["args"];
  if (
    args !== undefined &&
    !(Array.isArray(args) && args.every((arg) => typeof arg === "string"))
  ) {
    return `server "${name}": args must be an array of strings`;
  }
  if (raw["env"] !== undefined && !isStringRecord(raw["env"]))
    return `server "${name}": env must map names to strings`;
  return raw as unknown as McpServerConfig;
}

function isStringRecord(value: unknown): boolean {
  return isRecord(value) && Object.values(value).every((entry) => typeof entry === "string");
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
