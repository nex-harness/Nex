import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, readdir, realpath, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  fauxAssistantMessage,
  registerFauxProvider,
  type FauxProviderRegistration,
  type FauxResponseStep,
} from "@earendil-works/pi-ai/compat";
import type { TranscriptContext } from "@earendil-works/pi-ai";
import { ModelRuntime, SettingsManager } from "@earendil-works/pi-coding-agent";
import {
  commandAckSchema,
  conversationSnapshotSchema,
  type CommandEnvelope,
  type ConversationSnapshot,
} from "@nex/shared/nex-protocol-v4";
import { PiEngine } from "../src/engine/pi/piEngine.js";
import type { EngineMessage, EngineSessionState } from "../src/engine/types.js";
import { createLogger } from "../src/logger.js";
import { projectHistory } from "../src/projection/restore.js";
import { buildConversationSnapshot } from "../src/projection/snapshot.js";
import type { CompleteConversationWireFrame } from "../src/v4/envelopes.js";
import { ConversationPublisher } from "../src/v4/publisher.js";

/**
 * Batch 2d against the REAL pi engine and pi-ai's faux provider: compact (success / noop /
 * cancel / queued), edit rewinds and re-runs (live + restore), retry, and fork (child file,
 * fork notice on restore, parent untouched). Every ACK/snapshot is parsed with the real zod
 * schemas (docs/next/conversation-features.md acceptance).
 */

const logger = createLogger("error");
const fauxs: FauxProviderRegistration[] = [];

function currentFaux(): FauxProviderRegistration {
  const faux = fauxs[fauxs.length - 1];
  if (!faux) throw new Error("no faux provider registered");
  return faux;
}

/** `contextWindow` is small so pi's own automatic threshold compaction can be exercised. */
let modelContextWindow = 200_000;
/** Per-test settings (e.g. a `keepRecentTokens` budget the test transcript can exceed). */
let settingsOverride: Record<string, unknown> | null = null;

async function makeRuntime(): Promise<ModelRuntime> {
  const faux = registerFauxProvider({
    models: [{ id: "faux-1", name: "Faux", contextWindow: modelContextWindow }],
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

interface Harness {
  root: string;
  workspace: string;
  engine: PiEngine;
  publisher: ConversationPublisher;
  sessionId: string;
  frames: CompleteConversationWireFrame[];
  snapshot: () => ConversationSnapshot;
  send: (text: string) => Promise<ReturnType<typeof commandAckSchema.parse>>;
  command: (
    type: CommandEnvelope["type"],
    payload?: Record<string, unknown>,
  ) => Promise<ReturnType<typeof commandAckSchema.parse>>;
}

function snapshotOf(frames: CompleteConversationWireFrame[]): ConversationSnapshot {
  const last = frames[frames.length - 1];
  if (!last || last.frame.payload.kind !== "snapshot") throw new Error("no snapshot");
  return conversationSnapshotSchema.parse(last.frame.payload.snapshot);
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
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error(`timed out waiting for ${label}`);
}

/**
 * ~28k chars per message (~7k tokens), three messages ≈ 21k tokens: below pi's
 * 200k − 16384 threshold (no automatic compaction during the turns) and above the 20k
 * `keepRecentTokens` budget (a manual compact has something to summarize).
 */
const LONG = "context line ".repeat(3000);

async function start(): Promise<Harness> {
  modelContextWindow = 200_000;
  // `settingsOverride` is set by a test BEFORE `start()`; only reset after a run.
  const root = await mkdtemp(join(tmpdir(), "nex-conv-pi-"));
  const workspace = join(root, "workspace");
  await mkdir(workspace, { recursive: true });
  const resolved = await realpath(workspace);
  const engine = new PiEngine({
    dataDir: join(root, "data"),
    logger,
    createModelRuntime: makeRuntime,
    createSettingsManager: () =>
      settingsOverride
        ? SettingsManager.inMemory(settingsOverride as never)
        : SettingsManager.inMemory(),
    titleSettings: { enabled: false },
  });
  await engine.init();
  const publisher = new ConversationPublisher({ engine, logger });
  const frames: CompleteConversationWireFrame[] = [];
  publisher.addConversationSink(resolved, (frame) => frames.push(frame));
  const created = commandAckSchema.parse(
    await publisher.sendCommand({
      workspacePath: resolved,
      envelope: envelope({
        type: "createSession",
        sessionId: null,
        payload: {
          workspaceId: resolved,
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
  const sessionId = created.result?.type === "createSession" ? created.result.sessionId : "";
  publisher.subscribeConversation({ workspacePath: resolved, sessionId });
  await waitFor(() => (frames.length > 0 ? true : undefined), "initial frame");
  const snapshot = () => snapshotOf(frames);
  const command = async (type: CommandEnvelope["type"], payload: Record<string, unknown> = {}) => {
    const current = snapshot();
    const ack = commandAckSchema.parse(
      await publisher.sendCommand({
        workspacePath: resolved,
        envelope: envelope({
          type,
          sessionId,
          payload,
          baseRevision: current.revision,
          baseLogEpoch: current.logEpoch,
        }),
      }),
    );
    if (ack.status === "accepted" || ack.status === "noop") {
      await waitFor(
        () => (snapshot().revision >= ack.revisionAtDecision ? true : undefined),
        `${type} snapshot`,
      );
    }
    return ack;
  };
  const send = (text: string) => command("sendText", { text });
  return {
    root,
    workspace: resolved,
    engine,
    publisher,
    sessionId,
    frames,
    snapshot,
    send,
    command,
  };
}

async function cleanup(h: Harness): Promise<void> {
  await h.publisher.dispose();
  await h.engine.dispose();
  currentFaux().unregister();
  await new Promise((resolve) => setTimeout(resolve, 50));
  await rm(h.root, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 });
}

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

function compactMarkers(snapshot: ConversationSnapshot) {
  return snapshot.rows.window.flatMap((row) =>
    row.kind === "timelineMarker" && row.marker.type === "compact" ? [row.marker] : [],
  );
}

/** Sends and waits for the reply to land (phases alone can coincide with a stale frame). */
async function sendAndWait(h: Harness, text: string, reply: string): Promise<void> {
  const before = h.snapshot().rows.window.filter((row) => row.kind === "assistantText").length;
  const ack = await h.send(text);
  assert.equal(ack.status, "accepted");
  // The row count alone can match a stale frame when replies are identical: wait for both.
  // A trailing `settled` event may still be in flight right after the reply row, so the
  // count is the reliable signal (phases are checked by the callers that need them).
  await waitFor(
    () => {
      const snap = h.snapshot();
      const count = snap.rows.window.filter((row) => row.kind === "assistantText").length;
      return count > before && hasText(snap, reply) ? true : undefined;
    },
    `turn completed (${reply.slice(0, 12)}, before ${before})`,
  );
}

function userRows(snapshot: ConversationSnapshot, text: string) {
  return snapshot.rows.window.filter((row) => row.kind === "userInput" && row.text === text);
}

test("pi + faux compact: manual success marker, then a second one is noop", async () => {
  // 3 turns ≈ 9.4k tokens: below the 200k − 16384 auto threshold, above this keep budget.
  settingsOverride = {
    compaction: { enabled: true, reserveTokens: 16_384, keepRecentTokens: 3_000 },
  };
  const h = await start();
  try {
    stock(1, () => "zz-small");
    for (let index = 0; index < 3; index += 1) {
      await sendAndWait(h, `${LONG} question ${index}`, "zz-small");
    }
    const ack = await h.command("compact");
    assert.equal(ack.status, "accepted");
    const done = await waitFor(() => {
      const marker = compactMarkers(h.snapshot())[0];
      return marker && marker.status !== "running" ? marker : undefined;
    }, "compact terminal");
    assert.equal(done.status, "success");
    assert.equal(done.origin, "manual");
    assert.ok((done.tokensBefore ?? 0) > 0);
    // Pre-compaction rows survive (the marker is a boundary, not a truncation).
    assert.ok(userRows(h.snapshot(), `${LONG} question 0`).length > 0);
    // Immediately compacting again finds nothing to summarize (pi: "Already compacted").
    const second = await h.command("compact");
    assert.equal(second.status, "accepted");
    const statuses = await waitFor(() => {
      const markers = compactMarkers(h.snapshot());
      return markers.length === 2 && markers.every((marker) => marker.status !== "running")
        ? markers.map((marker) => marker.status)
        : undefined;
    }, "second compact terminal");
    assert.deepEqual(statuses, ["success", "noop"]);
    // The manual origin/source survive a restore through the metadata row.
    const restored = await restoreSnapshot(h, await h.engine.history(h.sessionId));
    const restoredMarker = compactMarkers(restored)[0];
    assert.equal(restoredMarker?.origin, "manual");
    assert.equal(restoredMarker?.status, "success");
  } finally {
    await cleanup(h);
  }
});

test("pi + faux compact: a small session is noop, and the marker says so", async () => {
  const h = await start();
  try {
    stock(1, () => "short answer");
    await sendAndWait(h, "short question", "short answer");
    const ack = await h.command("compact");
    assert.equal(ack.status, "accepted");
    const marker = await waitFor(() => {
      const first = compactMarkers(h.snapshot())[0];
      return first && first.status !== "running" ? first : undefined;
    }, "compact noop");
    assert.equal(marker.status, "noop");
  } finally {
    await cleanup(h);
  }
});

test("pi + faux compact: Stop cancels a running compaction (marker cancelled)", async () => {
  settingsOverride = {
    compaction: { enabled: true, reserveTokens: 16_384, keepRecentTokens: 3_000 },
  };
  const h = await start();
  try {
    stock(1, () => "zz-small");
    for (let index = 0; index < 3; index += 1) {
      await sendAndWait(h, `${LONG} question ${index}`, "zz-small");
    }
    // The top of the stack now hangs: the summary request stalls and the compact stays
    // running until Stop aborts it.
    const gate = makeGate();
    currentFaux().setResponses([gatedAnswer(gate, "never")]);
    const compacting = h.command("compact");
    await waitFor(
      () => (compactMarkers(h.snapshot())[0]?.status === "running" ? true : undefined),
      "compact running",
    );
    assert.equal(h.snapshot().availability.compact.allowed, false);
    const stopped = h.command("stop");
    // pi's abort can only land while the faux request is in flight (its signal is checked
    // on resolution), so release the stalled answer once Stop has been dispatched.
    setTimeout(() => gate.release(), 200);
    assert.equal((await compacting).status, "accepted");
    await stopped;
    const marker = await waitFor(() => {
      const first = compactMarkers(h.snapshot())[0];
      return first && first.status !== "running" ? first : undefined;
    }, "compact cancelled");
    assert.equal(marker.status, "cancelled");
  } finally {
    await cleanup(h);
  }
});

test("pi + faux compact: queued while busy, drains after the run", async () => {
  settingsOverride = {
    compaction: { enabled: true, reserveTokens: 16_384, keepRecentTokens: 3_000 },
  };
  const h = await start();
  try {
    stock(1, () => "zz-small");
    for (let index = 0; index < 2; index += 1) {
      await sendAndWait(h, `${LONG} question ${index}`, "zz-small");
    }
    // Start a long turn, then queue compacts while it streams. The stalled answer sits on
    // top of the responder; once released, the queued compact's summary finds the responder.
    const gate = makeGate();
    currentFaux().setResponses([
      gatedAnswer(gate, `${LONG} long answer`),
      responder(() => "zz-small"),
    ]);
    const longTurn = await h.send(`${LONG} long question`);
    assert.equal(longTurn.status, "accepted");
    await waitFor(() => (gate.hit ? true : undefined), "long turn in flight");
    await waitFor(
      () => (h.snapshot().control.phase === "running" ? true : undefined),
      "running frame",
    );
    const queued = await h.command("compact");
    assert.equal(queued.status, "accepted");
    // The long turn is now streaming under a real prompt; only the compact is queued.
    await waitFor(
      () => (h.snapshot().queue.items.some((item) => item.kind === "compact") ? true : undefined),
      "compact queued",
    );
    assert.deepEqual(
      h
        .snapshot()
        .queue.items.map((item) => item.kind)
        .filter((kind) => kind === "compact"),
      ["compact"],
    );
    const duplicate = await h.command("compact");
    assert.equal(duplicate.reasonCode, "compactOperationLock");
    gate.release();
    const marker = await waitFor(() => {
      const first = compactMarkers(h.snapshot())[0];
      return first && first.status !== "running" && h.snapshot().queue.items.length === 0
        ? first
        : undefined;
    }, "queued compact ran");
    assert.equal(marker.status, "success");
  } finally {
    await cleanup(h);
  }
});

function makeGate() {
  let release: () => void = () => {};
  const promise = new Promise<void>((resolve) => {
    release = resolve;
  });
  return {
    promise,
    release,
    hit: false,
  };
}

/**
 * Keeps the faux queue stocked: every request re-queues itself FIRST and then answers, so
 * a request that borrows the step still leaves an answer for the next one (compaction
 * summary calls and mid-turn retries share the queue).
 */
function responder(reply: (context: TranscriptContext) => string) {
  const step = async (context: TranscriptContext) => {
    currentFaux().appendResponses([step as unknown as FauxResponseStep]);
    return fauxAssistantMessage(reply(context));
  };
  return step as unknown as FauxResponseStep;
}

/** Queues `count` fresh copies of the responder (one is consumed per request). */
function stock(count: number, reply: (context: TranscriptContext) => string) {
  currentFaux().setResponses(Array.from({ length: count }, () => responder(reply)));
}

/** Short for compaction summaries, long enough for turns to reach the keep budget. */
function autoAnswer(): FauxResponseStep {
  return responder((context) =>
    JSON.stringify(context.messages).includes("# Conversation") ? "summary" : "z".repeat(30_000),
  );
}

/** One-shot answer that resolves only when the test releases the gate. */
function gatedAnswer(gate: { promise: Promise<void>; hit: boolean }, text: string) {
  const step = async () => {
    gate.hit = true;
    await gate.promise;
    return fauxAssistantMessage(text);
  };
  return step as unknown as FauxResponseStep;
}

test("pi + faux edit: rewinds to the latest user turn, re-runs it, old rows stay gone after restore", async () => {
  const h = await start();
  try {
    currentFaux().setResponses([fauxAssistantMessage("first answer")]);
    await sendAndWait(h, "first question", "first answer");
    currentFaux().setResponses([fauxAssistantMessage("second answer")]);
    await sendAndWait(h, "second question", "second answer");
    const userRow = userRows(h.snapshot(), "second question")[0];
    assert.ok(userRow?.entityId && userRow.rowId !== undefined);

    currentFaux().setResponses([fauxAssistantMessage("edited answer")]);
    const ack = await h.command("editUserQuery", {
      target: { rowId: userRow.rowId, entityId: userRow.entityId },
      newText: "edited question",
    });
    assert.equal(ack.status, "accepted");
    assert.equal(ack.result?.type === "editUserQuery" ? ack.result.disposition : "", "rewind");
    await waitFor(
      () =>
        h.snapshot().control.phase === "completedSuccess" &&
        userRows(h.snapshot(), "edited question").length > 0
          ? true
          : undefined,
      "edited turn ran",
    );
    const live = h.snapshot();
    conversationSnapshotSchema.parse(live);
    assert.equal(
      live.rows.window.some((row) => row.kind === "assistantText" && row.text === "edited answer"),
      true,
    );
    assert.equal(
      live.rows.window.some((row) => row.kind === "assistantText" && row.text === "second answer"),
      false,
    );
    assert.equal(
      live.rows.window.some((row) => row.kind === "assistantText" && row.text === "first answer"),
      true,
    );
    // The new user row is editable again (latest) and has a fresh, never-reused rowId.
    const editedRow = userRows(live, "edited question")[0];
    assert.ok(editedRow && editedRow.rowId !== userRow.rowId);

    // Restore: the pi file's branch is the rewritten one — old rows must not come back.
    const history = await h.engine.history(h.sessionId);
    assert.equal(history.filter((message) => message.role === "user").length, 2);
    const restored = await restoreSnapshot(h, history);
    assert.equal(hasText(restored, "second answer"), false);
    assert.ok(hasText(restored, "edited answer"));
  } finally {
    await cleanup(h);
  }
});

test("pi + faux retry: re-sends the original user text (unit-hidden in UI, served here)", async () => {
  const h = await start();
  try {
    currentFaux().setResponses([fauxAssistantMessage("original answer")]);
    await sendAndWait(h, "original question", "original answer");
    const reply = h
      .snapshot()
      .rows.window.find((row) => row.kind === "assistantText" && row.text === "original answer");
    assert.ok(reply?.entityId && reply.rowId !== undefined);
    currentFaux().setResponses([fauxAssistantMessage("retried answer")]);
    const ack = await h.command("retryTurn", {
      target: { rowId: reply.rowId, entityId: reply.entityId },
    });
    assert.equal(ack.status, "accepted");
    await waitFor(
      () =>
        h
          .snapshot()
          .rows.window.some((row) => row.kind === "assistantText" && row.text === "retried answer")
          ? true
          : undefined,
      "retry ran",
    );
    assert.equal(hasText(h.snapshot(), "original answer"), false);
    // A retry of an older reply is refused.
    const again = await h.command("retryTurn", {
      target: { rowId: reply.rowId, entityId: reply.entityId },
    });
    assert.equal(again.status, "rejected");
  } finally {
    await cleanup(h);
  }
});

test("pi + faux fork: child file has the history up to the target + fork notice; parent untouched", async () => {
  const h = await start();
  try {
    currentFaux().setResponses([fauxAssistantMessage("turn one")]);
    await sendAndWait(h, "question one", "turn one");
    currentFaux().setResponses([fauxAssistantMessage("turn two")]);
    await sendAndWait(h, "question two", "turn two");
    currentFaux().setResponses([fauxAssistantMessage("turn three")]);
    await sendAndWait(h, "question three", "turn three");
    // Target: the reply of turn two (a stable, completed turn).
    const target = h
      .snapshot()
      .rows.window.find((row) => row.kind === "assistantText" && row.text === "turn two");
    assert.ok(target?.entityId && target.rowId !== undefined);

    const parentFile = await sessionFileOf(h, h.sessionId);
    const parentBytes = await readFile(parentFile);
    const parentStat = await stat(parentFile);
    const parentRows = JSON.stringify(h.snapshot().rows.window);

    const ack = await h.command("forkAssistant", {
      target: { rowId: target.rowId, entityId: target.entityId },
    });
    assert.equal(ack.status, "accepted");
    const childId = ack.result?.type === "forkAssistant" ? ack.result.sessionId : "";
    assert.ok(childId && childId !== h.sessionId);
    // Parent untouched: same bytes, same rows, no fork marker.
    assert.deepEqual(await readFile(parentFile), parentBytes);
    assert.equal((await stat(parentFile)).size, parentStat.size);
    assert.equal(JSON.stringify(h.snapshot().rows.window), parentRows);
    assert.equal(
      h.snapshot().rows.window.some((row) => row.kind === "timelineMarker"),
      false,
    );

    // Child: listed, titled `Fork of …`, history ends at the target (no "turn three").
    const sessions = await h.engine.listSessions(h.workspace);
    const child = sessions.find((item) => item.sessionId === childId);
    assert.equal(child?.title.startsWith("Fork of "), true);
    assert.equal(child?.titleSource, "generated");
    const history = await h.engine.history(childId);
    const texts = history.flatMap((message) => messageTexts(message));
    assert.ok(texts.includes("turn two"));
    assert.equal(texts.includes("turn three"), false);
    assert.ok(history.some((message) => message.role === "runnerMarker"));

    // Child restore: the `nex.fork` entry becomes a forkNotice marker at the head.
    const restored = await restoreSnapshot(h, history, childId);
    const notice = restored.rows.window.find(
      (row) => row.kind === "timelineMarker" && row.marker.type === "forkNotice",
    );
    assert.ok(notice);
    assert.equal(
      notice.kind === "timelineMarker" && notice.marker.type === "forkNotice"
        ? notice.marker.parentSessionId
        : "",
      h.sessionId,
    );
    // The child is a real session on disk and in the sidebar index.
    await h.engine.openSession(h.workspace, childId);
    assert.ok(await sessionFileOf(h, childId));
  } finally {
    await cleanup(h);
  }
});

test("pi + faux: pi's own automatic (overflow) compaction produces an origin:auto marker", async () => {
  // A tiny context window forces pi to compact on its own before a request.
  modelContextWindow = 8_000;
  const root = await mkdtemp(join(tmpdir(), "nex-conv-pi-auto-"));
  const workspace = join(root, "workspace");
  await mkdir(workspace, { recursive: true });
  const resolved = await realpath(workspace);
  let faux: FauxProviderRegistration | undefined;
  const engine = new PiEngine({
    dataDir: join(root, "data"),
    logger,
    createModelRuntime: async () => {
      const runtime = await makeRuntime();
      faux = currentFaux();
      return runtime;
    },
    // `start()` may have left a per-test compaction budget behind; this test needs pi's
    // defaults (2000-token reserve is irrelevant — the context window is 8000).
    createSettingsManager: () => SettingsManager.inMemory(),
    titleSettings: { enabled: false },
  });
  await engine.init();
  // Drive the engine directly: the workspace is fresh, so there are no rows to project.
  let sessionId = "";
  try {
    if (!faux) throw new Error("faux provider was not registered");
    ({ sessionId } = await engine.createSession(resolved, {
      modelSelection: { providerId: "faux", modelId: "faux-1", options: { reasoningLevel: "off" } },
    }));
    const markers: Array<{ origin: string; status: string; tokensBefore?: number }> = [];
    engine.subscribe(sessionId, (event) => {
      if (event.type === "compact_start" && event.reason !== "manual") {
        markers.push({ origin: "auto", status: "running" });
        return;
      }
      if (event.type === "compact_end" && event.reason !== "manual") {
        const open = markers.findIndex((marker) => marker.status === "running");
        const next = {
          origin: "auto",
          status: event.result ? "success" : event.aborted ? "cancelled" : "noop",
          ...(event.result ? { tokensBefore: event.result.tokensBefore } : {}),
        };
        if (open >= 0) markers[open] = next;
        else markers.push(next);
      }
    });
    // Every request (turn, retry or summary) gets an answer; summaries are short.
    faux.setResponses([autoAnswer()]);
    for (let index = 0; index < 4; index += 1) {
      await engine.submit(sessionId, `${"z".repeat(30_000)} q${index}`, {
        requestId: `auto-${index}`,
      });
      await engine.waitForIdle(sessionId);
    }
    const marker = markers.find((item) => item.status === "success");
    assert.ok(marker, JSON.stringify(markers));
    assert.equal(marker.origin, "auto");
    assert.ok((marker.tokensBefore ?? 0) > 0);
  } finally {
    await engine.dispose();
    faux?.unregister();
    await new Promise((resolve) => setTimeout(resolve, 50));
    await rm(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 });
  }
});

/** Text of any user/assistant row equal to `text`. */
function hasText(snapshot: ConversationSnapshot, text: string): boolean {
  return snapshot.rows.window.some(
    (row) =>
      (row.kind === "assistantText" || row.kind === "userInput") &&
      typeof row.text === "string" &&
      row.text.startsWith(text),
  );
}

function messageTexts(message: EngineMessage): string[] {
  if (message.role === "user") return [message.text];
  if (message.role === "assistant") {
    return message.parts.flatMap((part) => (part.type === "text" ? [part.text] : []));
  }
  return [];
}

async function restoreSnapshot(h: Harness, history: EngineMessage[], sessionId = h.sessionId) {
  const sessions = await h.engine.listSessions(h.workspace);
  const meta = sessions.find((item) => item.sessionId === sessionId) as EngineSessionState;
  return conversationSnapshotSchema.parse(
    buildConversationSnapshot(projectHistory(meta, history, "restore-epoch")),
  );
}

async function sessionFileOf(h: Harness, sessionId: string): Promise<string> {
  const digest = createHash("sha1").update(h.workspace).digest("hex");
  const dir = join(h.root, "data", "sessions", digest);
  const files = await readdir(dir);
  const file = files.find((name) => name.endsWith(`_${sessionId}.jsonl`));
  if (!file) throw new Error(`no session file for ${sessionId}`);
  return join(dir, file);
}
