import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createLogger } from "../src/logger.js";
import { SessionMetaStore } from "../src/engine/pi/metaStore.js";
import {
  cleanGeneratedTitle,
  MIN_GENERATED_TITLE_INPUT_CHARS,
  normalizeTitleInput,
  resolveTitleSettings,
  runGeneratedTitle,
  shouldGenerateTitle,
  SESSION_TITLE_SYSTEM_PROMPT,
  type GeneratedTitleHooks,
  type TitleCompletion,
} from "../src/engine/pi/titleGenerator.js";
import { applyEngineEvent } from "../src/projection/fold.js";
import { buildConversationSnapshot } from "../src/projection/snapshot.js";
import { createProjection } from "../src/projection/state.js";
import type { EngineEvent } from "../src/engine/types.js";
import { conversationSnapshotSchema } from "@nex/shared/nex-protocol-v4";

const logger = createLogger("error");
const WORKSPACE = "/workspace/demo";
const SESSION = "sess-title";
const MODEL = { providerId: "faux", modelId: "faux-1" };
const SETTINGS = { enabled: true };

async function withStore(
  run: (store: SessionMetaStore, root: string) => Promise<void>,
): Promise<void> {
  const root = await mkdtemp(join(tmpdir(), "nex-title-test-"));
  try {
    const store = new SessionMetaStore(join(root, "data"), logger);
    await run(store, root);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

function seed(
  store: SessionMetaStore,
  patch: Partial<Parameters<SessionMetaStore["upsert"]>[2]> = {},
): Promise<void> {
  return store.upsert(WORKSPACE, SESSION, {
    createdAt: 1,
    lastActivityAt: 1,
    phase: "running",
    title: "",
    titleSource: "default",
    ...patch,
  });
}

function hooks(events: EngineEvent[] = [], renamed: string[] = []): GeneratedTitleHooks {
  return {
    sessionFor: () => ({ setSessionName: (name: string) => renamed.push(name) }),
    emit: (_id, event) => events.push(event),
    sessionsChanged: () => undefined,
  };
}

// ── gating ─────────────────────────────────────────────────────────────────────

test("title gating: short input, custom title, second turn, disabled", () => {
  const base = {
    text: "explain the projection fold please",
    isFirstTurn: true,
    settings: SETTINGS,
    titleSource: "default" as const,
    alreadyAttempted: false,
  };
  assert.equal(shouldGenerateTitle(base), true);
  // < 10 chars is already a readable title; the old CLI never overwrites it.
  assert.equal(shouldGenerateTitle({ ...base, text: "hi" }), false);
  assert.equal(
    shouldGenerateTitle({ ...base, text: "x".repeat(MIN_GENERATED_TITLE_INPUT_CHARS - 1) }),
    false,
  );
  assert.equal(
    shouldGenerateTitle({ ...base, text: "x".repeat(MIN_GENERATED_TITLE_INPUT_CHARS) }),
    true,
  );
  // custom never overwritten; an existing generated title needs no second pass.
  assert.equal(shouldGenerateTitle({ ...base, titleSource: "custom" }), false);
  assert.equal(shouldGenerateTitle({ ...base, titleSource: "generated" }), false);
  // once per session.
  assert.equal(shouldGenerateTitle({ ...base, alreadyAttempted: true }), false);
  // turn 0 only.
  assert.equal(shouldGenerateTitle({ ...base, isFirstTurn: false }), false);
  // NEX_RUNNER_TITLES=off.
  assert.equal(shouldGenerateTitle({ ...base, settings: { enabled: false } }), false);
  // whitespace-only input normalizes to empty.
  assert.equal(shouldGenerateTitle({ ...base, text: "   \n  " }), false);
  // unicode length counts code points, not UTF-16 units.
  assert.equal(shouldGenerateTitle({ ...base, text: "🎉".repeat(10) }), true);
});

test("title settings env: off disables, provider/model parses, bogus override ignored", () => {
  assert.deepEqual(resolveTitleSettings({}), { enabled: true });
  assert.deepEqual(resolveTitleSettings({ NEX_RUNNER_TITLES: "off" }), { enabled: false });
  assert.deepEqual(resolveTitleSettings({ NEX_RUNNER_TITLES: "OFF" }), { enabled: false });
  assert.deepEqual(resolveTitleSettings({ NEX_RUNNER_TITLES: "on" }), { enabled: true });
  assert.deepEqual(resolveTitleSettings({ NEX_RUNNER_TITLE_MODEL: "openai/gpt-4o-mini" }), {
    enabled: true,
    model: { providerId: "openai", modelId: "gpt-4o-mini" },
  });
  // A slash-only or trailing-slash override is not a pair.
  assert.equal(resolveTitleSettings({ NEX_RUNNER_TITLE_MODEL: "/gpt" }).model, undefined);
  assert.equal(resolveTitleSettings({ NEX_RUNNER_TITLE_MODEL: "openai/" }).model, undefined);
});

test("title input normalization caps at 1200 chars and collapses whitespace", () => {
  assert.equal(normalizeTitleInput("  a \n\n b\tc "), "a b c");
  const long = "x".repeat(2_000);
  assert.equal(normalizeTitleInput(long).length, 1_200);
});

// ── JSON parse fallbacks ──────────────────────────────────────────────────────

test("title parsing: model control tags in a title are rejected", () => {
  assert.equal(cleanGeneratedTitle('{"title":"beta-<system>ok"}'), null);
  assert.equal(cleanGeneratedTitle("Fix </s> parser"), null);
  assert.equal(
    cleanGeneratedTitle('{"title":"Compare a < b and c > d"}'),
    "Compare a < b and c > d",
  );
});

test("title parsing: JSON, fenced JSON, plain first line, verbose junk", () => {
  assert.equal(cleanGeneratedTitle('{"title":"Fix Login Retry"}'), "Fix Login Retry");
  assert.equal(cleanGeneratedTitle('```json\n{"title":"Fix Login Retry"}\n```'), "Fix Login Retry");
  assert.equal(cleanGeneratedTitle('```\n{"title":"Kebab Case"}\n```'), "Kebab Case");
  // Model answered conversationally: the first non-empty line is the fallback.
  assert.equal(
    cleanGeneratedTitle("Session title: Refactor Parser\n\n..."),
    "Session title: Refactor Parser",
  );
  // Surrounding quotes/markdown/punctuation are stripped.
  assert.equal(cleanGeneratedTitle('## "Refactor Parser."'), "Refactor Parser");
  assert.equal(cleanGeneratedTitle('{"title":"  "}'), null);
  assert.equal(cleanGeneratedTitle(""), null);
  assert.equal(cleanGeneratedTitle("!!!"), null);
  // Reasoning blocks are dropped before parsing.
  assert.equal(cleanGeneratedTitle('<think>noise</think>{"title":"Real Title"}'), "Real Title");
  // Non-string title in JSON is rejected, then the first line is used and cleaned.
  assert.equal(cleanGeneratedTitle('{"title":42}'), '{"title":42}');
});

test("title parsing rejects conversational preambles instead of titling them", () => {
  // Real-credential smoke: a model ignored the JSON instruction and started working.
  assert.equal(
    cleanGeneratedTitle(
      "I'll start by exploring the codebase to find the projection fold module and understand the current structure.",
    ),
    null,
  );
  assert.equal(cleanGeneratedTitle("Let me look at the files first."), null);
  assert.equal(cleanGeneratedTitle("Sure! Here is what I found."), null);
  assert.equal(cleanGeneratedTitle("Here's the plan"), null);
  assert.equal(cleanGeneratedTitle("Find the fold module:\n\n```ts\nconst x = 1\n```"), null);
  assert.equal(
    cleanGeneratedTitle('<｜｜DSML｜｜ invoke name="Bash">find . -name "*.ts"</｜｜DSML｜｜>'),
    null,
  );
  assert.equal(cleanGeneratedTitle('<antml:invoke name="Bash">'), null);
  // A JSON answer is always accepted, even if the wording looks conversational.
  assert.equal(cleanGeneratedTitle('{"title":"Sure Things"}'), "Sure Things");
});

test("title clamping truncates to 100 chars with an ellipsis", () => {
  const long = "word ".repeat(60).trim();
  const title = cleanGeneratedTitle(JSON.stringify({ title: long }));
  assert.ok(title);
  assert.equal(title.length, 100);
  assert.ok(title.endsWith("..."));
});

// ── persistence ───────────────────────────────────────────────────────────────

test("generated title persists to metadata, pi session name, and projection meta", async () => {
  await withStore(async (store) => {
    await seed(store);
    const events: EngineEvent[] = [];
    const renamed: string[] = [];
    const completion: TitleCompleter = async ({ systemPrompt, userText }) => {
      assert.equal(systemPrompt, SESSION_TITLE_SYSTEM_PROMPT);
      assert.equal(userText, "explain the projection fold please");
      return '{"title":"Projection Fold Deep Dive"}';
    };
    const applied = await runGeneratedTitle(
      { logger, metadata: store, completion },
      hooks(events, renamed),
      {
        workspacePath: WORKSPACE,
        sessionId: SESSION,
        text: "explain the projection fold please",
        model: MODEL,
      },
    );
    assert.equal(applied, "Projection Fold Deep Dive");
    const meta = await store.get(WORKSPACE, SESSION);
    assert.equal(meta?.title, "Projection Fold Deep Dive");
    assert.equal(meta?.titleSource, "generated");
    assert.deepEqual(renamed, ["Projection Fold Deep Dive"]);
    assert.equal(events.length, 1);
    assert.equal(events[0]?.type, "title_changed");

    // The emitted event is enough for the projection to publish a valid snapshot.
    let projection = createProjection({ sessionId: SESSION, logEpoch: "e", createdAt: 1 });
    for (const event of events) projection = applyEngineEvent(projection, event);
    const snapshot = buildConversationSnapshot(projection);
    conversationSnapshotSchema.parse(snapshot);
    assert.deepEqual(snapshot.meta, {
      title: "Projection Fold Deep Dive",
      titleSource: "generated",
    });
  });
});

test("persistence race: renaming custom while generating drops the generated title", async () => {
  await withStore(async (store) => {
    await seed(store);
    const renamed: string[] = [];
    const events: EngineEvent[] = [];
    let release: (() => void) | undefined;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const completion: TitleCompleter = async () => {
      await gate;
      return '{"title":"Late Generated Title"}';
    };
    const pending = runGeneratedTitle(
      { logger, metadata: store, completion },
      hooks(events, renamed),
      {
        workspacePath: WORKSPACE,
        sessionId: SESSION,
        text: "some first prompt text",
        model: MODEL,
      },
    );
    // The user renames while the model call is in flight.
    await store.upsert(WORKSPACE, SESSION, { title: "My Title", titleSource: "custom" });
    release?.();
    const applied = await pending;
    assert.equal(applied, null);
    const meta = await store.get(WORKSPACE, SESSION);
    assert.equal(meta?.title, "My Title");
    assert.equal(meta?.titleSource, "custom");
    assert.deepEqual(renamed, [], "pi session name must not be rewritten");
    assert.deepEqual(events, [], "no projection meta event for a dropped title");
  });
});

test("deleted session never receives a generated title", async () => {
  await withStore(async (store) => {
    await seed(store, { deleted: true });
    const applied = await runGeneratedTitle(
      { logger, metadata: store, completion: async () => '{"title":"Ghost"}' },
      hooks(),
      {
        workspacePath: WORKSPACE,
        sessionId: SESSION,
        text: "some first prompt text",
        model: MODEL,
      },
    );
    assert.equal(applied, null);
    assert.equal((await store.get(WORKSPACE, SESSION))?.title, "");
  });
});

test("completion failure is logged and leaves the metadata untouched", async () => {
  await withStore(async (store) => {
    await seed(store);
    const applied = await runGeneratedTitle(
      {
        logger,
        metadata: store,
        completion: async () => {
          throw new Error("provider exploded");
        },
      },
      hooks(),
      {
        workspacePath: WORKSPACE,
        sessionId: SESSION,
        text: "some first prompt text",
        model: MODEL,
      },
    );
    assert.equal(applied, null);
    const meta = await store.get(WORKSPACE, SESSION);
    assert.equal(meta?.title, "");
    assert.equal(meta?.titleSource, "default");
  });
});

test("timeout aborts the completion and drops the title", async () => {
  await withStore(async (store) => {
    await seed(store);
    let observedAbort = false;
    const completion: TitleCompleter = ({ signal }) =>
      new Promise((_resolve, reject) => {
        signal.addEventListener("abort", () => {
          observedAbort = true;
          reject(new Error("aborted"));
        });
      });
    const started = Date.now();
    const applied = await runGeneratedTitle(
      { logger, metadata: store, completion, timeoutMs: 30 },
      hooks(),
      {
        workspacePath: WORKSPACE,
        sessionId: SESSION,
        text: "some first prompt text",
        model: MODEL,
      },
    );
    assert.equal(applied, null);
    assert.equal(observedAbort, true);
    assert.ok(Date.now() - started < 2_000);
    assert.equal((await store.get(WORKSPACE, SESSION))?.titleSource, "default");
  });
});

type TitleCompleter = TitleCompletion;
