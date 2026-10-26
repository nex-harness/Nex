import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile } from "node:fs/promises";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
  fauxAssistantMessage,
  fauxToolCall,
  registerFauxProvider,
  type FauxProviderRegistration,
} from "@earendil-works/pi-ai/compat";
import { ModelRuntime } from "@earendil-works/pi-coding-agent";
import {
  nexMcpServerStatusSnapshotSchema,
  nexSkillsReferenceCatalogResultSchema,
  nexWorkspacePresentationSchema,
} from "@nex/shared";
import { PiEngine } from "../src/engine/pi/piEngine.js";
import { createLogger } from "../src/logger.js";
import { ChannelRegistry } from "../src/link/registry.js";
import { registerChannels } from "../src/channels/register.js";
import { ConversationPublisher } from "../src/v4/publisher.js";
import { RuntimeConfigManager } from "../src/config/applyRuntimeConfig.js";
import { digestFiles, materializeBundles } from "../src/config/bundles.js";
import { buildMcpGeneration, docOSVariant } from "../src/config/mcpConfig.js";
import type { EngineEvent } from "../src/engine/types.js";

/**
 * P1/P2 against the REAL pi engine (docs/next/config-mcp-skills.md):
 *
 * - a server-owned stdio MCP server (the repo's `mcp-echo` fixture) connects for
 *   a new session; the model calls `echo` and the tool result reaches the turn;
 * - `mcp-sync.listWorkspaceMcpServerStatuses` reports the live status and tool
 *   count, and the facts carry the same list;
 * - a skill bundle materializes with a verified digest, appears in the
 *   `getSkillReferenceCatalog` result and the `/` menu, and `/skill:<name>`
 *   expands into pi's `<skill …>` block in the prompt the model receives;
 * - a corrupted bundle is refused (`bundle-integrity`) while the rest applies.
 */

const logger = createLogger("error");
// Isolate pi's user-scope discovery (skills, prompts, models) from the
// developer's ~/.pi/agent: an empty agent dir makes the assertions about the
// doc's resources exact.
const AGENT_DIR = mkdtempSync(join(tmpdir(), "nex-mcp-skills-agent-"));
process.env["PI_CODING_AGENT_DIR"] = AGENT_DIR;
const HERE = dirname(fileURLToPath(import.meta.url));
const ECHO_SERVER = join(HERE, "fixtures", "mcp-echo", "server.mjs");
const fauxs: FauxProviderRegistration[] = [];

function currentFaux(): FauxProviderRegistration {
  const faux = fauxs.at(-1);
  if (!faux) throw new Error("no faux provider registered");
  return faux;
}

async function makeRuntime(): Promise<ModelRuntime> {
  const faux = registerFauxProvider({
    models: [{ id: "faux-1", name: "Faux", contextWindow: 200_000 }],
  });
  const model = faux.getModel();
  const runtime = await ModelRuntime.create({ refreshOnCreate: false });
  runtime.registerProvider(model.provider, {
    baseUrl: model.baseUrl,
    api: faux.api,
    apiKey: "faux-key",
    models: faux.models.map((registered) => ({
      id: registered.id,
      name: registered.name,
      api: registered.api,
      reasoning: registered.reasoning,
      input: registered.input,
      cost: registered.cost,
      contextWindow: registered.contextWindow,
      maxTokens: registered.maxTokens,
      baseUrl: registered.baseUrl,
    })),
  });
  fauxs.push(faux);
  return runtime;
}

async function waitFor<T>(
  read: () => T | undefined,
  label: string,
  timeoutMs = 20_000,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = read();
    if (value !== undefined) return value;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error(`timed out waiting for ${label}`);
}

async function waitForIdle(engine: PiEngine, sessionId: string): Promise<void> {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if (engine.runState(sessionId).idle) return;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error("session did not become idle");
}

const SKILL_MD = `---
name: echo-helper
description: Explains how to use the echo tool.
---

# Echo helper

When asked to greet, call the echo tool with the greeting text.
`;

function skillBundle(name = "echo-helper", content = SKILL_MD) {
  const files = [{ path: "SKILL.md", content: content.replace("echo-helper", name) }];
  return { name, kind: "skill", digest: digestFiles(files), files };
}

function frameFor(options: {
  version: number;
  mcp?: unknown[];
  bundles?: unknown[];
  skills?: Array<{ name: string; digest: string; kind: string; enabled: boolean }>;
}) {
  return {
    version: options.version,
    doc: {
      schema: 1,
      version: options.version,
      // No server providers: providers stay in fallback, MCP/skills are owned.
      owned: false,
      providers: [],
      settings: {},
      ...(options.mcp ? { mcp: options.mcp, mcpOwned: true } : {}),
      ...(options.skills ? { skills: options.skills } : {}),
    },
    secrets: {},
    bundles: options.bundles ?? [],
  };
}

test("P1: the doc's stdio MCP server connects, the model calls echo, statuses report it", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-mcp-pi-"));
  const workspace = join(root, "workspace");
  await mkdir(workspace, { recursive: true });
  const runtimeConfig = new RuntimeConfigManager({ dataDir: join(root, "data"), logger });
  const engine = new PiEngine({
    dataDir: join(root, "data"),
    logger,
    runtimeConfig,
    createModelRuntime: () => makeRuntime(),
    titleSettings: { enabled: false },
  });
  await engine.init();
  try {
    const faux = currentFaux();
    // The model: one turn with the MCP tool, then a closing sentence.
    faux.setResponses([
      fauxAssistantMessage([fauxToolCall("mcp__echo_server__echo", { text: "pong from mcp" })]),
      fauxAssistantMessage("the echo tool answered"),
    ]);
    const applied = await runtimeConfig.apply(
      frameFor({
        version: 1,
        mcp: [
          {
            name: "echo-server",
            enabled: true,
            exposure: "direct",
            transport: { kind: "stdio", command: process.execPath, args: [ECHO_SERVER] },
          },
        ],
      }),
    );
    assert.equal(applied.ok, true, JSON.stringify(applied.errors));
    assert.equal(applied.facts["mcpOwned"], true);

    const registry = new ChannelRegistry();
    const published: Array<{ sessionId: string; payload: unknown }> = [];
    const publisher = new ConversationPublisher({
      engine,
      logger,
      attachments: undefined as never,
    });
    void publisher;
    registerChannels(registry, {
      engine,
      publisher: {
        subscribe: (sessionId: string, listener: (payload: unknown) => void) => {
          void listener;
          return () => undefined;
        },
        sessions: { isWatched: () => false },
        attachmentRegistry: () => undefined,
      } as never,
      logger,
      workspacePaths: [workspace],
    });
    void published;

    engine.adoptRuntimeConfig();
    // Register the faux model on the engine's fallback runtime (the engine holds
    // the runtime created at init, not this manager's later generation).
    const fauxModel = faux.getModel();
    const engineRuntime = engine.requireRuntime();
    engineRuntime.registerProvider(fauxModel.provider, {
      baseUrl: fauxModel.baseUrl,
      api: faux.api,
      apiKey: "faux-key",
      models: faux.models.map((registered) => ({
        id: registered.id,
        name: registered.name,
        api: registered.api,
        reasoning: registered.reasoning,
        input: registered.input,
        cost: registered.cost,
        contextWindow: registered.contextWindow,
        maxTokens: registered.maxTokens,
        baseUrl: registered.baseUrl,
      })),
    });
    const session = await engine.createSession(workspace, {
      modelSelection: {
        providerId: fauxModel.provider,
        modelId: "faux-1",
        options: { reasoningLevel: "off" },
      },
    });
    const events: EngineEvent[] = [];
    engine.subscribe(session.sessionId, (event: EngineEvent) => {
      events.push(event);
      // The gate's default asks before a tool call; this test approves every call.
      if (event.type === "approval_request") {
        const approval = (event as unknown as { approval: { interactionId: string } }).approval;
        void engine.resolveApproval(session.sessionId, approval.interactionId, {
          optionId: "allowOnce",
        });
      }
    });
    await engine.submit(session.sessionId, "call the echo tool", { requestId: "req-mcp" });

    const status = await waitFor(() => {
      const snapshot = engine.refreshMcpStatus();
      return snapshot.statuses["echo-server"]?.state === "connected" &&
        (snapshot.statuses["echo-server"]?.toolCount ?? 0) > 0
        ? snapshot
        : undefined;
    }, "mcp connected with tools");
    assert.equal(status.statuses["echo-server"]?.toolCount, 1);
    assert.deepEqual(status.statuses["echo-server"]?.toolNames, ["mcp__echo_server__echo"]);

    await waitForIdle(engine, session.sessionId);
    await waitFor(
      () =>
        events.some((event) => JSON.stringify(event).includes("pong from mcp")) ? true : undefined,
      "echo tool result",
    );

    // The channel the server forwards for the settings list.
    const call = registry.getCall("mcp-sync", "listWorkspaceMcpServerStatuses");
    assert.ok(call, "mcp-sync status method registered");
    const answered = (await call?.({ workspacePath: workspace })) as {
      statuses: Record<string, unknown>;
    };
    assert.ok(answered.statuses["echo-server"], JSON.stringify(answered));
    // 回归：浏览器按共享协议 schema 严格校验整条结果；曾经直接返回引擎的
    // `{state,…}`，缺少 `status`/`updatedAt`，导致 Settings 里所有服务器都
    // 显示 “Unable to retrieve MCP status”。
    for (const [name, row] of Object.entries(answered.statuses)) {
      const parsed = nexMcpServerStatusSnapshotSchema.safeParse(row);
      assert.ok(parsed.success, `${name}: ${JSON.stringify(parsed.error?.issues ?? row)}`);
      if (parsed.success) {
        assert.equal(parsed.data.status, "connected");
        assert.equal(parsed.data.toolCount, 1);
      }
    }

    // Facts carry the same list.
    const facts = runtimeConfig.appliedResult?.facts["mcp"] as Array<{ name: string }>;
    assert.ok(facts.some((entry) => entry.name === "echo-server"));
  } finally {
    await engine.dispose();
    await runtimeConfig.dispose();
  }
});

test("P1: a draft created before the doc's MCP push uses it on its first turn", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-mcp-draft-"));
  const workspace = join(root, "workspace");
  await mkdir(workspace, { recursive: true });
  const runtimeConfig = new RuntimeConfigManager({ dataDir: join(root, "data"), logger });
  const engine = new PiEngine({
    dataDir: join(root, "data"),
    logger,
    runtimeConfig,
    createModelRuntime: () => makeRuntime(),
    titleSettings: { enabled: false },
  });
  await engine.init();
  try {
    const faux = currentFaux();
    faux.setResponses([
      fauxAssistantMessage([fauxToolCall("mcp__echo_server__echo", { text: "pong from draft" })]),
      fauxAssistantMessage("the echo tool answered"),
    ]);
    // No MCP in the doc yet: the draft is built on pi's own discovery.
    await runtimeConfig.apply(frameFor({ version: 1 }));
    engine.adoptRuntimeConfig();
    // Each config generation owns its model runtime; the faux provider is registered on
    // every generation the test uses (the draft starts on v1, its rebuild runs on v2).
    const registerFaux = () => {
      const fauxModel = faux.getModel();
      const engineRuntime = engine.requireRuntime();
      engineRuntime.registerProvider(fauxModel.provider, {
        baseUrl: fauxModel.baseUrl,
        api: faux.api,
        apiKey: "faux-key",
        models: faux.models.map((registered) => ({
          id: registered.id,
          name: registered.name,
          api: registered.api,
          reasoning: registered.reasoning,
          input: registered.input,
          cost: registered.cost,
          contextWindow: registered.contextWindow,
          maxTokens: registered.maxTokens,
          baseUrl: registered.baseUrl,
        })),
      });
    };
    registerFaux();
    const draft = await engine.createSession(workspace, {
      modelSelection: {
        providerId: faux.getModel().provider,
        modelId: "faux-1",
        options: { reasoningLevel: "off" },
      },
    });
    // The user adds the server in Settings while the draft is open.
    const applied = await runtimeConfig.apply(
      frameFor({
        version: 2,
        mcp: [
          {
            name: "echo-server",
            enabled: true,
            exposure: "direct",
            transport: { kind: "stdio", command: process.execPath, args: [ECHO_SERVER] },
          },
        ],
      }),
    );
    assert.equal(applied.ok, true, JSON.stringify(applied.errors));
    engine.adoptRuntimeConfig();
    registerFaux();

    // 回归：pi 在 session_start 读取 MCP 列表；draft 早于 push 创建时会一直用旧
    // 列表（这里是没有 MCP 的默认发现），新服务器要等下一次新开会话才连上。
    // 未开始的 draft 在第一次提交前用同一会话文件重建。
    // 回归：订阅发生在重建之前（UI 打开 draft 时就订阅），重建后的事件必须仍送达这个订阅者，
    // 否则聊天视图停在欢迎页。
    const seen: string[] = [];
    engine.subscribe(draft.sessionId, (event) => {
      seen.push(event.type);
      if (event.type === "approval_request") {
        const approval = (event as unknown as { approval: { interactionId: string } }).approval;
        void engine.resolveApproval(draft.sessionId, approval.interactionId, {
          optionId: "allowOnce",
        });
      }
    });
    await engine.submit(draft.sessionId, "call the echo tool", { requestId: "req-draft" });
    const connected = await waitFor(() => {
      const status = engine.refreshMcpStatus().statuses["echo-server"];
      return status?.state === "connected" && status.toolCount > 0 ? status : undefined;
    }, "draft session connected to the pushed MCP server");
    assert.equal(connected.toolCount, 1);
    // The rebuilt turn starts asynchronously; wait for its own settle, not just "idle".
    await waitFor(() => (seen.includes("settled") ? true : undefined), "rebuilt turn settled");
    await waitForIdle(engine, draft.sessionId);
    assert.ok(
      seen.includes("tool_call_start") && seen.includes("settled"),
      `subscriber kept across the rebuild; saw ${[...new Set(seen)].join(",")}`,
    );
  } finally {
    await engine.dispose();
    await runtimeConfig.dispose();
  }
});

test("P1: status connects before any chat; a re-applied doc keeps the current draft", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-mcp-status-"));
  const workspace = join(root, "workspace");
  await mkdir(workspace, { recursive: true });
  const runtimeConfig = new RuntimeConfigManager({ dataDir: join(root, "data"), logger });
  const engine = new PiEngine({
    dataDir: join(root, "data"),
    logger,
    runtimeConfig,
    createModelRuntime: () => makeRuntime(),
    titleSettings: { enabled: false },
    workspacePaths: [workspace],
  });
  await engine.init();
  const echoFrame = (version: number) =>
    frameFor({
      version,
      mcp: [
        {
          name: "echo-server",
          enabled: true,
          exposure: "direct",
          transport: { kind: "stdio", command: process.execPath, args: [ECHO_SERVER] },
        },
      ],
    });
  try {
    // 回归（开源问题 a）：此前没有会话就没有 tracker，状态查询对文档里的服务器一律返回
    // "connecting"/0 工具，设置页在第一次聊天之前永远连不上。隐藏的状态会话按生效列表
    // 连接一次，状态查询就能给出真实结果。
    await runtimeConfig.apply(echoFrame(1));
    engine.adoptRuntimeConfig();
    const before = await engine.requestMcpStatus();
    assert.equal(before.statuses["echo-server"]?.state, "connected");
    assert.equal(before.statuses["echo-server"]?.toolCount, 1);

    // 回归（开源问题 b）：同一份 MCP 列表被再次 push 时 `owned` 不变，draft 若只比较
    // `owned` 就不会再被换成当前列表；第二次提交曾因此落在已关闭的会话上（send_failed）。
    const faux = currentFaux();
    faux.setResponses([
      fauxAssistantMessage("first turn ok"),
      fauxAssistantMessage("second turn ok"),
    ]);
    const fauxModel = faux.getModel();
    engine.requireRuntime().registerProvider(fauxModel.provider, {
      baseUrl: fauxModel.baseUrl,
      api: faux.api,
      apiKey: "faux-key",
      models: faux.models.map((registered) => ({
        id: registered.id,
        name: registered.name,
        api: registered.api,
        reasoning: registered.reasoning,
        input: registered.input,
        cost: registered.cost,
        contextWindow: registered.contextWindow,
        maxTokens: registered.maxTokens,
        baseUrl: registered.baseUrl,
      })),
    });
    const draft = await engine.createSession(workspace, {
      modelSelection: {
        providerId: fauxModel.provider,
        modelId: "faux-1",
        options: { reasoningLevel: "off" },
      },
    });
    // A second push of the same list (the server re-sends after an edit elsewhere).
    await runtimeConfig.apply(echoFrame(2));
    engine.adoptRuntimeConfig();
    // pi writes the draft's session file in the background; the rebuild happens once
    // that start finished (the E2E's draft is seconds old when its first send lands).
    await new Promise((resolve) => setTimeout(resolve, 500));
    const first = await engine.submit(draft.sessionId, "first", { requestId: "req-1" });
    assert.equal(first.status, "accepted");
    await waitForIdle(engine, draft.sessionId);
    const second = await engine.submit(draft.sessionId, "second", { requestId: "req-2" });
    assert.equal(second.status, "accepted");
    await waitForIdle(engine, draft.sessionId);
    assert.equal(engine.runState(draft.sessionId).idle, true);
  } finally {
    await engine.dispose();
    await runtimeConfig.dispose();
  }
});

test("P2: a skill bundle materializes, appears in the catalog and the / menu, and /skill: expands", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-skills-pi-"));
  const workspace = join(root, "workspace");
  await mkdir(workspace, { recursive: true });
  const runtimeConfig = new RuntimeConfigManager({ dataDir: join(root, "data"), logger });
  const engine = new PiEngine({
    dataDir: join(root, "data"),
    logger,
    runtimeConfig,
    createModelRuntime: () => makeRuntime(),
    titleSettings: { enabled: false },
  });
  await engine.init();
  try {
    const faux = currentFaux();
    const prompt = "Greet the user.";
    const scripted = { text: "greeted" };
    const bundle = skillBundle();
    const promptTemplate = {
      name: "review",
      kind: "prompt",
      digest: digestFiles([{ path: "review.md", content: "Review this diff carefully.\n" }]),
      files: [{ path: "review.md", content: "Review this diff carefully.\n" }],
    };
    const applied = await runtimeConfig.apply(
      frameFor({
        version: 1,
        bundles: [bundle, promptTemplate],
        skills: [{ name: bundle.name, digest: bundle.digest, kind: "skill", enabled: true }],
      }),
    );
    assert.equal(applied.ok, true, JSON.stringify(applied.errors));

    // Materialized with a verified digest and a real SKILL.md.
    const bundleDir = join(root, "data", "bundles", bundle.digest.replace("sha256:", ""));
    const materialized = await readFile(
      join(bundleDir, "files", "echo-helper", "SKILL.md"),
      "utf8",
    );
    assert.equal(materialized, SKILL_MD);

    engine.adoptRuntimeConfig();
    const registry = new ChannelRegistry();
    registerChannels(registry, {
      engine,
      publisher: {
        subscribe: () => () => undefined,
        sessions: { isWatched: () => false },
        attachmentRegistry: () => undefined,
      } as never,
      logger,
      workspacePaths: [workspace],
    });

    const catalogCall = registry.getCall("nex-agent", "getSkillReferenceCatalog");
    const catalog = nexSkillsReferenceCatalogResultSchema.parse(
      await catalogCall?.({ workspacePath: workspace }),
    );
    assert.ok(
      catalog.skills.some((skill) => skill.name === "echo-helper"),
      JSON.stringify(catalog.skills),
    );

    const presentationCall = registry.getCall("nex-session", "readWorkspacePresentation");
    const presentation = nexWorkspacePresentationSchema.parse(
      await presentationCall?.({ workspacePath: workspace }),
    );
    const names = (presentation.slashCommands ?? []).map((command) => command.name);
    assert.ok(names.includes("skill:echo-helper"), names.join(","));
    assert.ok(
      names.includes("review"),
      `names=${names.join(",")} err=${JSON.stringify(presentation)}`,
    );

    // `/skill:echo-helper` reaches the model as pi's expanded skill block: the
    // faux step sees the request transcript (its last user message).
    const promptsSeen: string[] = [];
    faux.setResponses([
      (context: { messages?: Array<{ role: string; content: unknown }> }) => {
        faux.setResponses([fauxAssistantMessage(scripted.text)]);
        const users = (context.messages ?? []).filter((message) => message.role === "user");
        const content = users.at(-1)?.content;
        promptsSeen.push(
          typeof content === "string"
            ? content
            : Array.isArray(content)
              ? content.map((block) => (block as { text?: string }).text ?? "").join("\n")
              : JSON.stringify(content ?? ""),
        );
        return fauxAssistantMessage(scripted.text);
      },
    ]);
    // The engine holds the runtime created at init; register the faux model on it
    // (the server-owned runtime this test built is only consulted in server mode).
    const engineCatalog = await engine.catalog();
    const fauxModel = faux.getModel();
    await engine.requireRuntime().registerProvider(fauxModel.provider, {
      baseUrl: fauxModel.baseUrl,
      api: faux.api,
      apiKey: "faux-key",
      models: faux.models.map((registered) => ({
        id: registered.id,
        name: registered.name,
        api: registered.api,
        reasoning: registered.reasoning,
        input: registered.input,
        cost: registered.cost,
        contextWindow: registered.contextWindow,
        maxTokens: registered.maxTokens,
        baseUrl: registered.baseUrl,
      })),
    });
    const modelSelection = engineCatalog.models.find(
      (model) => model.providerId === fauxModel.provider,
    );
    const session = await engine.createSession(workspace, {
      ...(modelSelection
        ? {
            modelSelection: {
              providerId: modelSelection.providerId,
              modelId: modelSelection.modelId,
              options: { reasoningLevel: "off" },
            },
          }
        : (undefined as {} | undefined)),
    });
    await engine.submit(session.sessionId, `/skill:echo-helper ${prompt}`, {
      requestId: "req-skill",
    });
    await waitFor(() => (promptsSeen.length > 0 ? true : undefined), "a model request");
    const received = promptsSeen.join("\n");
    assert.ok(
      received.includes(`<skill name="echo-helper"`) && received.includes(prompt),
      `prompt carried the expanded skill: ${received.slice(0, 800)}`,
    );
  } finally {
    await engine.dispose();
    await runtimeConfig.dispose();
  }
});

test("P2: a corrupted bundle is refused with bundle-integrity and nothing is written", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-bundle-bad-"));
  const good = skillBundle("good-skill");
  const bad = { ...skillBundle("bad-skill"), digest: good.digest };
  const result = await materializeBundles(join(root, "data"), [good, bad]);
  assert.deepEqual(
    result.issues.map((issue) => issue.code),
    ["bundle-integrity"],
  );
  assert.equal(result.skillPaths.length, 1);
  assert.ok(result.skillPaths[0]?.endsWith(join("good-skill")));
});

test("P1: per-OS variants pick this runner's command and disabled servers never connect", () => {
  const generation = buildMcpGeneration(
    [
      {
        name: "docs",
        enabled: true,
        transport: {
          kind: "stdio",
          command: "npx",
          args: ["server"],
          variants: { darwin: { command: "/opt/homebrew/bin/npx" } },
        },
      },
      { name: "off", enabled: false, transport: { kind: "stdio", command: "x" } },
      { name: "bad", enabled: true, transport: { kind: "http", url: "ftp://nope" } },
    ],
    { os: docOSVariant("darwin") },
  );
  assert.equal(generation.owned, true);
  assert.deepEqual(
    generation.servers.map((server) => server.name),
    ["docs"],
  );
  assert.equal(
    (generation.servers[0]?.config as { command?: string } | undefined)?.command,
    "/opt/homebrew/bin/npx",
  );
  assert.deepEqual(
    generation.issues.map((issue) => issue.code),
    ["mcp-config-invalid"],
  );
  assert.equal(docOSVariant("linux"), "linux");
  assert.equal(docOSVariant("win32"), "win32");
});

test("P1 fallback: no MCP entry in the doc keeps pi's own discovery", () => {
  const generation = buildMcpGeneration(undefined);
  assert.equal(generation.owned, false);
  assert.equal(generation.servers.length, 0);
});
