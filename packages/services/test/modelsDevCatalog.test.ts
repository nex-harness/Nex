import { describe, expect, it } from "vitest";
import {
  findModelsDevModels,
  modelConfigFromMatches,
  type ModelsDevCatalog,
} from "../src/model-provider/modelsDevCatalog.js";

const catalog: ModelsDevCatalog = {
  openai: {
    models: {
      "gpt-5.4": {
        limit: { context: 1_050_000, output: 128_000 },
        tool_call: true,
        structured_output: true,
        modalities: { input: ["text", "image"] },
        reasoning_options: [{ type: "effort", values: ["low", "medium", "high"] }],
      },
    },
  },
  azure: {
    models: {
      "GPT-5.4": {
        limit: { context: 1_050_000, output: 128_000 },
        tool_call: true,
        modalities: { input: ["text", "pdf"] },
        reasoning_options: [{ type: "effort", values: ["low", "medium", "high"] }],
      },
    },
  },
  aggregator: {
    models: {
      "gpt-5.4": {
        // 聚合商常见的缩水上限，不能压过官方值。
        limit: { context: 400_000, output: 0 },
        tool_call: false,
        reasoning_options: [{ type: "effort", values: [] }],
      },
    },
  },
};

describe("findModelsDevModels", () => {
  it("matches namespaced and differently-cased IDs across providers", () => {
    expect(findModelsDevModels(catalog, "gpt-5.4")).toHaveLength(3);
    expect(findModelsDevModels(catalog, "  cli/GPT-5.4 ")).toHaveLength(3);
    expect(findModelsDevModels(catalog, "not-a-real-model")).toEqual([]);
    expect(findModelsDevModels(catalog, "   ")).toEqual([]);
  });

  it("strips any number of namespace prefixes", () => {
    expect(findModelsDevModels(catalog, "cst/openai/gpt-5.4")).toHaveLength(3);
    expect(findModelsDevModels(catalog, "a/b/c/GPT-5.4")).toHaveLength(3);
    expect(findModelsDevModels(catalog, "cst/openai/")).toEqual([]);

    const slashKeyCatalog: ModelsDevCatalog = {
      router: { models: { "anthropic/claude-sonnet-5-5": { limit: { context: 1 } } } },
    };
    expect(findModelsDevModels(slashKeyCatalog, "cst/anthropic/claude-sonnet-5-5")).toHaveLength(1);
  });
});

describe("modelConfigFromMatches", () => {
  it("aggregates by majority and ignores invalid values", () => {
    const config = modelConfigFromMatches(findModelsDevModels(catalog, "gpt-5.4"));
    expect(config.properties?.contextWindow).toBe(1_050_000);
    expect(config.properties?.supportsToolCall).toBe(true);
    // 只有一个条目声明 structured_output，众数即为该值。
    expect(config.properties?.supportsJsonSchemaOutput).toBe(true);
    expect(config.properties?.inputFormat).toEqual({
      supportsText: true,
      supportsImage: true,
      supportsVideo: false,
      supportsAudio: false,
      supportsPdf: true,
    });
    expect(config.properties?.outputFormat).toEqual({ supportsText: true });
    // output: 0 不是合法上限，被丢弃后只剩 128000。
    expect(config.optionSpecs?.maxOutputTokens).toEqual({ max: 128_000 });
    // 空推理档位不满足 reasoningLevel.values 非空约束，不能参与聚合。
    expect(config.optionSpecs?.reasoningLevel).toEqual({ values: ["low", "medium", "high"] });
  });

  it("breaks numeric ties toward the larger value", () => {
    const config = modelConfigFromMatches([
      { providerId: "a", model: { limit: { context: 200_000 } } },
      { providerId: "b", model: { limit: { context: 1_000_000 } } },
    ]);
    expect(config.properties?.contextWindow).toBe(1_000_000);
  });

  it("returns an empty config when nothing is usable", () => {
    expect(modelConfigFromMatches([{ providerId: "a", model: {} }])).toEqual({});
  });
});
