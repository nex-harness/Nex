/**
 * Wire shape of the server's `config` frame and its structural validation
 * (docs/next/config-mcp-skills.md decision 4). Extracted from `runtimeConfig.ts`
 * to keep each file under the size cap; the parsing rules are unchanged.
 */
import { asString, asStringRecord, isRecord, positive } from "./runtimeSettings.js";

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

export interface BundleRefWire {
  name: string;
  digest: string;
}

export interface WireDoc {
  schema: number;
  version: number;
  owned: boolean;
  providers: WireProvider[];
  settings?: Record<string, unknown>;
  /** Effective MCP servers (P1); absent/empty ⇒ pi's own mcp.json discovery. */
  mcp?: unknown[];
  mcpOwned?: boolean;
  /** Enabled skill/prompt bundle refs (P2). */
  skills?: Array<{ name: string; digest: string; kind?: string; enabled?: boolean }>;
  prompts?: Array<{ name: string; digest: string; kind?: string; enabled?: boolean }>;
}

/** One bundle as pushed: the manifest plus its files. */
export interface WireBundle {
  name: string;
  kind: string;
  digest: string;
  description?: string;
  files: Array<{ path: string; content: string }>;
}

export interface ConfigFrame {
  version: number;
  doc: WireDoc;
  secrets: Record<string, string>;
  bundles: WireBundle[];
  warnings: string[];
}

export interface ConfigIssue {
  scope: string;
  path: string;
  code: string;
  message: string;
}

/** Stable env var name for one provider's key: `NEX_PROVIDER_<ID>_API_KEY`. */
export function envVarFor(providerId: string): string {
  const sanitized = providerId.replace(/[^A-Za-z0-9]+/gu, "_").toUpperCase();
  return `NEX_PROVIDER_${sanitized}_API_KEY`;
}

export class ConfigRejectedError extends Error {
  readonly issues: ConfigIssue[];
  constructor(message: string, issues: ConfigIssue[] = []) {
    super(message);
    this.name = "ConfigRejectedError";
    this.issues = issues;
  }
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
      const model = normalizeWireModel(modelRaw, `${id}.models.${modelIndex}`, issues);
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
  const bundles: WireBundle[] = [];
  if (Array.isArray(frame["bundles"])) {
    for (const [index, raw] of frame["bundles"].entries()) {
      const bundle = parseBundle(raw, index, issues);
      if (bundle) bundles.push(bundle);
    }
  }
  const mcp = doc["mcp"];
  return {
    version,
    doc: {
      schema: RUNTIME_CONFIG_SCHEMA,
      version: typeof doc["version"] === "number" ? doc["version"] : version,
      owned,
      providers,
      settings,
      ...(Array.isArray(mcp) ? { mcp } : {}),
      ...(doc["mcpOwned"] === true ? { mcpOwned: true } : {}),
      skills: bundleRefs(doc["skills"]),
      prompts: bundleRefs(doc["prompts"]),
    },
    secrets,
    bundles,
    warnings: Array.isArray(frame["warnings"])
      ? frame["warnings"].filter((entry): entry is string => typeof entry === "string")
      : [],
  };
}

function bundleRefs(raw: unknown): Array<{ name: string; digest: string }> {
  if (!Array.isArray(raw)) return [];
  const seen = new Set<string>();
  const out: BundleRefWire[] = [];
  for (const entry of raw) {
    if (!isRecord(entry)) continue;
    const name = asString(entry["name"]).trim();
    const digest = asString(entry["digest"]).trim();
    if (!name || !digest || entry["enabled"] === false || seen.has(name)) continue;
    seen.add(name);
    out.push({ name, digest });
  }
  return out;
}

function parseBundle(raw: unknown, index: number, issues: ConfigIssue[]): WireBundle | null {
  const path = `bundles.${index}`;
  if (!isRecord(raw)) {
    issues.push({
      scope: "bundle",
      path,
      code: "bundle-invalid",
      message: "bundle must be an object",
    });
    return null;
  }
  const name = asString(raw["name"]).trim();
  const digest = asString(raw["digest"]).trim();
  const files = Array.isArray(raw["files"])
    ? raw["files"].flatMap((file) =>
        isRecord(file) && typeof file["path"] === "string" && typeof file["content"] === "string"
          ? [{ path: file["path"], content: file["content"] }]
          : [],
      )
    : [];
  if (!name || !/^sha256:[a-f0-9]{64}$/u.test(digest) || files.length === 0) {
    issues.push({
      scope: "bundle",
      path,
      code: "bundle-invalid",
      message: "bundle needs a name, digest and files",
    });
    return null;
  }
  return {
    name,
    kind: asString(raw["kind"]) || "skill",
    digest,
    files,
    ...(typeof raw["description"] === "string" ? { description: raw["description"] } : {}),
  };
}

export function normalizeWireModel(
  raw: unknown,
  path: string,
  issues: ConfigIssue[],
): WireModel | null {
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
