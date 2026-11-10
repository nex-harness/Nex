import { DEFAULT_NEX_ENDPOINT_ORIGIN } from "./nexEndpoint.js";

export const NEX_SOURCE_HEADERS = {
  "User-Agent": "Nex/unknown",
  "HTTP-Referer": DEFAULT_NEX_ENDPOINT_ORIGIN,
  "X-Title": "Z Code@electron",
} as const;

export interface BuildNexSourceHeadersFromContextOptions {
  appVersion?: string;
  arch?: string;
  clientLanguage?: string;
  clientTimezone?: string;
  deviceMid?: string;
  endpointOrigin?: string;
  osVersion?: string;
  platform?: string;
  releaseChannel?: string;
  sourceTitle?: string;
}

export function normalizeNexSourceHeaderValue(value: string | undefined): string | undefined {
  const trimmed = value?.trim();
  if (!trimmed || !/^[\x20-\x7e]+$/.test(trimmed)) {
    return undefined;
  }
  return trimmed;
}

export function buildNexSourceHeadersFromContext(
  options: BuildNexSourceHeadersFromContextOptions = {},
): Record<string, string> {
  const appVersion = normalizeNexSourceHeaderValue(options.appVersion);
  const arch = normalizeNexSourceHeaderValue(options.arch);
  const clientLanguage = normalizeNexSourceHeaderValue(options.clientLanguage) ?? "unknown";
  const clientTimezone = normalizeNexSourceHeaderValue(options.clientTimezone) ?? "unknown";
  const deviceMid = normalizeNexSourceHeaderValue(options.deviceMid);
  const endpointOrigin =
    normalizeNexSourceHeaderValue(options.endpointOrigin) ?? DEFAULT_NEX_ENDPOINT_ORIGIN;
  const osVersion = normalizeNexSourceHeaderValue(options.osVersion);
  const platform = normalizeNexSourceHeaderValue(options.platform);
  const releaseChannel = normalizeNexSourceHeaderValue(options.releaseChannel);
  const sourceTitle = normalizeNexSourceHeaderValue(options.sourceTitle) ?? "electron";

  return {
    ...NEX_SOURCE_HEADERS,
    "HTTP-Referer": endpointOrigin,
    "User-Agent": `Nex/${appVersion ?? "unknown"}`,
    ...(appVersion ? { "X-Nex-App-Version": appVersion } : {}),
    "X-Title": `Z Code@${sourceTitle}`,
    ...(platform && arch ? { "X-Platform": `${platform}-${arch}` } : {}),
    ...(releaseChannel ? { "X-Release-Channel": releaseChannel } : {}),
    "X-Client-Language": clientLanguage,
    "X-Client-Timezone": clientTimezone,
    ...(platform ? { "X-Os-Category": normalizeOsCategory(platform) } : {}),
    ...(osVersion ? { "X-Os-Version": osVersion } : {}),
    ...(deviceMid ? { "X-Device-Mid": deviceMid } : {}),
  };
}

function normalizeOsCategory(platform: string): string {
  switch (platform) {
    case "darwin":
      return "macos";
    case "win32":
      return "windows";
    default:
      return "linux";
  }
}
