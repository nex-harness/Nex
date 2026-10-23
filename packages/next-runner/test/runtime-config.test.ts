import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync } from "node:fs";
import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { ModelRuntime } from "@earendil-works/pi-coding-agent";
import { InMemoryModelsStore } from "@earendil-works/pi-ai";
import { createLogger } from "../src/logger.js";
import { MemoryCredentialStore, RuntimeConfigManager } from "../src/config/applyRuntimeConfig.js";
import { createServerRuntime } from "../src/config/credentials.js";
import { buildRuntimeConfig, envVarFor, validateFrame } from "../src/config/runtimeConfig.js";
import { configDigest, modelsJsonFor } from "../src/config/modelsJson.js";
import { exportLocal } from "../src/channels/configChannel.js";
import { ConfigExecutor } from "../src/config/configAck.js";

/**
 * Server-owned config (docs/next/config-mcp-skills.md P0): apply/rollback,
 * fallback vs server-owned, running sessions keep their runtime, export.
 *
 * The runtime is the REAL pi ModelRuntime (an in-memory credential store and
 * models store), so the tests prove the derived models.json + `setRuntimeApiKey`
 * path pi actually uses.
 */

const logger = createLogger("error");

function providerFrame(version: number, options: { enabled?: boolean; key?: string | null } = {}) {
  const key = options.key === undefined ? "sk-live-secret" : options.key;
  return {
    version,
    doc: {
      schema: 1,
      version,
      owned: true,
      providers: [
        {
          id: "acme",
          name: "Acme",
          api: "openai-completions",
          baseUrl: "https://api.acme.test/v1",
          enabled: options.enabled ?? true,
          models: [
            {
              id: "acme-large",
              name: "Acme Large",
              reasoning: true,
              reasoningLevels: ["off", "low", "high"],
              contextWindow: 32000,
              maxTokens: 2048,
              input: ["text"],
              enabled: true,
            },
            {
              id: "acme-small",
              reasoning: false,
              reasoningLevels: ["off"],
              contextWindow: 8000,
              maxTokens: 1024,
              enabled: true,
            },
          ],
        },
      ],
      settings: { defaultThinkingLevel: "low" },
    },
    secrets: key === null ? {} : { sec_prov_acme_apikey: key },
  };
}

function managerFor(root: string): RuntimeConfigManager {
  return new RuntimeConfigManager({
    dataDir: root,
    logger,
    createRuntime: (config, modelsPath) =>
      config.mode === "server"
        ? createServerRuntime(config, modelsPath)
        : ModelRuntime.create({ refreshOnCreate: false }),
  });
}

test("validateFrame rejects a broken envelope and keeps the previous config", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-runtime-config-"));
  const manager = managerFor(root);
  const first = await manager.apply(providerFrame(3));
  assert.equal(first.ok, true, JSON.stringify(first.errors));
  assert.equal(manager.appliedVersion, 3);

  const rejected = await manager.apply({ version: 4, doc: { schema: 99 }, secrets: {} });
  assert.equal(rejected.ok, false);
  assert.equal(manager.appliedVersion, 3, "a rejected doc must not advance the applied version");
  assert.equal(manager.current.config.version, 3, "the previous generation stays live");
});

test("server-owned mode: derived models.json without credentials, in-memory key", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-runtime-config-"));
  const manager = managerFor(root);
  const result = await manager.apply(providerFrame(5, { key: "sk-live-secret" }));
  assert.equal(result.ok, true, JSON.stringify(result.errors));
  assert.equal(manager.current.config.mode, "server");

  const modelsPath = join(root, "derived", "5", "models.json");
  const modelsJson = await readFile(modelsPath, "utf8");
  assert.ok(modelsJson.includes("acme-large"));
  assert.ok(
    modelsJson.includes(`$${envVarFor("acme")}`),
    "models.json references the key by env name",
  );
  assert.ok(!modelsJson.includes("sk-live-secret"), "models.json never carries the key");

  // The process env carries the key for pi's own tool subprocesses.
  assert.equal(process.env[envVarFor("acme")], "sk-live-secret");

  // pi knows the model and its credentials come from the in-memory store.
  const runtime = manager.runtime();
  assert.ok(runtime, "server mode builds a runtime");
  const model = runtime.getModel("acme", "acme-large");
  assert.ok(model, "derived model is registered");
  assert.deepEqual(model.reasoning, true);
  assert.equal(runtime.getProviderAuthStatus("acme").configured, true);
  assert.equal(runtime.getProviderAuthStatus("acme").source, "runtime");

  // No auth.json anywhere in the data dir or the user's agent dir.
  assert.equal(existsSync(join(root, "auth.json")), false);

  // Facts mirror the doc's models with their real level lists.
  const facts = manager.facts() as {
    mode: string;
    modelsByProvider: Record<
      string,
      { configured: boolean; models: Array<{ modelId: string; reasoningLevels: string[] }> }
    >;
  };
  assert.equal(facts.mode, "server");
  assert.equal(facts.modelsByProvider["acme"]?.configured, true);
  const levels = facts.modelsByProvider["acme"]?.models.find(
    (item) => item.modelId === "acme-large",
  )?.reasoningLevels;
  assert.deepEqual(levels, ["off", "low", "high"]);

  // The applied cache has the version and mode but no secret.
  const cache = JSON.parse(await readFile(join(root, "applied-config.json"), "utf8"));
  assert.equal(cache.version, 5);
  assert.equal(cache.mode, "server");
  assert.ok(!JSON.stringify(cache).includes("sk-live-secret"));

  // A key that the server withheld becomes a fact, never a failed apply.
  const withoutKey = await manager.apply(providerFrame(6, { key: null }));
  assert.equal(withoutKey.ok, true);
  assert.equal(withoutKey.errors[0]?.code, "secret-not-provisioned");
  const factsWithout = manager.facts() as {
    modelsByProvider: Record<string, { configured: boolean }>;
  };
  assert.equal(factsWithout.modelsByProvider["acme"]?.configured, false);
});

test("an empty doc falls back to the local ~/.pi/agent mode", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-runtime-config-"));
  let fallbackCreations = 0;
  const manager = new RuntimeConfigManager({
    dataDir: root,
    logger,
    createRuntime: async (config) => {
      if (config.mode === "fallback") fallbackCreations += 1;
      return ModelRuntime.create({ refreshOnCreate: false });
    },
  });
  const result = await manager.apply({
    version: 1,
    doc: { schema: 1, version: 1, owned: false, providers: [], settings: {} },
    secrets: {},
  });
  assert.equal(result.ok, true);
  assert.equal(manager.current.config.mode, "fallback");
  assert.equal(fallbackCreations, 1);
  // Back to server mode, then to fallback again.
  await manager.apply(providerFrame(2));
  assert.equal(manager.current.config.mode, "server");
  await manager.apply({
    version: 3,
    doc: { schema: 1, version: 3, owned: false, providers: [], settings: {} },
    secrets: {},
  });
  assert.equal(manager.current.config.mode, "fallback");
  assert.equal(fallbackCreations, 2);
});

test("running sessions keep their generation while new ones get the latest", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-runtime-config-"));
  const manager = managerFor(root);
  await manager.apply(providerFrame(1, { key: "sk-first" }));
  const firstRuntime = manager.runtime();
  await manager.apply(providerFrame(2, { key: "sk-second" }));
  const secondRuntime = manager.runtime();
  assert.notEqual(firstRuntime, secondRuntime, "each version gets its own runtime object");
  // The first runtime keeps working with the key it was built with; the env
  // var now carries the newest value for both (process-wide, newest wins).
  assert.ok(firstRuntime?.getModel("acme", "acme-large"));
  assert.equal(process.env[envVarFor("acme")], "sk-second");
});

test("fallback mode facts report the runner's own catalog (decision 2)", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-runtime-config-"));
  const manager = managerFor(root);
  // No providers: the runner keeps ~/.pi/agent and the server compiles the
  // composer's view from these facts.
  const empty = {
    version: 3,
    doc: { schema: 1, version: 3, owned: true, providers: [] },
    secrets: {},
  };
  assert.equal((await manager.apply(empty)).facts["mode"], "fallback");
  const executor = new ConfigExecutor({
    manager,
    logger,
    catalog: async () => ({
      defaultSelection: null,
      models: [
        {
          providerId: "faux",
          providerName: "faux",
          modelId: "faux-1",
          modelName: "Faux 1",
          apiType: "openai-chat-completions",
          baseUrl: "http://127.0.0.1:18080/v1",
          reasoningLevels: ["off", "high"],
          maxOutputTokens: 8192,
          contextWindow: 128000,
          supportsImage: true,
          supportsVideo: false,
          supportsAudio: false,
          supportsPdf: false,
        },
      ],
    }),
    providerConfigured: (providerId) => providerId === "faux",
  });
  const result = await executor.apply({
    version: 4,
    doc: { schema: 1, owned: false, providers: [] },
    secrets: {},
  });
  assert.equal(result.ok, true);
  assert.equal(result.facts["mode"], "fallback");
  const providers = result.facts["modelsByProvider"] as Record<
    string,
    { configured: boolean; models: Array<Record<string, unknown>> }
  >;
  assert.equal(providers["faux"]?.configured, true);
  assert.equal(providers["faux"]?.models[0]?.modelId, "faux-1");
  assert.deepEqual(providers["faux"]?.models[0]?.reasoningLevels, ["off", "high"]);
  assert.deepEqual(providers["faux"]?.models[0]?.input, ["text", "image"]);
  const wire = JSON.stringify(result.facts);
  assert.ok(!wire.includes("sk-"), "facts never carry a credential");
});
test("models.json derivation and digest are stable", () => {
  const frame = validateFrame(providerFrame(7));
  const config = buildRuntimeConfig(frame, logger);
  const document = modelsJsonFor(config);
  const provider = document.providers["acme"] as {
    apiKey: string;
    models: Array<{ id: string; reasoning: boolean }>;
  };
  assert.equal(provider.apiKey, `$${envVarFor("acme")}`);
  assert.deepEqual(
    provider.models.map((model) => model.id),
    ["acme-large", "acme-small"],
  );
  assert.equal(provider.models[0]?.reasoning, true);
  assert.equal(
    configDigest(config),
    configDigest(buildRuntimeConfig(validateFrame(providerFrame(7)), logger)),
  );
});

test("exportLocal reads models.json keys, settings and mcp.json", async () => {
  const agentDir = await mkdtemp(join(tmpdir(), "nex-agent-"));
  await mkdir(agentDir, { recursive: true });
  await writeFile(
    join(agentDir, "models.json"),
    JSON.stringify({
      providers: {
        local: {
          name: "Local",
          api: "anthropic-messages",
          baseUrl: "https://api.anthropic.com",
          apiKey: "$ANTHROPIC_API_KEY",
          models: [
            {
              id: "claude-test",
              name: "Claude Test",
              reasoning: true,
              contextWindow: 200000,
              maxTokens: 8192,
            },
          ],
        },
      },
    }),
  );
  await writeFile(
    join(agentDir, "auth.json"),
    JSON.stringify({
      local: { type: "api_key", key: "sk-local-key" },
      other: { type: "oauth", access: "x" },
    }),
  );
  await writeFile(
    join(agentDir, "settings.json"),
    JSON.stringify({ defaultProvider: "local", defaultModel: "claude-test", theme: "x" }),
  );
  await writeFile(
    join(agentDir, "mcp.json"),
    JSON.stringify({ mcpServers: { demo: { command: "echo" } } }),
  );
  const exported = (await exportLocal(agentDir, logger)) as {
    providers: Record<
      string,
      { api: string; models: Array<{ id: string; reasoningLevels: string[] }> }
    >;
    keys: Record<string, string>;
    settings: Record<string, unknown>;
    mcp: unknown;
  };
  assert.equal(exported.providers["local"]?.api, "anthropic-messages");
  assert.deepEqual(exported.providers["local"]?.models[0]?.reasoningLevels, [
    "off",
    "minimal",
    "low",
    "medium",
    "high",
  ]);
  assert.deepEqual(exported.keys, { local: "sk-local-key" });
  assert.deepEqual(exported.settings, { defaultProvider: "local", defaultModel: "claude-test" });
  assert.equal((exported.mcp as { mcpServers: unknown }).mcpServers !== undefined, true);
});

test("MemoryCredentialStore never exposes values through list()", async () => {
  const store = new MemoryCredentialStore();
  await store.modify("p", async () => ({ type: "api_key", key: "sk" }));
  assert.deepEqual(await store.list(), [{ providerId: "p", type: "api_key" }]);
  assert.deepEqual(await store.read("p"), { type: "api_key", key: "sk" });
  await store.delete("p");
  assert.equal(await store.read("p"), undefined);
  // InMemoryModelsStore keeps the remote catalog cache out of the data dir.
  const modelsStore = new InMemoryModelsStore();
  await modelsStore.write("p", { models: [] });
  assert.deepEqual(await modelsStore.read("p"), { models: [] });
});
