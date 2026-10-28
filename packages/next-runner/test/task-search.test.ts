import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdir, mkdtemp, realpath, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fauxAssistantMessage, registerFauxProvider } from "@earendil-works/pi-ai/compat";
import type { FauxProviderRegistration, FauxResponseStep } from "@earendil-works/pi-ai/compat";
import { ModelRuntime, SettingsManager } from "@earendil-works/pi-coding-agent";
import { PiEngine } from "../src/engine/pi/piEngine.js";
import { NexTaskChannel } from "../src/channels/nexTask.js";
import {
  buildSearchSnippets,
  SEARCH_SNIPPET_LIMIT,
  SEARCH_SNIPPET_MAX_CHARS,
} from "../src/channels/taskSearchSnippets.js";
import { FakeEngine } from "./helpers/fakeEngine.js";
import { createLogger } from "../src/logger.js";
import type { EngineSessionState } from "../src/engine/types.js";

/**
 * 全文搜索（docs/next/automations-views-search.md §C）：摘要窗口、标题/正文命中规则、
 * limit/hasMore、索引失效，以及一条真实 pi 会话的集成用例。
 */

const WORKSPACE = "/workspace/search";
const logger = createLogger("error");
const T0 = 1_700_000_000_000;

function session(
  sessionId: string,
  overrides: Partial<EngineSessionState> = {},
): EngineSessionState {
  return {
    sessionId,
    workspacePath: WORKSPACE,
    title: "Untitled task",
    titleSource: "default",
    createdAt: T0,
    lastActivityAt: T0,
    phase: "completedSuccess",
    model: null,
    usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    isRunning: false,
    ...overrides,
  };
}

function makeChannel(): { engine: FakeEngine; channel: NexTaskChannel } {
  const engine = new FakeEngine();
  return { engine, channel: new NexTaskChannel(engine, logger) };
}

// ── snippet builder ──

test("snippets: window is centered on the match with prefix/suffix ellipsis", () => {
  const text = `${"a".repeat(100)}needle${"b".repeat(100)}`;
  const snippets = buildSearchSnippets(text, "needle");
  assert.equal(snippets.length, 1);
  // 20 字符前缀 + 命中 + 72 字符后缀，两侧都有省略号。
  assert.equal(snippets[0], `...${"a".repeat(20)}needle${"b".repeat(72)}...`);
});

test("snippets: no ellipsis at the text edges and whitespace is normalized", () => {
  const text = "start   with\n\nneedle\tand   more";
  const snippets = buildSearchSnippets(text, "NEEDLE");
  assert.deepEqual(snippets, ["start with needle and more"]);
});

test("snippets: caps at four non-overlapping windows", () => {
  const parts: string[] = [];
  for (let index = 0; index < 8; index += 1) {
    parts.push(`needle ${"x".repeat(200)}`);
  }
  const snippets = buildSearchSnippets(parts.join(" "), "needle");
  assert.equal(snippets.length, SEARCH_SNIPPET_LIMIT);
});

test("snippets: nearby duplicate windows are merged", () => {
  const snippets = buildSearchSnippets("needle needle needle", "needle");
  assert.equal(snippets.length, 1);
});

test("snippets: each snippet is truncated to the max length", () => {
  const query = "x".repeat(100);
  const snippets = buildSearchSnippets(`zzzzzzzzzz${query}${"y".repeat(200)}`, query);
  assert.equal(snippets.length, 1);
  assert.equal(snippets[0]?.length, SEARCH_SNIPPET_MAX_CHARS);
});

test("snippets: no search text or no match yields nothing", () => {
  assert.deepEqual(buildSearchSnippets("some text", "   "), []);
  assert.deepEqual(buildSearchSnippets("   ", "needle"), []);
  assert.deepEqual(buildSearchSnippets("some text", "absent"), []);
});

// ── channel search ──

test("search: transcript hit returns snippet fields and orders by updated time desc", async () => {
  const { engine, channel } = makeChannel();
  engine.seedSession(session("old", { lastActivityAt: T0 + 1 }), []);
  engine.seedSession(session("new", { lastActivityAt: T0 + 5 }), []);
  engine.searchTexts.set("old", "the alpha build finished");
  engine.searchTexts.set("new", "an alpha release shipped");

  const result = await channel.searchTasks({ workspacePath: WORKSPACE, query: "alpha" });
  assert.deepEqual(
    result.matches.map((match) => match.taskId),
    ["new", "old"],
  );
  assert.equal(result.total, 2);
  assert.equal(result.hasMore, false);
  const first = result.matches[0];
  assert.deepEqual(first?.searchSnippets, ["an alpha release shipped"]);
  assert.equal(first?.searchSnippet, "an alpha release shipped");
  channel.dispose();
});

test("search: title-only hit carries no snippet fields", async () => {
  const { engine, channel } = makeChannel();
  engine.seedSession(session("titled", { title: "Quarterly report" }), []);
  engine.searchTexts.set("titled", "unrelated transcript body");

  const result = await channel.searchTasks({ workspacePath: WORKSPACE, query: "quarterly" });
  assert.equal(result.matches.length, 1);
  assert.equal(result.matches[0]?.taskId, "titled");
  assert.equal("searchSnippet" in (result.matches[0] ?? {}), false);
  assert.equal("searchSnippets" in (result.matches[0] ?? {}), false);
  channel.dispose();
});

test("search: queries shorter than two characters match titles only", async () => {
  const { engine, channel } = makeChannel();
  engine.seedSession(session("titled", { title: "Alpha" }), []);
  // 正文也含 "a"，但短查询必须只看标题，并且不带摘要。
  engine.searchTexts.set("titled", "a body that mentions a lot");
  engine.seedSession(session("body-only", { title: "Zulu" }), []);
  engine.searchTexts.set("body-only", "a body only");

  const result = await channel.searchTasks({ workspacePath: WORKSPACE, query: "a" });
  assert.deepEqual(
    result.matches.map((match) => match.taskId),
    ["titled"],
  );
  assert.equal("searchSnippet" in (result.matches[0] ?? {}), false);
  channel.dispose();
});

test("search: blank query returns an empty result", async () => {
  const { engine, channel } = makeChannel();
  engine.seedSession(session("one", { title: "Alpha" }), []);
  const result = await channel.searchTasks({ workspacePath: WORKSPACE, query: "   " });
  assert.deepEqual(result, { matches: [], total: 0, hasMore: false });
  channel.dispose();
});

test("search: limit defaults to 80 and clamps to [1, 200]", async () => {
  const { engine, channel } = makeChannel();
  for (let index = 0; index < 250; index += 1) {
    engine.seedSession(session(`sess-${index}`, { title: `alpha ${index}` }), []);
  }

  const defaulted = await channel.searchTasks({ workspacePath: WORKSPACE, query: "alpha" });
  assert.equal(defaulted.matches.length, 80);
  assert.equal(defaulted.total, 250);
  assert.equal(defaulted.hasMore, true);

  const capped = await channel.searchTasks({
    workspacePath: WORKSPACE,
    query: "alpha",
    limit: 999,
  });
  assert.equal(capped.matches.length, 200);
  assert.equal(capped.hasMore, true);

  const zero = await channel.searchTasks({ workspacePath: WORKSPACE, query: "alpha", limit: 0 });
  assert.equal(zero.matches.length, 1);

  const exact = await channel.searchTasks({ workspacePath: WORKSPACE, query: "alpha", limit: 250 });
  assert.equal(exact.matches.length, 200);
  assert.equal(exact.hasMore, true);
  channel.dispose();
});

test("search: total counts every match while matches is limited", async () => {
  const { engine, channel } = makeChannel();
  for (let index = 0; index < 5; index += 1) {
    engine.seedSession(session(`sess-${index}`, { title: `alpha ${index}` }), []);
  }
  const result = await channel.searchTasks({ workspacePath: WORKSPACE, query: "alpha", limit: 2 });
  assert.equal(result.matches.length, 2);
  assert.equal(result.total, 5);
  assert.equal(result.hasMore, true);
  channel.dispose();
});

test("search: draft and deleted sessions are excluded", async () => {
  const { engine, channel } = makeChannel();
  engine.seedSession(session("draft", { phase: "draft", title: "alpha draft" }), []);
  engine.seedSession(session("live", { title: "alpha live" }), []);
  engine.seedSession(session("gone", { title: "alpha gone" }), []);
  await engine.deleteSession(WORKSPACE, "gone");

  const result = await channel.searchTasks({ workspacePath: WORKSPACE, query: "alpha" });
  assert.deepEqual(
    result.matches.map((match) => match.taskId),
    ["live"],
  );
  channel.dispose();
});

test("search: pinned and archived sessions stay searchable", async () => {
  const { engine, channel } = makeChannel();
  engine.seedSession(session("pinned", { title: "alpha pinned", pinned: true }), []);
  engine.seedSession(session("archived", { title: "alpha archived", archived: true }), []);
  const result = await channel.searchTasks({ workspacePath: WORKSPACE, query: "alpha" });
  assert.deepEqual(result.matches.map((match) => match.taskId).sort(), ["archived", "pinned"]);
  channel.dispose();
});

test("search: index is invalidated by onSessionsChanged", async () => {
  const { engine, channel } = makeChannel();
  engine.seedSession(session("one", { title: "first title" }), []);
  engine.searchTexts.set("one", "original body");

  const before = await channel.searchTasks({ workspacePath: WORKSPACE, query: "original" });
  assert.equal(before.matches.length, 1);

  // 正文/标题变化本身不触发索引更新：必须靠 onSessionsChanged 失效。
  engine.searchTexts.set("one", "rewritten body");
  const stale = await channel.searchTasks({ workspacePath: WORKSPACE, query: "rewritten" });
  assert.equal(stale.matches.length, 0);

  engine.emitSessionsChanged(WORKSPACE);
  const fresh = await channel.searchTasks({ workspacePath: WORKSPACE, query: "rewritten" });
  assert.equal(fresh.matches.length, 1);
  assert.equal(fresh.matches[0]?.taskId, "one");

  // 新会话同样只在失效后可见。
  engine.seedSession(session("two", { title: "second" }), []);
  assert.equal((await channel.searchTasks({ workspacePath: WORKSPACE, query: "second" })).total, 0);
  engine.emitSessionsChanged(WORKSPACE);
  assert.equal((await channel.searchTasks({ workspacePath: WORKSPACE, query: "second" })).total, 1);
  channel.dispose();
});

test("search: warm queries stay in memory (one text read per build)", async () => {
  const { engine, channel } = makeChannel();
  for (let index = 0; index < 50; index += 1) {
    engine.seedSession(session(`sess-${index}`, { title: `alpha ${index}` }), []);
  }
  // 并发搜索共享同一次构建。
  await Promise.all([
    channel.searchTasks({ workspacePath: WORKSPACE, query: "alpha" }),
    channel.searchTasks({ workspacePath: WORKSPACE, query: "alpha 1" }),
  ]);
  assert.equal(engine.searchTextReads, 1);
  await channel.searchTasks({ workspacePath: WORKSPACE, query: "alpha 2" });
  assert.equal(engine.searchTextReads, 1);
  channel.dispose();
});

// ── real pi integration ──

/** 回复里带一个只存在于正文的标记词，标题取自用户消息，不含该词。 */
const TRANSCRIPT_ONLY = "kumquat-marker";

function makeResponder(): FauxResponseStep {
  return async () => fauxAssistantMessage(`the ${TRANSCRIPT_ONLY} is in the reply`);
}

async function makeRuntime(): Promise<{ runtime: ModelRuntime; faux: FauxProviderRegistration }> {
  const faux = registerFauxProvider({ models: [{ id: "faux-1", name: "Faux" }] });
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
  return { runtime, faux };
}

async function waitFor<T>(read: () => Promise<T | undefined>, label: string): Promise<T> {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    const value = await read();
    if (value !== undefined) return value;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error(`timed out waiting for ${label}`);
}

test("pi: search finds a word that only exists in the session transcript", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-search-pi-"));
  const workspace = join(root, "workspace");
  await mkdir(workspace, { recursive: true });
  const resolved = await realpath(workspace);

  let faux: FauxProviderRegistration | undefined;
  const engine = new PiEngine({
    dataDir: join(root, "data"),
    logger,
    createModelRuntime: async () => {
      const created = await makeRuntime();
      faux = created.faux;
      return created.runtime;
    },
    createSettingsManager: () => SettingsManager.inMemory(),
    titleSettings: { enabled: false },
  });
  const channel = new NexTaskChannel(engine, logger);
  try {
    await engine.init();
    const { sessionId } = await engine.createSession(resolved, {
      modelSelection: {
        providerId: "faux",
        modelId: "faux-1",
        options: { reasoningLevel: "off" },
      },
    });
    if (!faux) throw new Error("faux provider was not registered");
    faux.setResponses([makeResponder()]);
    await engine.submit(sessionId, "Please summarize the deployment", { requestId: "req-search" });

    // 等 pi 把回复落盘（SessionManager.list 读的是 session 文件）。
    await waitFor(async () => {
      const texts = await engine.listSessionSearchText(resolved);
      return texts.get(sessionId)?.includes(TRANSCRIPT_ONLY) ? true : undefined;
    }, "transcript persisted");

    // 标题来自用户消息，不含标记词：命中只能来自正文。
    const sessions = await engine.listSessions(resolved);
    const title = sessions.find((item) => item.sessionId === sessionId)?.title ?? "";
    assert.equal(title.includes(TRANSCRIPT_ONLY), false);

    const result = await channel.searchTasks({ workspacePath: resolved, query: TRANSCRIPT_ONLY });
    assert.equal(result.total, 1);
    const match = result.matches[0];
    assert.equal(match?.taskId, sessionId);
    assert.equal(match?.searchSnippets?.length, 1);
    assert.match(match?.searchSnippet ?? "", /kumquat-marker/);

    // 标题命中但正文没有该词时同样不带摘要：改用自定义标题里的独有词。
    // rename 自身会触发 onSessionsChanged，索引随之失效。
    await engine.rename(sessionId, "Kestrel handoff notes");
    const byTitle = await channel.searchTasks({ workspacePath: resolved, query: "kestrel" });
    assert.equal(byTitle.matches[0]?.taskId, sessionId);
    assert.equal("searchSnippet" in (byTitle.matches[0] ?? {}), false);
    assert.equal("searchSnippets" in (byTitle.matches[0] ?? {}), false);
  } finally {
    channel.dispose();
    await engine.dispose();
    await new Promise((resolve) => setTimeout(resolve, 50));
    await rm(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 });
  }
});
