import type { ModelSelection } from "@nex/shared";
import type { EngineCatalog, EngineModelInfo } from "../engine/types.js";

/**
 * Minimal `model-selection.getView()` shape (docs/next/channels-research.md §4).
 *
 * Constraints that matter to the composer:
 * - at least one provider with `models.length > 0`;
 * - every model has a non-empty `optionSpecs.reasoningLevel.values`;
 * - `preferredSelection.options.reasoningLevel` is one of those values.
 *
 * `getView({selection})` must mirror `ModelSelectionFacade.getView(input)` and resolve the
 * caller's intent into `effectiveSelection` (+ `selectionIssue` when it is not executable).
 * 修复依据：`useDraftConfigControl`/`useModelSelectionServiceView` 以
 * `view.effectiveSelection ?? undefined` 作为当前草稿模型；只返回 `preferredSelection`
 * 会让 UI 把一次有效选择当成“未选模型”，工具栏显示 Choose model 且提交门槛永远关闭。
 *
 * `config.access.apiKey` is deliberately empty: the runner's real credential lives in
 * pi's auth store and must never cross the wire.
 */

const REASONING_MAP = '{"reasoning_effort": reasoningLevel}';
const MAX_OUTPUT_MAP = '{"max_tokens": maxOutputTokens}';

export interface ModelSelectionViewInput {
  selection: ModelSelection | null;
}

export interface ModelSelectionViews {
  modelSelection: unknown;
  providerSettings: unknown;
}

export function buildModelSelectionViews(
  catalog: EngineCatalog,
  revision: number,
  input?: ModelSelectionViewInput,
): ModelSelectionViews {
  const grouped = groupByProvider(catalog.models);
  const preferred = pickPreferred(catalog);
  const effective = input ? resolveEffectiveSelection(catalog, input.selection) : undefined;
  return {
    modelSelection: {
      revision,
      providers: [...grouped.values()].map((group) => ({
        providerId: group.providerId,
        providerName: group.providerName,
        templateId: group.providerId,
        config: providerConfig(group.models[0]),
        models: group.models.map((model) => ({
          modelId: model.modelId,
          config: modelConfig(model),
        })),
      })),
      ...(preferred ? { preferredSelection: preferred } : {}),
      ...effective,
    },
    providerSettings: {
      revision,
      providerTemplates: [],
      providerOrder: [...grouped.keys()],
      providers: [...grouped.values()].map((group) => ({
        providerId: group.providerId,
        providerName: group.providerName,
        templateId: group.providerId,
        enabled: true,
        executable: true,
        effectiveConfig: providerConfig(group.models[0]),
        issues: [],
        models: group.models.map((model) => ({
          kind: "candidate",
          modelId: model.modelId,
          builtin: true,
          effectiveBuiltinConfig: {},
          effectiveConfig: modelConfig(model),
          enabled: true,
          executable: true,
          selectable: true,
          issues: [],
        })),
      })),
    },
  };
}

export function preferredSelectionFor(catalog: EngineCatalog): ModelSelection | null {
  return pickPreferred(catalog);
}

/**
 * Mirrors `resolveEffectiveModelSelection` from `@nex/provider` for a single catalog:
 * - unknown provider/model → `effectiveSelection: null` + issue;
 * - missing/unsupported reasoning level → identity-only selection + issue;
 * - valid → a frozen copy of the requested selection.
 */
export function resolveEffectiveSelection(
  catalog: EngineCatalog,
  selection: ModelSelection | null,
): { effectiveSelection: ModelSelection | null; selectionIssue?: string } {
  if (!selection) {
    return { effectiveSelection: null, selectionIssue: "selection-missing" };
  }
  const model = catalog.models.find(
    (item) => item.providerId === selection.providerId && item.modelId === selection.modelId,
  );
  if (!model) {
    const providerExists = catalog.models.some((item) => item.providerId === selection.providerId);
    return {
      effectiveSelection: null,
      selectionIssue: providerExists ? "model-not-found" : "provider-not-found",
    };
  }
  const reasoningLevel = selection.options?.reasoningLevel;
  const levels = model.reasoningLevels.length > 0 ? model.reasoningLevels : ["off"];
  if (reasoningLevel === undefined) {
    return {
      effectiveSelection: {
        providerId: selection.providerId,
        modelId: selection.modelId,
      },
      selectionIssue: "reasoning-level-missing",
    };
  }
  if (!levels.includes(reasoningLevel)) {
    return {
      effectiveSelection: {
        providerId: selection.providerId,
        modelId: selection.modelId,
      },
      selectionIssue: "reasoning-level-not-supported",
    };
  }
  return {
    effectiveSelection: {
      providerId: selection.providerId,
      modelId: selection.modelId,
      options: { reasoningLevel },
    },
  };
}

function pickPreferred(catalog: EngineCatalog): ModelSelection | null {
  const declared = catalog.defaultSelection;
  if (declared) {
    const model = catalog.models.find(
      (item) => item.providerId === declared.providerId && item.modelId === declared.modelId,
    );
    if (model) {
      return {
        providerId: model.providerId,
        modelId: model.modelId,
        options: {
          reasoningLevel: levelOrFirst(declared.options?.reasoningLevel, model.reasoningLevels),
        },
      };
    }
  }
  const first = catalog.models[0];
  if (!first) return null;
  return {
    providerId: first.providerId,
    modelId: first.modelId,
    options: { reasoningLevel: first.reasoningLevels[0] ?? "off" },
  };
}

function levelOrFirst(requested: string | undefined, values: string[]): string {
  if (requested && values.includes(requested)) return requested;
  return values[0] ?? "off";
}

interface ProviderGroup {
  providerId: string;
  providerName: string;
  models: EngineModelInfo[];
}

function groupByProvider(models: EngineModelInfo[]): Map<string, ProviderGroup> {
  const groups = new Map<string, ProviderGroup>();
  for (const model of models) {
    const group = groups.get(model.providerId);
    if (group) {
      group.models.push(model);
    } else {
      groups.set(model.providerId, {
        providerId: model.providerId,
        providerName: model.providerName,
        models: [model],
      });
    }
  }
  return groups;
}

function providerConfig(model: EngineModelInfo | undefined) {
  // Group/access/api are required by ProviderConfigObject. `apiKey` stays empty on purpose.
  // `api` mirrors the model's real upstream so consumers that read it still see a valid
  // shape (the composer itself only filters by provider identity).
  return {
    group: "standard-personal",
    access: { type: "api-key", apiKey: "" },
    api: {
      type: model?.apiType ?? "anthropic-messages",
      baseUrl: model?.baseUrl || "https://api.anthropic.com",
    },
  };
}

function modelConfig(model: EngineModelInfo) {
  const levels = model.reasoningLevels.length > 0 ? model.reasoningLevels : ["off"];
  return {
    enabled: true,
    name: model.modelName,
    properties: {
      requiresMfjsToolSchema: false,
      contextWindow: model.contextWindow > 0 ? model.contextWindow : 200_000,
      inputFormat: {
        supportsText: true,
        supportsImage: model.supportsImage,
        supportsVideo: model.supportsVideo,
        supportsAudio: model.supportsAudio,
        supportsPdf: model.supportsPdf,
      },
      outputFormat: { supportsText: true },
      supportsToolCall: true,
      supportsJsonSchemaOutput: true,
      supportsNativeWebSearch: false,
      supportsMidConversationSystem: false,
    },
    optionSpecs: {
      reasoningLevel: { values: levels, map: REASONING_MAP },
      maxOutputTokens: {
        max: model.maxOutputTokens > 0 ? model.maxOutputTokens : 8192,
        map: MAX_OUTPUT_MAP,
      },
    },
  };
}

export type { ModelSelection };
