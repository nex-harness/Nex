import type { Logger } from "../logger.js";
import { modelsJsonModel } from "./modelsJson.js";
import { asString, asStringRecord, isRecord, positive } from "./runtimeSettings.js";

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

export const RUNTIME_CONFIG_SCHEMA = 1;

const API_TYPES = new Set(["anthropic-messages", "openai-completions", "openai-responses"]);
export const REASONING_ORDER = ["off", "minimal", "low", "medium", "high"];

export interface WireModel {
  id: string;
  name?: string;
  reasoning?: boolean;
  reasoningLevels?: string[];
  contextWindow?: number;
  maxTokens?: number;
  input?: string[];
  cost?: { input?: number; output?: number; cacheRead?: number; cacheWrite?: number };
  enabled?: boolean;
}

export interface WireProvider {
  id: string;
  api: string;
  baseUrl: string;
  headers?: Record<string, string>;
  name?: string;
  enabled?: boolean;
  models?: WireModel[];
  /** Server-side hint only; the runner recomputes it from `secrets`. */
  authConfigured?: boolean;
}

export interface WireDoc {
  schema: number;
  version: number;
  owned: boolean;
  providers: WireProvider[];
  settings?: Record<string, unknown>;
}

export interface ConfigFrame {
  version: number;
  doc: WireDoc;
  secrets: Record<string, string>;
}

export interface ConfigIssue {
  scope: string;
  path: string;
  code: string;
  message: string;
}

/** One pi provider definition for models.json. Credential-blind by design. */
export interface RuntimeProvider {
  id: string;
  name: string;
  api: string;
  baseUrl: string;
  headers: Record<string, string>;
  /** models.json `apiKey` reference: `$ENV_VAR` (the runner exports the value). */
  envVar?: string;
  modelsJsonModels: ReadonlyArray<Record<string, unknown>>;
  /** The doc's per-model enabled flags (pi has no per-model disable). */
  enabledModels: Set<string>;
  models: WireModel[];
  configured: boolean;
  error?: string;
}

export interface RuntimeConfig {
  readonly version: number;
  readonly owned: boolean;
  /** `fallback`: no server providers — the runner keeps its own ~/.pi/agent. */
  readonly mode: "fallback" | "server";
  readonly providers: readonly RuntimeProvider[];
  readonly settings: Readonly<Record<string, unknown>>;
  /** Secret values in this generation (provider id → key), never persisted. */
  readonly credentials: ReadonlyMap<string, string>;
  readonly issues: readonly ConfigIssue[];
  /** Env var name holding each provider key (exported to tools, never logged). */
  readonly env: Readonly<Record<string, string>>;
}

export class ConfigRejectedError extends Error {
  readonly issues: ConfigIssue[];
  constructor(message: string, issues: ConfigIssue[] = []) {
    super(message);
    this.name = "ConfigRejectedError";
    this.issues = issues;
  }
}

/** Stable env var name for one provider's key: `NEX_PROVIDER_<ID>_API_KEY`. */
export function envVarFor(providerId: string): string {
  const sanitized = providerId.replace(/[^A-Za-z0-9]+/gu, "_").toUpperCase();
  return `NEX_PROVIDER_${sanitized}_API_KEY`;
}

/** Validates the wire doc structurally. Throws `ConfigRejectedError` (ok:false). */
export function validateFrame(frame: unknown): ConfigFrame {
  if (!isRecord(frame)) throw new ConfigRejectedError("config frame must be an object");
  const version = frame["version"];
  const doc = frame["doc"];
  if (typeof version !== "number" || !Number.isFinite(version) || version < 0) {
    throw new ConfigRejectedError("config.version must be a non-negative number");
  }
  if (!isRecord(doc)) throw new ConfigRejectedError("config.doc must be an object");
  if (doc["schema"] !== RUNTIME_CONFIG_SCHEMA) {
    throw new ConfigRejectedError(`unsupported config doc schema ${String(doc["schema"])}`);
  }
  const owned = doc["owned"];
  if (typeof owned !== "boolean")
    throw new ConfigRejectedError("config.doc.owned must be a boolean");
  const rawProviders = doc["providers"];
  if (!Array.isArray(rawProviders))
    throw new ConfigRejectedError("config.doc.providers must be an array");
  const issues: ConfigIssue[] = [];
  const providers: WireProvider[] = [];
  const seen = new Set<string>();
  rawProviders.forEach((raw, index) => {
    const path = `providers.${index}`;
    if (!isRecord(raw)) {
      issues.push({
        scope: "doc",
        path,
        code: "provider-invalid",
        message: "provider entry must be an object",
      });
      return;
    }
    const id = asString(raw["id"]).trim();
    if (!id) {
      issues.push({
        scope: "doc",
        path: `${path}.id`,
        code: "provider-id-missing",
        message: "provider id is required",
      });
      return;
    }
    if (seen.has(id)) {
      issues.push({
        scope: "doc",
        path,
        code: "provider-duplicate",
        message: `duplicate provider ${id}`,
      });
      return;
    }
    seen.add(id);
    const api = asString(raw["api"]);
    if (!API_TYPES.has(api)) {
      issues.push({
        scope: "provider",
        path: `${id}.api`,
        code: "provider-api-unsupported",
        message: `unsupported api ${api}`,
      });
      return;
    }
    const baseUrl = asString(raw["baseUrl"]).trim();
    if (!baseUrl) {
      issues.push({
        scope: "provider",
        path: `${id}.baseUrl`,
        code: "provider-base-url-missing",
        message: "baseUrl is required",
      });
      return;
    }
    const models: WireModel[] = [];
    const rawModels = Array.isArray(raw["models"]) ? raw["models"] : [];
    rawModels.forEach((modelRaw, modelIndex) => {
      const model = normalizeModel(modelRaw, `${id}.models.${modelIndex}`, issues);
      if (model) models.push(model);
    });
    providers.push({
      id,
      api,
      baseUrl,
      ...(typeof raw["name"] === "string" ? { name: raw["name"] } : {}),
      enabled: raw["enabled"] === undefined ? true : raw["enabled"] === true,
      ...(isRecord(raw["headers"]) ? { headers: asStringRecord(raw["headers"]) } : {}),
      models,
    });
  });
  // A structurally broken doc (bad provider entries) still applies per item;
  // only a broken envelope/schema rejects it (research §C3 "partial failure").
  const secrets = isRecord(frame["secrets"]) ? asStringRecord(frame["secrets"]) : {};
  const settings = isRecord(doc["settings"]) ? (doc["settings"] as Record<string, unknown>) : {};
  return {
    version,
    doc: {
      schema: RUNTIME_CONFIG_SCHEMA,
      version: typeof doc["version"] === "number" ? doc["version"] : version,
      owned,
      providers,
      settings,
    },
    secrets,
  };
}

function normalizeModel(raw: unknown, path: string, issues: ConfigIssue[]): WireModel | null {
  if (!isRecord(raw)) {
    issues.push({
      scope: "model",
      path,
      code: "model-invalid",
      message: "model entry must be an object",
    });
    return null;
  }
  const id = asString(raw["id"]).trim();
  if (!id) {
    issues.push({
      scope: "model",
      path: `${path}.id`,
      code: "model-id-missing",
      message: "model id is required",
    });
    return null;
  }
  const reasoningLevels = Array.isArray(raw["reasoningLevels"])
    ? raw["reasoningLevels"].filter((value): value is string => typeof value === "string")
    : [];
  const ordered = reasoningLevels
    .filter((level, index) => reasoningLevels.indexOf(level) === index)
    .filter((level) => REASONING_ORDER.includes(level))
    .sort((left, right) => REASONING_ORDER.indexOf(left) - REASONING_ORDER.indexOf(right));
  return {
    id,
    ...(typeof raw["name"] === "string" && raw["name"].trim() ? { name: raw["name"].trim() } : {}),
    reasoning: ordered.length > 1 || ordered[0] !== "off",
    reasoningLevels: ordered.length > 0 ? ordered : ["off"],
    contextWindow: positive(raw["contextWindow"]) ?? 128_000,
    maxTokens: positive(raw["maxTokens"]) ?? 16_384,
    input:
      Array.isArray(raw["input"]) && raw["input"].length > 0
        ? raw["input"].filter((item): item is string => typeof item === "string")
        : ["text"],
    ...(isRecord(raw["cost"]) ? { cost: raw["cost"] as WireModel["cost"] } : {}),
    enabled: raw["enabled"] === undefined ? true : raw["enabled"] === true,
  };
}

/**
 * Builds the immutable RuntimeConfig of one version.
 *
 * Fallback mode: `owned` is false (no server providers). Server mode: every
 * enabled provider becomes a pi provider whose models.json entry carries an
 * `apiKey` of `$ENV` pointing at a process env var the runner sets for the
 * lifetime of the generation, so pi's own tool invocations (bash etc.) never see
 * the value and models.json on disk stays credential-blind.
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
  if (!owned) {
    return Object.freeze({
      version: frame.version,
      owned: false,
      mode: "fallback",
      providers: Object.freeze([]),
      settings: Object.freeze({ ...frame.doc.settings }),
      credentials: new Map(),
      issues: Object.freeze([...issues]),
      env: Object.freeze({}),
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
    version: frame.version,
    providers: providers.length,
    models: providers.reduce((total, provider) => total + provider.models.length, 0),
  });
  return Object.freeze({
    version: frame.version,
    owned: true,
    mode: "server",
    providers: Object.freeze(providers),
    settings: Object.freeze(settings),
    credentials,
    issues: Object.freeze(issues),
    env: Object.freeze(envRefs),
  });
}
