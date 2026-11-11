import assert from "node:assert/strict";
import test from "node:test";
import type { Provider, ProviderModel } from "@nex/provider";
import { getRegistryBackedModel } from "../src/app/provider-registry-selection.js";
import type { ProviderRegistryModelSource } from "../src/app/provider-registry-model-runtime.js";

const MODEL_ID = "cst/claude/claude-sonnet-5-5";

function registryWith(name: string | undefined): ProviderRegistryModelSource {
  const provider = { providerId: "magpie", providerName: "magpie", models: [] } as unknown as Provider;
  const model = {
    modelId: MODEL_ID,
    config: {
      ...(name === undefined ? {} : { name }),
      properties: {
        contextWindow: 200_000,
        inputFormat: { supportsText: true },
        outputFormat: { supportsText: true },
      },
      optionSpecs: {
        maxOutputTokens: { max: 32_000 },
        reasoningLevel: { values: ["low", "high"] },
      },
    },
  } as unknown as ProviderModel;
  return {
    getView: () => ({ providers: [{ ...provider, models: [model] }] }),
    getProvider: () => provider,
    getModel: () => model,
  } as unknown as ProviderRegistryModelSource;
}

test("model option label is the alias while ref keeps the model ID", () => {
  const option = getRegistryBackedModel(registryWith("sonnet-5-5"), {
    providerId: "magpie",
    modelId: MODEL_ID,
  });
  assert.equal(option?.label, "sonnet-5-5");
  assert.deepEqual(option?.ref, { providerId: "magpie", modelId: MODEL_ID });
});

test("model option label falls back to the model ID without an alias", () => {
  const option = getRegistryBackedModel(registryWith(undefined), {
    providerId: "magpie",
    modelId: MODEL_ID,
  });
  assert.equal(option?.label, MODEL_ID);
});
