import type { Logger } from "../logger.js";
import { modelsJsonModel } from "./modelsJson.js";
import { buildMcpGeneration } from "./mcpConfig.js";
import { REASONING_ORDER } from "./wireFrame.js";
import type { ConfigFrame, ConfigIssue, WireModel } from "./wireFrame.js";
import { envVarFor } from "./wireFrame.js";
import type { RuntimeConfig, RuntimeProvider } from "./runtimeTypes.js";

export { REASONING_ORDER, RUNTIME_CONFIG_SCHEMA } from "./wireFrame.js";
export { ConfigRejectedError, envVarFor, validateFrame } from "./wireFrame.js";
export type {
  BundleRefWire,
  ConfigFrame,
  ConfigIssue,
  WireBundle,
  WireDoc,
  WireModel,
  WireProvider,
} from "./wireFrame.js";
export type { RuntimeConfig, RuntimeProvider } from "./runtimeTypes.js";

/**
 * Server-owned runtime config (spec docs/next/config-mcp-skills.md decisions 4-5,
 * research §C3/C4).
 *
 * Wire shape of one S→R `config` frame:
 *
 *   { t:"config", version, doc:{schema, version, owned, providers[], settings?},
 *     secrets:{id→value}, bundles:[] }
 *
 * Pure module: validation, models.json derivation and the immutable
 * RuntimeConfig value. Applying it to pi lives in `applyRuntimeConfig.ts`.
 */

export function buildRuntimeConfig(frame: ConfigFrame, logger: Logger): RuntimeConfig {
  const issues: ConfigIssue[] = [];
  const providers: RuntimeProvider[] = [];
  const credentials = new Map<string, string>();
  const envRefs: Record<string, string> = {};
  const enabledProviders = frame.doc.providers.filter((provider) => provider.enabled !== false);
  const owned = frame.doc.owned && enabledProviders.length > 0;
  if (frame.doc.owned && enabledProviders.length === 0) {
    logger.warn("config doc owns the runner but lists no enabled provider; keeping fallback mode");
  }
  // MCP ownership is independent of provider ownership: a doc with MCP servers
  // but no providers still owns the server list (providers stay in fallback).
  const mcp = buildMcpGeneration(frame.doc.mcpOwned ? frame.doc.mcp : undefined, { logger });
  issues.push(
    ...mcp.issues.map((issue) => ({
      scope: "mcp",
      path: issue.path,
      code: issue.code,
      message: issue.message,
    })),
  );
  const common = {
    version: frame.version,
    credentials: new Map(),
    issues: Object.freeze([...issues]),
    env: Object.freeze({}),
    mcp,
    bundles: Object.freeze([...frame.bundles]),
    skills: Object.freeze([...(frame.doc.skills ?? [])]),
    prompts: Object.freeze([...(frame.doc.prompts ?? [])]),
    warnings: Object.freeze([...frame.warnings]),
    skillsEnabled: frame.doc.settings?.["enableSkillCommands"] !== false,
  };
  if (!owned) {
    return Object.freeze({
      ...common,
      owned: false,
      mode: "fallback",
      providers: Object.freeze([]),
      settings: Object.freeze({ ...frame.doc.settings }),
    });
  }
  for (const provider of enabledProviders) {
    const secretId = `sec_prov_${provider.id}_apikey`;
    const key = frame.secrets[secretId];
    if (!key) {
      // The server withheld this provider's key (runner-scope disable or a
      // missing secret): report it, never fail the whole apply.
      issues.push({
        scope: "provider",
        path: `${provider.id}.apiKey`,
        code: "secret-not-provisioned",
        message: `no API key received for provider ${provider.id}`,
      });
    }
    const envVar = envVarFor(provider.id);
    const withKey = typeof key === "string" && key.length > 0;
    if (withKey) credentials.set(provider.id, key);
    const models = (provider.models ?? []).filter((model) => model.enabled !== false);
    providers.push(
      Object.freeze({
        id: provider.id,
        name: provider.name?.trim() || provider.id,
        api: provider.api,
        baseUrl: provider.baseUrl,
        headers: { ...provider.headers },
        ...(withKey ? { envVar } : {}),
        modelsJsonModels: Object.freeze(
          provider.models
            ?.filter((m) => m.enabled !== false)
            .map((model) => modelsJsonModel(model, provider.api, provider.baseUrl)) ?? [],
        ),
        enabledModels: new Set(models.map((model) => model.id)),
        models,
        configured: withKey,
        ...(withKey ? {} : { error: "secret-not-provisioned" }),
      }),
    );
    if (withKey) envRefs[envVar] = key;
  }
  const settings = { ...frame.doc.settings };
  if (!settings["defaultProvider"] && providers.length > 0) {
    const first = providers.find((provider) => provider.models.length > 0);
    if (first) {
      settings["defaultProvider"] = first.id;
      settings["defaultModel"] = first.models[0]?.id;
    }
  }
  logger.debug("runtime config built", {
    mcp: mcp.servers.length,
    bundles: frame.bundles.length,
    version: frame.version,
    providers: providers.length,
    models: providers.reduce((total, provider) => total + provider.models.length, 0),
  });
  return Object.freeze({
    ...common,
    owned: true,
    mode: "server",
    providers: Object.freeze(providers),
    settings: Object.freeze(settings),
    credentials,
    issues: Object.freeze([...issues]),
    env: Object.freeze(envRefs),
  });
}
