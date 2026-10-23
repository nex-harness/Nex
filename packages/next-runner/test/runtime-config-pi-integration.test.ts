import { test } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import { mkdtemp, mkdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { ModelRuntime } from "@earendil-works/pi-coding-agent";
import { createLogger } from "../src/logger.js";
import { PiEngine } from "../src/engine/pi/piEngine.js";
import { RuntimeConfigManager } from "../src/config/applyRuntimeConfig.js";
import type { EngineEvent } from "../src/engine/types.js";

/**
 * Real pi engine + server-owned config + fake OpenAI-compatible server:
 *
 * - the first `config` push makes new sessions run on the server's provider and
 *   its key (the fake server asserts the Authorization header);
 * - a session created BEFORE the push keeps its original runtime (fallback) and
 *   is not broken by the swap;
 * - a later push moves new sessions to the new version.
 */

const logger = createLogger("error");

interface FakeRequest {
  authorization: string;
  model: string;
}

async function startFakeServer(): Promise<{
  url: string;
  requests: FakeRequest[];
  close: () => Promise<void>;
}> {
  const requests: FakeRequest[] = [];
  const server = http.createServer((request, response) => {
    let body = "";
    request.on("data", (chunk) => {
      body += chunk;
    });
    request.on("end", () => {
      let payload: { model?: string; stream?: boolean } = {};
      try {
        payload = JSON.parse(body || "{}");
      } catch {
        payload = {};
      }
      requests.push({
        authorization: request.headers["authorization"] ?? "",
        model: payload.model ?? "",
      });
      if (payload.stream === true) {
        response.writeHead(200, {
          "content-type": "text/event-stream",
          "cache-control": "no-cache",
          connection: "keep-alive",
        });
        response.write(sseChunk({ role: "assistant", content: "server-owned reply" }));
        response.write(sseChunk({}, "stop"));
        response.write("data: [DONE]\n\n");
        response.end();
        return;
      }
      const reply = {
        id: `chatcmpl-${requests.length}`,
        object: "chat.completion",
        created: Math.floor(Date.now() / 1000),
        model: payload.model ?? "server-model",
        choices: [
          {
            index: 0,
            message: { role: "assistant", content: "server-owned reply" },
            finish_reason: "stop",
          },
        ],
        usage: { prompt_tokens: 3, completion_tokens: 3, total_tokens: 6 },
      };
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify(reply));
    });
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  assert.ok(address && typeof address === "object");
  return {
    url: `http://127.0.0.1:${address.port}/v1`,
    requests,
    close: () => new Promise<void>((resolve) => server.close(() => resolve())),
  };
}

/** One OpenAI SSE stream chunk (pi's openai-completions parser). */
function sseChunk(delta: Record<string, unknown>, finish: string | null = null): string {
  return `data: ${JSON.stringify({
    id: "chatcmpl-stream",
    object: "chat.completion.chunk",
    created: Math.floor(Date.now() / 1000),
    model: "server-model",
    choices: [{ index: 0, delta, finish_reason: finish }],
  })}\n\n`;
}

function frameFor(version: number, baseUrl: string, key: string) {
  return {
    version,
    doc: {
      schema: 1,
      version,
      owned: true,
      providers: [
        {
          id: "server-provider",
          name: "Server Provider",
          api: "openai-completions",
          baseUrl,
          enabled: true,
          models: [
            {
              id: "server-model",
              name: "Server Model",
              reasoning: false,
              reasoningLevels: ["off"],
              contextWindow: 16000,
              maxTokens: 1024,
              enabled: true,
            },
          ],
        },
      ],
      settings: {},
    },
    secrets: { "sec_prov_server-provider_apikey": key },
  };
}

async function waitForEvent(
  events: EngineEvent[],
  predicate: (event: EngineEvent) => boolean,
): Promise<void> {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    if (events.some(predicate)) return;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error(`event not observed: ${events.map((event) => event.type).join(",")}`);
}

async function waitForIdle(engine: PiEngine, sessionId: string): Promise<void> {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    if (engine.runState(sessionId).idle) return;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error("session did not become idle");
}

test("server-owned config drives new sessions and keeps running sessions intact", async () => {
  const fake = await startFakeServer();
  const root = await mkdtemp(join(tmpdir(), "nex-runtime-pi-"));
  const workspace = join(root, "workspace");
  await mkdir(workspace, { recursive: true });
  const runtimeConfig = new RuntimeConfigManager({ dataDir: join(root, "data"), logger });
  const engine = new PiEngine({
    dataDir: join(root, "data"),
    logger,
    runtimeConfig,
    // The fallback runtime is the "local ~/.pi/agent": an empty static runtime.
    createModelRuntime: () => ModelRuntime.create({ refreshOnCreate: false }),
    titleSettings: { enabled: false },
  });
  await engine.init();
  try {
    // A session created BEFORE the first push uses the fallback generation.
    const before = await engine.createSession(workspace, {});
    const beforeRuntime = engine.modelRuntime;
    const beforeCatalog = await engine.catalog();
    assert.ok(
      beforeCatalog.models.every((model) => model.providerId !== "server-provider"),
      "before the push the catalog is the local one",
    );

    const result = await runtimeConfig.apply(frameFor(1, fake.url, "sk-server-key"));
    assert.equal(result.ok, true, JSON.stringify(result.errors));
    engine.adoptRuntimeConfig();
    assert.notEqual(engine.modelRuntime, beforeRuntime, "new sessions get the server runtime");

    const catalog = await engine.catalog();
    const serverModel = catalog.models.find((model) => model.modelId === "server-model");
    assert.ok(serverModel, `catalog lists the server model: ${JSON.stringify(catalog.models)}`);
    assert.equal(serverModel.providerId, "server-provider");
    assert.deepEqual(catalog.defaultSelection?.modelId, "server-model");

    const events: EngineEvent[] = [];
    engine.subscribe(before.sessionId, (event: EngineEvent) => events.push(event));
    const created = await engine.createSession(workspace, {
      modelSelection: {
        providerId: "server-provider",
        modelId: "server-model",
        options: { reasoningLevel: "off" },
      },
    });
    const newSessionEvents: EngineEvent[] = [];
    engine.subscribe(created.sessionId, (event: EngineEvent) => newSessionEvents.push(event));
    await engine.submit(created.sessionId, "hello from the test", { requestId: "req-1" });
    await waitForIdle(engine, created.sessionId);
    // The run settles asynchronously after the stream ends; wait for `settled`.
    await waitForEvent(newSessionEvents, (event) => event.type === "settled");

    const chatRequests = () => fake.requests.filter((entry) => entry.model === "server-model");
    assert.ok(
      chatRequests().length >= 1,
      `the fake server received a chat request: ${JSON.stringify(fake.requests)}`,
    );
    assert.equal(chatRequests()[0]?.authorization, "Bearer sk-server-key");

    // The pre-swap session still holds its own (fallback) runtime object.
    assert.ok(beforeRuntime);
    assert.notEqual(runtimeConfig.runtime(), beforeRuntime);

    // A second version pushes a new key; new sessions use it.
    const second = await runtimeConfig.apply(frameFor(2, fake.url, "sk-second-key"));
    assert.equal(second.ok, true);
    engine.adoptRuntimeConfig();
    const third = await engine.createSession(workspace, {
      modelSelection: {
        providerId: "server-provider",
        modelId: "server-model",
        options: { reasoningLevel: "off" },
      },
    });
    await engine.submit(third.sessionId, "second turn", { requestId: "req-2" });
    await waitForIdle(engine, third.sessionId);
    assert.ok(chatRequests().some((entry) => entry.authorization === "Bearer sk-second-key"));
    // The transcript folded the streamed reply through the normal event path.
    const replyText = newSessionEvents
      .filter(
        (event): event is Extract<EngineEvent, { type: "text_delta" }> =>
          event.type === "text_delta",
      )
      .map((event) => event.delta)
      .join("");
    assert.ok(
      replyText.includes("server-owned reply"),
      `streamed reply: ${JSON.stringify(replyText)} events: ${JSON.stringify(newSessionEvents.map((event) => event.type))}`,
    );
  } finally {
    await engine.dispose();
    await runtimeConfig.dispose();
    await fake.close();
  }
});
