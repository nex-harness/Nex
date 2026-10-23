import type { EngineCatalog } from "../engine/types.js";

/**
 * Fallback-mode catalog facts (spec docs/next/config-mcp-skills.md decision 2,
 * "local fallback until configured, then server wins").
 *
 * While the config doc has no providers the runner keeps using its own
 * `~/.pi/agent`. The nex-next server compiles the composer's model-selection
 * view from `configApplied.facts.modelsByProvider`, and in fallback mode it has
 * no doc to compile from — so the runner reports the models its pi runtime
 * actually exposes here. The shape matches the server-mode facts
 * (`RuntimeConfigManager.facts`): provider id → {configured, models[]}. Only
 * provider ids, model ids and public metadata cross the wire; no credential is
 * ever included.
 */
export function fallbackCatalogFacts(
  catalog: EngineCatalog,
  options: { version: number; configured: (providerId: string) => boolean },
): Record<string, unknown> {
  interface Entry {
    configured: boolean;
    models: Array<Record<string, unknown>>;
  }
  const modelsByProvider: Record<string, Entry> = {};
  for (const model of catalog.models) {
    const entry = (modelsByProvider[model.providerId] ??= {
      configured: false,
      models: [],
    });
    entry.models.push({
      providerId: model.providerId,
      modelId: model.modelId,
      name: model.modelName || model.modelId,
      api: model.apiType,
      baseUrl: model.baseUrl,
      reasoningLevels: model.reasoningLevels.length > 0 ? [...model.reasoningLevels] : ["off"],
      contextWindow: model.contextWindow,
      maxTokens: model.maxOutputTokens,
      input: model.supportsImage ? ["text", "image"] : ["text"],
    });
  }
  for (const [providerId, entry] of Object.entries(modelsByProvider)) {
    entry.configured = options.configured(providerId);
  }
  return { mode: "fallback", version: options.version, modelsByProvider };
}
