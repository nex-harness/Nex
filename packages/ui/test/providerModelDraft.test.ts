import { describe, expect, it } from "vitest";
import type { ProviderSettingsFormModel } from "../src/lib/providerSettingsFormTypes.js";
import {
  createProviderModelDraftValues,
  resolveProviderModelDraftCommit,
} from "../src/settings/model-provider-section/ProviderModelMetadata.js";
import {
  projectModelDraft,
  updateModelDraft,
} from "../src/settings/model-provider-section/ProviderModelDraftState.js";
import { modelInfoDraftPatch } from "../src/settings/model-provider-section/useModelInfoLookup.js";

function modelWithBaseline(contextWindow: number, maxOutput: number): ProviderSettingsFormModel {
  const inherited = {
    properties: { contextWindow },
    optionSpecs: { maxOutputTokens: { max: maxOutput } },
  };
  return {
    modelId: "gpt-5.4",
    config: inherited,
    inheritedConfig: inherited,
    personalConfig: {},
  } as unknown as ProviderSettingsFormModel;
}

describe("projectModelDraft", () => {
  it("fills empty text controls from the inherited baseline", () => {
    const model = modelWithBaseline(200_000, 32_000);
    const draft = projectModelDraft(
      {
        ...createProviderModelDraftValues(model),
        contextWindowValue: "",
        maxOutputTokensValue: "",
      },
      model,
    );
    expect(draft.contextWindowValue).toBe("200000");
    expect(draft.maxOutputTokensValue).toBe("32000");
  });
});

describe("updateModelDraft", () => {
  it("clears auto-filled values on ID change but keeps manual edits", () => {
    const model = modelWithBaseline(200_000, 32_000);
    const draft = {
      ...projectModelDraft(createProviderModelDraftValues(model), model),
      contextWindowValue: "200000",
      maxOutputTokensValue: "555555",
    };
    const next = updateModelDraft(draft, { idValue: "other-model" }, model);
    expect(next.idValue).toBe("other-model");
    expect(next.contextWindowValue).toBe("");
    expect(next.maxOutputTokensValue).toBe("555555");
  });
});

describe("modelInfoDraftPatch", () => {
  it("only writes fields present in the looked-up config", () => {
    expect(modelInfoDraftPatch({})).toEqual({});
    expect(
      modelInfoDraftPatch({
        properties: { contextWindow: 1_050_000, inputFormat: { supportsImage: true } },
        optionSpecs: { reasoningLevel: { values: ["low", "high"] } },
      }),
    ).toEqual({
      contextWindowValue: "1050000",
      reasoningLevelValuesValue: ["low", "high"],
      inputFormatValue: {
        supportsText: true,
        supportsImage: true,
        supportsVideo: false,
        supportsAudio: false,
        supportsPdf: false,
      },
    });
  });
});

const MODEL_ID = "cst/claude/claude-sonnet-5-5";

function committableModel(
  personalConfig: ProviderSettingsFormModel["personalConfig"] = {},
): ProviderSettingsFormModel {
  const inherited = {
    enabled: true,
    properties: { contextWindow: 200_000, inputFormat: { supportsText: true } },
    optionSpecs: {
      maxOutputTokens: { max: 32_000 },
      reasoningLevel: {
        values: ["low", "high"],
        map: '{"reasoning_effort": reasoningLevel}',
      },
    },
  };
  return {
    kind: "candidate",
    modelId: MODEL_ID,
    builtin: false,
    config: { ...inherited, ...personalConfig },
    inheritedConfig: inherited,
    personalConfig,
    useRecommendedConfig: true,
    hasPersonalConfig: Object.keys(personalConfig).length > 0,
    executable: true,
    selectable: true,
  } as unknown as ProviderSettingsFormModel;
}

function commitDraft(
  model: ProviderSettingsFormModel,
  patch: Partial<ReturnType<typeof createProviderModelDraftValues>>,
) {
  return resolveProviderModelDraftCommit({
    currentModel: model,
    draft: { ...createProviderModelDraftValues(model), ...patch },
  });
}

describe("resolveProviderModelDraftCommit", () => {
  it("keeps the models.dev fill values when the form is saved", () => {
    const result = commitDraft(committableModel(), {
      contextWindowValue: "1050000",
      maxOutputTokensValue: "128000",
      reasoningLevelValuesValue: ["low", "medium", "high"],
      reasoningLevelMapValue: '{"reasoning_effort": reasoningLevel}',
    });
    expect(result.status).toBe("commit");
    if (result.status !== "commit") return;
    expect(result.model.personalConfig.properties?.contextWindow).toBe(1_050_000);
    expect(result.model.personalConfig.optionSpecs?.maxOutputTokens?.max).toBe(128_000);
    expect(result.model.personalConfig.optionSpecs?.reasoningLevel?.values).toEqual([
      "low",
      "medium",
      "high",
    ]);
  });
});
