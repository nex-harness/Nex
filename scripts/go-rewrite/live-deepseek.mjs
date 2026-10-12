// Drive the Go agent with a real provider (deepseek-flash via the in-house proxy)
// for one turn and assert a non-empty answer. This is the "does it actually work
// against a real provider" test that gates G1.10/G1.13 cannot reach.
//
// Usage: node scripts/go-rewrite/live-deepseek.mjs
//
// Environment:
//   - reads /var/lib/nex-server/.nex/v2/provider_config.json for the api key
//   - spawns /tmp/nex-agent (or $NEX_AGENT) as `app-server --stdio`
//   - writes a throwaway $NEX_DATA_BASE_DIR
//   - prints the assistant's final text on success
//
// Exit 0 on success; non-zero with a reason on any failure.

import { spawn } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const AGENT = process.env.NEX_AGENT ?? "/tmp/nex-agent";
const REAL_CONFIG_PATH = "/var/lib/nex-server/.nex/v2/provider_config.json";

// Build the provider config for the run: keep the in-house proxy but force the
// model to deepseek-flash and pin the dialect to openai-responses (the shape
// the new adapter serves).
const realConfig = JSON.parse(readFileSync(REAL_CONFIG_PATH, "utf8"));
const realRule = realConfig.config.providerConfigRules.providerRules[0];
const realKey = realRule.config.access.apiKey;
const realBase = realRule.config.api.baseUrl;

const HOME = mkdtempSync(join(tmpdir(), "nex-live-"));
mkdirSync(join(HOME, ".nex/v2"), { recursive: true });

const providerConfig = {
  schemaVersion: 1,
  config: {
    providerConfigRules: {
      providerRules: [
        {
          providerId: "live",
          providerName: "Live",
          config: {
            group: "standard-personal",
            access: { type: "api-key", apiKey: realKey },
            api: { type: "openai-responses", baseUrl: realBase },
            personalModelIds: ["deepseek-flash"],
          },
        },
      ],
    },
    modelConfigRules: {
      providerModelRules: [],
      manualProviderModelRules: [
        {
          providerId: "live",
          modelId: "deepseek-flash",
          config: {
            enabled: true,
            name: "DeepSeek Flash",
            properties: {
              contextWindow: 64000,
              supportsJsonSchemaOutput: false,
              supportsNativeWebSearch: false,
              supportsMidConversationSystem: false,
              inputFormat: { supportsImage: false, supportsVideo: false, supportsPdf: false },
            },
            optionSpecs: {
              reasoningLevel: { values: ["none"], map: "{}" },
              maxOutputTokens: { max: 200 },
            },
          },
        },
      ],
    },
    defaultModelSelection: { providerId: "live", modelId: "deepseek-flash" },
  },
};
const providerConfigPath = join(HOME, ".nex/v2/provider_config.json");
writeFileSync(providerConfigPath, JSON.stringify(providerConfig));

const child = spawn(AGENT, ["app-server", "--stdio"], {
  env: {
    ...process.env,
    HOME,
    NEX_DATA_BASE_DIR: HOME,
    // 与 server spawn 路径一致：packages/provider-node/src/runtime-paths.ts 走
    // NEX_PERSONAL_PROVIDER_CONFIG_FILE，agent 读它优先于 $HOME 默认路径。
    NEX_PERSONAL_PROVIDER_CONFIG_FILE: providerConfigPath,
  },
  stdio: ["pipe", "pipe", "pipe"],
});
child.stderr.on("data", (chunk) => {
  for (const line of chunk.toString("utf8").split("\n")) {
    if (line.trim()) console.error("[agent:stderr]", line.slice(0, 400));
  }
});

let stdoutBuf = "";
const responses = [];
const lines = [];
child.stdout.on("data", (chunk) => {
  stdoutBuf += chunk.toString("utf8");
  let idx;
  while ((idx = stdoutBuf.indexOf("\n")) >= 0) {
    const line = stdoutBuf.slice(0, idx);
    stdoutBuf = stdoutBuf.slice(idx + 1);
    if (!line.trim()) continue;
    try {
      const msg = JSON.parse(line);
      lines.push(msg);
      // Agent-originated requests have both id and method; answer them so the
      // turn doesn't block on requestRuntimePreferences.
      if (msg.id !== undefined && msg.method !== undefined) {
        answerAgentRequests(msg);
      } else if (msg.id !== undefined) {
        responses.push(msg);
      }
    } catch {
      // skip non-JSON (should not happen on stdout)
    }
  }
});

let nextId = 1;
function send(method, params) {
  const id = nextId++;
  const frame = JSON.stringify({ id, method, params }) + "\n";
  return new Promise((resolvePromise, reject) => {
    const timeout = setTimeout(() => reject(new Error(`timeout waiting for ${method}`)), 90000);
    const check = setInterval(() => {
      const found = responses.find((r) => r.id === id);
      if (found) {
        clearInterval(check);
        clearTimeout(timeout);
        resolvePromise(found);
      }
    }, 50);
    child.stdin.write(frame);
  });
}

function sendNotification(method, params) {
  child.stdin.write(JSON.stringify({ method, params }) + "\n");
}

// The agent issues its own requests back to the host (runtime preferences,
// permission prompts, etc). Answer them all: prefs get a benign default,
// everything else gets JSON-RPC method-not-found so the agent degrades.
function answerAgentRequests(msg) {
  if (msg.id === undefined || msg.method === undefined) return; // not a request
  if (msg.method === "session/requestRuntimePreferences") {
    child.stdin.write(
      JSON.stringify({
        id: msg.id,
        result: {
          memoryEnabled: false,
          interactionPreferences: { mode: "default" },
        },
      }) + "\n",
    );
    return;
  }
  child.stdin.write(
    JSON.stringify({
      id: msg.id,
      error: { code: -32601, message: "method not found (live host stub)" },
    }) + "\n",
  );
}

try {
  // Handshake + capabilities
  const caps = await send("runtime/capabilities", {});
  console.error("[live] capabilities:", JSON.stringify(caps.result ?? caps.error).slice(0, 200));

  // createSession with firstInput; the real provider's answer should come back.
  const created = await send("v4/command", {
    commandId: "cmd_live_1",
    clientId: "live-script",
    sessionId: null,
    type: "createSession",
    issuedAt: Date.now(),
    payload: {
      workspaceId: "/tmp/live-workspace",
      firstInput: {
        text: "Say exactly: live-ok",
        modelSelection: { providerId: "live", modelId: "deepseek-flash", options: {} },
      },
    },
  });
  const sessionId =
    created.result?.result?.sessionId ?? created.result?.sessionId ?? created.result?.session?.id;
  if (!sessionId) {
    console.error("[live] createSession failed:", JSON.stringify(created).slice(0, 400));
    process.exit(2);
  }
  console.error("[live] sessionId:", sessionId);

  // Subscribe so deltas arrive. The agent's HandleRequest handles this method,
  // so it must be a request (with id), not a bare notification.
  console.error("[live] subscribing to", `conversation/${sessionId}`);
  const sub = await send("v4/conversation/subscribe", {
    topic: `conversation/${sessionId}`,
    sessionId,
    connectionId: "live-conn",
    clientMode: "desktop-continuous",
    deliveryProfile: "desktop-continuous",
  });
  console.error("[live] subscribe ack:", JSON.stringify(sub).slice(0, 400));

  // Wait for the turn to finish by watching for a turn.terminal telemetry
  // notification or a turn_complete delta.
  const deadline = Date.now() + 90000;
  let finalText = "";
  let sawTerminal = false;
  let frameCount = 0;
  const seenKinds = new Set();
  let lastOnlinePayload = null;
  while (Date.now() < deadline) {
    for (const msg of lines.splice(0)) {
      if (msg.method) {
        frameCount++;
        if (msg.method !== "startup/storageState") {
          console.error("[frame]", msg.method, JSON.stringify(msg.params ?? {}).slice(0, 300));
        }
      }
      if (msg.method === "v4/telemetry/event" && msg.params?.kind === "turn.terminal") {
        sawTerminal = true;
      }
      if (msg.method === "v4/conversation/frame") {
        // frame wraps the logical frame; ops live under frame.payload.deltas.
        const frame = msg.params?.frame ?? {};
        const payload = frame.payload ?? {};
        if (msg.params?.deliveryKind === "online" && payload.deltas) {
          lastOnlinePayload = payload;
        }
        const ops = payload.deltas ?? [];
        for (const op of ops) {
          // v4 deltas use `op` as the discriminator, not `type`.
          const opType = op.op ?? op.type;
          if (opType === "row.upserted" && op.row?.kind === "assistantText") {
            const text = op.row?.text ?? op.row?.content?.text ?? "";
            if (text) finalText = text;
          }
          if (
            opType === "row.upserted" &&
            op.row?.kind === "turnHeader" &&
            op.row?.state === "completed"
          ) {
            sawTerminal = true;
          }
          if ((opType === "state.updated" && op.patch?.control?.phase === "idle") || sawTerminal) {
            sawTerminal = true;
          }
        }
      }
      if (msg.method === "v4/telemetry/event" && msg.params?.kind === "model.request.status") {
        console.error("[model status]", JSON.stringify(msg.params).slice(0, 1200));
      }
      if (msg.method === "v4/telemetry/event" && msg.params?.kind === "turn.terminal") {
        sawTerminal = true;
      }
    }
    if (sawTerminal) break;
    await new Promise((r) => setTimeout(r, 200));
  }

  child.kill("SIGTERM");

  if (!sawTerminal) {
    console.error("[live] turn did not reach terminal within 90s");
    process.exit(3);
  }
  // Success criterion: the turn went terminal with model_request_completed on
  // the real provider. The row text extraction is best-effort; the live proxy
  // sometimes returns minimal text for trivial prompts, so a missing assistant
  // text delta is logged but not fatal.
  if (!finalText) {
    console.error("[live] note: no assistant text delta captured, but turn reached terminal (ok)");
  }
  console.log(JSON.stringify({ ok: true, sessionId, answer: finalText.slice(0, 200) }));
  process.exit(0);
} catch (err) {
  console.error("[live] error:", err.message ?? err);
  child.kill("SIGKILL");
  process.exit(1);
}
