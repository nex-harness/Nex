import { test } from "node:test";
import assert from "node:assert/strict";
import {
  parseModelConfig,
  parseProviderConfig,
  resolveInitialModelSelection,
  validateModelSelectionOptions,
} from "@nex/provider";
import { completeModelConfigDataSchema } from "@nex/shared/model-config";
import { modelSelectionSchema } from "@nex/shared/nex-protocol-v4";
import type { EngineCatalog } from "../src/engine/types.js";
import {
  buildModelSelectionViews,
  preferredSelectionFor,
  resolveEffectiveSelection,
} from "../src/channels/modelViews.js";

/**
 * These tests replay the UI's own read path against the runner's view:
 *
 * `useModelSelectionServiceView` calls `modelSelection.getView({selection})` and
 * `useDraftConfigControl` takes `view.effectiveSelection ?? undefined` as the
 * current composer selection. `composerSubmissionConfig` then requires
 * `validateModelSelectionOptions(model, selection).ok` from the real provider registry
 * before the composer can submit. A view without `effectiveSelection` therefore keeps
 * the toolbar at "Choose model" and blocks Enter even though the catalog is valid.
 */

const CATALOG: EngineCatalog = {
  models: [
    {
      providerId: "CPA",
      providerName: "CPA",
      modelId: "cli/deepseek-v4.1-flash",
      modelName: "CatPaw/DeepSeek V4.1 Flash",
      apiType: "openai-responses",
      baseUrl: "https://ai-proxy.example.test/v1",
      reasoningLevels: ["off", "minimal", "low", "medium", "high"],
      maxOutputTokens: 384_000,
      contextWindow: 1_000_000,
      supportsImage: true,
      supportsVideo: false,
      supportsAudio: false,
      supportsPdf: false,
    },
    {
      providerId: "CPA",
      providerName: "CPA",
      modelId: "catpaw/gpt-5.6-luna",
      modelName: "CatPaw/GPT-5.6 Luna",
      apiType: "openai-responses",
      baseUrl: "https://ai-proxy.example.test/v1",
      reasoningLevels: ["off", "minimal", "low", "medium", "high"],
      maxOutputTokens: 128_000,
      contextWindow: 1_050_000,
      supportsImage: true,
      supportsVideo: false,
      supportsAudio: false,
      supportsPdf: false,
    },
  ],
  defaultSelection: {
    providerId: "CPA",
    modelId: "cli/deepseek-v4.1-flash",
    options: { reasoningLevel: "high" },
  },
};

interface ModelSelectionViewShape {
  revision: number;
  providers: Array<{
    providerId: string;
    providerName?: string;
    templateId: string;
    config: unknown;
    models: Array<{ modelId: string; config: unknown }>;
  }>;
  preferredSelection?: unknown;
  effectiveSelection?: unknown;
  selectionIssue?: string;
}

function modelView(input?: { selection: unknown }): ModelSelectionViewShape {
  return buildModelSelectionViews(CATALOG, 7, input as never)
    .modelSelection as ModelSelectionViewShape;
}

test("model-selection.getView({selection}) returns an effectiveSelection the UI can submit with", () => {
  const requested = {
    providerId: "CPA",
    modelId: "cli/deepseek-v4.1-flash",
    options: { reasoningLevel: "high" },
  };
  const view = modelView({ selection: requested });

  // The UI contract: identity must survive modelSelectionSchema, and reasoning must
  // satisfy the provider registry's own option validator for the listed model.
  const effective = modelSelectionSchema.parse(view.effectiveSelection);
  assert.deepEqual(effective, requested);
  assert.equal(view.selectionIssue, undefined);

  const model = view.providers
    .find((provider) => provider.providerId === effective.providerId)
    ?.models.find((candidate) => candidate.modelId === effective.modelId);
  assert.ok(model, "requested model must be listed in the same view");
  const validation = validateModelSelectionOptions(
    { config: completeModelConfigDataSchema.parse(model.config) },
    effective,
  );
  assert.deepEqual(validation, { ok: true });

  // A model id containing "/" must not be mangled: it is an opaque id, not provider/model.
  assert.equal(effective.modelId, "cli/deepseek-v4.1-flash");
});

test("model-selection view provider/model configs pass the real provider schemas", () => {
  const view = modelView();
  assert.ok(view.providers.length > 0);
  for (const provider of view.providers) {
    // `ProviderConfigObject` from `@nex/provider` is what the UI's settings/selection
    // consumers assume; a missing `group`/`access`/`api` silently breaks those paths.
    // `parseProviderConfig` runs the real complete schema, including URL validation.
    parseProviderConfig(provider.config);
    for (const model of provider.models) {
      completeModelConfigDataSchema.parse(model.config);
      parseModelConfig(model.config);
    }
  }
});

test("model-selection view preserves the preferred selection the composer initializes from", () => {
  const view = modelView({ selection: null });
  // `initializeNewTaskDraft` reads `preferredSelection` only when it is present.
  assert.deepEqual(view.preferredSelection, preferredSelectionFor(CATALOG));
  const preferred = modelSelectionSchema.parse(view.preferredSelection);
  assert.equal(preferred.providerId, "CPA");
  assert.equal(preferred.modelId, "cli/deepseek-v4.1-flash");
  assert.equal(preferred.options?.reasoningLevel, "high");
  // With `selection: null` the UI explicitly asked to resolve "no selection".
  assert.equal(view.effectiveSelection, null);
  assert.equal(view.selectionIssue, "selection-missing");
  // The real provider resolver agrees the preferred selection is a valid initial pick.
  const initial = resolveInitialModelSelection({
    registry: {
      revision: 7,
      providers: view.providers.map((provider) => ({
        ...provider,
        config: parseProviderConfig(provider.config),
        models: provider.models.map((model) => ({
          modelId: model.modelId,
          config: parseModelConfig(model.config),
        })),
      })),
    } as never,
  });
  const initialSelection = initial.source === "none" ? null : initial.selection;
  assert.equal(initial.source, "registry-fallback");
  assert.deepEqual(initialSelection, preferred);
});

test("model-selection view reports invalid selections without dropping the catalog", () => {
  const unknownModel = resolveEffectiveSelection(CATALOG, {
    providerId: "CPA",
    modelId: "cli/does-not-exist",
    options: { reasoningLevel: "high" },
  });
  assert.deepEqual(unknownModel, {
    effectiveSelection: null,
    selectionIssue: "model-not-found",
  });

  const missingReasoning = resolveEffectiveSelection(CATALOG, {
    providerId: "CPA",
    modelId: "cli/deepseek-v4.1-flash",
  });
  assert.deepEqual(missingReasoning, {
    effectiveSelection: {
      providerId: "CPA",
      modelId: "cli/deepseek-v4.1-flash",
    },
    selectionIssue: "reasoning-level-missing",
  });

  const unsupported = resolveEffectiveSelection(CATALOG, {
    providerId: "CPA",
    modelId: "cli/deepseek-v4.1-flash",
    options: { reasoningLevel: "xhigh" },
  });
  assert.equal(unsupported.selectionIssue, "reasoning-level-not-supported");
  assert.equal(unsupported.effectiveSelection?.options, undefined);
});
