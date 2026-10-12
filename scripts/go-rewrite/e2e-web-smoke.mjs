/* eslint-disable max-lines -- web 冒烟：一条脚本起 server / 起浏览器 / 拆断言，拆开会让前后关系断裂 */
// Phase 1 G1.13a 端到端冒烟（docs/specs/go-rewrite/phase-1-go-agent.md，步骤 1.14 的 Web 一半）。
//
// G1.13 是"Web and Desktop 人工清单"，本脚本只把其中能被机器证明的最小子集自动化，
// 证明 Web UI 真的能对着 Go agent 跑起来，而不是"页面能打开"：
//   1. GET  /api/server-info 带会话 cookie 返回 capabilities.desktopContinuous === true（鉴权生效）。
//   2. 鉴权本身：POST /api/auth/login 拿 cookie；未带凭证访问受保护路径得到 401（fail closed）。
//   3. GET  / 返回 @nex/web 产物的 index.html（静态资源由同一个 server 供给，即 UI 的真实来源）。
//   4. WebSocket 连 /ws 成功，并完成 v4 握手（hello → clientHello），证明 UI 那条链路通到 host。
//   5. 有浏览器时：用真实 Chromium 打开 /、登录、在 composer 里发 "Say hello"，
//      等到 assistant 行出现 mock 的固定回答 —— 这一步证明"UI 在跟 Go agent 对话"。
//      没有可用浏览器时打印 skip 原因，仍然以 1-4 通过，不假装浏览器部分被验证过。
//
// 关键约束（与 e2e-agent-smoke.mjs 同源，见那里文件头的解释）：
//   - cwd 必须落在仓库之外，否则 resolveDefaultNexAgentCommand 的 findUpward 会命中
//     monorepo bundle，GLM_BINARY_PATH 永远不被读，测的就不是 Go agent 了；
//   - env 显式白名单，外层 shell 残留的 NEX_* 会劫持 provider config 路径与 agent 命令解析。
//
// 成功时最后一行打印 `web-smoke=ok`，退出码 0。
//
//   node --import tsx scripts/go-rewrite/e2e-web-smoke.mjs [--agent <go-binary>] [--no-browser]
import { spawn } from "node:child_process";
import {
  createWriteStream,
  existsSync,
  mkdirSync,
  mkdtempSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { ChannelClient, SocketProtocol, VSBuffer, Emitter } from "../../packages/rpc/src/index.ts";
import { startMockModel } from "./record/mock-model.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../..");
const SERVER_ENTRY = join(REPO, "packages/server/dist/entry-http.js");
const WEB_DIST = join(REPO, "packages/web/dist");

const CANNED_ANSWER = "Hello from the Go agent web smoke test.";
const USER_PROMPT = "Say hello";
const MODEL_ID = "mock-model";
const PROVIDER_ID = "mock";
const AUTH_TOKEN = "test";

/** 显式白名单：外层 shell 的 NEX_* 会劫持 provider config 路径与 agent 命令解析。 */
const INHERITED_ENV_KEYS = ["PATH", "USERPROFILE", "SystemRoot", "TEMP", "TMP", "TMPDIR"];

/** 浏览器可执行文件的候选位置；容器里通常是系统 chromium 而非 playwright 自带的。 */
const BROWSER_CANDIDATES = [
  process.env["CHROMIUM_PATH"],
  "/usr/bin/chromium",
  "/usr/bin/chromium-browser",
  "/usr/bin/google-chrome",
  "/usr/bin/google-chrome-stable",
];

function argValue(name) {
  const args = process.argv.slice(2);
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
}

function log(...parts) {
  console.log("[e2e-web-smoke]", ...parts);
}

const failures = [];
const skipped = [];
const assert = (ok, what, detail) => {
  if (ok) log(`ok   ${what}`);
  else {
    failures.push(what);
    log(`FAIL ${what}${detail ? ` — ${String(detail).slice(0, 400)}` : ""}`);
  }
  return ok;
};

/** 端口交给 OS（port 0），避免并发跑门禁时撞端口。 */
async function freePort() {
  return new Promise((resolvePort, reject) => {
    const probe = createServer();
    probe.on("error", reject);
    probe.listen(0, "127.0.0.1", () => {
      const { port } = probe.address();
      probe.close(() => resolvePort(port));
    });
  });
}

function delay(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

/**
 * provider_config.json 的形状来自 scripts/go-rewrite/record/record-agent-session.mjs 的
 * providerConfig()（录制器用同一份结构喂 Node agent）。baseUrl 指向 mock 的 /v1。
 */
function providerConfig(mockPort) {
  return {
    schemaVersion: 1,
    config: {
      providerConfigRules: {
        providerRules: [
          {
            providerId: PROVIDER_ID,
            providerName: "Mock",
            config: {
              group: "standard-personal",
              access: { type: "api-key", apiKey: "sk-test-fake" },
              api: { type: "openai-chat-completions", baseUrl: `http://127.0.0.1:${mockPort}/v1` },
              personalModelIds: [MODEL_ID],
            },
          },
        ],
      },
      modelConfigRules: {
        providerModelRules: [],
        manualProviderModelRules: [
          {
            providerId: PROVIDER_ID,
            modelId: MODEL_ID,
            config: {
              enabled: true,
              name: "Mock Model",
              properties: {
                contextWindow: 100000,
                supportsJsonSchemaOutput: false,
                supportsNativeWebSearch: false,
                supportsMidConversationSystem: false,
                inputFormat: { supportsImage: false, supportsVideo: false, supportsPdf: false },
              },
              optionSpecs: {
                reasoningLevel: { values: ["none"], map: "{}" },
                maxOutputTokens: { max: 8000 },
              },
            },
          },
        ],
      },
      defaultModelSelection: { providerId: PROVIDER_ID, modelId: MODEL_ID },
    },
  };
}

async function waitForServerInfo(baseUrl, childExited, serverLog) {
  const deadline = Date.now() + 60_000;
  for (;;) {
    if (childExited())
      throw new Error(
        `server process exited before /api/server-info answered:\n${serverLog().slice(-2000)}`,
      );
    try {
      const response = await fetch(`${baseUrl}/api/server-info?token=${AUTH_TOKEN}`);
      if (response.ok) return await response.json();
    } catch {
      /* 还没监听 */
    }
    if (Date.now() > deadline) throw new Error("timeout waiting for /api/server-info");
    await delay(200);
  }
}

/** 从 set-cookie 头里取出 nex_session 的值。 */
function readSessionCookie(response) {
  const raw = response.headers.getSetCookie?.() ?? [];
  const hit = raw.find((entry) => entry.startsWith("nex_session="));
  if (!hit) return "";
  return hit.split(";")[0].trim();
}

/** Node 24 自带 WebSocket；ws:// 走的是 server 的 /ws 路由，即 web-remote-replayable 客户端。 */
function connectRpc(url, cookie) {
  return new Promise((resolveConnection, reject) => {
    const ws = new WebSocket(url, { headers: { cookie } });
    ws.binaryType = "arraybuffer";
    let settled = false;
    const timer = setTimeout(() => {
      if (!settled) reject(new Error(`WebSocket open timeout: ${url}`));
    }, 20_000);
    ws.addEventListener("error", () => {
      if (!settled) reject(new Error(`WebSocket connection failed: ${url}`));
    });
    ws.addEventListener("open", () => {
      settled = true;
      clearTimeout(timer);
      const onData = new Emitter();
      const onClose = new Emitter();
      const onEnd = new Emitter();
      ws.addEventListener("message", (event) =>
        onData.fire(VSBuffer.wrap(new Uint8Array(event.data))),
      );
      ws.addEventListener("close", () => {
        onClose.fire();
        onEnd.fire();
      });
      const socket = {
        onData: onData.event,
        onClose: onClose.event,
        onEnd: onEnd.event,
        write: (buffer) => ws.send(buffer.buffer),
        end: () => ws.close(),
        drain: () => Promise.resolve(),
        dispose: () => ws.close(),
      };
      const client = new ChannelClient(new SocketProtocol(socket));
      resolveConnection({ client, ws });
    });
  });
}

// ---- 浏览器部分 ------------------------------------------------------------------

function findBrowserExecutable() {
  for (const candidate of BROWSER_CANDIDATES) {
    if (candidate && existsSync(candidate)) return candidate;
  }
  return null;
}

/**
 * 找 playwright-core（仓库根 node_modules 里已存在，作为 e2e 的传递依赖）。
 * 用 createRequire 从 REPO 解析，避免 scripts/ 下解析不到 hoisting 依赖。
 * playwright-core 是 CJS，`import()` 只拿到 default 导出，所以两处都要兜住。
 */
async function loadPlaywright() {
  const { createRequire } = await import("node:module");
  const require = createRequire(join(REPO, "package.json"));
  try {
    const loaded = await import(require.resolve("playwright-core"));
    return loaded.chromium ? loaded : (loaded.default ?? null);
  } catch {
    return null;
  }
}

/**
 * 真实浏览器里跑一次完整 UI 流程：打开 / → 登录页填 token → 等 composer 出现 → 输入并发送。
 * 断言只依赖 packages/shared/src/test-ids.ts 里的稳定 test id，不依赖文案或 CSS 结构。
 * 模型不额外点选：provider 配置里 defaultModelSelection 就是 mock，
 * 下面「mock 收到请求」与「回答等于固定文本」两条断言已经把这件事钉住了。
 */
async function driveBrowser({ executablePath, baseUrl, timeoutMs }) {
  const playwright = await loadPlaywright();
  if (!playwright)
    return { ran: false, reason: "playwright-core is not resolvable from the repo root" };

  const browser = await playwright.chromium.launch({
    executablePath,
    args: ["--no-sandbox", "--disable-dev-shm-usage", "--disable-gpu"],
  });
  try {
    const page = await browser.newPage();
    await page.goto(`${baseUrl}/`, { waitUntil: "domcontentloaded" });

    // 未登录时应当看到登录表单；有会话时直接进应用。
    const tokenInput = page.locator("#web-login-token");
    const loginVisible = await tokenInput
      .waitFor({ state: "visible", timeout: 15_000 })
      .then(() => true)
      .catch(() => false);
    if (loginVisible) {
      await tokenInput.fill(AUTH_TOKEN);
      await page.locator('form button[type="submit"]').click();
    }

    // composer 是"应用真的跑起来了"的最短信号：它出现说明 ws 握手 + Root 渲染都完成了。
    const composer = page.locator('[data-testid="v4-composer-input"]');
    await composer.waitFor({ state: "visible", timeout: 60_000 });
    assert(true, "browser: v4 composer is visible after login");

    await composer.click();
    await composer.fill(USER_PROMPT);
    await page.locator('[data-testid="v4-composer-send"]').click();

    // assistant 行把固定回答渲染出来 = UI → server → Go agent → mock → 回 UI 整条链路闭合。
    await page
      .locator('[data-testid^="v4-row"]')
      .first()
      .waitFor({ state: "visible", timeout: 60_000 });

    const deadline = Date.now() + timeoutMs;
    let body = "";
    for (;;) {
      body = await page.locator("body").innerText();
      if (body.includes(CANNED_ANSWER)) break;
      if (Date.now() > deadline)
        throw new Error(
          `timeout waiting for the assistant reply in the UI; page text=${JSON.stringify(body).slice(0, 400)}`,
        );
      await delay(250);
    }

    return { ran: true, sawReply: body.includes(CANNED_ANSWER) };
  } finally {
    await browser.close().catch(() => {});
  }
}

async function main() {
  const agentPath = resolve(argValue("--agent") ?? "/tmp/go-rewrite-bin/nex-agent");
  const browserDisabled = process.argv.includes("--no-browser");

  if (!existsSync(agentPath))
    throw new Error(
      `Go agent binary not found at ${agentPath}; build it with ` +
        "`cd go && go build -buildvcs=false -o <path> ./cmd/nex-agent`",
    );
  if (!existsSync(SERVER_ENTRY))
    throw new Error(
      `@nex/server build missing at ${SERVER_ENTRY}; run pnpm --filter @nex/server build`,
    );
  if (!existsSync(join(WEB_DIST, "index.html")))
    throw new Error(`@nex/web build missing at ${WEB_DIST}; run pnpm --filter @nex/web build`);

  const root = mkdtempSync(join(tmpdir(), "nex-web-smoke-"));
  const dataDir = join(root, "data");
  const workspacePath = join(root, "ws");
  mkdirSync(join(dataDir, ".nex/v2"), { recursive: true });
  mkdirSync(workspacePath, { recursive: true });

  const mock = await startMockModel({ steps: [{ text: CANNED_ANSWER }] });
  writeFileSync(
    join(dataDir, ".nex/v2/provider_config.json"),
    JSON.stringify(providerConfig(mock.port)),
  );

  const port = await freePort();
  const baseUrl = `http://127.0.0.1:${port}`;

  // 干净 env：见文件头关于 cwd 与残留 NEX_* 的说明。
  const env = Object.fromEntries(INHERITED_ENV_KEYS.map((k) => [k, process.env[k]]));
  for (const [key, value] of Object.entries(env)) if (value === undefined) delete env[key];
  Object.assign(env, {
    HOME: dataDir,
    NEX_DATA_BASE_DIR: dataDir,
    PORT: String(port),
    NEX_SERVER_HOST: "127.0.0.1",
    GLM_BINARY_PATH: agentPath,
    NEX_SERVER_AUTH_TOKEN: AUTH_TOKEN,
    NEX_WEB_STATIC_ROOT: WEB_DIST,
    // 注册一个 workspace，UI 启动时才有初始目录可回显（server-info 的 workspaces 由此而来）。
    NEX_SERVER_WORKSPACE: workspacePath,
  });

  let serverExited = false;
  // cwd 放在仓库外：否则 findUpward 会命中 monorepo bundle，GLM_BINARY_PATH 永远不被读。
  const server = spawn(process.execPath, [SERVER_ENTRY], {
    cwd: root,
    env,
    stdio: ["ignore", "pipe", "pipe"],
  });
  let serverLog = "";
  const serverLogStream = createWriteStream(join(root, "server.log"));
  const captureServerLog = (chunk) => {
    serverLog += chunk;
    serverLogStream.write(chunk);
  };
  server.stdout.on("data", captureServerLog);
  server.stderr.on("data", captureServerLog);
  server.on("exit", () => (serverExited = true));

  let connection;
  try {
    const info = await waitForServerInfo(
      baseUrl,
      () => serverExited,
      () => serverLog,
    );
    log(`server up: ${info.serverId} v${info.version} authRequired=${info.authRequired}`);

    // (b) 鉴权先 fail closed：受保护路径没有凭证必须是 401，不能是 200。
    const anonymous = await fetch(`${baseUrl}/api/server-info`);
    assert(
      anonymous.status === 401,
      "server-info rejects anonymous access (401)",
      anonymous.status,
    );

    // (b) 登录换会话 cookie。
    const login = await fetch(`${baseUrl}/api/auth/login`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ token: AUTH_TOKEN }),
    });
    const cookie = readSessionCookie(login);
    assert(
      login.ok && cookie !== "",
      "POST /api/auth/login returns a session cookie",
      login.status,
    );
    log(`session cookie: ${cookie.slice(0, 24)}…`);

    // (a) 带 cookie 的 /api/server-info，能力位证明这是 desktop-continuous 链路可用的 server。
    const authenticated = await fetch(`${baseUrl}/api/server-info`, { headers: { cookie } });
    const authenticatedInfo = authenticated.ok ? await authenticated.json() : undefined;
    assert(
      authenticated.status === 200 && authenticatedInfo?.capabilities?.desktopContinuous === true,
      "GET /api/server-info returns 200 with capabilities.desktopContinuous === true",
      JSON.stringify(authenticatedInfo).slice(0, 200),
    );
    assert(
      authenticatedInfo?.authRequired === true,
      "server-info reports authRequired === true",
      JSON.stringify(authenticatedInfo?.authRequired),
    );

    // (c) 静态：/ 必须返回 @nex/web 产物的 index.html。
    const index = await fetch(`${baseUrl}/`, { headers: { cookie } });
    const html = index.ok ? await index.text() : "";
    assert(index.ok, "GET / returns 200", index.status);
    assert(
      (index.headers.get("content-type") ?? "").includes("text/html"),
      "GET / is served as text/html",
      index.headers.get("content-type"),
    );
    assert(
      html.includes('<div id="root"') || html.includes("<script"),
      "GET / body is the web index.html",
      html.slice(0, 200),
    );

    // 登录后 UI 拿到的 index.html 之外的 bundle 也必须可取，否则静态只有壳。
    const assetMatch = /src="(\/assets\/[^"]+\.js)"/.exec(html);
    if (
      assert(
        assetMatch !== null,
        "index.html references a hashed /assets bundle",
        html.slice(0, 300),
      )
    ) {
      const asset = await fetch(`${baseUrl}${assetMatch[1]}`, { headers: { cookie } });
      assert(asset.ok, `GET ${assetMatch[1]} returns 200`, asset.status);
    }

    // (d) WebSocket：UI 用的就是 /ws。带 cookie 连上并跑完 v4 握手。
    connection = await connectRpc(`ws://127.0.0.1:${port}/ws`, cookie);
    const channel = connection.client.getChannel("nex-agent");
    const hello = await channel.call("helloConversationV4", []);
    assert(
      typeof hello?.protocolVersion === "number",
      "WebSocket /ws connects and helloConversationV4 answers",
      JSON.stringify(hello).slice(0, 200),
    );
    await channel.call("initializeConversationV4", [
      {
        kind: "clientHello",
        protocolVersion: hello.protocolVersion,
        clientId: "web-smoke-client",
        clientKind: "web",
        appVersion: "0.0.0-e2e",
        capabilities: { workflowRunDeltas: hello.capabilities?.workflowRunDeltas === true },
      },
    ]);
    log(`v4 handshake ok (clientMode=${hello.clientMode}, profile=${hello.deliveryProfile})`);

    // (e) 浏览器部分。没有浏览器就如实记为 skip，不当成通过。
    if (browserDisabled) {
      skipped.push("browser: disabled via --no-browser");
    } else {
      const executablePath = findBrowserExecutable();
      if (!executablePath) {
        skipped.push(
          "browser: no chromium/chrome executable found (tried " +
            `${BROWSER_CANDIDATES.filter(Boolean).join(", ")}); the login + streamed-reply ` +
            "path of the G1.13 checklist stays manual",
        );
      } else {
        log(`browser: launching ${executablePath}`);
        const result = await driveBrowser({
          executablePath,
          baseUrl,
          timeoutMs: 90_000,
        });
        if (!result.ran) skipped.push(`browser: ${result.reason}`);
        else {
          assert(result.sawReply, "browser: streamed assistant reply equals the mock answer");
          assert(
            mock.requestCount() > 0,
            `mock model received ${mock.requestCount()} request(s) from the browser`,
          );
          assert(
            serverLog.includes(`'${agentPath}'`) || serverLog.includes(`"${agentPath}"`),
            "server spawned the Go agent binary for the browser session",
            serverLog.slice(-1500),
          );
        }
      }
    }

    channel.dispose?.();
    rmSync(root, { recursive: true, force: true });
  } catch (error) {
    failures.push(String(error?.message ?? error));
    log(`artifacts (including server log) kept at ${root}`);
    throw error;
  } finally {
    try {
      connection?.ws?.close();
    } catch {
      /* 已关闭 */
    }
    server.kill("SIGTERM");
    await delay(500);
    if (!serverExited) server.kill("SIGKILL");
    await mock.close();
  }

  for (const reason of skipped) log(`SKIP ${reason}`);
  if (failures.length > 0) {
    log(`FAILED: ${failures.join("; ")}`);
    process.exit(1);
  }
  log("web-smoke=ok");
  process.exit(0);
}

await main();
