// 录制器：用真实的 Node agent（app-server --stdio）跑一个场景，并把两个方向的所有帧落盘。
// 它在这个会话里扮演 host：agent 主动发来的 request（interaction/*、session/requestRuntimePreferences）必须被应答。
//
//   node record-agent-session.mjs <scenario.json> <outDir> [--agent <path-to-nex.cjs>]
//
// 输出（<outDir>）：
//   in.ndjson   host -> agent，每行 {"t":<ms since start>,"frame":<message>}
//   out.ndjson  agent -> host，同上
//   meta.json   场景 id、agent 命令、规范化后的哈希、统计
import { spawn } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
import { startMockModel } from "./mock-model.mjs";
import { normalizeFrames } from "./normalize.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../../..");
const DEFAULT_AGENT = join(REPO, "apps/nex-cli/packages/cli/dist/nex.cjs");

// 固定的 host 偏好应答：与 nexSessionRuntimePreferencesResultSchema 对齐。
const RUNTIME_PREFERENCES = {
  nativeSearchEnhancementsEnabled: false,
  memoryEnabled: false,
  toolSearchEnabled: false,
  codemodeEnabled: false,
  askUserQuestionAutoResolutionEnabled: true,
  modelContextBudgetStrategy: "preflight-v1",
};

function providerConfig(port) {
  return {
    schemaVersion: 1,
    config: {
      providerConfigRules: {
        providerRules: [
          {
            providerId: "mock",
            providerName: "Mock",
            config: {
              group: "standard-personal",
              access: { type: "api-key", apiKey: "sk-test-fake" },
              api: { type: "openai-chat-completions", baseUrl: `http://127.0.0.1:${port}/v1` },
              personalModelIds: ["mock-model"],
            },
          },
        ],
      },
      modelConfigRules: {
        providerModelRules: [],
        manualProviderModelRules: [
          {
            providerId: "mock",
            modelId: "mock-model",
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
      defaultModelSelection: { providerId: "mock", modelId: "mock-model" },
    },
  };
}

// host 对 agent 反向请求的应答。permission：按 optionId 选择 agent 自己给出的选项（和真实 UI 点按钮等价）。
function hostReply(frame, policy) {
  if (frame.method === "session/requestRuntimePreferences")
    return { id: frame.id, result: RUNTIME_PREFERENCES };
  if (frame.method === "interaction/requestPermission") {
    const wanted = policy.permission ?? "deny";
    const option = (frame.params.options ?? []).find((o) => o.optionId === wanted);
    if (!option)
      return {
        id: frame.id,
        error: {
          code: -32602,
          message: `scenario wants option ${wanted}; agent offered ${(frame.params.options ?? []).map((o) => o.optionId).join(",")}`,
        },
      };
    return { id: frame.id, result: option.response };
  }
  return { id: frame.id, error: { code: -32601, message: `Method not found: ${frame.method}` } };
}

const substitute = (value, vars) => JSON.parse(JSON.stringify(value).replaceAll("$WS", vars.WS));

// agent 的形态：字符串 = Node bundle 路径（用 node 启动）；对象 = 任意可执行文件，例如 Go agent。
function agentCommand(agent) {
  if (typeof agent === "string")
    return { command: "node", args: [agent, "app-server", "--stdio"], label: "node" };
  return {
    command: agent.command,
    args: agent.args ?? ["app-server", "--stdio"],
    label: agent.label ?? "custom",
  };
}

export async function recordScenario(
  scenarioPath,
  outDir,
  { agent = DEFAULT_AGENT, quiet = false, raw = false, extraEnv = {} } = {},
) {
  const scenario = JSON.parse(readFileSync(scenarioPath, "utf8"));
  const root = mkdtempSync(join(tmpdir(), `nex-rec-${scenario.id}-`));
  const HOME = join(root, "home"),
    WS = join(root, "ws");
  mkdirSync(join(HOME, ".nex/v2"), { recursive: true });
  mkdirSync(WS, { recursive: true });
  for (const [name, content] of Object.entries(scenario.files ?? {}))
    writeFileSync(join(WS, name), content);

  const steps = substitute(scenario.model.steps, { WS });
  const modelRequestBodies = [];
  const mock = await startMockModel({
    onRequest: (body) => modelRequestBodies.push(body),
    steps,
    logFile: join(root, "model-requests.jsonl"),
    bodyDir: process.env.NEX_REC_BODY_DIR,
  });
  writeFileSync(
    join(HOME, ".nex/v2/provider_config.json"),
    JSON.stringify(providerConfig(mock.port)),
  );

  const started = Date.now();
  const cmd = agentCommand(agent);
  const child = spawn(cmd.command, cmd.args, {
    env: {
      PATH: process.env.PATH,
      HOME,
      NEX_DATA_BASE_DIR: HOME,
      ...(scenario.env ?? {}),
      ...extraEnv,
    },
    stdio: ["pipe", "pipe", "pipe"],
  });
  const inbound = [],
    outbound = [],
    waiters = [];
  let stderr = "",
    buf = "",
    nextId = 0;
  const send = (frame) => {
    inbound.push({ t: Date.now() - started, frame });
    child.stdin.write(JSON.stringify(frame) + "\n");
  };
  child.stderr.on("data", (d) => (stderr += d));
  child.stdout.on("data", (d) => {
    buf += d;
    let i;
    while ((i = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, i);
      buf = buf.slice(i + 1);
      if (!line.trim()) continue;
      const frame = JSON.parse(line);
      outbound.push({ t: Date.now() - started, frame });
      if (frame.id !== undefined && frame.method) send(hostReply(frame, scenario.host ?? {}));
      waiters.splice(0).forEach((w) => w());
    }
  });
  const exited = new Promise((r) => child.on("exit", (code, signal) => r({ code, signal })));
  const waitFor = async (pred, what, ms = 20000) => {
    const t = Date.now();
    for (;;) {
      const hit = outbound.map((o) => o.frame).find(pred);
      if (hit) return hit;
      if (Date.now() - t > ms)
        throw new Error(
          `timeout waiting for ${what}; last: ${JSON.stringify(outbound.slice(-2)).slice(0, 400)}`,
        );
      await new Promise((r) => {
        waiters.push(r);
        setTimeout(r, 100);
      });
    }
  };
  const request = async (method, params) => {
    const id = ++nextId;
    send({ id, method, params });
    return waitFor((f) => f.id === id && !f.method, `response to ${method}`);
  };

  const results = {};
  const ws = { workspacePath: WS, workspaceKey: WS };
  try {
    for (const raw of scenario.script) {
      const step = substitute(raw, { WS });
      if (step.op === "waitReady")
        await waitFor(
          (f) => f.method === "startup/storageState" && f.params.phase === "ready",
          "storage ready",
        );
      else if (step.op === "request") results[step.as] = await request(step.method, step.params);
      else if (step.op === "command") {
        const n = Object.keys(results).length + 1;
        results[step.as] = await request("v4/command", {
          commandId: `cmd-${String(n).padStart(4, "0")}`,
          clientId: "rec-client",
          sessionId: step.sessionFrom ? results[step.sessionFrom].result.result.sessionId : null,
          type: step.type,
          payload: step.payload,
          issuedAt: 1700000000000,
        });
      } else if (step.op === "subscribe") {
        const sid = results[step.sessionFrom].result.result.sessionId;
        results[step.as] = await request("v4/conversation/subscribe", {
          topic: `conversation/${sid}`,
          connectionId: "rec-conn-1",
          clientMode: "desktop-continuous",
          workspace: ws,
        });
      } else if (step.op === "waitTelemetry")
        await waitFor(
          (f) => f.method === "v4/telemetry/event" && f.params.kind === step.kind,
          `telemetry ${step.kind}`,
        );
      else throw new Error(`unknown script op ${step.op}`);
    }
    await new Promise((r) => setTimeout(r, 300)); // 让尾部帧（telemetry、最后的 delta）落地
  } finally {
    child.stdin.end();
    const killer = setTimeout(() => child.kill("SIGKILL"), 5000);
    var exit = await exited;
    clearTimeout(killer);
    await mock.close();
  }

  mkdirSync(outDir, { recursive: true });
  const ndjson = (rows) => rows.map((r) => JSON.stringify(r)).join("\n") + "\n";
  const normOpts = { WS, HOME };
  const normalizedOut = normalizeFrames(
    outbound.map((o) => o.frame),
    normOpts,
  );
  const normalizedIn = normalizeFrames(
    inbound.map((o) => o.frame),
    normOpts,
  );
  const hash = createHash("sha256").update(JSON.stringify(normalizedOut)).digest("hex");
  const meta = {
    id: scenario.id,
    title: scenario.title,
    outFrames: outbound.length,
    inFrames: inbound.length,
    modelRequests: mock.requestCount(),
    exitCode: exit.code,
    normalizedOutSha256: hash,
  };
  writeFileSync(join(outDir, "meta.json"), JSON.stringify(meta, null, 1) + "\n");
  writeFileSync(join(outDir, "out.normalized.json"), JSON.stringify(normalizedOut, null, 1) + "\n");
  writeFileSync(join(outDir, "in.normalized.json"), JSON.stringify(normalizedIn, null, 1) + "\n");
  if (raw) {
    // 原始帧含随机临时路径与墙钟时间，只用于人工调试，绝不提交。
    writeFileSync(join(outDir, "in.raw.ndjson"), ndjson(inbound));
    writeFileSync(join(outDir, "out.raw.ndjson"), ndjson(outbound));
  }
  if (process.env.KEEP_REC_DIR) console.log("kept", root);
  else rmSync(root, { recursive: true, force: true });
  if (!quiet)
    console.log(
      `${scenario.id} [${cmd.label}]: ${outbound.length} out / ${inbound.length} in frames, ${mock.requestCount()} model requests, exit=${exit.code}, sha=${hash.slice(0, 12)}`,
    );
  return {
    ...meta,
    normalizedOut,
    normalizedIn,
    stderr,
    modelRequestBodies: modelRequestBodies.map((b) => normalizeFrames([b], { WS, HOME })[0]),
  };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const [scenarioPath, outDir] = process.argv.slice(2);
  const agentFlag = process.argv.indexOf("--agent");
  await recordScenario(
    resolve(scenarioPath),
    resolve(outDir),
    agentFlag > 0 ? { agent: resolve(process.argv[agentFlag + 1]) } : {},
  );
}
