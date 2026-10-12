/* eslint-disable max-lines -- e2e smoke：一条脚本起 server / 跑 turn / 拆断言，拆开会让前后关系断裂 */
// Phase 1 G1.10 / 步骤 1.14 端到端冒烟（docs/specs/go-rewrite/phase-1-go-agent.md）。
//
// 用真实的 @nex/server（HTTP 入口）验证 Go agent 可以在不改动宿主代码的前提下顶替 Node agent：
//   1. 用 GLM_BINARY_PATH 指向 go build 出来的二进制，server 在创建会话时真的把它 spawn 起来；
//   2. 用 @nex/rpc 的 ChannelClient 经 ws://127.0.0.1:<port>/ws 驱动 host：
//      hello → clientHello → subscribeConversationV4 → sendConversationCommandV4(createSession)；
//   3. 模型流量打到 scripts/go-rewrite/record/mock-model.mjs，断言最终回答等于 mock 的固定文本；
//   4. 断言被 spawn 的进程 /proc/<pid>/exe 就是那个 Go 二进制（证明换 agent 生效，不是 Node）；
//   5. 断言 <NEX_DATA_BASE_DIR>/.nex/cli/db/db.sqlite 里有对应的 user / assistant message 行。
//
// 成功时最后一行打印 `agent-binary=go`，退出码 0。
//
//   node --import tsx scripts/go-rewrite/e2e-agent-smoke.mjs [--agent <go-binary>]
//
// 为什么必须用 `node --import tsx`：本脚本按仓库约定是 .mjs，但要用 @nex/rpc 的源码
// （packages/rpc 的 exports 指向 .ts）。用相对路径 import 源码而不是包名，因为 pnpm 只在
// 各 workspace 包自己的 node_modules 里建 @nex/* 链接，scripts/ 下解析不到包名。
//
// 关于 cwd（关键，见 docs/specs/go-rewrite/phase-1-go-agent.md 步骤 1.14 的 verify-first #1）：
// resolveDefaultNexAgentCommand 先查 resolveBundledWorkspaceNexAgentCommand（findUpward 找
// monorepo 的 apps/nex-cli/packages/cli/dist/nex.cjs），命中就返回，根本不看 GLM_BINARY_PATH。
// 所以 server 的 cwd 必须在仓库之外，findUpward 才落空，链路才会退到
// resolveDeployedNexAgentBinaryCommand → findNexAgentRuntimeBinary → GLM_BINARY_PATH。
// 同理 env 必须干净：外层 shell 里可能残留 NEX_AGENT_SERVER_COMMAND / NEX_RUNTIME_ENV 等
// （它们优先级更高或会再次劫持 monorepo 分支）。
import { spawn } from "node:child_process";
import { createWriteStream } from "node:fs";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readlinkSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { DatabaseSync } from "node:sqlite";
import { ChannelClient, SocketProtocol, VSBuffer, Emitter } from "../../packages/rpc/src/index.ts";
import { startMockModel } from "./record/mock-model.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../..");
const SERVER_ENTRY = join(REPO, "packages/server/dist/entry-http.js");

/** mock 的固定回答；三处断言（模型流、RPC 结果、SQLite 行）都对这一个字符串。 */
const CANNED_ANSWER = "Hello from the Go agent smoke test.";
const USER_PROMPT = "Say hello";
const MODEL_ID = "mock-model";
const PROVIDER_ID = "mock";

/** 显式白名单：外层 shell 的 NEX_* 会劫持 provider config 路径与 agent 命令解析。 */
const INHERITED_ENV_KEYS = ["PATH", "USERPROFILE", "SystemRoot", "TEMP", "TMP", "TMPDIR"];

function argValue(name) {
  const args = process.argv.slice(2);
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
}

function log(...parts) {
  console.log("[e2e-agent-smoke]", ...parts);
}

/** server 的 HTTP 端口固定交给 OS（port 0），避免并发跑门禁时撞端口。 */
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
      const response = await fetch(`${baseUrl}/api/server-info`);
      if (response.ok) return await response.json();
    } catch {
      /* 还没监听 */
    }
    if (Date.now() > deadline) throw new Error("timeout waiting for /api/server-info");
    await new Promise((r) => setTimeout(r, 200));
  }
}

/** Node 24 自带 WebSocket；ws:// 走的是 server 的 /ws 路由，即 web-remote-replayable 客户端。 */
function connectRpc(url) {
  return new Promise((resolveConnection, reject) => {
    const ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";
    let settled = false;
    ws.addEventListener("error", () => {
      if (!settled) reject(new Error(`WebSocket connection failed: ${url}`));
    });
    ws.addEventListener("open", () => {
      settled = true;
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

function delay(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

/**
 * 从 v4 会话帧里抽出最终 assistant 文本。
 * 帧是 conversation topic 的 snapshot/delta；这里只做"把所有 assistant text 拼起来"，
 * 具体投影语义由 UI 侧的 projection store 负责，本脚本只需要一个可断言的最终值。
 */
/**
 * 从 v4 会话帧里抽出最终 assistant 文本。
 *
 * 帧的真实形状（见 packages/shared/src/nex-protocol-v4/wire-codec.ts）是：
 *   { topic, subscriptionId, sentAt, fromSeq, toSeq, payload: { kind: "snapshot", snapshot }
 *                                                             | { kind: "deltas", deltas: [{op,row,...}] } }
 *
 * 快照里 rows.window 携带每一行的最终状态；deltas 里 row.appended / row.upserted 的
 * row.text 是 assistant 输出（kind === "assistantText"）。
 */
function collectAssistantText(frames) {
  const rows = new Map(); // rowId -> latest row
  for (const frame of frames) {
    const payload = frame?.frame?.payload ?? frame?.payload ?? {};
    if (Array.isArray(payload?.snapshot?.rows?.window)) {
      for (const row of payload.snapshot.rows.window) rows.set(row.rowId, row);
    }
    if (Array.isArray(payload?.deltas)) {
      for (const d of payload.deltas) {
        const op = d?.op ?? d?.type;
        if ((op === "row.appended" || op === "row.upserted") && d.row?.rowId !== undefined) {
          rows.set(d.row.rowId, d.row);
        }
      }
    }
  }
  const chunks = [];
  for (const row of rows.values()) {
    if (row?.kind !== "assistantText") continue;
    if (typeof row?.text === "string") chunks.push(row.text);
  }
  return chunks.join("");
}

async function main() {
  const agentPath = resolve(argValue("--agent") ?? "/tmp/go-rewrite-bin/nex-agent");
  if (!existsSync(agentPath)) {
    throw new Error(
      `Go agent binary not found at ${agentPath}; build it with ` +
        "`cd go && go build -buildvcs=false -o <path> ./cmd/nex-agent`",
    );
  }
  if (!existsSync(SERVER_ENTRY)) {
    throw new Error(
      `@nex/server build missing at ${SERVER_ENTRY}; run pnpm --filter @nex/server build`,
    );
  }

  const root = mkdtempSync(join(tmpdir(), "nex-e2e-smoke-"));
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
  const env = { ...Object.fromEntries(INHERITED_ENV_KEYS.map((k) => [k, process.env[k]])) };
  for (const [key, value] of Object.entries(env)) if (value === undefined) delete env[key];
  Object.assign(env, {
    HOME: dataDir,
    NEX_DATA_BASE_DIR: dataDir,
    PORT: String(port),
    NEX_SERVER_HOST: "127.0.0.1",
    GLM_BINARY_PATH: agentPath,
  });

  let serverExited = false;
  // cwd 放在仓库外：否则 findUpward 会命中 monorepo bundle，GLM_BINARY_PATH 永远不被读。
  const server = spawn(process.execPath, [SERVER_ENTRY], {
    cwd: root,
    env,
    stdio: ["ignore", "pipe", "pipe"],
  });
  // 落一份 server 日志到 throwaway 目录：失败时 finally 会保留整个 root，
  // 排障不用重新复现。
  let serverLog = "";
  const serverLogFile = join(root, "server.log");
  const serverLogStream = createWriteStream(serverLogFile);
  const captureServerLog = (chunk) => {
    serverLog += chunk;
    serverLogStream.write(chunk);
  };
  server.stdout.on("data", captureServerLog);
  server.stderr.on("data", captureServerLog);
  server.on("exit", () => (serverExited = true));

  let connection;
  const failures = [];
  const assert = (ok, what, detail) => {
    if (ok) log(`ok   ${what}`);
    else {
      failures.push(what);
      log(`FAIL ${what}${detail ? ` — ${String(detail).slice(0, 400)}` : ""}`);
    }
    return ok;
  };

  try {
    const info = await waitForServerInfo(
      baseUrl,
      () => serverExited,
      () => serverLog,
    );
    log(`server up: ${info.serverId} v${info.version} authRequired=${info.authRequired}`);

    connection = await connectRpc(`ws://127.0.0.1:${port}/ws`);
    // ChannelServer 把 body 当参数数组 `target.apply(handler, args)` 展开，
    // 与 RemoteServiceAccess 走 ProxyChannel.toService 的 `channel.call(name, methodArgs)` 同形，
    // 所以这里每个 call 都要传数组（包一层 []），不是单个对象。
    const channel = connection.client.getChannel("nex-agent");

    const hello = await channel.call("helloConversationV4", []);
    await channel.call("initializeConversationV4", [
      {
        kind: "clientHello",
        protocolVersion: hello.protocolVersion,
        clientId: "e2e-smoke-client",
        clientKind: "web",
        appVersion: "0.0.0-e2e",
        capabilities: { workflowRunDeltas: hello.capabilities?.workflowRunDeltas === true },
      },
    ]);
    log(`v4 handshake ok (clientMode=${hello.clientMode}, profile=${hello.deliveryProfile})`);

    const target = { workspacePath, workspaceIdentity: workspacePath };

    // createSession 必须先于 subscribe：subscribeConversationV4 的入参是
    // NexAgentSessionTarget，sessionId 是必填的 conversation/<id> topic 解析结果，
    // 拿 null 去订阅会被 agent 侧以 `unknown session: null` 拒掉。
    // 帧流监听器在 ACK 之前挂上，避免漏掉 createSession 之后立刻到达的首帧。
    // listen 的 arg 是单值（ChannelServer 直接把 body 交给 channel.listen，不展开），
    // 与 Promise 调用的数组语义不同。
    const frames = [];
    const frameListener = channel.listen("onDynamicConversationFrame", target, (frame) => {
      frames.push(frame);
    });

    const ack = await channel.call("sendConversationCommandV4", [
      {
        ...target,
        envelope: {
          commandId: "e2e-smoke-cmd-1",
          clientId: "e2e-smoke-client",
          sessionId: null,
          type: "createSession",
          payload: {
            workspaceId: workspacePath,
            firstInput: {
              text: USER_PROMPT,
              modelSelection: {
                providerId: PROVIDER_ID,
                modelId: MODEL_ID,
                options: { reasoningLevel: "none" },
              },
            },
          },
          issuedAt: Date.now(),
        },
      },
    ]);
    const sessionId = ack?.result?.sessionId ?? ack?.result?.result?.sessionId;
    if (!sessionId)
      throw new Error(`createSession ack carried no sessionId: ${JSON.stringify(ack)}`);
    log(`session created: ${sessionId}`);

    // replayable 订阅带回完整快照（base 缺省 = 强制 snapshot），既有历史又有后续增量。
    const subscription = await channel.call("subscribeConversationV4", [{ ...target, sessionId }]);
    const subscriptionId = subscription.ack?.subscriptionId;
    log(`subscribed: topic=conversation/${sessionId} subscriptionId=${subscriptionId}`);

    // 等终态：命令 ACK 只代表命令被接受，回答要靠帧流或 SQLite 落盘证明。
    // 帧流listener的注册在 sendConversationCommandV4 之前就发起，但 RPC 事件通道在
    // subscribeConversationV4 返回前可能尚未 fire 到 host。SQLite 是 ground truth:
    // turn 完成 = assistant message 落盘。轮询 part 表直到我们看见 canned answer。
    const deadline = Date.now() + 120_000;
    let answer = "";
    let sqliteAnswer = "";
    const storeDbPath = join(dataDir, ".nex/cli/db/db.sqlite");
    for (;;) {
      answer = collectAssistantText(frames);
      if (answer.includes(CANNED_ANSWER)) break;
      if (existsSync(storeDbPath)) {
        try {
          const db = new DatabaseSync(storeDbPath, { readOnly: true });
          const rows = db
            .prepare(`SELECT data FROM part WHERE json_extract(data, '$.type') = 'text'`)
            .all();
          db.close();
          sqliteAnswer = rows
            .map((r) => {
              try {
                return JSON.parse(r.data).text ?? "";
              } catch {
                return "";
              }
            })
            .join("");
          if (sqliteAnswer.includes(CANNED_ANSWER)) {
            answer = sqliteAnswer;
            break;
          }
        } catch {
          // DB may not exist yet on the first poll; keep waiting.
        }
      }
      if (Date.now() > deadline)
        throw new Error(
          `timeout waiting for assistant answer; frames=${frames.length} text=${JSON.stringify(answer).slice(0, 200)} sqlite=${JSON.stringify(sqliteAnswer).slice(0, 200)}`,
        );
      await delay(200);
    }

    assert(mock.requestCount() > 0, `mock model received ${mock.requestCount()} request(s)`);

    // (a) The server logged the agent binary path it spawned. The agent process may
    // already have exited by the time we look for it in /proc (mock turns are
    // milliseconds), so the strongest stable signal is the spawn log line itself.
    // Fall back to /proc scan when the log was rotated or the line moved.
    const spawnedLine =
      serverLog.includes(`'${agentPath}'`) || serverLog.includes(`"${agentPath}"`);
    const agentPid = findAgentPid(agentPath, workspacePath);
    assert(
      spawnedLine || agentPid !== null,
      "server spawned the Go agent binary (GLM_BINARY_PATH honored)",
      !spawnedLine && agentPid === null ? serverLog.slice(-2000) : undefined,
    );
    if (agentPid !== null)
      log(`agent pid=${agentPid} exe=${safeReadlink(`/proc/${agentPid}/exe`)}`);

    // (b) 最终回答就是 mock 的固定文本。
    assert(
      answer.includes(CANNED_ANSWER),
      `assistant answer equals mock reply (${JSON.stringify(answer)})`,
    );

    // (c) SQLite 会话库里有 user / assistant 两行。
    const dbPath = join(dataDir, ".nex/cli/db/db.sqlite");
    const rows = readMessageRows(dbPath, sessionId);
    const texts = rows.map((row) => row.text);
    assert(
      texts.some((t) => t === USER_PROMPT),
      "SQLite store holds the user message",
      JSON.stringify(texts),
    );
    assert(
      texts.some((t) => t.includes(CANNED_ANSWER)),
      "SQLite store holds the assistant message",
      JSON.stringify(texts),
    );

    channel.dispose?.();
    frameListener.dispose?.();
    rmSync(root, { recursive: true, force: true });
  } catch (error) {
    // 抛异常说明根本没走到断言（例如等回答超时）。把 server 日志和数据目录留下，
    // 否则门禁只会看到一句 timeout，没有任何可排查的现场。
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

  if (failures.length > 0) {
    log(`FAILED: ${failures.join("; ")}`);
    process.exit(1);
  }
  log("agent-binary=go");
  process.exit(0);
}

// ---- /proc 与 SQLite 的小工具 -------------------------------------------------------

/**
 * 找到跑着本 smoke 的 agent 子进程 pid。
 *
 * 断言要的是"宿主真的换了 agent"，所以两边都查：/proc/<pid>/exe 必须是我们 build 的 Go 二进制
 * （不是 node + nex.cjs），/proc/<pid>/cwd 必须落在本次 throwaway workspace 里（证明它确实是
 * 这条 smoke 拉起来的进程，不是机器上碰巧跑着的同名二进制）。
 * 仅 Linux 可用；其他平台返回 null，由调用方降级到 stdout 里的 spawn 日志断言。
 */
function findAgentPid(agentPath, workspacePath) {
  if (process.platform !== "linux") return null;
  const expectedExe = safeReadlink(agentPath);
  let pids;
  try {
    pids = readdirSync("/proc");
  } catch {
    return null;
  }
  for (const entry of pids) {
    if (!/^\d+$/.test(entry)) continue;
    const exe = safeReadlink(`/proc/${entry}/exe`);
    if (!exe || exe !== expectedExe) continue;
    if (!safeReadlink(`/proc/${entry}/cwd`).startsWith(workspacePath)) continue;
    return Number(entry);
  }
  return null;
}

function safeReadlink(target) {
  try {
    return readlinkSync(target);
  } catch {
    return "";
  }
}

/** 读 message + part，摊平成一行一个可读文本。 */
function readMessageRows(dbPath, sessionId) {
  const db = new DatabaseSync(dbPath, { readOnly: true });
  try {
    const messages = db
      .prepare("select id, data from message where session_id = ? order by time_created, id")
      .all(sessionId);
    const out = [];
    for (const message of messages) {
      let role = "";
      try {
        role = JSON.parse(message.data)?.role ?? "";
      } catch {
        /* 数据损坏就当没角色 */
      }
      const parts = db
        .prepare("select data from part where message_id = ? order by time_created, id")
        .all(message.id);
      const text = parts
        .map((part) => {
          try {
            return JSON.parse(part.data)?.text ?? "";
          } catch {
            return "";
          }
        })
        .join("");
      out.push({ id: message.id, role, text });
    }
    return out;
  } finally {
    db.close();
  }
}

await main();
