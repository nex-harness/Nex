import { buildRuntimeNexApiUrl, resolveZaiBusinessBaseUrl } from "@nex/shared";

export const NEX_CLIENT_SCENES_URL = buildRuntimeNexApiUrl(
  process.env,
  "/api/v1/client/scenes",
);

export const ZAI_API_HOST = resolveZaiBusinessBaseUrl(process.env);
