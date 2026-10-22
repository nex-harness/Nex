import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  fauxAssistantMessage,
  fauxToolCall,
  registerFauxProvider,
} from "@earendil-works/pi-ai/compat";
import type { FauxProviderRegistration } from "@earendil-works/pi-ai/compat";
import { ModelRuntime, SettingsManager } from "@earendil-works/pi-coding-agent";
import { conversationSnapshotSchema } from "@nex/shared/nex-protocol-v4";
import { PiEngine } from "../src/engine/pi/piEngine.js";
import { createLogger } from "../src/logger.js";
import { applyEngineEvent } from "../src/projection/fold.js";
import { buildConversationSnapshot } from "../src/projection/snapshot.js";
import { createProjection, type ProjectionState } from "../src/projection/state.js";
import type { EngineEvent } from "../src/engine/types.js";

/**
 * Integration test with the REAL pi engine and pi-ai's faux provider: the inline
 * `nex-permission-gate` extension must intercept the hook before `write` runs, the
 * approval card must reach the projection, and the decision must gate the file write.
 */

const logger = createLogger("error");

async function makeRuntime(): Promise<ModelRuntime> {
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
  fauxs.push(faux);
  return runtime;
}

const fauxs: FauxProviderRegistration[] = [];

/** The runtime is per-engine, so the faux handle is looked up by call order. */
function currentFaux(): FauxProviderRegistration {
  const faux = fauxs[fauxs.length - 1];
  if (!faux) throw new Error("no faux provider registered");
  return faux;
}

interface Harness {
  root: string;
  workspace: string;
  engine: PiEngine;
  sessionId: string;
  projection: () => ProjectionState;
  events: EngineEvent[];
}

async function startSession(mode: string, planEnabled = false): Promise<Harness> {
  const root = await mkdtemp(join(tmpdir(), "nex-perm-pi-"));
  const workspace = join(root, "workspace");
  await import("node:fs/promises").then((fs) => fs.mkdir(workspace, { recursive: true }));
  const engine = new PiEngine({
    dataDir: join(root, "data"),
    logger,
    createModelRuntime: makeRuntime,
    createSettingsManager: () => SettingsManager.inMemory(),
    // These tests script exactly one faux response per agent call; a title request would
    // consume one. Pinned so an operator's NEX_RUNNER_TITLE_MODEL cannot redirect them.
    titleSettings: { enabled: false },
  });
  await engine.init();
  const { sessionId } = await engine.createSession(workspace, {
    modelSelection: {
      providerId: "faux",
      modelId: "faux-1",
      options: { reasoningLevel: "off" },
    },
  });
  const events: EngineEvent[] = [];
  let projection = createProjection({
    sessionId,
    logEpoch: "pi-integration",
    createdAt: Date.now(),
  });
  engine.subscribe(sessionId, (event) => {
    events.push(event);
    projection = applyEngineEvent(projection, event);
  });
  void mode;
  void planEnabled;
  return { root, workspace, engine, sessionId, projection: () => projection, events };
}

async function waitFor<T>(read: () => T | undefined, timeoutMs = 5_000): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = read();
    if (value !== undefined) return value;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error("timed out waiting for condition");
}

async function waitForAsync<T>(read: () => Promise<T | undefined>, timeoutMs = 5_000): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = await read();
    if (value !== undefined) return value;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error("timed out waiting for condition");
}

function pending(snapshot: ReturnType<typeof buildConversationSnapshot>) {
  return snapshot.pendingInteractions.find((item) => item.kind === "permission");
}

async function stop(harness: Harness): Promise<void> {
  // Abort first: a denied/aborted run still emits agent_settled, whose metadata write
  // would otherwise race the temp-dir removal and surface as an unhandled rejection.
  await harness.engine.abort(harness.sessionId).catch(() => undefined);
  await new Promise((resolve) => setTimeout(resolve, 200));
  await harness.engine.dispose();
  currentFaux().unregister();
  await rm(harness.root, { recursive: true, force: true });
}

test("pi integration: build mode asks, allow writes the file, projection settles", async () => {
  const harness = await startSession("build");
  const file = join(harness.workspace, "perm-ok.txt");
  try {
    currentFaux().setResponses([
      fauxAssistantMessage([fauxToolCall("write", { path: "perm-ok.txt", content: "OK" })], {
        stopReason: "toolUse",
      }),
      fauxAssistantMessage("wrote it"),
    ]);
    await harness.engine.submit(harness.sessionId, "create perm-ok.txt with OK", {
      requestId: "req-build-1",
      mode: "build",
    });

    const interaction = await waitFor(() =>
      pending(buildConversationSnapshot(harness.projection())),
    );
    conversationSnapshotSchema.parse(buildConversationSnapshot(harness.projection()));
    assert.equal(interaction.payload.kind, "permission");
    assert.equal(interaction.payload.toolName, "Write");
    // The hook is awaiting: nothing is written before the decision.
    await assert.rejects(readFile(file, "utf8"));

    await harness.engine.resolveApproval(harness.sessionId, interaction.interactionId, {
      optionId: "allowOnce",
    });
    const outcome = await waitFor(() =>
      harness.events.find((event) => event.type === "approval_settled"),
    );
    assert.equal(outcome.type === "approval_settled" ? outcome.outcome : "", "allow");
    // The settle event fires before the tool executes; wait for the write itself.
    assert.equal(
      await waitForAsync(() => readFile(file, "utf8").catch(() => undefined), 15_000),
      "OK",
    );

    const settledSnapshot = await waitFor(() => {
      const snapshot = buildConversationSnapshot(harness.projection());
      return snapshot.pendingInteractions.length === 0 ? snapshot : undefined;
    }, 15_000);
    const row = settledSnapshot.rows.window.find((item) => item.kind === "toolCall");
    assert.equal(row?.kind === "toolCall" ? row.status : "", "success");
  } finally {
    await stop(harness);
  }
});

test("pi integration: deny blocks the tool, the row is cancelled, no file is written", async () => {
  const harness = await startSession("build");
  const file = join(harness.workspace, "perm-denied.txt");
  try {
    currentFaux().setResponses([
      fauxAssistantMessage([fauxToolCall("write", { path: "perm-denied.txt", content: "nope" })], {
        stopReason: "toolUse",
      }),
      fauxAssistantMessage("denied"),
    ]);
    await harness.engine.submit(harness.sessionId, "create perm-denied.txt", {
      requestId: "req-build-2",
      mode: "build",
    });
    const interaction = await waitFor(() =>
      pending(buildConversationSnapshot(harness.projection())),
    );
    await harness.engine.resolveApproval(harness.sessionId, interaction.interactionId, {
      optionId: "deny",
      freeText: "not this time",
    });
    await waitFor(() => harness.events.find((event) => event.type === "approval_settled"));
    await waitFor(() => {
      const snapshot = buildConversationSnapshot(harness.projection());
      const row = snapshot.rows.window.find((item) => item.kind === "toolCall");
      return row?.kind === "toolCall" && row.status === "cancelled" ? row : undefined;
    }, 15_000);
    await assert.rejects(readFile(file, "utf8"));
  } finally {
    await stop(harness);
  }
});

test("pi integration: edit mode writes without a card; abort while pending cancels", async () => {
  const harness = await startSession("edit");
  const file = join(harness.workspace, "auto.txt");
  try {
    currentFaux().setResponses([
      fauxAssistantMessage([fauxToolCall("write", { path: "auto.txt", content: "auto" })], {
        stopReason: "toolUse",
      }),
      fauxAssistantMessage("done"),
    ]);
    await harness.engine.submit(harness.sessionId, "create auto.txt", {
      requestId: "req-edit-1",
      mode: "edit",
    });
    assert.equal(
      await waitForAsync(() => readFile(file, "utf8").catch(() => undefined), 15_000),
      "auto",
    );
    assert.equal(harness.events.filter((event) => event.type === "approval_request").length, 0);

    // Second run in build mode waits for approval; abort must settle it, not hang.
    currentFaux().appendResponses([
      fauxAssistantMessage([fauxToolCall("bash", { command: "touch never.txt" })], {
        stopReason: "toolUse",
      }),
      fauxAssistantMessage("stopped"),
    ]);
    await harness.engine.submit(harness.sessionId, "run bash", {
      requestId: "req-edit-2",
      mode: "build",
    });
    const interaction = await waitFor(() =>
      pending(buildConversationSnapshot(harness.projection())),
    );
    await harness.engine.abort(harness.sessionId);
    const settled = await waitFor(() =>
      harness.events.find(
        (event) =>
          event.type === "approval_settled" && event.interactionId === interaction.interactionId,
      ),
    );
    assert.equal(settled.type === "approval_settled" ? settled.outcome : "", "aborted");
    const snapshot = buildConversationSnapshot(harness.projection());
    assert.equal(snapshot.pendingInteractions.length, 0);
    const bashRow = snapshot.rows.window.find(
      (item) => item.kind === "toolCall" && item.toolName === "Bash",
    );
    assert.equal(bashRow?.kind === "toolCall" ? bashRow.status : "", "cancelled");
  } finally {
    await stop(harness);
  }
});

test("pi integration: read-only ls runs without a card in build mode", async () => {
  const harness = await startSession("build");
  try {
    currentFaux().setResponses([
      fauxAssistantMessage([fauxToolCall("ls", { path: "." })], { stopReason: "toolUse" }),
      fauxAssistantMessage("listed"),
    ]);
    await harness.engine.submit(harness.sessionId, "list files", {
      requestId: "req-build-3",
      mode: "build",
    });
    await waitFor(() => harness.events.find((event) => event.type === "tool_call_end"), 15_000);
    assert.equal(harness.events.filter((event) => event.type === "approval_request").length, 0);
  } finally {
    await stop(harness);
  }
});
