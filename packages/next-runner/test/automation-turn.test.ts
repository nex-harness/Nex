import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, realpath, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  fauxAssistantMessage,
  fauxToolCall,
  registerFauxProvider,
} from "@earendil-works/pi-ai/compat";
import type { FauxProviderRegistration } from "@earendil-works/pi-ai/compat";
import { ModelRuntime, SettingsManager } from "@earendil-works/pi-coding-agent";
import {
  commandAckSchema,
  conversationSnapshotSchema,
  type CommandAck,
  type CommandEnvelope,
  type ConversationSnapshot,
} from "@nex/shared/nex-protocol-v4";
import { PiEngine } from "../src/engine/pi/piEngine.js";
import { PermissionGate } from "../src/engine/permissions/gate.js";
import { UNATTENDED_DENY_REASON } from "../src/engine/permissions/gate.js";
import { createLogger } from "../src/logger.js";
import { ConversationPublisher } from "../src/v4/publisher.js";
import type { CompleteConversationWireFrame } from "../src/v4/envelopes.js";
import type { EngineEvent } from "../src/engine/types.js";
import { FakeEngine } from "./helpers/fakeEngine.js";

/**
 * automation 派发进“用户已有会话”的一轮（docs/next/automations-views-search.md §A）。
 *
 * 修复依据：automation 绑定既有会话时 server 发 `sendText` 并带 `automationId`；此前
 * runner 只在 createSession 路径读这个字段，于是这一轮仍按有人值守运行 —— 一旦模型调用
 * 需要审批的工具，审批卡永远没人点，整轮挂起。无人值守必须只作用于 automation 的这一轮：
 * 用户随后在同一会话里自己发消息时，审批要照常出现。
 */

const logger = createLogger("error");

// ── gate：turn 级无人值守 ─────────────────────────────────────────────────────

test("gate: a turn marked unattended denies immediately and stays attended afterwards", async () => {
  const events: EngineEvent[] = [];
  const gate = new PermissionGate({ emit: (event) => events.push(event) });
  gate.setTurnUnattended(true);
  const denied = await gate.authorize({
    toolCallId: "t-auto",
    toolName: "write",
    input: { path: "a.txt", content: "x" },
    signal: undefined,
  });
  assert.deepEqual(denied, { block: true, reason: UNATTENDED_DENY_REASON });
  assert.equal(gate.pendingApprovals().length, 0);
  assert.equal(events.filter((event) => event.type === "approval_request").length, 0);

  // 下一轮是普通用户输入：必须回到“等待审批”，而不是永久无人值守。
  gate.setTurnUnattended(false);
  const pending = gate.authorize({
    toolCallId: "t-user",
    toolName: "write",
    input: { path: "a.txt", content: "x" },
    signal: undefined,
  });
  assert.equal(gate.pendingApprovals().length, 1);
  gate.resolve(gate.pendingApprovals()[0]!.interactionId, { optionId: "deny" });
  assert.equal((await pending).block, true);
});

// ── queue：automationId 只随该队列项的那一轮提交 ─────────────────────────────

test("queue: an automation sendText queued behind a busy turn carries automationId on promotion", async () => {
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const WORKSPACE = "/workspace/demo";
  const frames: CompleteConversationWireFrame[] = [];
  publisher.addConversationSink(WORKSPACE, (frame) => frames.push(frame));
  try {
    const created = await publisher.sendCommand({
      workspacePath: WORKSPACE,
      envelope: envelope({ type: "createSession", payload: { workspaceId: WORKSPACE } }),
    });
    const sessionId = created.result?.type === "createSession" ? created.result.sessionId : "";
    publisher.subscribeConversation({ workspacePath: WORKSPACE, sessionId });
    await waitFor(() => frames.length > 0, "initial frame");

    await send(publisher, WORKSPACE, sessionId, { text: "user first" });
    await waitFor(() => engine.submitted.length === 1, "first submitted");
    // 忙时到达的 automation 输入进入队列（不打断用户那一轮）。
    const queued = await send(publisher, WORKSPACE, sessionId, {
      text: "automation second",
      automationId: "auto_queue_1",
    });
    assert.equal(queued.result?.type === "inputAccepted" ? queued.result.delivery : "", "queue");
    assert.equal(engine.submitted.length, 1);

    engine.settle(sessionId);
    await waitFor(() => engine.submitted.length === 2, "queued item promoted");
    assert.equal(engine.submitted[1]?.text, "automation second");
    assert.equal(engine.submitted[1]?.automationId, "auto_queue_1");
    // 普通用户输入不带 automationId：提升后的那一轮仍是有人值守。
    assert.equal(engine.submitted[0]?.automationId, undefined);

    // automation 那一轮结束后，同一会话的普通输入恢复有人值守（标记不残留）。
    engine.settle(sessionId);
    const plain = await send(publisher, WORKSPACE, sessionId, { text: "user third" });
    assert.equal(plain.result?.type === "inputAccepted" ? plain.result.delivery : "", "startNow");
    await waitFor(() => engine.submitted.length === 3, "plain input submitted");
    assert.equal(engine.submitted[2]?.automationId, undefined);
    // 会话元数据不被标记成 automation 会话（侧栏不出现 cron 徽章）。
    assert.equal(engine.sessions.get(sessionId)?.automationId, undefined);
  } finally {
    publisher.dispose();
    await engine.dispose();
  }
});

// ── 真实 pi 引擎：同一会话里 automation 轮无人值守、用户轮照常审批 ────────────

interface Harness {
  root: string;
  workspace: string;
  engine: PiEngine;
  publisher: ConversationPublisher;
  faux: FauxProviderRegistration;
  sessionId: string;
  frames: CompleteConversationWireFrame[];
  events: EngineEvent[];
  snapshot: () => ConversationSnapshot;
  send: (payload: Record<string, unknown>) => Promise<CommandAck>;
}

async function makeHarness(
  existing?: { root: string; workspace: string; sessionId: string },
  options: { subscribe?: boolean } = {},
): Promise<Harness> {
  const root = existing?.root ?? (await mkdtemp(join(tmpdir(), "nex-auto-turn-")));
  const workspace =
    existing?.workspace ??
    (await realpath(join(root, "workspace")).catch(async () => {
      await mkdir(join(root, "workspace"), { recursive: true });
      return realpath(join(root, "workspace"));
    }));
  let faux: FauxProviderRegistration | undefined;
  const engine = new PiEngine({
    dataDir: join(root, "data"),
    logger,
    createModelRuntime: async () => {
      const registered = registerFauxProvider({ models: [{ id: "faux-1", name: "Faux" }] });
      faux = registered;
      const model = registered.getModel();
      const runtime = await ModelRuntime.create({ refreshOnCreate: false });
      runtime.registerProvider(model.provider, {
        baseUrl: model.baseUrl,
        api: registered.api,
        apiKey: "faux-key",
        models: registered.models.map((item) => ({
          id: item.id,
          name: item.name,
          api: item.api,
          reasoning: item.reasoning,
          input: item.input,
          cost: item.cost,
          contextWindow: item.contextWindow,
          maxTokens: item.maxTokens,
          baseUrl: item.baseUrl,
        })),
      });
      return runtime;
    },
    createSettingsManager: () => SettingsManager.inMemory(),
    titleSettings: { enabled: false },
  });
  await engine.init();
  const publisher = new ConversationPublisher({ engine, logger });
  const frames: CompleteConversationWireFrame[] = [];
  publisher.addConversationSink(workspace, (frame) => frames.push(frame));
  let sessionId = existing?.sessionId;
  if (!sessionId) {
    const created = commandAckSchema.parse(
      await publisher.sendCommand({
        workspacePath: workspace,
        envelope: envelope({
          type: "createSession",
          payload: {
            firstInput: {
              modelSelection: {
                providerId: "faux",
                modelId: "faux-1",
                options: { reasoningLevel: "off" },
              },
            },
          },
        }),
      }),
    );
    sessionId = created.result?.type === "createSession" ? created.result.sessionId : "";
    if (!sessionId) throw new Error("session was not created");
  }
  const events: EngineEvent[] = [];
  if (options.subscribe !== false) {
    // 冷恢复场景会话尚未打开（`subscribe` 要求 requireOpen），只能按 listSessions 轮询。
    engine.subscribe(sessionId, (event) => events.push(event));
    publisher.subscribeConversation({ workspacePath: workspace, sessionId });
    await waitFor(() => frames.length > 0, "initial frame");
  }
  if (!faux) throw new Error("faux provider was not registered");
  const id = sessionId;
  return {
    root,
    workspace,
    engine,
    publisher,
    faux,
    sessionId: id,
    frames,
    events,
    snapshot: () => conversationSnapshotSchema.parse(lastSnapshotOf(frames)),
    send: (payload) => send(publisher, workspace, id, payload),
  };
}

async function cleanup(h: Harness): Promise<void> {
  await h.engine.abort(h.sessionId).catch(() => undefined);
  await h.publisher.dispose();
  await h.engine.dispose();
  await new Promise((resolve) => setTimeout(resolve, 100));
  await rm(h.root, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 });
}

test("pi integration: an automation sendText into an existing session never hangs on approval", async () => {
  const h = await makeHarness();
  try {
    const file = join(h.workspace, "auto-turn.txt");
    h.faux.setResponses([
      fauxAssistantMessage([fauxToolCall("write", { path: "auto-turn.txt", content: "x" })], {
        stopReason: "toolUse",
      }),
      fauxAssistantMessage("blocked by policy"),
    ]);
    const ack = await h.send({
      text: "automation write",
      automationId: "auto_turn_1",
      mode: "build",
    });
    assert.equal(ack.status, "accepted");

    // 这一轮必须自己结束：没有审批卡，也没有 pending promise。
    await waitFor(() => h.events.some((event) => event.type === "settled"), "settled", 20_000);
    assert.equal(h.events.filter((event) => event.type === "approval_request").length, 0);
    assert.equal(h.snapshot().pendingInteractions.length, 0);
    const blocked = h.events.find((event) => event.type === "tool_call_end");
    assert.match(
      blocked?.type === "tool_call_end" ? blocked.text : "",
      /approval needed: automation runs unattended/u,
    );
    await assert.rejects(readFile(file, "utf8"));

    // 不把用户会话标成 automation 会话（侧栏 cron 徽章不该出现）。
    const state = (await h.engine.listSessions(h.workspace)).find(
      (item) => item.sessionId === h.sessionId,
    );
    assert.equal(state?.automationId, undefined);

    // 同一会话的下一条普通用户输入恢复有人值守：审批卡必须出现。
    h.faux.setResponses([
      fauxAssistantMessage([fauxToolCall("write", { path: "user-turn.txt", content: "y" })], {
        stopReason: "toolUse",
      }),
      fauxAssistantMessage("user decided"),
    ]);
    const userAck = await h.send({ text: "user write", mode: "build" });
    assert.equal(userAck.status, "accepted");
    const interaction = await waitFor(
      () => h.snapshot().pendingInteractions.find((item) => item.kind === "permission"),
      "attended approval",
      20_000,
    );
    assert.equal(interaction.payload.kind, "permission");
    await h.engine.resolveApproval(h.sessionId, interaction.interactionId, { optionId: "deny" });
    await waitFor(() => h.snapshot().pendingInteractions.length === 0, "approval settled", 20_000);
  } finally {
    await cleanup(h);
  }
});

test("pi integration: an automation turn stays unattended when the session is opened cold", async () => {
  const first = await makeHarness();
  let second: Harness | undefined;
  try {
    // 先产生一轮，让会话文件落盘；随后丢弃整个引擎（模拟 runner 重启/会话未加载）。
    first.faux.setResponses([fauxAssistantMessage("warm up")]);
    assert.equal((await first.send({ text: "warm up" })).status, "accepted");
    await waitFor(() => first.events.some((event) => event.type === "settled"), "warm up settled");
    await first.publisher.dispose();
    await first.engine.dispose();

    // 冷恢复且没有会话订阅：sendText 到达时才 ensure() → openSession。
    second = await makeHarness(
      { root: first.root, workspace: first.workspace, sessionId: first.sessionId },
      { subscribe: false },
    );
    second.faux.setResponses([
      fauxAssistantMessage([fauxToolCall("write", { path: "cold.txt", content: "z" })], {
        stopReason: "toolUse",
      }),
      fauxAssistantMessage("blocked"),
    ]);
    const ack = await second.send({
      text: "cold automation write",
      automationId: "auto_cold_1",
      mode: "build",
    });
    assert.equal(ack.status, "accepted");
    // 这一轮必须自己跑到终态（无人值守 deny 而不是挂起等审批）：转录里出现被拒绝的
    // toolResult，且 reason 就是契约文案。
    const blocked = await waitForAsync(async () => {
      const history = await second!.engine.history(second!.sessionId).catch(() => []);
      return history.find(
        (message) =>
          message.role === "toolResult" &&
          message.toolName === "write" &&
          message.text.includes(UNATTENDED_DENY_REASON),
      );
    }, "cold automation run settled");
    assert.equal(blocked.role, "toolResult");
    assert.equal(blocked.role === "toolResult" ? blocked.isError : false, true);
    await assert.rejects(readFile(join(second.workspace, "cold.txt"), "utf8"));
    const coldSessionId = second.sessionId;
    const state = (await second.engine.listSessions(second.workspace)).find(
      (item) => item.sessionId === coldSessionId,
    );
    assert.equal(state?.automationId, undefined);
  } finally {
    if (second) await cleanup(second);
    else await rm(first.root, { recursive: true, force: true });
  }
});

// ── helpers ─────────────────────────────────────────────────────────────────

function envelope(
  overrides: Partial<CommandEnvelope> & { type: CommandEnvelope["type"] },
): CommandEnvelope {
  return {
    commandId: `cmd-${Math.random().toString(36).slice(2)}`,
    clientId: "client-1",
    sessionId: null,
    payload: {},
    issuedAt: Date.now(),
    ...overrides,
  };
}

async function send(
  publisher: ConversationPublisher,
  workspacePath: string,
  sessionId: string,
  payload: Record<string, unknown>,
): Promise<CommandAck> {
  return commandAckSchema.parse(
    await publisher.sendCommand({
      workspacePath,
      envelope: envelope({ type: "sendText", sessionId, payload }),
    }),
  );
}

function lastSnapshotOf(frames: CompleteConversationWireFrame[]): ConversationSnapshot {
  const last = frames[frames.length - 1];
  if (!last || last.frame.payload.kind !== "snapshot") throw new Error("no snapshot frame yet");
  return last.frame.payload.snapshot;
}

async function waitFor<T>(
  read: () => T | undefined | false,
  label: string,
  timeoutMs = 10_000,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = read();
    if (value) return value as T;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error(`timed out waiting for ${label}`);
}

async function waitForAsync<T>(
  read: () => Promise<T | undefined>,
  label: string,
  timeoutMs = 20_000,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = await read();
    if (value) return value;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error(`timed out waiting for ${label}`);
}
