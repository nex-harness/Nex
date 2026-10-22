import type * as T from "../../src/engine/types.js";

/** Catalog model every fake-engine test starts from. */
export const SAMPLE_MODEL: T.EngineModelInfo = {
  providerId: "anthropic",
  providerName: "Anthropic",
  modelId: "claude-sonnet-4-5",
  modelName: "Claude Sonnet 4.5",
  apiType: "anthropic-messages",
  baseUrl: "https://api.anthropic.com",
  reasoningLevels: ["off", "low", "medium", "high"],
  maxOutputTokens: 8192,
  contextWindow: 200_000,
  supportsImage: true,
  supportsVideo: false,
  supportsAudio: false,
  supportsPdf: false,
};

export function modelState(
  selection: T.CreateEngineSessionOptions["modelSelection"],
): T.EngineModelState {
  return {
    providerId: selection?.providerId ?? SAMPLE_MODEL.providerId,
    modelId: selection?.modelId ?? SAMPLE_MODEL.modelId,
    displayName: SAMPLE_MODEL.modelName,
    thought: selection?.options?.reasoningLevel ?? "medium",
    thoughtLevels: SAMPLE_MODEL.reasoningLevels,
  };
}

export function defaultTitle(text: string): string {
  const trimmed = text.trim().replace(/\s+/gu, " ");
  return trimmed.length > 60 ? `${trimmed.slice(0, 60)}…` : trimmed;
}
