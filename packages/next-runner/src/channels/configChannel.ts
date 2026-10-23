import { readFile } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";
import { getAgentDir } from "@earendil-works/pi-coding-agent";
import type { Logger } from "../logger.js";
import type { ChannelRegistryLike } from "../link/registry.js";
import type { RuntimeConfigManager } from "../config/applyRuntimeConfig.js";
import type { RuntimeProvider, RuntimeConfig } from "../config/runtimeConfig.js";

/**
 * `config` channel (docs/next/config-mcp-skills.md decision 4 + import).
 *
 * Methods:
 * - `apply(frame)` — the same path the pushed `config` frame takes; exists so
 *   the E2E can drive an apply through a call. Not used by the browser.
 * - `status()` — applied version, mode and issues (no secrets).
 * - `exportLocal()` — the runner's own `~/.pi/agent` providers/models, their
 *   API keys (values travel only over the runner link to the server), the
 *   settings defaults and `mcp.json` when present. The server turns the keys into
 *   encrypted secrets and never shows them to the browser.
 */

export interface ConfigChannelOptions {
  manager: RuntimeConfigManager | null;
  logger: Logger;
  /** Test seam: agent dir holding models.json/auth.json/mcp.json. */
  agentDir?: string;
}

interface ModelsJsonProvider {
  name?: string;
  api?: string;
  baseUrl?: string;
  apiKey?: string;
  models?: Array<{
    id?: string;
    name?: string;
    reasoning?: boolean;
    thinkingLevelMap?: Record<string, string | null>;
    contextWindow?: number;
    maxTokens?: number;
    input?: string[];
    cost?: unknown;
  }>;
}

const PI_TO_DOC_API: Record<string, string> = {
  "anthropic-messages": "anthropic-messages",
  "openai-completions": "openai-completions",
  "openai-responses": "openai-responses",
  "azure-openai-responses": "openai-responses",
  "openai-codex-responses": "openai-responses",
};

const DOC_REASONING = ["off", "minimal", "low", "medium", "high"];

export function registerConfigChannel(
  registry: ChannelRegistryLike,
  options: ConfigChannelOptions,
): { appliedFacts: () => Record<string, unknown> | null } {
  const agentDir = options.agentDir ?? getAgentDir();

  registry.call("config", "apply", async (arg) => {
    if (!options.manager) throw new Error("runtime config is not configured on this runner");
    const result = await options.manager.apply(arg);
    return result as unknown as Record<string, unknown>;
  });

  registry.call("config", "status", async () => {
    const manager = options.manager;
    if (!manager) return { configured: false, version: 0, mode: "fallback" };
    const result = manager.appliedResult;
    return {
      configured: true,
      version: manager.appliedVersion,
      mode: manager.current.config.mode,
      ok: result?.ok ?? null,
      errors: result?.errors ?? [],
      facts: result?.facts ?? null,
    };
  });

  registry.call("config", "exportLocal", async () => exportLocal(agentDir, options.logger));

  return { appliedFacts: () => options.manager?.appliedResult?.facts ?? null };
}

/** Reads the runner's own pi config (no secrets are logged). */
export async function exportLocal(
  agentDir: string,
  logger: Logger,
): Promise<Record<string, unknown>> {
  const errors: Array<{ scope: string; path: string; code: string; message: string }> = [];
  const modelsPath = join(agentDir, "models.json");
  const authPath = join(agentDir, "auth.json");
  const settingsPath = join(agentDir, "settings.json");
  const mcpPath = join(agentDir, "mcp.json");

  const models = await readJSON(modelsPath, logger, errors, "models.json");
  const auth = await readJSON(authPath, logger, errors, "auth.json");
  const settings = await readJSON(settingsPath, logger, errors, "settings.json");
  const mcp = await readJSON(mcpPath, logger, errors, "mcp.json");

  const providers: Record<string, unknown> = {};
  const keys: Record<string, string> = {};
  const configured = isRecord(models) && isRecord(models["providers"]) ? models["providers"] : {};
  for (const [id, raw] of Object.entries(configured)) {
    if (!isRecord(raw)) continue;
    const entry = raw as ModelsJsonProvider;
    const api = PI_TO_DOC_API[entry.api ?? ""];
    if (!api) {
      errors.push({
        scope: "import",
        path: `${id}.api`,
        code: "provider-api-unsupported",
        message: `unsupported api ${String(entry.api)}`,
      });
      continue;
    }
    const provider: RuntimeProvider = {
      id,
      name: entry.name ?? id,
      api,
      baseUrl: entry.baseUrl ?? "",
      headers: {},
      modelsJsonModels: [],
      enabledModels: new Set(),
      models: (entry.models ?? []).flatMap((model) => {
        if (!model.id) return [];
        const levels = DOC_REASONING.filter((level) => model.thinkingLevelMap?.[level] !== null);
        return [
          {
            id: model.id,
            ...(model.name ? { name: model.name } : {}),
            reasoning: model.reasoning === true,
            reasoningLevels:
              model.reasoning === true ? (levels.length > 0 ? levels : DOC_REASONING) : ["off"],
            contextWindow: model.contextWindow ?? 128_000,
            maxTokens: model.maxTokens ?? 16_384,
            input: model.input ?? ["text"],
            enabled: true,
          },
        ];
      }),
      configured: false,
    };
    providers[id] = {
      id: provider.id,
      api: provider.api,
      baseUrl: provider.baseUrl,
      name: provider.name,
      enabled: true,
      models: provider.models,
    };
  }
  // auth.json: api keys are `{ type:"api_key", key }`; OAuth entries are not
  // exportable (a refresh token bound to this machine stays local).
  if (isRecord(auth)) {
    for (const [id, credential] of Object.entries(auth)) {
      if (
        isRecord(credential) &&
        credential["type"] === "api_key" &&
        typeof credential["key"] === "string"
      ) {
        keys[id] = credential["key"];
      }
    }
  }
  const settingsDefaults = isRecord(settings)
    ? Object.fromEntries(
        ["defaultProvider", "defaultModel", "defaultThinkingLevel", "enabledModels"]
          .filter((key) => settings[key] !== undefined)
          .map((key) => [key, settings[key]]),
      )
    : {};
  logger.info("exported local pi config", {
    providers: Object.keys(providers).length,
    keys: Object.keys(keys).length,
    mcp: mcp !== null,
  });
  return {
    providers,
    keys,
    settings: settingsDefaults,
    ...(mcp !== null ? { mcp } : {}),
    ...(errors.length > 0 ? { errors } : {}),
    modelsPath,
    authPath,
  };
}

async function readJSON(
  path: string,
  logger: Logger,
  errors: Array<{ scope: string; path: string; code: string; message: string }>,
  label: string,
): Promise<unknown | null> {
  try {
    return JSON.parse(await readFile(path, "utf8"));
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return null;
    logger.warn("local config unreadable", {
      file: label,
      message: error instanceof Error ? error.message : String(error),
    });
    errors.push({
      scope: "import",
      path: label,
      code: "local-config-unreadable",
      message: `${label} could not be parsed`,
    });
    return null;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Kept for the README's documented home-dir fallback. */
export const localAgentDir = (): string => join(homedir(), ".pi", "agent");

export type { RuntimeConfig };
