import { NEX_AGENT_PROVIDER } from "@nex/shared";
import {
  legacyTelemetryModelValue,
  legacyTelemetryProviderId,
} from "./providerTelemetryIdentity.js";
export function resolveLegacyRuntimeModelValue(params: {
  configProvider?: string | null;
  modelName?: string | null;
}): string | null | undefined {
  const configProvider = params.configProvider?.trim();
  const modelName = params.modelName?.trim();
  if (!configProvider || !modelName || configProvider === NEX_AGENT_PROVIDER) {
    return params.modelName;
  }
  if (modelName.startsWith(`${configProvider}/`)) return legacyTelemetryModelValue(modelName);
  return `${legacyTelemetryProviderId(configProvider)}/${modelName}`;
}
