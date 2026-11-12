import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { mkdir, mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Hono } from "hono";
import { resolveServerLayout } from "../src/runtime/paths.js";
import {
  createLoginRateLimiter,
  createWebAuth,
  createWebSessionStore,
} from "../src/server-core/webAuth.js";
import {
  readOrCreateWebAccessTokenFile,
  resolveWebAccessToken,
  rotateWebAccessTokenFile,
} from "../src/server-core/webAccessToken.js";
import {
  parseServeFlags,
  readServeConfig,
  resolveServeOptions,
  writeServeConfig,
} from "../src/server-core/serveConfig.js";
import { assertWebRoot, createStaticWebHandler } from "../src/server-core/staticWeb.js";

const TOKEN = "correct-horse-battery";
let dir: string;

beforeEach(async () => {
  dir = await mkdtemp(join(tmpdir(), "nex-web-login-"));
});
afterEach(async () => {
  await rm(dir, { recursive: true, force: true });
});

function buildApp(options: { now?: () => number; sessionsFilePath?: string } = {}) {
  const auth = createWebAuth({
    token: TOKEN,
    sessionsFilePath: options.sessionsFilePath ?? join(dir, "web-sessions.json"),
    now: options.now,
    isIngressAuthenticated: (c) => c.req.header("authorization") === "Bearer ingress",
    unauthenticatedServerInfo: () => ({ authRequired: true }),
  });
  const app = new Hono();
  app.use("*", auth.middleware());
  auth.mountRoutes(app);
  app.get("/api/server-info", (c) => c.json({ full: true }));
  app.get("/api/thing", (c) => c.json({ ok: true }));
  app.get("/ws", (c) => c.text("ws"));
  app.get("/", (c) => c.text("page"));
  return app;
}

const login = (app: Hono, token: string, headers: Record<string, string> = {}) =>
  app.request("/api/auth/login", {
    method: "POST",
    headers: { "content-type": "application/json", ...headers },
    body: JSON.stringify({ token }),
  });

function cookieOf(response: Response): string {
  const header = response.headers.get("set-cookie") ?? "";
  return header.split(";")[0] ?? "";
}

describe("web login middleware and routes", () => {
  it("serves pages without a session but rejects /api and /ws", async () => {
    const app = buildApp();
    expect((await app.request("/")).status).toBe(200);
    expect((await app.request("/api/thing")).status).toBe(401);
    expect((await app.request("/ws")).status).toBe(401);
  });

  it("limits unauthenticated server-info to non-identifying data", async () => {
    const app = buildApp();
    expect(await (await app.request("/api/server-info")).json()).toEqual({ authRequired: true });
  });

  it("reports session state without requiring authentication", async () => {
    const app = buildApp();
    expect(await (await app.request("/api/auth/session")).json()).toEqual({
      authRequired: true,
      authenticated: false,
    });
  });

  it("rejects a wrong token and accepts the right one with an HttpOnly cookie", async () => {
    const app = buildApp();
    expect((await login(app, "nope")).status).toBe(401);
    const ok = await login(app, TOKEN);
    expect(ok.status).toBe(200);
    const setCookie = ok.headers.get("set-cookie") ?? "";
    expect(setCookie).toContain("HttpOnly");
    expect(setCookie).toContain("SameSite=Lax");
    expect(setCookie).not.toContain(TOKEN);
    const cookie = cookieOf(ok);
    expect((await app.request("/api/thing", { headers: { cookie } })).status).toBe(200);
    expect(await (await app.request("/api/server-info", { headers: { cookie } })).json()).toEqual({
      full: true,
    });
    expect(await (await app.request("/api/auth/session", { headers: { cookie } })).json()).toEqual({
      authRequired: true,
      authenticated: true,
    });
  });

  it("adds Secure behind an https proxy", async () => {
    const app = buildApp();
    const ok = await login(app, TOKEN, { "x-forwarded-proto": "https" });
    expect(ok.headers.get("set-cookie")).toContain("Secure");
  });

  it("lets ingress-token requests through without a session", async () => {
    const app = buildApp();
    const response = await app.request("/api/thing", {
      headers: { authorization: "Bearer ingress" },
    });
    expect(response.status).toBe(200);
  });

  it("rejects cookie-authenticated requests from another origin", async () => {
    const app = buildApp();
    const cookie = cookieOf(await login(app, TOKEN));
    const response = await app.request("http://nex.test/api/thing", {
      headers: { cookie, origin: "http://evil.test", host: "nex.test" },
    });
    expect(response.status).toBe(403);
    const same = await app.request("http://nex.test/api/thing", {
      headers: { cookie, origin: "http://nex.test", host: "nex.test" },
    });
    expect(same.status).toBe(200);
  });

  it("rejects cross-origin login attempts", async () => {
    const app = buildApp();
    const response = await login(app, TOKEN, { origin: "http://evil.test", host: "nex.test" });
    expect(response.status).toBe(403);
  });

  it("returns 400 for malformed bodies", async () => {
    const app = buildApp();
    const response = await app.request("/api/auth/login", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: "{not json",
    });
    expect(response.status).toBe(400);
    expect((await login(app, "")).status).toBe(400);
  });

  it("ends the session on logout", async () => {
    const app = buildApp();
    const cookie = cookieOf(await login(app, TOKEN));
    const out = await app.request("/api/auth/logout", { method: "POST", headers: { cookie } });
    expect(out.status).toBe(200);
    expect((await app.request("/api/thing", { headers: { cookie } })).status).toBe(401);
  });

  it("keeps sessions across a restart but drops them when the token changes", async () => {
    const file = join(dir, "web-sessions.json");
    const first = buildApp({ sessionsFilePath: file });
    const cookie = cookieOf(await login(first, TOKEN));
    const afterRestart = buildApp({ sessionsFilePath: file });
    expect((await afterRestart.request("/api/thing", { headers: { cookie } })).status).toBe(200);

    const rotated = createWebAuth({
      token: "a-different-token-value",
      sessionsFilePath: file,
      isIngressAuthenticated: () => false,
      unauthenticatedServerInfo: () => ({}),
    });
    const app = new Hono();
    app.use("*", rotated.middleware());
    app.get("/api/thing", (c) => c.json({ ok: true }));
    expect((await app.request("/api/thing", { headers: { cookie } })).status).toBe(401);
  });

  it("never writes the raw session id or token to disk", async () => {
    const file = join(dir, "web-sessions.json");
    const app = buildApp({ sessionsFilePath: file });
    const cookie = cookieOf(await login(app, TOKEN));
    const sessionId = decodeURIComponent(cookie.split("=")[1] ?? "");
    const text = await readFile(file, "utf8");
    expect(text).not.toContain(sessionId);
    expect(text).not.toContain(TOKEN);
    if (process.platform !== "win32") {
      expect((await stat(file)).mode & 0o777).toBe(0o600);
    }
  });

  it("treats a corrupt sessions file as no sessions rather than disabling auth", async () => {
    const file = join(dir, "web-sessions.json");
    await writeFile(file, "{corrupt");
    const app = buildApp({ sessionsFilePath: file });
    expect((await app.request("/api/thing")).status).toBe(401);
    const cookie = cookieOf(await login(app, TOKEN));
    expect((await app.request("/api/thing", { headers: { cookie } })).status).toBe(200);
  });
});

describe("session expiry and rate limiting", () => {
  it("expires sessions after 30 days without sliding renewal", async () => {
    let now = 1_000_000;
    const store = createWebSessionStore({ token: TOKEN, now: () => now });
    const id = await store.create();
    now += 29 * 24 * 60 * 60 * 1000;
    expect(await store.validate(id)).toBe(true);
    now += 2 * 24 * 60 * 60 * 1000;
    expect(await store.validate(id)).toBe(false);
  });

  it("locks an address for a minute after five failures", () => {
    let now = 0;
    const limiter = createLoginRateLimiter(() => now);
    for (let attempt = 0; attempt < 4; attempt += 1) limiter.recordFailure("a");
    expect(limiter.lockedForMs("a")).toBe(0);
    limiter.recordFailure("a");
    expect(limiter.lockedForMs("a")).toBe(60_000);
    expect(limiter.lockedForMs("b")).toBe(0);
    now += 60_001;
    expect(limiter.lockedForMs("a")).toBe(0);
  });

  it("answers 429 with Retry-After once locked", async () => {
    const app = buildApp();
    for (let attempt = 0; attempt < 5; attempt += 1) await login(app, "bad");
    const locked = await login(app, TOKEN);
    expect(locked.status).toBe(429);
    expect(Number(locked.headers.get("retry-after"))).toBeGreaterThan(0);
  });
});

describe("web access token", () => {
  it("creates a 0600 token file once and reuses it", async () => {
    const layout = resolveServerLayout(join(dir, "server"));
    const first = await readOrCreateWebAccessTokenFile(layout);
    expect(first.length).toBeGreaterThanOrEqual(32);
    expect(await readOrCreateWebAccessTokenFile(layout)).toBe(first);
    if (process.platform !== "win32") {
      expect((await stat(layout.webAccessTokenFile)).mode & 0o777).toBe(0o600);
    }
  });

  it("rotates to a different token", async () => {
    const layout = resolveServerLayout(join(dir, "server"));
    const first = await readOrCreateWebAccessTokenFile(layout);
    const next = await rotateWebAccessTokenFile(layout);
    expect(next).not.toBe(first);
    expect(await readOrCreateWebAccessTokenFile(layout)).toBe(next);
  });

  it("prefers the environment token and rejects short ones", async () => {
    const layout = resolveServerLayout(join(dir, "server"));
    expect(await resolveWebAccessToken(layout, { NEX_SERVER_WEB_TOKEN: "x".repeat(12) })).toEqual({
      token: "x".repeat(12),
      source: "env",
    });
    await expect(resolveWebAccessToken(layout, { NEX_SERVER_WEB_TOKEN: "short" })).rejects.toThrow(
      /at least 12/,
    );
    expect((await resolveWebAccessToken(layout, {})).source).toBe("file");
  });
});

describe("serve configuration", () => {
  it("parses serve flags and leaves other arguments alone", () => {
    const parsed = parseServeFlags([
      "--daemon",
      "--host",
      "0.0.0.0",
      "--port",
      "8080",
      "--web-root",
      "/srv/web",
    ]);
    expect(parsed.patch).toEqual({ host: "0.0.0.0", port: 8080, webRoot: "/srv/web" });
    expect(parsed.rest).toEqual(["--daemon"]);
    expect(parseServeFlags(["--daemon"]).patch).toBeUndefined();
  });

  it("rejects invalid ports, relative web roots and missing values", () => {
    expect(() => parseServeFlags(["--port", "99999"])).toThrow(/between 0 and 65535/);
    expect(() => parseServeFlags(["--port", "80x"])).toThrow(/between 0 and 65535/);
    expect(() => parseServeFlags(["--web-root", "dist"])).toThrow(/absolute/);
    expect(() => parseServeFlags(["--host"])).toThrow(/requires a value/);
    expect(() => parseServeFlags(["--clear-serve-config", "--port", "1"])).toThrow(
      /cannot be combined/,
    );
  });

  it("persists options and lets the environment override them", async () => {
    const layout = resolveServerLayout(join(dir, "server"));
    await writeServeConfig(layout, { host: "0.0.0.0", port: 4000, webRoot: "/srv/web" });
    expect(await readServeConfig(layout)).toEqual({
      host: "0.0.0.0",
      port: 4000,
      webRoot: "/srv/web",
    });
    const fromFile = await resolveServeOptions(layout, {});
    expect(fromFile).toMatchObject({ host: "0.0.0.0", port: 4000, webLoginEnabled: true });
    const overridden = await resolveServeOptions(layout, {
      NEX_SERVER_LISTEN_HOST: "127.0.0.1",
      NEX_SERVER_LISTEN_PORT: "5000",
      NEX_SERVER_WEB_ROOT: "/other",
    });
    expect(overridden).toMatchObject({ host: "127.0.0.1", port: 5000, webRoot: "/other" });
  });

  it("keeps the default loopback behavior when nothing is configured", async () => {
    const layout = resolveServerLayout(join(dir, "server"));
    expect(await resolveServeOptions(layout, {})).toEqual({
      host: undefined,
      port: undefined,
      webRoot: undefined,
      webLoginEnabled: false,
    });
  });

  it("enables login for a non-loopback host even without a web root", async () => {
    const layout = resolveServerLayout(join(dir, "server"));
    const resolved = await resolveServeOptions(layout, { NEX_SERVER_LISTEN_HOST: "0.0.0.0" });
    expect(resolved.webLoginEnabled).toBe(true);
  });

  it("fails on a corrupt config instead of silently ignoring it", async () => {
    const layout = resolveServerLayout(join(dir, "server"));
    await mkdir(layout.serverRoot, { recursive: true });
    await writeFile(layout.serveConfigFile, JSON.stringify({ version: 1, port: "x" }));
    await expect(readServeConfig(layout)).rejects.toThrow(/Invalid/);
  });
});

describe("static web hosting", () => {
  async function buildWebRoot() {
    const root = join(dir, "web");
    await mkdir(join(root, "assets"), { recursive: true });
    await writeFile(join(root, "index.html"), "<html>index</html>");
    await writeFile(join(root, "assets", "app-abc123.js"), "console.log(1)");
    await writeFile(join(root, "favicon.ico"), "ico");
    return root;
  }
  const appFor = (root: string) => {
    const app = new Hono();
    app.get("/api/server-info", (c) => c.json({ api: true }));
    app.get("*", createStaticWebHandler(root));
    return app;
  };

  it("requires index.html", async () => {
    await expect(assertWebRoot(dir)).rejects.toThrow(/index\.html/);
    expect(await assertWebRoot(await buildWebRoot())).toContain("web");
  });

  it("serves assets with the right type and caching", async () => {
    const app = appFor(await assertWebRoot(await buildWebRoot()));
    const asset = await app.request("/assets/app-abc123.js");
    expect(asset.status).toBe(200);
    expect(asset.headers.get("content-type")).toContain("javascript");
    expect(asset.headers.get("cache-control")).toContain("immutable");
    const index = await app.request("/");
    expect(await index.text()).toContain("index");
    expect(index.headers.get("cache-control")).toBe("no-store");
  });

  it("falls back to index.html for app routes but not for missing files or API paths", async () => {
    const app = appFor(await assertWebRoot(await buildWebRoot()));
    expect(await (await app.request("/login")).text()).toContain("index");
    expect((await app.request("/assets/missing.js")).status).toBe(404);
    expect((await app.request("/api/nothing")).status).toBe(404);
    expect((await app.request("/ws/unknown")).status).toBe(404);
  });

  it("does not escape the web root", async () => {
    const root = await assertWebRoot(await buildWebRoot());
    await writeFile(join(dir, "secret.txt"), "secret");
    const app = appFor(root);
    for (const path of ["/..%2fsecret.txt", "/%2e%2e/secret.txt", "/assets/..%2f..%2fsecret.txt"]) {
      const response = await app.request(path);
      expect(await response.text()).not.toContain("secret");
    }
  });
});
