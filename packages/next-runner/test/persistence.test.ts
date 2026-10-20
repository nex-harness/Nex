import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { SessionMetaStore } from "../src/engine/pi/metaStore.js";
import { projectHistory } from "../src/projection/restore.js";
import { buildConversationSnapshot } from "../src/projection/snapshot.js";
import { conversationSnapshotSchema } from "@nex/shared/nex-protocol-v4";
import { createLogger } from "../src/logger.js";
import type { EngineSessionState } from "../src/engine/types.js";

const logger = createLogger("error");

test("metadata: survives a process restart (new store instance) and keeps tombstones", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-next-meta-"));
  const workspace = "/workspace/demo";
  try {
    const first = new SessionMetaStore(root, logger);
    await first.upsert(workspace, "sess-1", {
      createdAt: 100,
      lastActivityAt: 200,
      phase: "completedSuccess",
      title: "Hello",
      titleSource: "custom",
      seenRequestId: "cmd-1",
    });
    await first.upsert(workspace, "sess-2", { createdAt: 101, lastActivityAt: 201 });
    await first.markDeleted(workspace, "sess-2");

    const second = new SessionMetaStore(root, logger);
    assert.deepEqual(
      (await second.list(workspace)).map((entry) => [entry.sessionId, entry.title, entry.deleted]),
      [
        ["sess-1", "Hello", false],
        ["sess-2", "", true],
      ],
    );
    const kept = await second.get(workspace, "sess-1");
    assert.deepEqual(kept?.seenRequestIds, ["cmd-1"]);
    assert.equal(kept?.titleSource, "custom");
    assert.deepEqual(await second.list(workspace), await second.list(workspace));
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("metadata: concurrent upserts are serialized per workspace", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-next-meta-"));
  try {
    const store = new SessionMetaStore(root, logger);
    await Promise.all(
      Array.from({ length: 20 }, (_, index) =>
        store.upsert("/workspace/demo", `sess-${index}`, {
          createdAt: index,
          lastActivityAt: index,
        }),
      ),
    );
    const entries = await store.list("/workspace/demo");
    assert.equal(entries.length, 20);
    assert.equal(
      await new SessionMetaStore(root, logger)
        .list("/workspace/demo")
        .then((items) => items.length),
      20,
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("restore after restart: metadata + transcript rebuild the same snapshot", () => {
  const session: EngineSessionState = {
    sessionId: "sess-1",
    workspacePath: "/workspace/demo",
    title: "Persisted",
    titleSource: "custom",
    createdAt: 1_000,
    lastActivityAt: 2_000,
    phase: "completedSuccess",
    model: {
      providerId: "anthropic",
      modelId: "claude-sonnet-4-5",
      thought: "high",
      thoughtLevels: ["off", "low", "medium", "high"],
    },
    usage: { input: 1, output: 2, cacheRead: 3, cacheWrite: 4 },
    isRunning: false,
  };
  const state = projectHistory(
    session,
    [
      { role: "user", at: 1_100, text: "hello" },
      { role: "assistant", at: 1_200, stopReason: "stop", parts: [{ type: "text", text: "hi" }] },
    ],
    "epoch-new",
  );
  const snapshot = conversationSnapshotSchema.parse(buildConversationSnapshot(state));
  assert.equal(snapshot.logEpoch, "epoch-new");
  assert.equal(snapshot.meta.title, "Persisted");
  assert.equal(snapshot.meta.titleSource, "custom");
  assert.equal(snapshot.config.thought, "high");
  assert.equal(snapshot.rows.window.length, 3);
  assert.equal(snapshot.control.phase, "completedSuccess");
});
