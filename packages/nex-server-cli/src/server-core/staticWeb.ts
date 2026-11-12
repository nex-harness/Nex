import { readFile, stat } from "node:fs/promises";
import { extname, join, normalize, resolve, sep } from "node:path";
import type { Context } from "hono";

// Core 直接托管 Web UI 构建产物（--web-root）。规则见 docs/specs/server-web-login.md。

const MIME_TYPES: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".webmanifest": "application/manifest+json; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".gif": "image/gif",
  ".webp": "image/webp",
  ".ico": "image/x-icon",
  ".woff": "font/woff",
  ".woff2": "font/woff2",
  ".ttf": "font/ttf",
  ".wasm": "application/wasm",
  ".map": "application/json; charset=utf-8",
  ".txt": "text/plain; charset=utf-8",
};

export async function assertWebRoot(webRoot: string): Promise<string> {
  const root = resolve(webRoot);
  const index = await stat(join(root, "index.html")).catch(() => undefined);
  if (!index?.isFile()) {
    // 缺 index.html 时静默退化成 API-only 会让用户看到空白页却找不到原因，启动即失败。
    throw new Error(`Web root ${root} does not contain index.html`);
  }
  return root;
}

function isReservedPath(pathname: string): boolean {
  return (
    pathname === "/ws" ||
    pathname.startsWith("/ws/") ||
    pathname === "/api" ||
    pathname.startsWith("/api/")
  );
}

function resolveFilePath(root: string, pathname: string): string | undefined {
  let decoded: string;
  try {
    decoded = decodeURIComponent(pathname);
  } catch {
    return undefined;
  }
  if (decoded.includes("\0")) return undefined;
  const candidate = normalize(join(root, decoded));
  return candidate === root || candidate.startsWith(root + sep) ? candidate : undefined;
}

function cacheControlFor(filePath: string, root: string): string {
  if (filePath.endsWith(`${sep}index.html`) && filePath === join(root, "index.html")) {
    return "no-store";
  }
  // vite 产物带内容哈希，位于 assets/ 下可长期缓存；其它文件（图标等）只做短缓存。
  return filePath.startsWith(join(root, "assets") + sep)
    ? "public, max-age=31536000, immutable"
    : "public, max-age=300";
}

async function serveFile(c: Context, root: string, filePath: string): Promise<Response> {
  const body = await readFile(filePath);
  return c.body(new Uint8Array(body), 200, {
    "Content-Type": MIME_TYPES[extname(filePath).toLowerCase()] ?? "application/octet-stream",
    "Cache-Control": cacheControlFor(filePath, root),
    "X-Content-Type-Options": "nosniff",
  });
}

/** 静态资源 + SPA 回落；/api、/ws 下未匹配的路径返回 404，不回落成页面。 */
export function createStaticWebHandler(root: string) {
  return async (c: Context): Promise<Response> => {
    const pathname = new URL(c.req.url).pathname;
    if (isReservedPath(pathname)) return c.json({ error: "Not found" }, 404);
    const filePath = resolveFilePath(root, pathname);
    if (!filePath) return c.json({ error: "Bad request" }, 400);
    const info = await stat(filePath).catch(() => undefined);
    if (info?.isFile()) return serveFile(c, root, filePath);
    // 带扩展名的路径是缺失的资源，不能回落成 index.html（否则脚本/样式 404 会变成 HTML 200）。
    if (extname(pathname)) return c.json({ error: "Not found" }, 404);
    return serveFile(c, root, join(root, "index.html"));
  };
}
