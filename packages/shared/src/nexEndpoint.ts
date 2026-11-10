import type { NexEnv } from "./env.js";

export const DEFAULT_NEX_ENDPOINT_ORIGIN = "https://zcode.z.ai";
export const TEST_NEX_ENDPOINT_ORIGIN = "https://zcode.z.ai";
export const CODING_PLAN_LOCAL_WEBVIEW_ORIGIN = "http://localhost:3000";
export const DEFAULT_BIGMODEL_API_ORIGIN = "https://bigmodel.cn";
export const TEST_BIGMODEL_API_ORIGIN = "https://bigmodel.cn";
export const DEFAULT_ZAI_OAUTH_ORIGIN = "https://chat.z.ai";
export const TEST_ZAI_OAUTH_ORIGIN = "https://chat.z.ai";
export const DEFAULT_ZAI_BUSINESS_BASE_URL = "https://api.z.ai";
export const TEST_ZAI_BUSINESS_BASE_URL = "https://api.z.ai";
export const DEFAULT_ZAI_OAUTH_CLIENT_ID = "client_P8X5CMWmlaRO9gyO-KSqtg";
export const TEST_ZAI_OAUTH_CLIENT_ID = "client_P8X5CMWmlaRO9gyO-KSqtg";
export const DEFAULT_WEB_REMOTE_CONTROL_RELAY_WS_URL = "wss://zcode.z.ai/api/v1/remote/ws2";
export const WEB_REMOTE_CONTROL_V4_MIN_APP_VERSION = "3.4.0";

export interface BuildNexEndpointUrlsOptions {
  appVersion?: string;
}

function isWebRemoteControlV4AppVersion(appVersion: string | undefined): boolean {
  if (!appVersion) {
    return false;
  }

  // nexEndpoint 会被 Vite 配置在 Node 加载期直接导入，不能依赖源码态 `.js` runtime import。
  // 这里只比较固定的稳定版门槛；高于 3.4.0 的预发布版本仍满足门槛，3.4.0 自身的预发布版本不满足。
  const normalizedVersion = appVersion.trim().replace(/^v/i, "");
  const match = normalizedVersion.match(
    /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([^+]+))?(?:\+(.+))?$/,
  );
  if (!match) {
    return false;
  }

  const prereleaseIdentifiers = match[4]?.split(".");
  const hasValidPrerelease =
    prereleaseIdentifiers === undefined ||
    prereleaseIdentifiers.every(
      (identifier) =>
        /^[0-9A-Za-z-]+$/.test(identifier) &&
        (!/^\d+$/.test(identifier) || /^(0|[1-9]\d*)$/.test(identifier)),
    );
  const buildIdentifiers = match[5]?.split(".");
  const hasValidBuild =
    buildIdentifiers === undefined ||
    buildIdentifiers.every((identifier) => /^[0-9A-Za-z-]+$/.test(identifier));
  if (!hasValidPrerelease || !hasValidBuild) {
    return false;
  }

  const versionParts = [Number(match[1]), Number(match[2]), Number(match[3])];
  const floorParts = WEB_REMOTE_CONTROL_V4_MIN_APP_VERSION.split(".").map(Number);
  for (let index = 0; index < floorParts.length; index += 1) {
    if (versionParts[index]! !== floorParts[index]!) {
      return versionParts[index]! > floorParts[index]!;
    }
  }

  return prereleaseIdentifiers === undefined;
}

export interface NexEndpointUrls {
  origin: string;
  apiBaseUrl: string;
  remoteUrl: string;
  webRemoteCallbackUrl: string;
  webShareCallbackUrl: string;
  relayWsUrl: string;
  nexPlanOpenAiBaseUrl: string;
  nexPlanAnthropicBaseUrl: string;
  nexPlanBillingCurrentUrl: string;
  nexPlanBillingBalanceUrl: string;
}

export interface RuntimeNexEndpointEnv {
  [key: string]: string | undefined;
  NEX_ENV?: string;
  NEX_BASE_URL?: string;
  NEX_ENDPOINT_ORIGIN?: string;
  NEX_TEST_BASE_URL?: string;
  NEX_PRODUCTION_BASE_URL?: string;
}

export interface RuntimeBigModelApiEnv {
  [key: string]: string | undefined;
  NEX_ENV?: string;
  BIGMODEL_API_BASE_URL?: string;
  BIGMODEL_TEST_API_BASE_URL?: string;
  BIGMODEL_PRODUCTION_API_BASE_URL?: string;
}

export interface RuntimeZaiEndpointEnv {
  [key: string]: string | undefined;
  NEX_ENV?: string;
  ZAI_OAUTH_ORIGIN?: string;
  ZAI_TEST_OAUTH_ORIGIN?: string;
  ZAI_PRODUCTION_OAUTH_ORIGIN?: string;
  ZAI_BUSINESS_BASE_URL?: string;
  ZAI_TEST_BUSINESS_BASE_URL?: string;
  ZAI_PRODUCTION_BUSINESS_BASE_URL?: string;
  ZAI_OAUTH_CLIENT_ID?: string;
  ZAI_TEST_OAUTH_CLIENT_ID?: string;
  ZAI_PRODUCTION_OAUTH_CLIENT_ID?: string;
  ZAI_OAUTH_APP_ID?: string;
}

export interface RuntimeProductEndpointEnv
  extends RuntimeNexEndpointEnv, RuntimeBigModelApiEnv, RuntimeZaiEndpointEnv {}

export interface RuntimeProductEndpointConfig {
  nexEnv: NexEnv;
  nexEndpointOrigin: string;
  nexEndpointUrls: NexEndpointUrls;
  zaiOAuthOrigin: string;
  zaiBusinessBaseUrl: string;
  zaiOAuthClientId: string;
  bigModelApiOrigin: string;
}

function readRuntimeEnvValue(
  env: Record<string, string | undefined>,
  key: string,
): string | undefined {
  const value = env[key]?.trim();
  return value ? value : undefined;
}

function readRuntimeNexEnvScopedValue(
  env: Record<string, string | undefined>,
  nexEnv: NexEnv,
  testKey: string,
  productionKey: string,
): string | undefined {
  return readRuntimeEnvValue(env, nexEnv === "production" ? productionKey : testKey);
}

export function normalizeNexEndpointOrigin(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) {
    throw new Error("Nex endpoint origin is empty");
  }

  const parsed = new URL(trimmed);
  if (parsed.protocol !== "https:" && parsed.protocol !== "http:") {
    throw new Error("Nex endpoint origin must use http or https");
  }
  return parsed.origin;
}

function isLoopbackHostname(hostname: string): boolean {
  return hostname === "localhost" || hostname === "127.0.0.1" || hostname === "::1";
}

export function isTrustedCodingPlanWebviewOrigin(
  value: string | null | undefined,
  options?: {
    e2eStoreBridgeEnabled?: boolean;
  },
): boolean {
  if (!value) return false;
  try {
    const origin = normalizeNexEndpointOrigin(value);
    if (
      origin === DEFAULT_NEX_ENDPOINT_ORIGIN ||
      origin === TEST_NEX_ENDPOINT_ORIGIN ||
      origin === CODING_PLAN_LOCAL_WEBVIEW_ORIGIN
    ) {
      return true;
    }
    const parsed = new URL(origin);
    return options?.e2eStoreBridgeEnabled === true && isLoopbackHostname(parsed.hostname);
  } catch {
    return false;
  }
}

export function resolveNexEndpointOrigin(options?: {
  env?: NexEnv;
  envBaseOrigin?: string | null;
  overrideOrigin?: string | null;
}): string {
  const env = options?.env ?? "test";
  const envBaseOrigin = options?.envBaseOrigin?.trim();
  if (env === "production") {
    return envBaseOrigin
      ? normalizeNexEndpointOrigin(envBaseOrigin)
      : DEFAULT_NEX_ENDPOINT_ORIGIN;
  }

  const overrideOrigin = options?.overrideOrigin?.trim();
  if (overrideOrigin) {
    return normalizeNexEndpointOrigin(overrideOrigin);
  }

  return envBaseOrigin ? normalizeNexEndpointOrigin(envBaseOrigin) : TEST_NEX_ENDPOINT_ORIGIN;
}

export function resolveRuntimeNexEnv(env: RuntimeNexEndpointEnv = {}): NexEnv {
  // 独立 CLI / 服务测试未注入 NEX_ENV 时默认生产域；桌面测试包会显式下发 NEX_ENV=test。
  return env.NEX_ENV?.trim().toLowerCase() === "test" ? "test" : "production";
}

export function resolveRuntimeNexEndpointOrigin(
  env: RuntimeNexEndpointEnv = {},
  options?: { overrideOrigin?: string | null },
): string {
  const nexEnv = resolveRuntimeNexEnv(env);
  const scopedBaseOrigin =
    nexEnv === "production"
      ? readRuntimeEnvValue(env, "NEX_PRODUCTION_BASE_URL")
      : readRuntimeEnvValue(env, "NEX_TEST_BASE_URL");

  return resolveNexEndpointOrigin({
    env: nexEnv,
    envBaseOrigin:
      readRuntimeEnvValue(env, "NEX_BASE_URL") ??
      readRuntimeEnvValue(env, "NEX_ENDPOINT_ORIGIN") ??
      scopedBaseOrigin,
    overrideOrigin: options?.overrideOrigin,
  });
}

export function buildRuntimeNexEndpointUrls(
  env: RuntimeNexEndpointEnv = {},
): NexEndpointUrls {
  return buildNexEndpointUrls(resolveRuntimeNexEndpointOrigin(env));
}

export function buildRuntimeNexApiUrl(env: RuntimeNexEndpointEnv = {}, path: string): string {
  const normalizedPath = path.startsWith("/") ? path : `/${path}`;
  return `${resolveRuntimeNexEndpointOrigin(env)}${normalizedPath}`;
}

export function resolveWebRemoteControlRelayWsUrl(options?: {
  endpointOrigin?: string | null | undefined;
  overrideUrl?: string | null | undefined;
}): string {
  const overrideUrl = options?.overrideUrl?.trim();
  if (overrideUrl) {
    return overrideUrl;
  }

  const origin = new URL(
    normalizeNexEndpointOrigin(options?.endpointOrigin?.trim() || DEFAULT_NEX_ENDPOINT_ORIGIN),
  );
  origin.protocol = origin.protocol === "https:" ? "wss:" : "ws:";
  origin.pathname = "/api/v1/remote/ws2";
  return origin.toString();
}

export function resolveBigModelApiOrigin(env: RuntimeBigModelApiEnv = {}): string {
  const nexEnv = resolveRuntimeNexEnv(env);
  const scopedBaseOrigin =
    nexEnv === "production"
      ? readRuntimeEnvValue(env, "BIGMODEL_PRODUCTION_API_BASE_URL")
      : readRuntimeEnvValue(env, "BIGMODEL_TEST_API_BASE_URL");
  const fallbackOrigin =
    nexEnv === "production" ? DEFAULT_BIGMODEL_API_ORIGIN : TEST_BIGMODEL_API_ORIGIN;

  return normalizeNexEndpointOrigin(
    readRuntimeEnvValue(env, "BIGMODEL_API_BASE_URL") ?? scopedBaseOrigin ?? fallbackOrigin,
  );
}

export function buildBigModelApiUrl(env: RuntimeBigModelApiEnv = {}, path: string): string {
  const normalizedPath = path.startsWith("/") ? path : `/${path}`;
  return `${resolveBigModelApiOrigin(env)}${normalizedPath}`;
}

export function buildBigModelCodingPlanPersonalManageUrl(env: RuntimeBigModelApiEnv = {}): string {
  // 修复原因：BigModel Coding Plan 管理页要跟随 NEX_ENV；
  // 测试环境如果继续写死 bigmodel.cn，会把测试账号带到生产套餐页。
  return buildBigModelApiUrl(env, "/coding-plan/personal/overview");
}

export function buildBigModelCodingPlanTeamManageUrl(env: RuntimeBigModelApiEnv = {}): string {
  return buildBigModelApiUrl(env, "/coding-plan/team/plans");
}

export function resolveZaiOAuthOrigin(env: RuntimeZaiEndpointEnv = {}): string {
  const nexEnv = resolveRuntimeNexEnv(env);
  const scopedOrigin = readRuntimeNexEnvScopedValue(
    env,
    nexEnv,
    "ZAI_TEST_OAUTH_ORIGIN",
    "ZAI_PRODUCTION_OAUTH_ORIGIN",
  );
  const fallbackOrigin =
    nexEnv === "production" ? DEFAULT_ZAI_OAUTH_ORIGIN : TEST_ZAI_OAUTH_ORIGIN;

  return normalizeNexEndpointOrigin(
    readRuntimeEnvValue(env, "ZAI_OAUTH_ORIGIN") ?? scopedOrigin ?? fallbackOrigin,
  );
}

export function resolveZaiBusinessBaseUrl(env: RuntimeZaiEndpointEnv = {}): string {
  const nexEnv = resolveRuntimeNexEnv(env);
  const scopedOrigin = readRuntimeNexEnvScopedValue(
    env,
    nexEnv,
    "ZAI_TEST_BUSINESS_BASE_URL",
    "ZAI_PRODUCTION_BUSINESS_BASE_URL",
  );
  const fallbackOrigin =
    nexEnv === "production" ? DEFAULT_ZAI_BUSINESS_BASE_URL : TEST_ZAI_BUSINESS_BASE_URL;

  return normalizeNexEndpointOrigin(
    readRuntimeEnvValue(env, "ZAI_BUSINESS_BASE_URL") ?? scopedOrigin ?? fallbackOrigin,
  );
}

export function resolveZaiOAuthClientId(env: RuntimeZaiEndpointEnv = {}): string {
  const nexEnv = resolveRuntimeNexEnv(env);
  const scopedClientId = readRuntimeNexEnvScopedValue(
    env,
    nexEnv,
    "ZAI_TEST_OAUTH_CLIENT_ID",
    "ZAI_PRODUCTION_OAUTH_CLIENT_ID",
  );
  const fallbackClientId =
    nexEnv === "production" ? DEFAULT_ZAI_OAUTH_CLIENT_ID : TEST_ZAI_OAUTH_CLIENT_ID;

  return (
    readRuntimeEnvValue(env, "ZAI_OAUTH_CLIENT_ID") ??
    scopedClientId ??
    readRuntimeEnvValue(env, "ZAI_OAUTH_APP_ID") ??
    fallbackClientId
  );
}

export function buildZaiOAuthUrl(origin: string, path: string): string {
  const normalizedPath = path.startsWith("/") ? path : `/${path}`;
  return `${normalizeNexEndpointOrigin(origin)}${normalizedPath}`;
}

export function buildRuntimeZaiOAuthUrl(env: RuntimeZaiEndpointEnv = {}, path: string): string {
  return buildZaiOAuthUrl(resolveZaiOAuthOrigin(env), path);
}

export function buildRuntimeZaiBusinessUrl(env: RuntimeZaiEndpointEnv = {}, path: string): string {
  const normalizedPath = path.startsWith("/") ? path : `/${path}`;
  return `${resolveZaiBusinessBaseUrl(env)}${normalizedPath}`;
}

export function resolveRuntimeProductEndpointConfig(
  env: RuntimeProductEndpointEnv = {},
): RuntimeProductEndpointConfig {
  const nexEnv = resolveRuntimeNexEnv(env);
  const nexEndpointOrigin = resolveRuntimeNexEndpointOrigin(env);

  return {
    nexEnv,
    nexEndpointOrigin,
    nexEndpointUrls: buildNexEndpointUrls(nexEndpointOrigin),
    zaiOAuthOrigin: resolveZaiOAuthOrigin(env),
    zaiBusinessBaseUrl: resolveZaiBusinessBaseUrl(env),
    zaiOAuthClientId: resolveZaiOAuthClientId(env),
    bigModelApiOrigin: resolveBigModelApiOrigin(env),
  };
}

export function buildNexEndpointUrls(
  origin: string,
  options: BuildNexEndpointUrlsOptions = {},
): NexEndpointUrls {
  const normalizedOrigin = normalizeNexEndpointOrigin(origin);
  const parsed = new URL(normalizedOrigin);
  const wsProtocol = parsed.protocol === "https:" ? "wss:" : "ws:";
  const wsOrigin = `${wsProtocol}//${parsed.host}`;
  // 无法解析或低于分界版本时保守使用 v3，避免开发版本和旧客户端进入未兼容的 v4 页面。
  const remoteVersion = isWebRemoteControlV4AppVersion(options.appVersion) ? "v4" : "v3";

  return {
    origin: normalizedOrigin,
    apiBaseUrl: `${normalizedOrigin}/api/v1`,
    remoteUrl: `${normalizedOrigin}/remote/${remoteVersion}`,
    webRemoteCallbackUrl: `${normalizedOrigin}/remote/v4`,
    webShareCallbackUrl: `${normalizedOrigin}/cn/share/callback`,
    relayWsUrl: `${wsOrigin}/api/v1/remote/ws2`,
    nexPlanOpenAiBaseUrl: `${normalizedOrigin}/api/v1/nex-plan`,
    nexPlanAnthropicBaseUrl: `${normalizedOrigin}/api/v1/nex-plan/anthropic`,
    nexPlanBillingCurrentUrl: `${normalizedOrigin}/api/v1/nex-plan/billing/current`,
    nexPlanBillingBalanceUrl: `${normalizedOrigin}/api/v1/nex-plan/billing/balance`,
  };
}

export function rewriteNexEndpointUrl(input: string | URL, endpointOrigin: string): string | URL {
  const originalUrl = typeof input === "string" ? input : input.toString();
  let parsed: URL;
  try {
    parsed = new URL(originalUrl);
  } catch {
    return input;
  }
  const sourceOrigin = DEFAULT_NEX_ENDPOINT_ORIGIN;
  if (parsed.origin !== sourceOrigin) {
    return input;
  }

  const targetOrigin = normalizeNexEndpointOrigin(endpointOrigin);
  if (targetOrigin === sourceOrigin) {
    return input;
  }

  const target = new URL(targetOrigin);
  target.pathname = parsed.pathname;
  target.search = parsed.search;
  target.hash = parsed.hash;
  return target.toString();
}
