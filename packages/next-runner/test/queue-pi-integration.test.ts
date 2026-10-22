import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdir, mkdtemp, realpath, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { registerFauxProvider } from "@earendil-works/pi-ai/compat";
import type { FauxProviderRegistration, FauxResponseStep } from "@earendil-works/pi-ai/compat";
import { ModelRuntime, SettingsManager } from "@earendil-works/pi-coding-agent";
import type { TranscriptContext } from "@earendil-works/pi-ai";
import {
  commandAckSchema,
  conversationSnapshotSchema,
  type CommandEnvelope,
  type ConversationSnapshot,
} from "@nex/shared/nex-protocol-v4";
import { PiEngine } from "../src/engine/pi/piEngine.js";
import { createLogger } from "../src/logger.js";
import { ConversationPublisher } from "../src/v4/publisher.js";
import type { CompleteConversationWireFrame } from "../src/v4/envelopes.js";

/**
 * Runner-owned queue against the REAL pi engine and pi-ai's faux provider: two messages sent
 * while a reply streams must run in order; Stop pauses the queue; resuming drains it. This is
 * the integration proof for docs/next/conversation-features.md decision 4 — pi's own queues
 * are never used for queued text.
 */

const logger = createLogger("error");

interface Harness {
  root: string;
  workspace: string;
  engine: PiEngine;
  publisher: ConversationPublisher;
  faux: FauxProviderRegistration;
  sessionId: string;
  frames: CompleteConversationWireFrame[];
  snapshot: () => ConversationSnapshot;
  send: (payload: Record<string, unknown>) => Promise<ReturnType<typeof commandAckSchema.parse>>;
  command: (
    type: CommandEnvelope["type"],
    payload: Record<string, unknown>,
  ) => Promise<ReturnType<typeof commandAckSchema.parse>>;
}

function lastSnapshotOf(frames: CompleteConversationWireFrame[]): ConversationSnapshot {
  const last = frames[frames.length - 1];
  if (!last || last.frame.payload.kind !== "snapshot") throw new Error("no snapshot");
  return last.frame.payload.snapshot;
}

function lastUserText(context: TranscriptContext): string {
  for (let index = context.messages.length - 1; index >= 0; index -= 1) {
    const message = context.messages[index];
    if (!message || message.role !== "user") continue;
    const content = message.content;
    return typeof content === "string"
      ? content
      : content
          .filter((block) => block.type === "text")
          .map((block) => (block.type === "text" ? block.text : ""))
          .join(" ");
  }
  return "";
}

/**
 * Answers every request with `reply:<last user text>` and puts itself back at the tail of the
 * faux queue, so a draining queue never runs out of scripted responses. The first request can
 * be gated so a test can send more input while the run is genuinely busy.
 */
function makeResponder(h: () => Harness, gate?: { promise: Promise<void>; mark: () => void }) {
  let first = true;
  const responder = async (context: TranscriptContext) => {
    h().faux.appendResponses([responder as unknown as FauxResponseStep]);
    const prompt = lastUserText(context);
    if (first && gate) {
      first = false;
      gate.mark();
      await gate.promise;
    }
    return {
      role: "assistant" as const,
      content: [{ type: "text" as const, text: `reply:${prompt}` }],
      stopReason: "stop" as const,
    };
  };
  return responder as unknown as FauxResponseStep;
}

function makeGate(): {
  promise: Promise<void>;
  release: () => void;
  mark: () => void;
  hit: () => boolean;
} {
  let release: () => void = () => {};
  let hit = false;
  const promise = new Promise<void>((resolve) => {
    release = resolve;
  });
  return {
    promise,
    release: () => release(),
    mark: () => {
      hit = true;
    },
    hit: () => hit,
  };
}

async function waitFor<T>(
  read: () => Promise<T | undefined> | T | undefined,
  label: string,
  timeoutMs = 10_000,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = await read();
    // Callers use booleans ("has this happened yet") as well as object-or-undefined reads.
    if (value) return value;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error(`timed out waiting for ${label}`);
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

/** Creates the session shell; each test seeds its own responder afterwards. */
async function createHarness(): Promise<Harness> {
  const root = await mkdtemp(join(tmpdir(), "nex-queue-pi-"));
  // $TMPDIR is a symlink on macOS (/var → /private/var); the engine announces the resolved
  // path, and conversation frames are scoped by exact workspace string equality.
  const workspace = await realpath(join(root, "workspace")).catch(async () => {
    await mkdir(join(root, "workspace"), { recursive: true });
    return realpath(join(root, "workspace"));
  });

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
    // Titles are irrelevant here; a title call must never steal a scripted response.
    titleSettings: { enabled: false },
  });
  await engine.init();
  const publisher = new ConversationPublisher({ engine, logger });
  const frames: CompleteConversationWireFrame[] = [];
  publisher.addConversationSink(workspace, (frame) => frames.push(frame));

  const created = commandAckSchema.parse(
    await publisher.sendCommand({
      workspacePath: workspace,
      envelope: envelope({
        type: "createSession",
        sessionId: null,
        payload: {
          // The faux provider is the only configured model; without an explicit selection pi
          // refuses to start a run.
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
  publisher.subscribeConversation({ workspacePath: workspace, sessionId });
  await waitFor(() => frames.length > 0, "initial frame");
  if (!faux) throw new Error("faux provider was not registered");

  const snapshot = () => conversationSnapshotSchema.parse(lastSnapshotOf(frames));

  const send = async (payload: Record<string, unknown>) => {
    const ack = commandAckSchema.parse(
      await publisher.sendCommand({
        workspacePath: workspace,
        envelope: envelope({ type: "sendText", sessionId, payload }),
      }),
    );
    if (ack.status === "accepted" || ack.status === "noop") {
      await waitFor(
        () => lastSnapshotOf(frames).revision >= ack.revisionAtDecision,
        "snapshot for send ack",
      );
    }
    return ack;
  };

  const command = async (type: CommandEnvelope["type"], payload: Record<string, unknown>) => {
    const ack = commandAckSchema.parse(
      await publisher.sendCommand({
        workspacePath: workspace,
        envelope: envelope({ type, sessionId, payload, baseRevision: snapshot().revision }),
      }),
    );
    if (ack.status === "accepted" || ack.status === "noop") {
      await waitFor(
        () => lastSnapshotOf(frames).revision >= ack.revisionAtDecision,
        `${type} snapshot`,
      );
    }
    return ack;
  };

  return {
    root,
    workspace,
    engine,
    publisher,
    faux,
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
  await new Promise((resolve) => setTimeout(resolve, 50));
  await rm(h.root, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 });
}

function assistantTexts(h: Harness): string[] {
  return h
    .snapshot()
    .rows.window.filter((row) => row.kind === "assistantText")
    .map((row) => (row.kind === "assistantText" ? row.text : ""));
}

test("pi + faux: two messages queued while busy run in order, one per settle", async () => {
  const h = await createHarness();
  try {
    const gate = makeGate();
    h.faux.setResponses([makeResponder(() => h, gate)]);

    const first = await h.send({ text: "first prompt" });

    assert.equal(first.status, "accepted");
    await waitFor(() => (gate.hit() ? true : undefined), "first request in flight");

    const second = await h.send({ text: "second prompt" });
    const third = await h.send({ text: "third prompt" });
    assert.equal(second.result?.type === "inputAccepted" ? second.result.delivery : "", "queue");
    assert.equal(third.result?.type === "inputAccepted" ? third.result.delivery : "", "queue");
    assert.deepEqual(
      h.snapshot().queue.items.map((item) => item.text),
      ["second prompt", "third prompt"],
    );

    gate.release();
    const drained = await waitFor(
      () => {
        const snapshot = h.snapshot();
        return snapshot.queue.items.length === 0 && snapshot.control.phase === "completedSuccess"
          ? snapshot
          : undefined;
      },
      "queue drained and run settled",
      20_000,
    );
    conversationSnapshotSchema.parse(drained);
    assert.deepEqual(assistantTexts(h), [
      "reply:first prompt",
      "reply:second prompt",
      "reply:third prompt",
    ]);
  } finally {
    await cleanup(h);
  }
});

test("pi + faux: stop pauses the queue and resume drains it", async () => {
  const h = await createHarness();
  try {
    const gate = makeGate();
    h.faux.setResponses([makeResponder(() => h, gate)]);

    await h.send({ text: "long first" });
    await waitFor(() => (gate.hit() ? true : undefined), "first request in flight");
    const queued = await h.send({ text: "queued while busy" });
    assert.equal(queued.result?.type === "inputAccepted" ? queued.result.delivery : "", "queue");

    // Stop while busy: the run unwinds and the remaining item is held, not replayed.
    // `stop` awaits pi's abort barrier, which cannot complete while the faux provider is
    // still holding the request, so release the gate concurrently with the command.
    const stopPromise = h.command("stop", {});
    gate.release();
    const stop = await stopPromise;
    assert.equal(stop.status, "accepted");
    const paused = await waitFor(
      () => {
        const snapshot = h.snapshot();
        return !snapshot.queue.autoDrain && snapshot.control.phase !== "running"
          ? snapshot
          : undefined;
      },
      "queue paused",
      20_000,
    );
    conversationSnapshotSchema.parse(paused);
    assert.equal(paused.queue.pauseReason, "stopped");
    assert.equal(paused.control.phase, "completedInterrupted");
    assert.equal(paused.queue.items.length, 1);
    // The aborted run produced no reply, and the queued item was not replayed into it.
    await new Promise((resolve) => setTimeout(resolve, 300));
    assert.deepEqual(assistantTexts(h), []);

    // Resume: autoDrain re-arms and the idle session drains immediately.
    const resumed = await h.command("setAutoDrain", { autoDrain: true });
    assert.equal(resumed.status, "accepted");
    const drained = await waitFor(
      () => {
        const snapshot = h.snapshot();
        return snapshot.queue.items.length === 0 &&
          snapshot.control.phase !== "running" &&
          assistantTexts(h).length >= 1
          ? snapshot
          : undefined;
      },
      "resumed drain",
      20_000,
    );
    conversationSnapshotSchema.parse(drained);
    assert.deepEqual(assistantTexts(h), ["reply:queued while busy"]);
    assert.equal(drained.queue.autoDrain, true);
  } finally {
    await cleanup(h);
  }
});

test("pi + faux: guide mode steers into the live run instead of starting a new one", async () => {
  const h = await createHarness();
  try {
    const gate = makeGate();
    h.faux.setResponses([makeResponder(() => h, gate)]);

    await h.command("setFollowupMode", { mode: "guide" });
    await waitFor(() => h.snapshot().config.followupMode === "guide", "guide mode");
    await h.send({ text: "guidable first" });
    await waitFor(() => (gate.hit() ? true : undefined), "first request in flight");

    const guide = await h.send({ text: "guide payload" });
    assert.equal(guide.result?.type === "inputAccepted" ? guide.result.delivery : "", "guide");
    const item = h.snapshot().queue.items[0];
    assert.ok(item);
    assert.equal(item.delivery.admitted, "guide");

    gate.release();
    // pi consumes the steering text at the next model step inside the SAME run: the pending
    // guide item is removed by `queue_update`, and the model answers it as a continuation.
    // The runner never submits a second prompt, so this is one turn with two replies.
    const settled = await waitFor(
      () => {
        const snapshot = h.snapshot();
        return snapshot.queue.items.length === 0 &&
          snapshot.control.phase !== "running" &&
          assistantTexts(h).length >= 2
          ? snapshot
          : undefined;
      },
      "guide consumed and run settled",
      20_000,
    );
    conversationSnapshotSchema.parse(settled);
    assert.deepEqual(assistantTexts(h), ["reply:guidable first", "reply:guide payload"]);
    // The fold opens a turn header per `user_input` event, and a steered message is a user
    // message — so two headers is expected. What matters is that the runner never promoted
    // a queued item: the queue stayed empty and no queue item survived.
    assert.equal(h.snapshot().queue.items.length, 0);
  } finally {
    await cleanup(h);
  }
});
