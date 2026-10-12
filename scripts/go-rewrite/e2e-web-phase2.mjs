/* eslint-disable max-lines -- web e2e：起两种 front / 起浏览器 / 拆断言 / 比序列，拆开会让前后关系断裂 */
// 步骤 2.12「真实 UI 端到端」（docs/specs/go-rewrite/phase-2-go-server.md）。
//
// 与 Phase 1 的 e2e-web-smoke.mjs 的差别只有一处，但那一处正是这一步的意义：那个脚本测的是
// Node server + Go agent；这里的 subject 是 **Go front**（go/dist/<target>/nex-server），也就是
// 「浏览器真正连的那个进程」。同一个 UI 流程跑两遍：
//
//   front=go    Go 二进制当 HTTP/WS 入口，Node upstream 在它后面
//   front=node  packages/server/dist/entry-http.js 直接当入口（被替换掉的实现）
//
// 然后断言两件事：
//   1. 浏览器 console 里没有 WebSocket / RPC 错误（截图与 console 日志落在
//      go/testdata/e2e/phase2/<front>/）；
//   2. 两次运行在**同一个场景内**发出的 (channel, method) 逐项相同 —— UI 的协议调用没有因为
//      换了 front 而变（场景的划法与理由见 scenarioCalls）。
//
// 为什么序列来自浏览器而不是 server 日志（spec 写的是「record via the gateway's debug log」）：
// Go front 的进程日志级别在 step 2.10 固定为 info，转发行是 debug 且没有任何环境变量可以调高
// （D7 禁止新增变量）；它 spawn 的 Node upstream 的 stdout 被 internal/adapters/exec 收进
// OutputCollector，不再回写到父进程的 stderr，所以 `[rpc:call] …` 那套 Node 侧日志在 Go front
// 下根本不可见。于是序列改为在页面里读**客户端发出的原始 WS 帧**，用 packages/rpc 自己的
// deserialize 解头（[RequestType, id, channelName, methodName]）—— 这是 UI 真正发到线上的东西，
// 与 front 无关，两次运行用的是同一段解码代码，drift 不可能只发生在一侧。
//
// 关键约束（与 e2e-web-smoke.mjs 同源，见那里文件头的解释）：
//   - cwd 必须落在仓库之外，否则 agent 命令解析的 findUpward 命中 monorepo bundle，
//     GLM_BINARY_PATH 永远不被读，测的就不是 Go agent 了；
//   - env 显式白名单，外层 shell 残留的 NEX_* 会劫持 provider config 路径与 agent 命令解析。
//
// 成功时最后一行打印 `phase2-e2e=ok`，退出码 0。
//
//   node --import tsx scripts/go-rewrite/e2e-web-phase2.mjs [--agent <go-binary>]
//                                                         [--server <nex-server>]
//                                                         [--front go|node|both] [--timeout-ms N]
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { BufferReader, deserialize, VSBuffer } from "../../packages/rpc/src/index.ts";
import { startMockModel } from "./record/mock-model.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../..");
const NODE_SERVER_ENTRY = join(REPO, "packages/server/dist/entry-http.js");
const NODE_SERVER_DIST = join(REPO, "packages/server/dist");
const WEB_DIST = join(REPO, "packages/web/dist");
const ARTIFACT_ROOT = join(REPO, "go/testdata/e2e/phase2");

const CANNED_ANSWER = "Hello from the Go front phase2 e2e test.";
const USER_PROMPT = "Say hello";
const MODEL_ID = "mock-model";
const PROVIDER_ID = "mock";
const AUTH_TOKEN = "test-token";

/** 默认总预算。门禁里两次 front 连起来跑，90 s 是"慢机器上仍然不等死"的上限。 */
const DEFAULT_TIMEOUT_MS = 90_000;
/** 单个 front 内部每个等待步骤的上限，比总预算短，失败时能说清卡在哪一步。 */
const STEP_TIMEOUT_MS = 30_000;

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

/**
 * Node upstream 入口的解析根。Go front 不看环境变量以外的线索（cmd/nex-server/nodeentry.go 的
 * 第 1 条候选），所以它必须被指向一个含 runtime/ 或 entry-http.js 的目录 —— 仓库里就是
 * packages/server/dist，和 step 2.11 文档里"开发检出"那一条同源。
 */
const GO_UPSTREAM_ROOT = NODE_SERVER_DIST;

/**
 * Go front 给自己留的 node-upstream 数据基目录名（go/cmd/nex-server/upstream.go 的
 * upstreamDataDirName）。它挂在**前端的 app config dir 之下**，不是数据基之下，所以 upstream
 * 的 provider_config.json 还要再往下一层 `.nex/v2` —— 见下面 upstreamAppConfigDir。
 */
const UPSTREAM_DATA_DIR_NAME = "node-upstream";

/** app config dir：<数据基>/.nex/v2（packages/services/src/paths.ts 的 getAppConfigDir）。 */
const appConfigDir = (dataBase) => join(dataBase, ".nex", "v2");

// ---- 断言与日志 ----------------------------------------------------------------

const failures = [];
const notes = [];

function log(...parts) {
  console.log("[e2e-web-phase2]", ...parts);
}

const assert = (ok, what, detail) => {
  if (ok) log(`ok   ${what}`);
  else {
    failures.push(what);
    log(`FAIL ${what}${detail ? ` — ${String(detail).slice(0, 400)}` : ""}`);
  }
  return ok;
};

function argValue(name, fallback) {
  const args = process.argv.slice(2);
  const i = args.indexOf(name);
  return i >= 0 && args[i + 1] !== undefined ? args[i + 1] : fallback;
}

function delay(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

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

/**
 * deadline 让整条脚本不可能挂死：每个等待步骤都先问一次剩余预算，超了就抛，抛出的错误会被
 * runFront 记成失败而不是把门禁卡住 —— 门禁因此不需要另加 `timeout` 包装。
 *
 * 预算是"整个 front 的运行时间"，不是每一步的时间，所以两次 front 连起来跑的总时长仍然有界。
 */
class Deadline {
  constructor(ms, label) {
    this.label = label;
    this.budget = ms;
    this.at = Date.now() + ms;
  }

  /** 剩余毫秒；耗尽即抛，错误文案里带上这个 front 的总预算，读者不用回推。 */
  remaining() {
    const left = this.at - Date.now();
    if (left <= 0)
      throw new Error(`${this.label}: exceeded its ${Math.round(this.budget / 1000)}s budget`);
    return left;
  }

  /** 轮询直到 predicate 返回真值。intervalMs 只是轮询节奏，超时被 remaining() 兜住。 */
  async until(step, predicate, intervalMs = 200) {
    for (;;) {
      this.remaining();
      const value = await predicate();
      if (value) return value;
      await delay(Math.min(intervalMs, this.remaining()));
    }
  }

  /** 单步上限：给 playwright 的 waitFor* 用，两个边界里先到者生效。 */
  stepTimeout() {
    return Math.min(STEP_TIMEOUT_MS, this.remaining());
  }
}

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

/** /api/auth/session 永远 200（docker-single-image.md 的 healthcheck 路由），拿来当就绪信号。 */
async function waitForHealthy(baseUrl, childExited, serverLog, deadline) {
  return deadline.until("waiting for the server to answer /api/auth/session", async () => {
    if (childExited())
      throw new Error(`the server exited during startup:\n${serverLog().slice(-2000)}`);
    try {
      const response = await fetch(`${baseUrl}/api/auth/session`);
      if (response.ok) return await response.json();
    } catch {
      /* 还没监听 */
    }
    return null;
  });
}

// ---- 浏览器 ---------------------------------------------------------------------

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
 * 在页面里挂一个 WebSocket.prototype.send 的旁路，把客户端发出的每一帧的头部留下来。
 *
 * 为什么只留前 256 字节：RPC 帧是 13 字节协议头 + serialize(header) + serialize(body)，
 * 头部（[RequestType, id, channelName, methodName]）远不到 256 字节，而 body 是用户提示词，
 * 一个会话里能到几百 KB。只留头部既是"UI 实际发了什么调用"，又不把提示词抄进测试产物。
 *
 * 为什么只挂 send 而不是用 playwright 的 CDPSession framereceived：那套 API 要 root 之外
 * 还得起一个 debug port，而 WebSocket.send 的旁路是纯页面内的，两行 monkey patch 就够，
 * 且和"用哪个 front"完全无关 —— 两边跑的是同一段页面代码。
 */
const RPC_TAP_INIT = `(() => {
  const nativeSend = window.WebSocket.prototype.send;
  window.__nexRpcFrames = [];
  window.WebSocket.prototype.send = function (data) {
    try {
      // 客户端传的是 VSBuffer.buffer（Uint8Array），不是 ArrayBuffer；两种都收。
      let bytes = null;
      if (data instanceof ArrayBuffer) bytes = new Uint8Array(data);
      else if (ArrayBuffer.isView(data))
        bytes = new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
      if (bytes) {
        window.__nexRpcFrames.push({
          url: this.url,
          head: Array.from(bytes.slice(0, 256)),
        });
      }
    } catch (error) {
      /* 旁路失败不许影响真实连接 */
    }
    return nativeSend.call(this, data);
  };
})();`;

/** 协议头长度：1(type) + 4(id) + 4(ack) + 4(length)，packages/rpc/src/protocol.ts HEADER_SIZE。 */
const HEADER_SIZE = 13;

/** packages/rpc/src/channels.shared.ts 的 RequestType。 */
const REQUEST_TYPE_NAMES = { 100: "promise", 101: "cancel", 102: "listen", 103: "dispose" };

/**
 * 把页面里抓到的原始帧解成 (type, channel, method)。
 *
 * 反序列化交给 packages/rpc 自己的 deserialize，而不是在这里手写一份 VQL/字符串解析：
 * 帧格式改了，这里必须跟着改，而"抄一份解码器"的人不会记得回来改。BufferReader 要的是
 * VSBuffer（它按 byteOffset 取视图），所以外面包一层 VSBuffer.wrap 而不是直接喂 Uint8Array。
 *
 * head 是 Array.from 出来的普通数组，没有 byteOffset 概念；head.length <= HEADER_SIZE 的帧
 * （截断到 256 字节之后连头都不完整的、或协议头为空的）直接判 null。
 */
function decodeFrame(head) {
  if (!Array.isArray(head) || head.length <= HEADER_SIZE) return null;
  const payload = Uint8Array.from(head.slice(HEADER_SIZE));
  try {
    const header = deserialize(new BufferReader(VSBuffer.wrap(payload)));
    if (!Array.isArray(header) || header.length < 4) return null;
    const [type, , channelName, methodName] = header;
    return {
      type: REQUEST_TYPE_NAMES[type] ?? String(type),
      channel: String(channelName),
      method: String(methodName),
    };
  } catch {
    // 响应帧（PromiseSuccess 等）没有 channel/method，记 null 而不是让一次读取炸掉整个脚本。
    return null;
  }
}

/**
 * 从整条调用序列里取出**本场景决定的那一段**：从握手之后的第一个
 * `nex-agent.sendConversationCommandV4`（发 "Say hello"）起，到 turn 落地的
 * `subscribeConversationV4` 为止，并且只保留会话相关的两个频道。
 *
 * 为什么不能整条逐项比较（这是本脚本唯一一个需要判断的地方，所以写清楚）：一次登录后的
 * Web 应用本来就在并发跑一串后台 effect —— 主题、语言、首次运行提示、设置页轮询、git 刷新、
 * 任务列表刷新 —— 它们的**先后顺序由 React effect 的调度决定，与 front 无关**。实测两次
 * front=node 的运行彼此也会在 `setting.update` / `settings-sync.markFirstRunPromptHandled` /
 * `model-selection.getView` 这几个上互换个位。所以整条比较测的是 Chromium 的调度，不是
 * Go front。
 *
 * 频道筛选是第二层收窄：会话协议只在 `nex-agent`（对话与 frame 订阅）和 `nex-task`
 * （任务与会话文件）上，`setting.*` / `model-selection.*` 之类是 UI 的本地设置，不构成
 * "front 改变了协议行为"这个命题。要断言 front 的转发语义，这两组已经全覆盖：Go front
 * 对所有频道一视同仁地转发，差别只可能来自它有没有把某一帧留在自己手里。
 *
 * 而留下的这一段不是噪声：它是用户动作直接引起的调用，顺序由代码里的 await 链决定。
 * Go front 对这些帧只做转发（forward.go 不重编码、不注入），一旦顺序变了就是 front 真改了行为。
 */
const SCENARIO_CHANNELS = ["nex-agent.", "nex-task."];

function scenarioCalls(calls) {
  const first = calls.indexOf("nex-agent.sendConversationCommandV4");
  if (first < 0) return [];
  const tail = calls.slice(first);
  const last = tail.lastIndexOf("nex-agent.subscribeConversationV4");
  const inScenario = last >= 0 ? tail.slice(0, last + 1) : tail;
  return inScenario.filter((call) => SCENARIO_CHANNELS.some((c) => call.startsWith(c)));
}

/** console 里的 WebSocket / RPC 错误才算数；普通 info/debug 噪音不拦。 */
function isTransportError(entry) {
  if (entry.type === "pageerror") return true;
  if (entry.type !== "error") return false;
  return /websocket|rpc|ws\//i.test(entry.text);
}

async function shoot(page, outDir, name) {
  try {
    await page.screenshot({ path: join(outDir, `${name}.png`), fullPage: false });
  } catch (error) {
    log(`screenshot ${name} failed: ${String(error?.message ?? error).slice(0, 200)}`);
  }
}

/**
 * 一次完整 UI 流程：打开 / → 被弹到登录页（web-token-login.md 规则 2）→ 提交 token（规则 3）
 * → 落到 workspace → 建任务 → 发 "Say hello" → 等到 mock 的固定回答。
 *
 * 断言只依赖 packages/shared/src/test-ids.ts 里的稳定 test id 与登录页唯一的
 * `#web-login-token`，不依赖文案或 CSS 结构。
 */
async function driveUi({ playwright, executablePath, baseUrl, outDir, deadline }) {
  const consoleLog = [];
  const browser = await playwright.chromium.launch({
    executablePath,
    args: ["--no-sandbox", "--disable-dev-shm-usage", "--disable-gpu"],
  });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    page.on("console", (message) =>
      consoleLog.push({ type: message.type(), text: message.text() }),
    );
    page.on("pageerror", (error) => consoleLog.push({ type: "pageerror", text: String(error) }));
    page.on("requestfailed", (request) =>
      consoleLog.push({
        type: "requestfailed",
        text: `${request.method()} ${request.url()} ${request.failure()?.errorText ?? ""}`,
      }),
    );
    await page.addInitScript(RPC_TAP_INIT);

    await shoot(page, outDir, "00-first-load");
    await page.goto(`${baseUrl}/`, { waitUntil: "domcontentloaded" });

    // 规则 2：鉴权开启且没有会话时，/ 会被整页换到 /login。web-token-login.md 的验收 1 写的是
    // "/login?next=%2F"，但 buildLoginUrl 在 next 恰好是 "/" 时返回的是裸的 /login（next 参数
    // 没有信息量）；两条路径都到达登录页，所以断言的是"落在 /login 且 next 要么缺省要么 /"。
    await page.waitForURL(/\/login/, { timeout: deadline.stepTimeout() });
    const next = new URL(page.url()).searchParams.get("next");
    assert(
      next === null || next === "/",
      "login redirect lands on /login with next absent or / (rule 2)",
      page.url(),
    );
    await shoot(page, outDir, "01-login");

    const tokenInput = page.locator("#web-login-token");
    await tokenInput.waitFor({
      state: "visible",
      timeout: deadline.stepTimeout(),
    });
    await tokenInput.fill(AUTH_TOKEN);
    await page.locator('form button[type="submit"]').click();
    await shoot(page, outDir, "02-logged-in");

    // composer 是"应用真的跑起来了"的最短信号：它出现说明 ws 握手 + Root 渲染都完成了。
    const composer = page.locator('[data-testid="v4-composer-input"]');
    await composer.waitFor({
      state: "visible",
      timeout: deadline.stepTimeout(),
    });
    assert(true, "browser: v4 composer is visible after login");

    // 建任务：UI 有两个"新建"入口（侧栏组内与 workspace 头部），哪个在就用哪个，
    // 但必须真的点一次 —— 任务列表为空时 composer 直接发也能建会话，点了才是同一条用户路径。
    const newTask = page.locator(
      '[data-testid="conversation-new-task"], [data-testid="task-new-button"]',
    );
    const newTaskCount = await newTask.count();
    if (newTaskCount > 0) {
      await newTask.first().click();
      await shoot(page, outDir, "03-new-task");
    } else {
      notes.push("no new-task test id was rendered; the composer path created the session instead");
    }

    await composer.click();
    await composer.fill(USER_PROMPT);
    await page.locator('[data-testid="v4-composer-send"]').click();

    // assistant 行把固定回答渲染出来 = UI → front → Node upstream → Go agent → mock → UI 闭合。
    await page.locator('[data-testid^="v4-row"]').first().waitFor({
      state: "visible",
      timeout: deadline.stepTimeout(),
    });

    const body = await deadline.until("waiting for the assistant reply", async () => {
      const text = await page.locator("body").innerText();
      return text.includes(CANNED_ANSWER) ? text : null;
    });
    await shoot(page, outDir, "04-answered");

    // 序列只取"客户端发出的"帧（tap 挂在 send 上）：响应帧不记录，因为它们是 Node 侧的内部节奏，
    // 把它算进来只会让比较被上游的实现细节带偏，而 UI 的行为恰恰是它发出的那一半。
    const frames = (await page.evaluate(() => window.__nexRpcFrames ?? [])).map((f) =>
      decodeFrame(f.head),
    );
    const calls = frames.filter(Boolean).map((f) => `${f.channel}.${f.method}`);

    return { sawReply: body.includes(CANNED_ANSWER), consoleLog, calls };
  } finally {
    await browser.close().catch(() => {});
  }
}

// ---- 一个 front 的完整运行 ----------------------------------------------------------

/**
 * 起一个 front，跑完整 UI 流程，返回它看到的 (channel, method) 序列与 console 日志。
 *
 * front 是失败也要返回结果而不是抛出：门禁要的是"两次运行比序列"，某一侧挂掉时把两侧
 * 都留到 artifacts 里再报错，比第一处异常就短路有用。
 */
async function runFront({ front, agentPath, serverPath, executablePath, playwright, mockPort }) {
  const root = mkdtempSync(join(tmpdir(), `nex-phase2-${front}-`));
  const dataDir = join(root, "data");
  const workspacePath = join(root, "ws");
  const outDir = join(ARTIFACT_ROOT, front);
  mkdirSync(join(dataDir, ".nex/v2"), { recursive: true });
  mkdirSync(workspacePath, { recursive: true });
  mkdirSync(outDir, { recursive: true });

  // 两处都写，路径各按 front 自己的解析规则算：
  //   纯 Node front  -> <数据基>/.nex/v2/provider_config.json
  //   Go front       -> agent 由 Node upstream spawn，它的数据基是
  //                     <数据基>/.nex/v2/node-upstream，所以配置文件落在
  //                     <数据基>/.nex/v2/node-upstream/.nex/v2/provider_config.json
  // 两份内容相同，于是两个 front 跑的是同一段流程，只有"谁在前面"不同。
  const config = JSON.stringify(providerConfig(mockPort));
  writeFileSync(join(appConfigDir(dataDir), "provider_config.json"), config);
  const upstreamAppConfigDir = appConfigDir(join(appConfigDir(dataDir), UPSTREAM_DATA_DIR_NAME));
  mkdirSync(upstreamAppConfigDir, { recursive: true });
  writeFileSync(join(upstreamAppConfigDir, "provider_config.json"), config);

  const port = await freePort();
  const baseUrl = `http://127.0.0.1:${port}`;
  const deadline = new Deadline(
    Number(argValue("--timeout-ms", String(DEFAULT_TIMEOUT_MS))),
    `front=${front}`,
  );

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

  const command =
    front === "go"
      ? { file: serverPath, args: [], label: `${serverPath} (Go front, Node upstream behind it)` }
      : {
          file: process.execPath,
          args: [NODE_SERVER_ENTRY],
          label: `${NODE_SERVER_ENTRY} (pure Node front)`,
        };
  if (front === "go") env.NEX_SERVER_ROOT = GO_UPSTREAM_ROOT;

  log(`front=${front}: starting ${command.label}`);
  // cwd 放在仓库外：否则 agent 命令解析的 findUpward 命中 monorepo bundle，GLM_BINARY_PATH 永远不被读。
  const child = spawn(command.file, command.args, {
    cwd: root,
    env,
    stdio: ["ignore", "pipe", "pipe"],
  });

  let serverLog = "";
  const captureServerLog = (chunk) => {
    serverLog += chunk;
  };
  let exited = false;
  child.stdout.on("data", captureServerLog);
  child.stderr.on("data", captureServerLog);
  child.on("exit", () => (exited = true));

  let result;
  try {
    const session = await waitForHealthy(
      baseUrl,
      () => exited,
      () => serverLog,
      deadline,
    );
    log(`front=${front}: up (authRequired=${session.authRequired})`);

    result = await driveUi({
      playwright,
      executablePath,
      baseUrl,
      outDir,
      deadline,
    });

    assert(result.sawReply, `front=${front}: the mock's canned answer reached the UI`);
  } catch (error) {
    result = { sawReply: false, consoleLog: [], calls: [], error: String(error?.message ?? error) };
    failures.push(`front=${front}: ${result.error}`);
    log(`FAIL front=${front}: ${result.error}`);
  } finally {
    writeFileSync(join(outDir, "server.log"), serverLog);
    writeFileSync(
      join(outDir, "console.log"),
      (result?.consoleLog ?? []).map((e) => `[${e.type}] ${e.text}`).join("\n") + "\n",
    );
    writeFileSync(join(outDir, "rpc-sequence.txt"), (result?.calls ?? []).join("\n") + "\n");
    writeFileSync(
      join(outDir, "rpc-scenario-sequence.txt"),
      scenarioCalls(result?.calls ?? []).join("\n") + "\n",
    );
    rmSync(root, { recursive: true, force: true });
    child.kill("SIGTERM");
    await delay(500);
    if (!exited) child.kill("SIGKILL");
  }
  return result;
}

// ---- main ---------------------------------------------------------------------------

async function main() {
  const agentPath = resolve(argValue("--agent", "/tmp/go-rewrite-bin/nex-agent"));
  const serverPath = resolve(argValue("--server", join(REPO, "go/dist/linux-amd64/nex-server")));
  const wanted = argValue("--front", "both");

  for (const [path, hint] of [
    [agentPath, "cd go && go build -buildvcs=false -o <path> ./cmd/nex-agent"],
    [serverPath, "bash scripts/go-rewrite/build-server.sh"],
    [NODE_SERVER_ENTRY, "pnpm --filter @nex/server build"],
    [join(WEB_DIST, "index.html"), "pnpm --filter @nex/web build"],
  ]) {
    if (!existsSync(path)) throw new Error(`missing ${path}; build it with: ${hint}`);
  }

  const executablePath = findBrowserExecutable();
  if (!executablePath)
    throw new Error(
      `no chromium/chrome executable found (tried ${BROWSER_CANDIDATES.filter(Boolean).join(", ")})`,
    );
  const playwright = await loadPlaywright();
  if (!playwright) throw new Error("playwright-core is not resolvable from the repo root");

  const mock = await startMockModel({ steps: [{ text: CANNED_ANSWER }] });
  const runs = {};
  try {
    for (const front of wanted === "both" ? ["go", "node"] : [wanted]) {
      runs[front] = await runFront({
        front,
        agentPath,
        serverPath,
        executablePath,
        playwright,
        mockPort: mock.port,
      });
    }
  } finally {
    await mock.close();
  }

  for (const front of Object.keys(runs)) {
    const { consoleLog = [], calls = [] } = runs[front] ?? {};
    const transportErrors = consoleLog.filter(isTransportError);
    assert(
      transportErrors.length === 0,
      `front=${front}: browser console has no WebSocket/RPC errors`,
      transportErrors
        .slice(0, 5)
        .map((e) => `[${e.type}] ${e.text}`)
        .join(" | "),
    );
    log(
      `front=${front}: ${calls.length} RPC call(s), ${scenarioCalls(calls).length} in-scenario, ${mock.requestCount()} mock request(s)`,
    );
  }

  // 两次运行在**同一个场景内**发出的调用必须逐项相同。Go front 只是把字节转发给 Node
  // （forward.go 不重编码），所以场景内序列不同就意味着 front 改变了 UI 的协议行为。
  if (runs.go && runs.node) {
    const goScenario = scenarioCalls(runs.go.calls ?? []);
    const nodeScenario = scenarioCalls(runs.node.calls ?? []);
    assert(goScenario.length > 0, "the scenario sequence is not empty (nothing to compare)");
    const firstDifference = goScenario.findIndex((call, i) => call !== nodeScenario[i]);
    if (firstDifference === -1 && goScenario.length === nodeScenario.length) {
      log(
        `ok   go and node emit the same ${goScenario.length} in-scenario RPC call(s) in the same order`,
      );
    } else {
      failures.push("go and node in-scenario RPC sequences differ");
      log(`FAIL go vs node in-scenario RPC sequence differs at index ${firstDifference}`);
      log(
        `     go   : ${JSON.stringify(goScenario.slice(Math.max(0, firstDifference - 2), firstDifference + 4))}`,
      );
      log(
        `     node : ${JSON.stringify(nodeScenario.slice(Math.max(0, firstDifference - 2), firstDifference + 4))}`,
      );
    }
  }

  for (const reason of notes) log(`note ${reason}`);
  log(`artifacts (screenshots, console logs, RPC sequences) under ${ARTIFACT_ROOT}`);
  if (failures.length > 0) {
    log(`FAILED: ${failures.join("; ")}`);
    process.exit(1);
  }
  log("phase2-e2e=ok");
  process.exit(0);
}

await main().catch((error) => {
  // 门禁只取最后一行，所以错误详情留在上面，"phase2-e2e=ok" 不出现即失败。
  console.error("[e2e-web-phase2] threw:", error);
  process.exit(1);
});
