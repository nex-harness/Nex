import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { commandAckSchema, conversationSnapshotSchema } from "@nex/shared/nex-protocol-v4";
import type { CommandEnvelope } from "@nex/shared/nex-protocol-v4";
import { ConversationPublisher } from "../src/v4/publisher.js";
import type { CompleteConversationWireFrame } from "../src/v4/envelopes.js";
import { FakeEngine } from "./helpers/fakeEngine.js";
import { createLogger } from "../src/logger.js";

/**
 * `switchModelConfig` / `setFollowupMode` through the real command handler and projection.
 * Every emitted ACK/snapshot is parsed with the real @nex/shared zod schemas.
 */

const WORKSPACE = "/workspace/demo";
const logger = createLogger("error");

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

async function waitFor(check: () => boolean, label: string): Promise<void> {
  const deadline = Date.now() + 3_000;
  while (Date.now() < deadline) {
    if (check()) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error(`timed out waiting for ${label}`);
}

interface Harness {
  engine: FakeEngine;
  publisher: ConversationPublisher;
  frames: CompleteConversationWireFrame[];
  sessionId: string;
  lastSnapshot: () => ReturnType<typeof snapshotOf>;
}

function snapshotOf(frames: CompleteConversationWireFrame[]) {
  const last = frames[frames.length - 1];
  return last && last.frame.payload.kind === "snapshot" ? last.frame.payload.snapshot : undefined;
}

async function start(): Promise<Harness> {
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const frames: CompleteConversationWireFrame[] = [];
  publisher.addConversationSink(WORKSPACE, (frame) => frames.push(frame));
  const created = await publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: envelope({ type: "createSession", payload: { workspaceId: WORKSPACE } }),
  });
  const sessionId = created.result?.type === "createSession" ? created.result.sessionId : "";
  publisher.subscribeConversation({ workspacePath: WORKSPACE, sessionId });
  await waitFor(() => frames.length > 0, "initial frame");
  return {
    engine,
    publisher,
    frames,
    sessionId,
    lastSnapshot: () => snapshotOf(frames),
  };
}

test("switchModelConfig: identical values → noop config.unchanged", async () => {
  const h = await start();
  const before = h.lastSnapshot();
  const ack = await h.publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: envelope({
      type: "switchModelConfig",
      sessionId: h.sessionId,
      baseRevision: before?.revision ?? 0,
      baseLogEpoch: before?.logEpoch,
      payload: {
        provider: before?.config.provider,
        model: before?.config.model,
        thought: before?.config.thought,
      },
    }),
  });
  commandAckSchema.parse(ack);
  assert.equal(ack.status, "noop");
  assert.equal(ack.reasonCode, "config.unchanged");
  h.publisher.dispose();
});

test("switchModelConfig: unknown provider → failed provider.notInRegistry", async () => {
  const h = await start();
  const ack = await h.publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: envelope({
      type: "switchModelConfig",
      sessionId: h.sessionId,
      payload: { provider: "nope", model: "missing", thought: "off" },
    }),
  });
  commandAckSchema.parse(ack);
  assert.equal(ack.status, "failed");
  assert.equal(ack.reasonCode, "provider.notInRegistry");
  h.publisher.dispose();
});

test("switchModelConfig: success updates config/modelSelection and appends a modelChange marker", async () => {
  const h = await start();
  // A second model must exist for an identity change to be possible.
  const { SAMPLE_MODEL } = await import("./helpers/fakeEngineData.js");
  h.engine.registerModel({ ...SAMPLE_MODEL, modelId: "faux-2", modelName: "Faux Two" });

  const before = h.lastSnapshot();
  assert.ok(before);
  const ack = await h.publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: envelope({
      type: "switchModelConfig",
      sessionId: h.sessionId,
      baseRevision: before.revision,
      baseLogEpoch: before.logEpoch,
      payload: { provider: SAMPLE_MODEL.providerId, model: "faux-2", thought: "medium" },
    }),
  });
  commandAckSchema.parse(ack);
  assert.equal(ack.status, "accepted");

  await waitFor(() => {
    const snap = h.lastSnapshot();
    return snap?.config.model === "faux-2";
  }, "config.model updated");

  const snap = h.lastSnapshot();
  assert.ok(snap);
  conversationSnapshotSchema.parse(snap);
  assert.equal(snap.config.provider, SAMPLE_MODEL.providerId);
  assert.equal(snap.config.model, "faux-2");
  assert.equal(snap.config.thought, "medium");
  assert.equal(snap.config.modelSelection?.modelId, "faux-2");

  const markers = snap.rows.window.filter((row) => row.kind === "timelineMarker");
  assert.equal(markers.length, 1);
  const marker = markers[0];
  assert.equal(marker?.kind, "timelineMarker");
  if (marker?.kind === "timelineMarker") {
    assert.equal(marker.lane, "lightBoundary");
    assert.deepEqual(marker.marker, {
      type: "modelChange",
      fromProvider: before.config.provider,
      fromModel: before.config.model,
      toProvider: SAMPLE_MODEL.providerId,
      toModel: "faux-2",
      toThought: "medium",
    });
  }
  h.publisher.dispose();
});

test("setFollowupMode persists into metadata and snapshot config", async () => {
  const h = await start();
  const before = h.lastSnapshot();
  assert.equal(before?.config.followupMode, "queue");

  const ack = await h.publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: envelope({
      type: "setFollowupMode",
      sessionId: h.sessionId,
      baseRevision: before?.revision ?? 0,
      baseLogEpoch: before?.logEpoch,
      payload: { mode: "guide" },
    }),
  });
  commandAckSchema.parse(ack);
  assert.equal(ack.status, "accepted");

  await waitFor(() => h.lastSnapshot()?.config.followupMode === "guide", "followupMode=guide");
  const snap = h.lastSnapshot();
  assert.ok(snap);
  conversationSnapshotSchema.parse(snap);
  assert.equal(snap.config.followupMode, "guide");
  assert.equal(
    (await h.engine.listSessions(WORKSPACE)).find((s) => s.sessionId === h.sessionId)?.followupMode,
    "guide",
  );

  // Same value again is a noop, not a silent accepted.
  const repeat = await h.publisher.sendCommand({
    workspacePath: WORKSPACE,
    envelope: envelope({
      type: "setFollowupMode",
      sessionId: h.sessionId,
      payload: { mode: "guide" },
    }),
  });
  commandAckSchema.parse(repeat);
  assert.equal(repeat.status, "noop");
  assert.equal(repeat.reasonCode, "config.unchanged");
  h.publisher.dispose();
});

test("followupMode survives a runner restart (metadata is the owner)", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-followup-restore-"));
  try {
    const { SessionMetaStore } = await import("../src/engine/pi/metaStore.js");
    const store = new SessionMetaStore(join(root, "data"), logger);
    await store.upsert(WORKSPACE, "sess-1", {
      createdAt: 1,
      lastActivityAt: 1,
      phase: "completedSuccess",
      title: "T",
      titleSource: "default",
      followupMode: "guide",
    });
    const reloaded = new SessionMetaStore(join(root, "data"), logger);
    assert.equal((await reloaded.get(WORKSPACE, "sess-1"))?.followupMode, "guide");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
