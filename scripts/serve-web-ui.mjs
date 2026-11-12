#!/usr/bin/env node
/**
 * 本地 2.0 Web UI 前置：托管 packages/web/dist，并把 /api 与 /ws 反代到 server-core。
 *
 * server-core 只监听回环、且要求 Bearer ingress token（见
 * packages/nex-server-cli/src/server-core/http.ts），浏览器不持有该 token，
 * 所以这里在反代时注入 Authorization —— 与 docker/nginx.conf 的分工一致。
 *
 * 用法：node scripts/serve-web-ui.mjs --web <dist> --core <host:port> --token-file <path> --port 5175
 * 共享桌面远控时可加 --relay <host:port>，将 /api/v1/remote 转发到同一 Relay。
 */
import { createServer } from "node:http";
import { createReadStream, existsSync, readFileSync, statSync } from "node:fs";
import { extname, join, normalize, resolve, sep } from "node:path";
import { request as httpRequest } from "node:http";
import { WebSocket, WebSocketServer } from "ws";

const args = process.argv.slice(2);
const argOf = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};

const webRoot = resolve(argOf("--web") ?? "packages/web/dist");
const coreTarget = argOf("--core") ?? "127.0.0.1:65000";
const relayTarget = argOf("--relay");
const tokenFile = argOf("--token-file");
const port = Number(argOf("--port") ?? 5175);

const [coreHost, corePortRaw] = coreTarget.split(":");
const corePort = Number(corePortRaw);
const [relayHost, relayPortRaw] = relayTarget?.split(":") ?? [];
const relayPort = Number(relayPortRaw);

const readToken = () => {
  if (!tokenFile) return undefined;
  try {
    return readFileSync(tokenFile, "utf8").trim() || undefined;
  } catch {
    return undefined;
  }
};

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
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
  ".pdf": "application/pdf",
  ".txt": "text/plain; charset=utf-8",
};

function safePath(urlPath) {
  const decoded = decodeURIComponent(urlPath.split("?")[0]);
  const candidate = normalize(join(webRoot, decoded));
  if (candidate !== webRoot && !candidate.startsWith(webRoot + sep)) return null;
  return candidate;
}

const wss = new WebSocketServer({ noServer: true });

const server = createServer((req, res) => {
  const url = req.url ?? "/";
  const isApi = url.startsWith("/api/") || url === "/api";
  const isWs = url.startsWith("/ws");
  const isRelay = relayTarget && url.startsWith("/api/v1/remote/");

  if (isRelay) {
    const headers = { ...req.headers, host: `${relayHost}:${relayPort}` };
    const upstream = httpRequest(
      { host: relayHost, port: relayPort, path: url, method: req.method, headers },
      (up) => {
        res.writeHead(up.statusCode ?? 502, up.headers);
        up.pipe(res);
      },
    );
    upstream.on("error", (error) => {
      res.writeHead(502, { "content-type": "application/json" });
      res.end(JSON.stringify({ error: `relay upstream failed: ${error.message}` }));
    });
    req.pipe(upstream);
    return;
  }

  if (isApi) {
    const token = readToken();
    const headers = { ...req.headers };
    if (token) headers.authorization = `Bearer ${token}`;
    headers.host = `${coreHost}:${corePort}`;
    const upstream = httpRequest(
      { host: coreHost, port: corePort, path: url, method: req.method, headers },
      (up) => {
        res.writeHead(up.statusCode ?? 502, up.headers);
        up.pipe(res);
      },
    );
    upstream.on("error", (error) => {
      res.writeHead(502, { "content-type": "application/json" });
      res.end(JSON.stringify({ error: `upstream failed: ${error.message}` }));
    });
    req.pipe(upstream);
    return;
  }

  if (isWs) {
    // 升级请求由 server 的 "upgrade" 事件处理；这里返回 426 让客户端走那条路径。
    res.writeHead(426, { "content-type": "application/json" });
    res.end(JSON.stringify({ error: "websocket upgrade expected" }));
    return;
  }

  let filePath = safePath(url);
  if (!filePath) {
    res.writeHead(403).end();
    return;
  }
  // SPA fallback：非静态资源一律回 index.html。
  if (!existsSync(filePath) || statSync(filePath).isDirectory()) {
    filePath = join(webRoot, "index.html");
  }
  if (!existsSync(filePath)) {
    res.writeHead(404).end("web build not found; run pnpm --filter @nex/web build");
    return;
  }
  res.writeHead(200, {
    "content-type": MIME[extname(filePath)] ?? "application/octet-stream",
    "cache-control": filePath.endsWith("index.html") ? "no-store" : "public, max-age=31536000",
  });
  createReadStream(filePath).pipe(res);
});

server.on("upgrade", (req, socket, head) => {
  const url = req.url ?? "/";
  const isRelay = relayTarget && url.startsWith("/api/v1/remote/ws2");
  if (!url.startsWith("/ws") && !isRelay) {
    socket.destroy();
    return;
  }
  const token = isRelay ? undefined : readToken();
  const upstream = new WebSocket(
    isRelay ? `ws://${relayHost}:${relayPort}${url}` : `ws://${coreHost}:${corePort}${url}`,
    {
      headers: token ? { authorization: `Bearer ${token}` } : {},
    },
  );

  // 浏览器侧：用 ws 完成握手，再把它的原始 socket 与上游对接。
  wss.handleUpgrade(req, socket, head, (client) => {
    const pending = [];
    client.on("message", (data, isBinary) => {
      if (upstream.readyState === WebSocket.OPEN) upstream.send(data, { binary: isBinary });
      else pending.push([data, isBinary]);
    });
    client.on("close", () => upstream.close());
    client.on("error", () => upstream.terminate());

    upstream.on("open", () => {
      for (const [data, isBinary] of pending) upstream.send(data, { binary: isBinary });
      pending.length = 0;
    });
    upstream.on("message", (data, isBinary) => {
      if (client.readyState === WebSocket.OPEN) client.send(data, { binary: isBinary });
    });
    upstream.on("close", () => client.close());
    upstream.on("error", () => client.terminate());
  });
});

server.listen(port, "127.0.0.1", () => {
  console.log(`[web] http://127.0.0.1:${port}  (dist=${webRoot}, core=${coreTarget})`);
});
