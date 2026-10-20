import type { Model } from "@earendil-works/pi-ai";
import { clampThinkingLevel } from "@earendil-works/pi-ai";
import type { ModelRuntime, SettingsManager } from "@earendil-works/pi-coding-agent";
import type { ModelSelection } from "@nex/shared";
import type { EngineCatalog, EngineModelInfo } from "../types.js";
import { defaultThinking } from "./sessionHelpers.js";
import { toEngineModelInfo } from "./translate.js";

export async function resolveModel(
  selection: ModelSelection | undefined,
  runtime: ModelRuntime,
): Promise<Model<never> | undefined> {
  if (selection) {
    const model = runtime.getModel(selection.providerId, selection.modelId);
    if (!model) throw new Error(`Unknown model ${selection.providerId}/${selection.modelId}`);
    return model as Model<never>;
  }
  const available = await runtime.getAvailable();
  return available[0] as Model<never> | undefined;
}

export async function buildCatalog(
  runtime: ModelRuntime,
  settings: SettingsManager | null,
): Promise<EngineCatalog> {
  const available = (await runtime.getAvailable()) as Model<never>[];
  const models = available
    .map((model) => toEngineModelInfo(model, runtime))
    .filter((info): info is EngineModelInfo => info !== null);
  return {
    models,
    defaultSelection: await resolveDefaultSelection(runtime, models, available, settings),
  };
}

export async function resolveDefaultSelection(
  runtime: ModelRuntime,
  models: EngineModelInfo[],
  available: Model<never>[],
  settings: SettingsManager | null,
): Promise<ModelSelection | null> {
  if (settings) {
    const provider = settings.getDefaultProvider();
    const modelId = settings.getDefaultModel();
    if (provider && modelId) {
      const saved = runtime.getModel(provider, modelId);
      if (saved) {
        const level = settings.getDefaultThinkingLevel() ?? "medium";
        return {
          providerId: saved.provider,
          modelId: saved.id,
          options: { reasoningLevel: clampThinkingLevel(saved, level as never) },
        };
      }
    }
  }
  const listed = models[0];
  if (!listed) return null;
  const model = available[0];
  if (!model) return null;
  return {
    providerId: listed.providerId,
    modelId: listed.modelId,
    options: { reasoningLevel: defaultThinking(model) },
  };
}
