import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  fauxAssistantMessage,
  registerFauxProvider,
  type FauxProviderRegistration,
} from "@earendil-works/pi-ai/compat";
import { ModelRuntime, SessionManager, SettingsManager } from "@earendil-works/pi-coding-agent";
import { conversationSnapshotSchema } from "@nex/shared/nex-protocol-v4";
import { PiEngine } from "../src/engine/pi/piEngine.js";
import { createLogger } from "../src/logger.js";
import { applyEngineEvent } from "../src/projection/fold.js";
import { projectHistory } from "../src/projection/restore.js";
import { buildConversationSnapshot } from "../src/projection/snapshot.js";
import { createProjection, type ProjectionState } from "../src/projection/state.js";
import type { EngineEvent, EngineSessionState } from "../src/engine/types.js";

/**
 * Row identity across the live and restore paths (research §0-a/§0-b), driven by the REAL
 * pi engine with the faux provider:
 * - live rows learn `pi:<entryId>` from `message_entry`;
 * - a restart re-derives exactly the same ids from `SessionManager.getBranch()`;
 * - a compacted session still restores every pre-compaction row plus the compact marker.
 */

const logger = createLogger("error");
const fauxs: FauxProviderRegistration[] = [];

function currentFaux(): FauxProviderRegistration {
  const faux = fauxs[fauxs.length - 1];
  if (!faux) throw new Error("no faux provider registered");
  return faux;
}

async function makeRuntime(): Promise<ModelRuntime> {
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
  fauxs.push(faux);
  return runtime;
}

interface Harness {
  root: string;
  workspace: string;
  engine: PiEngine;
  sessionId: string;
  projection: () => ProjectionState;
  events: EngineEvent[];
}

async function start(options: { settings?: SettingsManager } = {}): Promise<Harness> {
  const root = await mkdtemp(join(tmpdir(), "nex-identity-"));
  const workspace = join(root, "workspace");
  await import("node:fs/promises").then((fs) => fs.mkdir(workspace, { recursive: true }));
  const engine = new PiEngine({
    dataDir: join(root, "data"),
    logger,
    createModelRuntime: makeRuntime,
    createSettingsManager: () => options.settings ?? SettingsManager.inMemory(),
    titleSettings: { enabled: false },
  });
  await engine.init();
  const { sessionId } = await engine.createSession(workspace, {
    modelSelection: { providerId: "faux", modelId: "faux-1", options: { reasoningLevel: "off" } },
  });
  const events: EngineEvent[] = [];
  let projection = createProjection({
    sessionId,
    logEpoch: "identity-epoch",
    createdAt: Date.now(),
  });
  engine.subscribe(sessionId, (event) => {
    events.push(event);
    projection = applyEngineEvent(projection, event);
  });
  return { root, workspace, engine, sessionId, projection: () => projection, events };
}

async function waitFor<T>(read: () => T | undefined, timeoutMs = 10_000): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = read();
    if (value !== undefined) return value;
    await new Promise((resolve) => setTimeout(resolve, 15));
  }
  throw new Error("timed out waiting for condition");
}

async function stop(h: Harness): Promise<void> {
  await h.engine.abort(h.sessionId).catch(() => undefined);
  await new Promise((resolve) => setTimeout(resolve, 150));
  await h.engine.dispose();
  currentFaux().unregister();
  await rm(h.root, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 });
}

function entityIdsOf(rows: Array<{ entityId?: string; kind: string }>): string[] {
  return rows.map((row) => `${row.kind}:${row.entityId ?? ""}`);
}

test("identity: one live turn learns the pi entry ids, and a restart restores identical entityIds", async () => {
  const h = await start();
  try {
    currentFaux().setResponses([fauxAssistantMessage("pong from faux")]);
    await h.engine.submit(h.sessionId, "ping", { requestId: "req-1" });

    // Live: rows exist before pi persists, then `message_entry` attaches the ids.
    const live = await waitFor(() => {
      const snapshot = buildConversationSnapshot(h.projection());
      const user = snapshot.rows.window.find((row) => row.kind === "userInput");
      const assistant = snapshot.rows.window.find((row) => row.kind === "assistantText");
      return user?.entityId && assistant?.entityId ? snapshot : undefined;
    });
    const liveIds = entityIdsOf(live.rows.window);
    assert.match(liveIds[0] ?? "", /^turnHeader:pi:turn:/u);
    assert.match(liveIds[1] ?? "", /^userInput:pi:[0-9a-f]+$/u);
    assert.match(liveIds[2] ?? "", /^assistantText:pi:[0-9a-f]+:text:0$/u);
    conversationSnapshotSchema.parse(live);

    // Restart: a brand-new engine over the same data dir reads the same session file.
    const meta = (await h.engine.listSessions(h.workspace)).find(
      (item) => item.sessionId === h.sessionId,
    );
    assert.ok(meta);
    await h.engine.dispose();
    const restarted = new PiEngine({
      dataDir: join(h.root, "data"),
      logger,
      createModelRuntime: makeRuntime,
      createSettingsManager: () => SettingsManager.inMemory(),
      titleSettings: { enabled: false },
    });
    await restarted.init();
    await restarted.openSession(h.workspace, h.sessionId);
    const history = await restarted.history(h.sessionId);
    assert.equal(history.length, 2);
    const restoredState = projectHistory(
      (await restarted.listSessions(h.workspace)).find(
        (item) => item.sessionId === h.sessionId,
      ) as EngineSessionState,
      history,
      "identity-epoch-2",
    );
    const restored = conversationSnapshotSchema.parse(buildConversationSnapshot(restoredState));
    // rowIds are renumbered per logEpoch, entityIds must not be.
    assert.notEqual(restored.logEpoch, live.logEpoch);
    assert.deepEqual(entityIdsOf(restored.rows.window), liveIds);
    // turnIds are the UI's render-unit key; live numbering must match the restore path.
    assert.deepEqual(
      restored.rows.window.map((row) => row.turnId),
      live.rows.window.map((row) => row.turnId),
    );
    await restarted.dispose();
    await rm(h.root, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 });
  } catch (error) {
    await stop(h).catch(() => undefined);
    throw error;
  }
});

test("identity: compaction keeps every pre-compaction row and adds a compact marker on restore", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-compact-"));
  const workspace = join(root, "workspace");
  await import("node:fs/promises").then((fs) => fs.mkdir(workspace, { recursive: true }));
  const sessionDir = join(root, "sessions");
  try {
    // Build a real session file: two complete turns, then a compaction entry appended with
    // pi's own SessionManager API (what `AgentSession.compact()` does after summarizing).
    const manager = SessionManager.create(workspace, sessionDir);
    const userA = manager.appendMessage({
      role: "user",
      content: "first question",
      timestamp: Date.now(),
    });
    manager.appendMessage(
      fauxAssistantMessage("first answer", { timestamp: Date.now() + 1 }) as never,
    );
    manager.appendMessage({
      role: "user",
      content: "second question",
      timestamp: Date.now() + 2,
    });
    manager.appendMessage(
      fauxAssistantMessage("second answer", { timestamp: Date.now() + 3 }) as never,
    );
    const compactionId = manager.appendCompaction(
      "summary of the first two turns",
      userA,
      1234,
      undefined,
      false,
    );
    const sessionId = manager.getSessionId();
    const sessionFile = manager.getSessionFile();
    assert.ok(sessionFile);

    // Open it through the engine: `history()` must read the raw branch, not the projected
    // context (which drops the summarized rows).
    const engine = new PiEngine({
      dataDir: join(root, "data"),
      logger,
      createModelRuntime: makeRuntime,
      createSettingsManager: () => SettingsManager.inMemory(),
      titleSettings: { enabled: false },
    });
    await engine.init();
    // Copy the file into the runner's session dir layout (findSessionFile globs by id).
    const targetDir = join(root, "data", "sessions");
    await import("node:fs/promises").then(async (fs) => {
      const { createHash } = await import("node:crypto");
      const digest = createHash("sha1").update(workspace).digest("hex");
      const dir = join(targetDir, digest);
      await fs.mkdir(dir, { recursive: true });
      await fs.copyFile(sessionFile, join(dir, `${Date.now()}_${sessionId}.jsonl`));
    });
    await engine.openSession(workspace, sessionId);

    const history = await engine.history(sessionId);
    assert.deepEqual(
      history.map((message) => message.role),
      ["user", "assistant", "user", "assistant", "compaction"],
    );
    const compaction = history.at(-1);
    assert.equal(compaction?.role === "compaction" ? compaction.entryId : "", compactionId);
    // Every message keeps its persisted entry id (identity survives compaction).
    for (const message of history.slice(0, 4)) {
      assert.ok(message.role !== "compaction" && message.entryId);
    }

    const meta = (await engine.listSessions(workspace)).find(
      (item) => item.sessionId === sessionId,
    );
    assert.ok(meta);
    const state = projectHistory({ ...meta, phase: "completedSuccess" }, history, "compact-epoch");
    const snapshot = conversationSnapshotSchema.parse(buildConversationSnapshot(state));
    assert.deepEqual(
      snapshot.rows.window.map((row) => row.kind),
      [
        "turnHeader",
        "userInput",
        "assistantText",
        "turnHeader",
        "userInput",
        "assistantText",
        "timelineMarker",
      ],
    );
    const marker = snapshot.rows.window.at(-1);
    assert.equal(marker?.kind, "timelineMarker");
    assert.deepEqual(marker?.kind === "timelineMarker" ? marker.marker : null, {
      type: "compact",
      origin: "auto",
      status: "success",
      tokensBefore: 1234,
    });
    assert.equal(marker?.entityId, `pi:${compactionId}`);
    // The marker belongs to the turn it closed (assistantWork lane → "already worked"),
    // not to a phantom marker-only unit.
    assert.equal(marker?.turnId, "turn-2");
    assert.equal(marker?.kind === "timelineMarker" ? marker.lane : "", "assistantWork");
    // The pre-compaction turns are still there — exactly what `session.messages` loses.
    assert.deepEqual(
      snapshot.rows.window
        .filter((row) => row.kind === "userInput")
        .map((row) => (row.kind === "userInput" ? row.text : "")),
      ["first question", "second question"],
    );
    await engine.dispose();
  } finally {
    await rm(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 });
  }
});
