import { test } from "node:test";
import assert from "node:assert/strict";
import { nexTaskMetaSchema } from "@nex/shared";
import type { CommandEnvelope } from "@nex/shared/nex-protocol-v4";
import { CommandHandler } from "../src/v4/commands.js";
import { SessionRuntimeStore } from "../src/v4/sessionStore.js";
import { toNexTaskMeta } from "../src/channels/nexTaskRows.js";
import { createLogger } from "../src/logger.js";
import type { CreateEngineSessionOptions, EngineSessionState } from "../src/engine/types.js";
import { FakeEngine } from "./helpers/fakeEngine.js";

/**
 * automation 派发会话（新增）：`createSession.firstInput.automationId` 必须
 *  1. 原样传给 engine.createSession（无人值守判定的输入）；
 *  2. 出现在 task rows 的 `cronAutomationId` 上（侧栏 cron 徽章）。
 * 缺失/空白按普通用户会话处理。
 */

const WORKSPACE = "/workspace/demo";
const logger = createLogger("error");

function createEnvelope(firstInput?: Record<string, unknown>): CommandEnvelope {
  return {
    commandId: "cmd-create-1",
    sessionId: null,
    type: "createSession",
    payload: {
      workspaceId: WORKSPACE,
      ...(firstInput ? { firstInput } : {}),
    },
  } as unknown as CommandEnvelope;
}

async function createViaHandler(
  firstInput?: Record<string, unknown>,
): Promise<{ engine: FakeEngine; options: CreateEngineSessionOptions | undefined; ack: unknown }> {
  const base = new FakeEngine();
  // 记录 engine 收到的 createSession options，再走真实实现。
  let captured: CreateEngineSessionOptions | undefined;
  const original = base.createSession.bind(base);
  const createSession = async (workspacePath: string, options: CreateEngineSessionOptions) => {
    captured = options;
    return original(workspacePath, options);
  };
  const engine = Object.assign(base, {
    createSession,
  }) as FakeEngine;
  const sessions = new SessionRuntimeStore(engine, logger);
  const handler = new CommandHandler(engine, sessions, logger);
  const ack = await handler.handle(WORKSPACE, createEnvelope(firstInput));
  handler.dispose();
  sessions.dispose();
  return { engine, options: captured, ack };
}

test("createSession: automationId is forwarded to engine.createSession", async () => {
  const { options } = await createViaHandler({ text: "run", automationId: "auto_1" });
  assert.equal(options?.automationId, "auto_1");
});

test("createSession: missing or blank automationId stays an ordinary session", async () => {
  const missing = await createViaHandler({ text: "run" });
  assert.equal(missing.options?.automationId, undefined);
  const blank = await createViaHandler({ text: "run", automationId: "   " });
  assert.equal(blank.options?.automationId, undefined);
});

test("toNexTaskMeta: automation session outputs cronAutomationId and the schema accepts it", () => {
  const session: EngineSessionState = {
    sessionId: "sess-auto",
    workspacePath: WORKSPACE,
    title: "Nightly check",
    titleSource: "generated",
    createdAt: 1_700_000_000_000,
    lastActivityAt: 1_700_000_000_000,
    phase: "completedSuccess",
    model: null,
    usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    isRunning: false,
    automationId: "auto_1",
  };
  const meta = toNexTaskMeta(WORKSPACE, session);
  assert.equal(meta.cronAutomationId, "auto_1");
  nexTaskMetaSchema.parse(meta);
});

test("toNexTaskMeta: ordinary sessions omit cronAutomationId", () => {
  const session: EngineSessionState = {
    sessionId: "sess-user",
    workspacePath: WORKSPACE,
    title: "Chat",
    titleSource: "default",
    createdAt: 1_700_000_000_000,
    lastActivityAt: 1_700_000_000_000,
    phase: "completedSuccess",
    model: null,
    usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    isRunning: false,
  };
  const meta = toNexTaskMeta(WORKSPACE, session);
  assert.equal("cronAutomationId" in meta, false);
  nexTaskMetaSchema.parse(meta);
});

// ── 真实 pi 引擎链路（新增）：automationId → 元数据 → listSessions → cronAutomationId ──
test("pi integration: automationId reaches session metadata and the task row projection", async () => {
  const { mkdtemp, mkdir, rm } = await import("node:fs/promises");
  const { tmpdir } = await import("node:os");
  const { join } = await import("node:path");
  const { ModelRuntime, SettingsManager } = await import("@earendil-works/pi-coding-agent");
  const { registerFauxProvider } = await import("@earendil-works/pi-ai/compat");
  const { PiEngine } = await import("../src/engine/pi/piEngine.js");

  const root = await mkdtemp(join(tmpdir(), "nex-auto-session-"));
  const workspace = join(root, "workspace");
  await mkdir(workspace, { recursive: true });
  const faux = registerFauxProvider({ models: [{ id: "faux-1", name: "Faux" }] });
  const model = faux.getModel();
  const createModelRuntime = async () => {
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
    return runtime;
  };
  const engine = new PiEngine({
    dataDir: join(root, "data"),
    logger,
    createModelRuntime,
    createSettingsManager: () => SettingsManager.inMemory(),
    titleSettings: { enabled: false },
  });
  try {
    await engine.init();
    const { sessionId } = await engine.createSession(workspace, {
      modelSelection: { providerId: "faux", modelId: "faux-1", options: {} },
      automationId: "auto_pi_1",
    });
    const sessions = await engine.listSessions(workspace);
    const created = sessions.find((item) => item.sessionId === sessionId);
    assert.equal(created?.automationId, "auto_pi_1");
    // 侧栏行投影（draft 也会进入 all/pinned/archived 集合的输入；此处直接投影）
    const meta = toNexTaskMeta(workspace, created!);
    assert.equal(meta.cronAutomationId, "auto_pi_1");
    nexTaskMetaSchema.parse(meta);

    // 普通会话不携带 automationId。
    const plain = await engine.createSession(workspace, {});
    const plainState = (await engine.listSessions(workspace)).find(
      (item) => item.sessionId === plain.sessionId,
    );
    assert.equal(plainState?.automationId, undefined);
  } finally {
    await engine.dispose();
    faux.unregister();
    await rm(root, { recursive: true, force: true });
  }
});

// ── 真实 pi 引擎（新增）：重建后的 draft 仍保持无人值守标记 ─────────────────────
test("pi integration: a rebuilt automation draft keeps the unattended marker", async () => {
  const { mkdtemp, mkdir, rm } = await import("node:fs/promises");
  const { tmpdir } = await import("node:os");
  const { join } = await import("node:path");
  const { ModelRuntime, SettingsManager } = await import("@earendil-works/pi-coding-agent");
  const { registerFauxProvider } = await import("@earendil-works/pi-ai/compat");
  const { PiEngine } = await import("../src/engine/pi/piEngine.js");

  const root = await mkdtemp(join(tmpdir(), "nex-auto-rebuild-"));
  const workspace = join(root, "workspace");
  await mkdir(workspace, { recursive: true });
  const faux = registerFauxProvider({ models: [{ id: "faux-1", name: "Faux" }] });
  const model = faux.getModel();
  const createModelRuntime = async () => {
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
    return runtime;
  };
  const engine = new PiEngine({
    dataDir: join(root, "data"),
    logger,
    createModelRuntime,
    createSettingsManager: () => SettingsManager.inMemory(),
    titleSettings: { enabled: false },
  });
  try {
    await engine.init();
    const { sessionId } = await engine.createSession(workspace, {
      modelSelection: { providerId: "faux", modelId: "faux-1", options: {} },
      automationId: "auto_rebuild_1",
    });
    // 直接走重建路径：重建出的 OpenSession 必须仍带 unattended（否则审批卡会挂起）。
    const rebuilt = await engine.sessionLifecycle.reopenDraft(sessionId);
    assert.equal(rebuilt.unattended, true);
    // 重建后过 gate 的 ask 决策应立刻 deny 而非常驻 pending。
    const decision = await rebuilt.gate.authorize({
      toolCallId: "t-rebuild",
      toolName: "write",
      input: { path: "x.txt", content: "x" },
      signal: undefined,
    });
    assert.deepEqual(decision, {
      block: true,
      reason: "approval needed: automation runs unattended",
    });
    assert.equal(rebuilt.gate.pendingApprovals().length, 0);
  } finally {
    await engine.dispose();
    faux.unregister();
    await rm(root, { recursive: true, force: true });
  }
});
