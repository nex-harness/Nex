import { createHash } from "node:crypto";
import { REASONING_ORDER, type RuntimeConfig, type WireModel } from "./runtimeConfig.js";

/**
 * Derivation and digest of the per-generation models.json (split out of
 * runtimeConfig.ts for the 400-line file budget). The document stays
 * credential-blind: keys travel as `$ENV_VAR` references the runner exports.
 */

/** pi models.json entry (pi's provider API ids; `openai-completions` is pi's). */
export function modelsJsonModel(
  model: WireModel,
  api: string,
  baseUrl: string,
): Record<string, unknown> {
  const levels = model.reasoningLevels ?? ["off"];
  return {
    id: model.id,
    ...(model.name ? { name: model.name } : {}),
    api,
    baseUrl,
    reasoning: model.reasoning === true,
    contextWindow: model.contextWindow ?? 128_000,
    maxTokens: model.maxTokens ?? 16_384,
    input: model.input ?? ["text"],
    ...(model.cost ? { cost: model.cost } : {}),
    // A model that advertises a narrower set than pi's default ("off", "low",
    // "high" only) hides the levels it does not support from pi as well.
    ...(levels.length > 0 && levels.length < REASONING_ORDER.length
      ? {
          thinkingLevelMap: Object.fromEntries(
            REASONING_ORDER.filter((level) => !levels.includes(level)).map((level) => [
              level,
              null,
            ]),
          ),
        }
      : {}),
  };
}

/** models.json document for one generation. */
export function modelsJsonFor(config: RuntimeConfig): {
  $schema?: string;
  providers: Record<string, unknown>;
} {
  const providers: Record<string, unknown> = {};
  for (const provider of config.providers) {
    providers[provider.id] = {
      name: provider.name,
      api: provider.api,
      baseUrl: provider.baseUrl,
      ...(provider.envVar ? { apiKey: `$${provider.envVar}` } : {}),
      ...(Object.keys(provider.headers).length > 0 ? { headers: provider.headers } : {}),
      models: provider.modelsJsonModels,
    };
  }
  return { providers };
}

/** Deterministic digest of a doc's provider/model shape (diagnostics). */
export function configDigest(config: RuntimeConfig): string {
  const shape = config.providers.map((provider) => ({
    id: provider.id,
    api: provider.api,
    baseUrl: provider.baseUrl,
    models: provider.models.map((model) => model.id).sort(),
  }));
  return createHash("sha256").update(JSON.stringify(shape)).digest("hex").slice(0, 16);
}

export {
  SETTINGS_KEYS,
  appliedCacheFor,
  pickSettings,
  type AppliedCache,
} from "./runtimeSettings.js";
