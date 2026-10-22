import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fauxAssistantMessage, registerFauxProvider } from "@earendil-works/pi-ai/compat";
import type { FauxProviderRegistration, FauxResponseStep } from "@earendil-works/pi-ai/compat";
import { ModelRuntime, SettingsManager } from "@earendil-works/pi-coding-agent";
import type { TranscriptContext } from "@earendil-works/pi-ai";
import { conversationSnapshotSchema } from "@nex/shared/nex-protocol-v4";
import { PiEngine } from "../src/engine/pi/piEngine.js";
import type { EngineEvent, EngineSessionState } from "../src/engine/types.js";
import { createLogger } from "../src/logger.js";
import { applyEngineEvent } from "../src/projection/fold.js";
import { buildConversationSnapshot } from "../src/projection/snapshot.js";
import { createProjection, type ProjectionState } from "../src/projection/state.js";
import type { TitleGenerationSettings } from "../src/engine/pi/titleGenerator.js";

/**
 * Generated titles against the REAL pi engine and pi-ai's faux provider: a real session,
 * a real `ModelRuntime.completeSimple` call for the title, and the real metadata store.
 */

const logger = createLogger("error");
const TITLE = "Projection Fold Refactor";

/** Title and agent calls share one provider, so responses are chosen by the request. */
function isTitleRequest(context: TranscriptContext): boolean {
  return context.messages.some((message) => {
    if (message.role !== "system") return false;
    const content = message.content;
    if (typeof content === "string") return content.includes("Generate a concise title");
    return content.some(
      (block) => block.type === "text" && block.text.includes("Generate a concise title"),
    );
  });
}

/** Answers title requests with `title` and every other request with `agentReply`. */
function responder(agentReply: string, title: string | (() => Promise<string>)): FauxResponseStep {
  return async (context: TranscriptContext) => {
    if (isTitleRequest(context)) {
      const text = typeof title === "function" ? await title() : title;
      return fauxAssistantMessage(text);
    }
    return fauxAssistantMessage(agentReply);
  };
}

async function makeRuntime(): Promise<{ runtime: ModelRuntime; faux: FauxProviderRegistration }> {
  const faux: FauxProviderRegistration = registerFauxProvider({
    models: [{ id: "faux-1", name: "Faux" }],
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
  return { runtime, faux };
}

async function waitFor<T>(
  read: () => Promise<T | undefined> | T | undefined,
  label = "condition",
  timeoutMs = 5_000,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = await read();
    if (value !== undefined) return value;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error(`timed out waiting for ${label}`);
}

interface Harness {
  root: string;
  workspace: string;
  engine: PiEngine;
  faux: FauxProviderRegistration;
  sessionId: string;
  events: EngineEvent[];
  projection: () => ProjectionState;
  session: () => Promise<EngineSessionState | undefined>;
}

async function startSession(
  options: { titleSettings?: TitleGenerationSettings } = {},
): Promise<Harness> {
  const root = await mkdtemp(join(tmpdir(), "nex-title-pi-"));
  const workspace = join(root, "workspace");
  await mkdir(workspace, { recursive: true });

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
    // Always pin the settings: an operator's NEX_RUNNER_TITLE_MODEL must not redirect
    // these faux-provider tests at a real provider.
    titleSettings: options.titleSettings ?? { enabled: true },
  });
  await engine.init();
  const { sessionId } = await engine.createSession(workspace, {
    modelSelection: {
      providerId: "faux",
      modelId: "faux-1",
      options: { reasoningLevel: "off" },
    },
  });
  if (!faux) throw new Error("faux provider was not registered");
  const events: EngineEvent[] = [];
  let projection = createProjection({
    sessionId,
    logEpoch: "title-integration",
    createdAt: Date.now(),
  });
  engine.subscribe(sessionId, (event) => {
    events.push(event);
    projection = applyEngineEvent(projection, event);
  });
  return {
    root,
    workspace,
    engine,
    faux,
    sessionId,
    events,
    projection: () => projection,
    session: async () =>
      (await engine.listSessions(workspace)).find((item) => item.sessionId === sessionId),
  };
}

async function cleanup(h: Harness): Promise<void> {
  await h.engine.dispose();
  // A late fire-and-forget title write may still be flushing its metadata file.
  await new Promise((resolve) => setTimeout(resolve, 50));
  await rm(h.root, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 });
}

test("pi + faux: first prompt produces a generated title, applied to meta and projection", async () => {
  const h = await startSession();
  try {
    h.faux.setResponses([responder("Sure — here is the refactor plan.", `{"title":"${TITLE}"}`)]);
    await h.engine.submit(h.sessionId, "Please refactor the projection fold module", {
      requestId: "req-1",
    });

    const meta = await waitFor(async () => {
      const session = await h.session();
      return session?.titleSource === "generated" ? session : undefined;
    });
    assert.equal(meta.title, TITLE);

    const snapshot = await waitFor(() => {
      const built = buildConversationSnapshot(h.projection());
      return built.meta.titleSource === "generated" ? built : undefined;
    });
    conversationSnapshotSchema.parse(snapshot);
    assert.equal(snapshot.meta.title, TITLE);

    // One projection meta event carried the title (pi's session name got the same value
    // through `setSessionName`).
    assert.equal(h.events.filter((event) => event.type === "title_changed").length, 1);
  } finally {
    await cleanup(h);
  }
});

test("pi + faux: short first prompt skips generation (no title model call)", async () => {
  const h = await startSession();
  try {
    let titleCalls = 0;
    let agentCalls = 0;
    h.faux.setResponses([
      async (context: TranscriptContext) => {
        if (isTitleRequest(context)) {
          titleCalls += 1;
          return fauxAssistantMessage('{"title":"Should Not Appear"}');
        }
        agentCalls += 1;
        return fauxAssistantMessage("hi");
      },
    ]);
    await h.engine.submit(h.sessionId, "hi", { requestId: "req-short" });
    await new Promise((resolve) => setTimeout(resolve, 200));
    assert.equal(agentCalls, 1);
    assert.equal(titleCalls, 0);
    const session = await h.session();
    assert.equal(session?.titleSource, "default");
    assert.notEqual(session?.title, "Should Not Appear");
  } finally {
    await cleanup(h);
  }
});

test("pi + faux: NEX_RUNNER_TITLES=off disables generation entirely", async () => {
  const h = await startSession({ titleSettings: { enabled: false } });
  try {
    let titleCalls = 0;
    h.faux.setResponses([
      async (context: TranscriptContext) => {
        if (isTitleRequest(context)) {
          titleCalls += 1;
          return fauxAssistantMessage('{"title":"Should Not Appear"}');
        }
        return fauxAssistantMessage("answer");
      },
    ]);
    await h.engine.submit(h.sessionId, "a long enough first prompt to qualify", {
      requestId: "req-off",
    });
    await new Promise((resolve) => setTimeout(resolve, 200));
    assert.equal(titleCalls, 0);
    assert.equal((await h.session())?.titleSource, "default");
  } finally {
    await cleanup(h);
  }
});

test("pi + faux: second turn never generates a title", async () => {
  const h = await startSession();
  try {
    let titleText = "First Turn Title";
    let titleCalls = 0;
    h.faux.setResponses([
      async (context: TranscriptContext) => {
        if (isTitleRequest(context)) {
          titleCalls += 1;
          return fauxAssistantMessage(JSON.stringify({ title: titleText }));
        }
        return fauxAssistantMessage("agent answer");
      },
    ]);
    await h.engine.submit(h.sessionId, "first prompt that is definitely long", {
      requestId: "req-a",
    });
    const first = await waitFor(async () => {
      const session = await h.session();
      return session?.titleSource === "generated" ? session : undefined;
    });
    assert.equal(first.title, "First Turn Title");
    assert.equal(titleCalls, 1);

    titleText = "Second Turn Title";
    await h.engine.submit(h.sessionId, "second prompt that is also long", { requestId: "req-b" });
    await new Promise((resolve) => setTimeout(resolve, 200));
    assert.equal(titleCalls, 1, "title generation is once per session");
    assert.equal((await h.session())?.title, "First Turn Title");
  } finally {
    await cleanup(h);
  }
});

test("pi + faux: rename to custom during generation wins the race", async () => {
  const h = await startSession();
  try {
    let releaseTitle: (() => void) | undefined;
    const gate = new Promise<void>((resolve) => {
      releaseTitle = resolve;
    });
    let titleReached = false;
    h.faux.setResponses([
      async (context: TranscriptContext) => {
        if (isTitleRequest(context)) {
          titleReached = true;
          await gate;
          return fauxAssistantMessage('{"title":"Generated Too Late"}');
        }
        return fauxAssistantMessage("answer");
      },
    ]);
    await h.engine.submit(h.sessionId, "a first prompt long enough to title", {
      requestId: "req-race",
    });
    await waitFor(() => (titleReached ? true : undefined), "title call started");
    await h.engine.rename(h.sessionId, "My Custom Title");
    releaseTitle?.();
    await new Promise((resolve) => setTimeout(resolve, 200));
    const session = await h.session();
    assert.equal(session?.title, "My Custom Title");
    assert.equal(session?.titleSource, "custom");
  } finally {
    await cleanup(h);
  }
});
