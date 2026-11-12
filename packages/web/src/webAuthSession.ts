// 规则与事件顺序见 docs/specs/server-web-login.md。

export const WEB_LOGIN_PATH = "/login";

export interface WebAuthSessionState {
  authRequired: boolean;
  authenticated: boolean;
}

export type WebLoginResult =
  | { kind: "ok" }
  | { kind: "invalid" }
  | { kind: "rateLimited"; retryAfterSeconds: number | null }
  | { kind: "error" };

type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

/** `next` 只接受同源相对路径；协议相对（//host）与带 scheme 的值一律回落到 `/`。 */
export function sanitizeNextPath(raw: string | null | undefined): string {
  if (!raw || !raw.startsWith("/") || raw.startsWith("//") || raw.includes("\\")) {
    return "/";
  }
  // 控制字符（含换行）会被浏览器当作头部/URL 分隔符处理，一律拒绝。
  if ([...raw].some((char) => char.charCodeAt(0) < 0x20)) {
    return "/";
  }
  // 登录页本身不能作为登录后的落点，否则会循环。
  const pathname = raw.split(/[?#]/, 1)[0];
  if (pathname === WEB_LOGIN_PATH) {
    return "/";
  }
  return raw;
}

export function buildLoginUrl(current: { pathname: string; search: string; hash: string }): string {
  const next = sanitizeNextPath(`${current.pathname}${current.search}${current.hash}`);
  return next === "/" ? WEB_LOGIN_PATH : `${WEB_LOGIN_PATH}?next=${encodeURIComponent(next)}`;
}

/** 查询当前浏览器会话；服务端不可达或不支持登录时返回 null，由调用方沿用原有启动路径。 */
export async function fetchWebAuthSession(
  fetchImpl: FetchLike = (input, init) => fetch(input, init),
): Promise<WebAuthSessionState | null> {
  try {
    const response = await fetchImpl("/api/auth/session", {
      cache: "no-store",
      credentials: "same-origin",
    });
    if (!response.ok) {
      return null;
    }
    const body = (await response.json()) as Partial<WebAuthSessionState>;
    if (typeof body.authRequired !== "boolean" || typeof body.authenticated !== "boolean") {
      return null;
    }
    return { authRequired: body.authRequired, authenticated: body.authenticated };
  } catch {
    return null;
  }
}

export async function submitWebLogin(
  token: string,
  fetchImpl: FetchLike = (input, init) => fetch(input, init),
): Promise<WebLoginResult> {
  try {
    const response = await fetchImpl("/api/auth/login", {
      method: "POST",
      credentials: "same-origin",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ token }),
    });
    if (response.ok) {
      return { kind: "ok" };
    }
    if (response.status === 401) {
      return { kind: "invalid" };
    }
    if (response.status === 429) {
      const retryAfter = Number(response.headers.get("retry-after"));
      return {
        kind: "rateLimited",
        retryAfterSeconds: Number.isFinite(retryAfter) && retryAfter > 0 ? retryAfter : null,
      };
    }
    return { kind: "error" };
  } catch {
    return { kind: "error" };
  }
}

export async function submitWebLogout(
  fetchImpl: FetchLike = (input, init) => fetch(input, init),
): Promise<void> {
  try {
    await fetchImpl("/api/auth/logout", { method: "POST", credentials: "same-origin" });
  } catch {
    // 登出请求失败时 Cookie 仍会在服务端过期；调用方照常跳转登录页。
  }
}

export type WebAuthGate = { kind: "proceed" } | { kind: "redirect"; to: string };

/**
 * 启动与掉线时共用的决策：需要登录且未登录 → 跳登录页；其余情况放行。
 * 会话接口不可达（null）放行，由原有启动/错误路径处理；服务端始终强制鉴权。
 */
export function decideWebAuthGate(
  session: WebAuthSessionState | null,
  current: { pathname: string; search: string; hash: string },
): WebAuthGate {
  if (session && session.authRequired && !session.authenticated) {
    return { kind: "redirect", to: buildLoginUrl(current) };
  }
  return { kind: "proceed" };
}

const DISCONNECT_RECHECK_DELAYS_MS = [1_000, 2_000, 4_000, 8_000, 15_000, 30_000];

/**
 * 应用运行中 WebSocket 断开后，等服务端给出明确的登录态。
 * 服务端重启（例如轮换 token）期间会话接口暂时不可达，单次检查会得到 null 而错过失效；
 * 这里按退避重试直到服务端应答，总时长有上限，超时返回 null（沿用断线原有处理）。
 */
export async function resolveWebAuthAfterDisconnect(options?: {
  fetchSession?: () => Promise<WebAuthSessionState | null>;
  sleep?: (ms: number) => Promise<void>;
  delaysMs?: readonly number[];
}): Promise<WebAuthSessionState | null> {
  const fetchSession = options?.fetchSession ?? (() => fetchWebAuthSession());
  const sleep =
    options?.sleep ?? ((ms: number) => new Promise<void>((done) => setTimeout(done, ms)));
  const delays = options?.delaysMs ?? DISCONNECT_RECHECK_DELAYS_MS;
  for (let attempt = 0; attempt <= delays.length; attempt += 1) {
    const session = await fetchSession();
    if (session) {
      return session;
    }
    const delay = delays[attempt];
    if (delay === undefined) {
      break;
    }
    await sleep(delay);
  }
  return null;
}
