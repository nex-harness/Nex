import { createHash, randomBytes, timingSafeEqual } from "node:crypto";
import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { dirname } from "node:path";
import { getConnInfo } from "@hono/node-server/conninfo";
import type { Context, Hono, MiddlewareHandler } from "hono";

// 规则、状态所有者与失败语义见 docs/specs/server-web-login.md。

const SESSION_COOKIE_NAME = "nex_session";
const SESSION_TTL_MS = 30 * 24 * 60 * 60 * 1000;
const MAX_STORED_SESSIONS = 100;
const SESSIONS_FILE_VERSION = 1;
const MAX_LOGIN_FAILURES = 5;
const LOGIN_LOCK_MS = 60_000;
const LIMITER_ENTRY_TTL_MS = 10 * 60_000;
const LIMITER_MAX_ENTRIES = 1000;
const MAX_TOKEN_LENGTH = 1024;

interface StoredSession {
  idHash: string;
  createdAt: number;
  expiresAt: number;
}

interface SessionsFile {
  version: typeof SESSIONS_FILE_VERSION;
  tokenFingerprint: string;
  sessions: StoredSession[];
}

function sha256Hex(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}

function tokenFingerprint(token: string): string {
  return sha256Hex(`nex-web-session:${token}`);
}

/** 先各自哈希再 timingSafeEqual，避免长度与内容泄漏。 */
export function tokensEqual(expected: string, provided: string): boolean {
  const a = createHash("sha256").update(expected).digest();
  const b = createHash("sha256").update(provided).digest();
  return timingSafeEqual(a, b);
}

function isStoredSession(value: unknown): value is StoredSession {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as Record<string, unknown>;
  return (
    typeof candidate["idHash"] === "string" &&
    typeof candidate["createdAt"] === "number" &&
    typeof candidate["expiresAt"] === "number"
  );
}

export interface WebSessionStore {
  create(): Promise<string>;
  validate(sessionId: string | undefined): Promise<boolean>;
  revoke(sessionId: string | undefined): Promise<void>;
}

export function createWebSessionStore(options: {
  token: string;
  /** 缺省时只在内存里保存。 */
  filePath?: string;
  now?: () => number;
  logger?: { warn: (...args: unknown[]) => void };
}): WebSessionStore {
  const now = options.now ?? Date.now;
  const fingerprint = tokenFingerprint(options.token);
  let sessions: StoredSession[] = [];
  let loaded: Promise<void> | null = null;
  // 所有写入串行化，保证 create/revoke 并发时文件内容是同一份内存状态的快照。
  let writeChain: Promise<void> = Promise.resolve();

  const prune = () => {
    const current = now();
    sessions = sessions.filter((session) => session.expiresAt > current);
    if (sessions.length > MAX_STORED_SESSIONS) {
      sessions = sessions
        .sort((left, right) => right.createdAt - left.createdAt)
        .slice(0, MAX_STORED_SESSIONS);
    }
  };

  const load = (): Promise<void> => {
    loaded ??= (async () => {
      if (!options.filePath) return;
      try {
        const parsed: unknown = JSON.parse(await readFile(options.filePath, "utf8"));
        if (
          typeof parsed === "object" &&
          parsed !== null &&
          (parsed as SessionsFile).version === SESSIONS_FILE_VERSION &&
          (parsed as SessionsFile).tokenFingerprint === fingerprint &&
          Array.isArray((parsed as SessionsFile).sessions)
        ) {
          sessions = (parsed as SessionsFile).sessions.filter(isStoredSession);
          prune();
        }
        // token 变更（指纹不同）时丢弃全部会话：旧 token 签发的登录态不能延续。
      } catch (error) {
        // 缺失、不可读或损坏都按「没有会话」处理（fail closed），不能因此关闭鉴权。
        if ((error as NodeJS.ErrnoException).code !== "ENOENT") {
          options.logger?.warn("[web-auth] sessions file unreadable, starting empty", error);
        }
      }
    })();
    return loaded;
  };

  const persist = async (): Promise<void> => {
    const { filePath } = options;
    if (!filePath) return;
    const payload: SessionsFile = {
      version: SESSIONS_FILE_VERSION,
      tokenFingerprint: fingerprint,
      sessions,
    };
    const run = async () => {
      await mkdir(dirname(filePath), { recursive: true });
      const tmpPath = `${filePath}.${process.pid}.tmp`;
      await writeFile(tmpPath, `${JSON.stringify(payload, null, 2)}\n`, { mode: 0o600 });
      await rename(tmpPath, filePath);
    };
    const result = writeChain.then(run, run);
    writeChain = result.catch(() => undefined);
    await result;
  };

  return {
    async create() {
      await load();
      const sessionId = randomBytes(32).toString("base64url");
      const createdAt = now();
      const entry: StoredSession = {
        idHash: sha256Hex(sessionId),
        createdAt,
        expiresAt: createdAt + SESSION_TTL_MS,
      };
      sessions.push(entry);
      prune();
      try {
        await persist();
      } catch (error) {
        // 落盘失败则会话不算创建成功，避免重启后「刚登录就失效」。
        sessions = sessions.filter((session) => session !== entry);
        throw error;
      }
      return sessionId;
    },
    async validate(sessionId) {
      if (!sessionId) return false;
      await load();
      const idHash = sha256Hex(sessionId);
      const current = now();
      return sessions.some((session) => session.idHash === idHash && session.expiresAt > current);
    },
    async revoke(sessionId) {
      if (!sessionId) return;
      await load();
      const idHash = sha256Hex(sessionId);
      const before = sessions.length;
      sessions = sessions.filter((session) => session.idHash !== idHash);
      if (sessions.length === before) return;
      try {
        await persist();
      } catch (error) {
        options.logger?.warn("[web-auth] failed to persist logout", error);
      }
    },
  };
}

interface LimiterEntry {
  failures: number;
  lockedUntil: number;
  lastFailureAt: number;
}

export interface LoginRateLimiter {
  /** 返回剩余锁定毫秒数；未锁定返回 0。 */
  lockedForMs(key: string): number;
  recordFailure(key: string): void;
  recordSuccess(key: string): void;
}

export function createLoginRateLimiter(now: () => number = Date.now): LoginRateLimiter {
  const entries = new Map<string, LimiterEntry>();
  const dropStale = () => {
    const current = now();
    for (const [key, entry] of entries) {
      if (entry.lockedUntil <= current && current - entry.lastFailureAt > LIMITER_ENTRY_TTL_MS) {
        entries.delete(key);
      }
    }
  };
  return {
    lockedForMs(key) {
      const entry = entries.get(key);
      if (!entry) return 0;
      return Math.max(0, entry.lockedUntil - now());
    },
    recordFailure(key) {
      if (entries.size >= LIMITER_MAX_ENTRIES) dropStale();
      const current = now();
      const entry = entries.get(key) ?? { failures: 0, lockedUntil: 0, lastFailureAt: current };
      // 锁定期满后重新计数，而不是第一次失败就再次锁定。
      if (entry.lockedUntil > 0 && entry.lockedUntil <= current) {
        entry.failures = 0;
        entry.lockedUntil = 0;
      }
      entry.failures += 1;
      entry.lastFailureAt = current;
      if (entry.failures >= MAX_LOGIN_FAILURES) {
        entry.lockedUntil = current + LOGIN_LOCK_MS;
      }
      entries.set(key, entry);
    },
    recordSuccess(key) {
      entries.delete(key);
    },
  };
}

function parseCookieHeader(header: string | undefined): Map<string, string> {
  const cookies = new Map<string, string>();
  if (!header) return cookies;
  for (const part of header.split(";")) {
    const separator = part.indexOf("=");
    if (separator <= 0) continue;
    const name = part.slice(0, separator).trim();
    const value = part.slice(separator + 1).trim();
    if (!name) continue;
    try {
      cookies.set(name, decodeURIComponent(value));
    } catch {
      cookies.set(name, value);
    }
  }
  return cookies;
}

function isSecureRequest(c: Context): boolean {
  return (
    new URL(c.req.url).protocol === "https:" ||
    c.req.header("x-forwarded-proto")?.split(",")[0]?.trim() === "https"
  );
}

function sessionCookie(c: Context, sessionId: string): string {
  const attributes = [
    `${SESSION_COOKIE_NAME}=${encodeURIComponent(sessionId)}`,
    "Path=/",
    "HttpOnly",
    "SameSite=Lax",
    `Max-Age=${SESSION_TTL_MS / 1000}`,
  ];
  if (isSecureRequest(c)) attributes.push("Secure");
  return attributes.join("; ");
}

function clearedSessionCookie(c: Context): string {
  const attributes = [`${SESSION_COOKIE_NAME}=`, "Path=/", "HttpOnly", "SameSite=Lax", "Max-Age=0"];
  if (isSecureRequest(c)) attributes.push("Secure");
  return attributes.join("; ");
}

function isAuthRoute(pathname: string): boolean {
  return pathname === "/api/auth" || pathname.startsWith("/api/auth/");
}

/** 受保护路径：WebSocket 与 /api/*，登录相关的 /api/auth/* 除外。 */
export function isTokenProtectedPath(pathname: string): boolean {
  if (isAuthRoute(pathname)) return false;
  return pathname === "/ws" || pathname.startsWith("/ws/") || pathname.startsWith("/api/");
}

function remoteAddressKey(c: Context): string {
  try {
    return getConnInfo(c).remote.address ?? "unknown";
  } catch {
    // 非 node-server 环境（如测试里的 app.request）没有 socket 信息。
    return "unknown";
  }
}

/**
 * 用 Cookie 鉴权的请求必须同源：浏览器对 WebSocket 与跨源 fetch 都会带 Origin，
 * 同站不同端口的页面不能借用户的会话 Cookie 去连这个 Server。没有 Origin（非浏览器客户端）放行。
 */
function isSameOriginRequest(c: Context): boolean {
  const origin = c.req.header("origin");
  if (!origin) return true;
  const host = c.req.header("x-forwarded-host")?.split(",")[0]?.trim() || c.req.header("host");
  if (!host) return false;
  try {
    return new URL(origin).host === host;
  } catch {
    return false;
  }
}

export interface WebAuth {
  middleware(): MiddlewareHandler;
  mountRoutes(app: Hono): void;
  /** 请求携带有效会话 Cookie（不含 ingress token）。 */
  hasValidSession(c: Context): Promise<boolean>;
}

export function createWebAuth(options: {
  token: string;
  sessionsFilePath?: string;
  /** ingress token 已校验通过的程序化请求；它们不需要会话。 */
  isIngressAuthenticated: (c: Context) => boolean;
  unauthenticatedServerInfo: () => unknown;
  now?: () => number;
  logger?: { warn: (...args: unknown[]) => void };
}): WebAuth {
  const token = options.token;
  const now = options.now ?? Date.now;
  const store = createWebSessionStore({
    token,
    ...(options.sessionsFilePath ? { filePath: options.sessionsFilePath } : {}),
    now,
    ...(options.logger ? { logger: options.logger } : {}),
  });
  const limiter = createLoginRateLimiter(now);

  const readSessionId = (c: Context) =>
    parseCookieHeader(c.req.header("cookie")).get(SESSION_COOKIE_NAME);
  const hasValidSession = (c: Context) => store.validate(readSessionId(c));

  return {
    hasValidSession,
    middleware() {
      return async (c, next) => {
        const pathname = new URL(c.req.url).pathname;
        // 页面与登录接口不鉴权；ingress token 的程序化请求保持原有行为。
        if (!isTokenProtectedPath(pathname) || options.isIngressAuthenticated(c)) return next();
        if (await hasValidSession(c)) {
          if (!isSameOriginRequest(c))
            return c.json({ error: "Cross-origin request rejected" }, 403);
          return next();
        }
        // 未登录的 server-info 只暴露版本/兼容性，不泄漏工作区路径与身份。
        if (pathname === "/api/server-info") return c.json(options.unauthenticatedServerInfo());
        return c.json({ error: "Unauthorized" }, 401);
      };
    },
    mountRoutes(app) {
      app.get("/api/auth/session", async (c) => {
        c.header("Cache-Control", "no-store");
        return c.json({
          authRequired: true,
          authenticated: options.isIngressAuthenticated(c) || (await hasValidSession(c)),
        });
      });

      app.post("/api/auth/login", async (c) => {
        c.header("Cache-Control", "no-store");
        if (!isSameOriginRequest(c)) return c.json({ error: "Cross-origin request rejected" }, 403);
        const key = remoteAddressKey(c);
        const lockedMs = limiter.lockedForMs(key);
        if (lockedMs > 0) {
          c.header("Retry-After", String(Math.ceil(lockedMs / 1000)));
          return c.json({ error: "Too many attempts" }, 429);
        }
        let provided: unknown;
        try {
          provided = ((await c.req.json()) as { token?: unknown } | null)?.token;
        } catch {
          return c.json({ error: "Invalid request body" }, 400);
        }
        if (typeof provided !== "string" || provided.length === 0) {
          return c.json({ error: "Invalid request body" }, 400);
        }
        if (provided.length > MAX_TOKEN_LENGTH || !tokensEqual(token, provided.trim())) {
          limiter.recordFailure(key);
          return c.json({ error: "Invalid token" }, 401);
        }
        limiter.recordSuccess(key);
        try {
          const sessionId = await store.create();
          c.header("Set-Cookie", sessionCookie(c, sessionId));
          return c.json({ ok: true });
        } catch (error) {
          options.logger?.warn("[web-auth] failed to create session", error);
          return c.json({ error: "Failed to create session" }, 500);
        }
      });

      app.post("/api/auth/logout", async (c) => {
        c.header("Cache-Control", "no-store");
        await store.revoke(readSessionId(c));
        c.header("Set-Cookie", clearedSessionCookie(c));
        return c.json({ ok: true });
      });
    },
  };
}
