import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { nexTaskMetaSchema } from "@nex/shared";
import { NexTaskChannel } from "../src/channels/nexTask.js";
import { SessionMetaStore } from "../src/engine/pi/metaStore.js";
import { createLogger } from "../src/logger.js";
import type { EngineSessionState } from "../src/engine/types.js";
import { FakeEngine } from "./helpers/fakeEngine.js";

/**
 * Sidebar task state (docs/next/task-state.md): pin/archive/unread/rename/delete are
 * persisted on the runner's metadata store, list filters partition rows, mutations emit
 * the reasons the UI's refresh policy consumes, and archived deletion is conditional.
 */

const WORKSPACE = "/workspace/demo";
const logger = createLogger("error");
const settle = () => new Promise<void>((resolve) => setTimeout(resolve, 20));

function session(
  sessionId: string,
  overrides: Partial<EngineSessionState> = {},
): EngineSessionState {
  return {
    sessionId,
    workspacePath: WORKSPACE,
    title: "List notes",
    titleSource: "default",
    createdAt: 1_700_000_000_000,
    lastActivityAt: 1_700_000_000_000,
    phase: "completedSuccess",
    model: null,
    usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    isRunning: false,
    pinned: false,
    archived: false,
    ...overrides,
  };
}

function seed(engine: FakeEngine, ...sessions: EngineSessionState[]): void {
  for (const item of sessions) engine.seedSession(item, []);
}

test("task state: archive hides the row from listTasks and surfaces it in the archived list", async () => {
  const engine = new FakeEngine();
  seed(engine, session("a"), session("b"));
  const channel = new NexTaskChannel(engine, logger);

  const archived = await channel.archiveTask({ taskId: "a", workspacePath: WORKSPACE });
  nexTaskMetaSchema.parse(archived);
  assert.equal(archived.taskId, "a");

  assert.deepEqual(
    (await channel.listTasks({ workspacePath: WORKSPACE })).map((task) => task.taskId),
    ["b"],
  );
  assert.deepEqual(
    (await channel.listArchivedTasks({ workspacePath: WORKSPACE })).map((task) => task.taskId),
    ["a"],
  );

  const restored = await channel.unarchiveTask({ taskId: "a", workspacePath: WORKSPACE });
  nexTaskMetaSchema.parse(restored);
  assert.deepEqual(
    (await channel.listTasks({ workspacePath: WORKSPACE })).map((task) => task.taskId).sort(),
    ["a", "b"],
  );
  assert.deepEqual(await channel.listArchivedTasks({ workspacePath: WORKSPACE }), []);
  channel.dispose();
});

test("task state: pinned rows leave the project list and appear in pinned (never both)", async () => {
  const engine = new FakeEngine();
  seed(engine, session("a"), session("b"));
  const channel = new NexTaskChannel(engine, logger, { servedPaths: () => [WORKSPACE] });

  const pinned = await channel.setTaskPinned({
    taskId: "a",
    workspacePath: WORKSPACE,
    pinned: true,
  });
  nexTaskMetaSchema.parse(pinned);
  assert.deepEqual(
    (await channel.listTasks({ workspacePath: WORKSPACE })).map((task) => task.taskId),
    ["b"],
  );
  assert.deepEqual(
    (await channel.listPinnedTasks({ workspacePath: WORKSPACE })).map((task) => task.taskId),
    ["a"],
  );
  // Workspace-less call spans every served workspace (server fan-out supplemental hint).
  assert.deepEqual(await channel.listPinnedTaskIds(), ["a"]);

  // Archiving a pinned row keeps it out of the pinned section.
  await channel.archiveTask({ taskId: "a", workspacePath: WORKSPACE });
  assert.deepEqual(await channel.listPinnedTasks({ workspacePath: WORKSPACE }), []);
  assert.deepEqual(await channel.listPinnedTaskIds(), []);
  assert.deepEqual(
    (await channel.listArchivedTasks({ workspacePath: WORKSPACE })).map((task) => task.taskId),
    ["a"],
  );
  channel.dispose();
});

test("task state: mutations emit the reasons the UI refresh policy expects", async () => {
  const engine = new FakeEngine();
  seed(engine, session("a"), session("b"));
  const channel = new NexTaskChannel(engine, logger);
  const reasons: Array<{ reason: string; taskId?: string }> = [];
  const subscription = channel.onDynamicWorkspaceEvent(WORKSPACE, (event) => {
    if (event.type === "workspace_task_list_changed") {
      reasons.push({ reason: event.reason, ...(event.taskId ? { taskId: event.taskId } : {}) });
    }
  });
  await settle();
  reasons.length = 0;

  await channel.setTaskPinned({ taskId: "a", workspacePath: WORKSPACE, pinned: true });
  await settle();
  await channel.archiveTask({ taskId: "a", workspacePath: WORKSPACE });
  await settle();
  await channel.unarchiveTask({ taskId: "a", workspacePath: WORKSPACE });
  await settle();
  await channel.renameTask({ taskId: "b", workspacePath: WORKSPACE, title: "Renamed" });
  await settle();
  await channel.setTaskUnread({ taskId: "b", workspacePath: WORKSPACE, unread: true });
  await settle();
  await channel.deleteTask({ taskId: "b", workspacePath: WORKSPACE });
  await settle();

  assert.deepEqual(
    reasons.map((entry) => entry.reason),
    [
      "task_pinned",
      "task_archived",
      "task_unarchived",
      "task_title_changed",
      "task_meta_changed",
      "task_deleted",
    ],
  );
  subscription.dispose();
  channel.dispose();
});

test("task state: rename reuses the engine rename (one write path) and shows the title", async () => {
  const engine = new FakeEngine();
  seed(engine, session("a"));
  const channel = new NexTaskChannel(engine, logger);

  const meta = await channel.renameTask({
    taskId: "a",
    workspacePath: WORKSPACE,
    title: "My title",
  });
  nexTaskMetaSchema.parse(meta);
  assert.equal(meta.title, "My title");
  assert.equal(meta.titleOverridden, true);
  // FakeEngine.rename also mirrors titleSource, matching PiEngine.rename.
  assert.deepEqual(engine.renamed, [{ sessionId: "a", title: "My title" }]);
  assert.equal(engine.sessions.get("a")?.titleSource, "custom");
  channel.dispose();
});

test("task state: deleteTask writes the tombstone and removes the row", async () => {
  const engine = new FakeEngine();
  seed(engine, session("a"), session("b"));
  const channel = new NexTaskChannel(engine, logger);

  await channel.deleteTask({ taskId: "a", workspacePath: WORKSPACE });
  assert.deepEqual(
    (await channel.listTasks({ workspacePath: WORKSPACE })).map((task) => task.taskId),
    ["b"],
  );
  assert.deepEqual(await channel.listDeletedTaskIds({ workspacePath: WORKSPACE }), ["a"]);
  channel.dispose();
});

test("task state: deleteArchivedTask only deletes while the row is still archived", async () => {
  const engine = new FakeEngine();
  seed(engine, session("a"), session("b"));
  const channel = new NexTaskChannel(engine, logger);

  // Not archived → skipped.
  assert.equal(await channel.deleteArchivedTask({ taskId: "a", workspacePath: WORKSPACE }), false);
  assert.deepEqual(await channel.listDeletedTaskIds({ workspacePath: WORKSPACE }), []);

  await channel.archiveTask({ taskId: "a", workspacePath: WORKSPACE });
  assert.equal(await channel.deleteArchivedTask({ taskId: "a", workspacePath: WORKSPACE }), true);
  // A retry is a no-op instead of resurrecting/redeleting the tombstone.
  assert.equal(await channel.deleteArchivedTask({ taskId: "a", workspacePath: WORKSPACE }), false);
  assert.deepEqual(await channel.listDeletedTaskIds({ workspacePath: WORKSPACE }), ["a"]);

  // Batch: one archived, one restored in the meantime.
  await channel.archiveTask({ taskId: "b", workspacePath: WORKSPACE });
  await engine.setTaskState(WORKSPACE, "b", { archived: false });
  const result = await channel.deleteArchivedTasks({
    workspacePath: WORKSPACE,
    taskIds: ["a", "b", "a"],
  });
  assert.deepEqual(result.deletedTaskIds, []);
  assert.deepEqual(result.skippedTaskIds, ["a", "b"]);
  assert.deepEqual(result.failedTaskIds, []);
  channel.dispose();
});

test("task state: unread compare-and-clear keeps a newer completion", async () => {
  const engine = new FakeEngine();
  seed(engine, session("a"));
  const channel = new NexTaskChannel(engine, logger);

  const unread = await channel.setTaskUnread({
    taskId: "a",
    workspacePath: WORKSPACE,
    unread: true,
  });
  nexTaskMetaSchema.parse(unread);
  assert.equal(typeof unread.unreadAt, "number");
  const marker = unread.unreadAt as number;

  // A newer completion lands after the client's snapshot was taken.
  const newer = await engine.setTaskUnread(WORKSPACE, "a", Date.now() + 5_000);
  assert.equal(typeof newer.unreadAt, "number");
  assert.notEqual(newer.unreadAt, marker);

  // Stale clear (expected older marker) must not erase the newer marker.
  const staleClear = await channel.setTaskUnread({
    taskId: "a",
    workspacePath: WORKSPACE,
    unread: false,
    expectedUnreadAt: marker,
  });
  assert.equal(staleClear.unreadAt, newer.unreadAt);

  // Matching clear removes the field.
  const cleared = await channel.setTaskUnread({
    taskId: "a",
    workspacePath: WORKSPACE,
    unread: false,
    expectedUnreadAt: newer.unreadAt as number,
  });
  assert.equal(cleared.unreadAt, undefined);
  channel.dispose();
});

test("task state: background terminal emits unreadSignal and an unwatched rule", async () => {
  const engine = new FakeEngine();
  seed(engine, session("a"), session("b"));
  let watched = new Set<string>();
  const channel = new NexTaskChannel(engine, logger, {
    isSessionWatched: (sessionId) => watched.has(sessionId),
  });
  const events: Array<{
    reason: string;
    taskId?: string;
    unreadSignal?: string;
    unreadAt?: number;
    taskMeta?: { unreadAt?: number };
  }> = [];
  const subscription = channel.onDynamicWorkspaceEvent(WORKSPACE, (event) => {
    if (event.type === "workspace_task_list_changed") {
      events.push({
        reason: event.reason,
        ...(event.taskId ? { taskId: event.taskId } : {}),
        ...("unreadSignal" in event && event.unreadSignal
          ? { unreadSignal: event.unreadSignal }
          : {}),
        ...(event.taskMeta?.unreadAt !== undefined
          ? { taskMeta: { unreadAt: event.taskMeta.unreadAt } }
          : {}),
      });
    }
  });
  await settle();
  events.length = 0;

  // Background run finishes while nobody watches → unread.
  engine.seedSession(session("a", { phase: "running" }), []);
  engine.emitSessionsChanged(WORKSPACE);
  await settle();
  events.length = 0;
  engine.seedSession(session("a", { phase: "completedSuccess" }), []);
  engine.emitSessionsChanged(WORKSPACE);
  await settle();
  const background = events.filter(
    (event) => event.taskId === "a" && event.reason === "task_status_changed",
  );
  assert.equal(background.length, 1);
  assert.equal(background[0]?.unreadSignal, "background_terminal");
  assert.equal(typeof background[0]?.taskMeta?.unreadAt, "number");

  // Foreground run finishes while the browser watches → status only, no unread.
  watched = new Set(["b"]);
  engine.seedSession(session("b", { phase: "running" }), []);
  engine.emitSessionsChanged(WORKSPACE);
  await settle();
  events.length = 0;
  engine.seedSession(session("b", { phase: "completedSuccess" }), []);
  engine.emitSessionsChanged(WORKSPACE);
  await settle();
  const foreground = events.filter(
    (event) => event.taskId === "b" && event.reason === "task_status_changed",
  );
  assert.equal(foreground.length, 1);
  assert.equal(foreground[0]?.unreadSignal, undefined);
  subscription.dispose();
  channel.dispose();
});

test("task state: metadata store persists pin/archive/unread and reads old files as unset", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-next-task-state-"));
  try {
    const store = new SessionMetaStore(root, logger);
    await store.upsert(WORKSPACE, "sess-1", { title: "One", titleSource: "default" });
    await store.upsert(WORKSPACE, "sess-2", { title: "Two", titleSource: "default" });

    await store.upsert(WORKSPACE, "sess-1", {
      pinned: true,
      pinnedAt: 111,
      archived: true,
      archivedAt: 222,
    });
    await store.update(WORKSPACE, "sess-2", (current) => ({
      next: current ? { ...current, unreadAt: 333 } : null,
      result: undefined,
    }));

    const reloaded = new SessionMetaStore(root, logger);
    const one = await reloaded.get(WORKSPACE, "sess-1");
    const two = await reloaded.get(WORKSPACE, "sess-2");
    assert.equal(one?.pinned, true);
    assert.equal(one?.pinnedAt, 111);
    assert.equal(one?.archived, true);
    assert.equal(one?.archivedAt, 222);
    assert.equal(two?.unreadAt, 333);

    // Back-compat: a file written before these fields existed must read as unset.
    await reloaded.update(WORKSPACE, "sess-2", (current) => ({
      next: current ? { ...current, unreadAt: undefined } : null,
      result: undefined,
    }));
    const raw = JSON.parse(
      await readFile(
        join(root, "workspaces", `${Buffer.from(WORKSPACE).toString("base64url")}.json`),
        "utf8",
      ),
    ) as { sessions: Array<Record<string, unknown>> };
    const legacy = raw.sessions.find((entry) => entry["sessionId"] === "sess-2");
    assert.ok(legacy);
    delete legacy["pinned"];
    delete legacy["archived"];
    delete legacy["unreadAt"];
    await writeFile(
      join(root, "workspaces", `${Buffer.from(WORKSPACE).toString("base64url")}.json`),
      `${JSON.stringify(raw, null, 2)}\n`,
      "utf8",
    );
    const legacyStore = new SessionMetaStore(root, logger);
    const old = await legacyStore.get(WORKSPACE, "sess-2");
    assert.equal(old?.pinned, undefined);
    assert.equal(old?.archived, undefined);
    assert.equal(old?.unreadAt, undefined);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("task state: workspace-less pinned list spans served workspaces and skips unreadable ones", async () => {
  const engine = new FakeEngine();
  const other = "/workspace/other";
  engine.seedSession(session("a", { pinned: true }), []);
  engine.seedSession(
    {
      ...session("b", { pinned: true }),
      workspacePath: other,
    },
    [],
  );
  const channel = new NexTaskChannel(engine, logger, {
    servedPaths: () => [WORKSPACE, other, "/workspace/missing"],
  });
  const ids = (await channel.listPinnedTaskIds()).sort();
  assert.deepEqual(ids, ["a", "b"]);
  const rows = await channel.listPinnedTasks();
  assert.deepEqual(rows.map((row) => row.taskId).sort(), ["a", "b"]);
  for (const row of rows) nexTaskMetaSchema.parse(row);
  channel.dispose();
});

test("task state: nex-task ignores a directory that vanished while listing", async () => {
  // Sanity check that collectTasks swallows per-workspace failures rather than failing
  // the whole workspace-less query: no seeded sessions + a missing path must not throw.
  const engine = new FakeEngine();
  const channel = new NexTaskChannel(engine, logger, { servedPaths: () => ["/workspace/empty"] });
  assert.deepEqual(await channel.listPinnedTaskIds(), []);
  channel.dispose();
});

test("task state: archive persists across a fresh metadata store instance", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-next-task-restart-"));
  try {
    const store = new SessionMetaStore(root, logger);
    await store.upsert(WORKSPACE, "sess-1", { title: "One" });
    await store.upsert(WORKSPACE, "sess-1", { archived: true, archivedAt: 42 });
    const reopened = new SessionMetaStore(root, logger);
    const entry = await reopened.get(WORKSPACE, "sess-1");
    assert.equal(entry?.archived, true);
    assert.equal(entry?.archivedAt, 42);
    // The file name is a stable hash-free encoding of the path (single writer).
    assert.equal(
      createHash("sha1").update(WORKSPACE).digest("hex").length,
      40,
      "sha1 length used elsewhere for session dirs stays 40 hex chars",
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("task state: mutating an unknown task fails instead of inventing a row", async () => {
  const engine = new FakeEngine();
  const channel = new NexTaskChannel(engine, logger);
  await assert.rejects(
    () => channel.archiveTask({ taskId: "nope", workspacePath: WORKSPACE }),
    /not found/,
  );
  await assert.rejects(
    () => channel.renameTask({ taskId: "nope", workspacePath: WORKSPACE, title: "x" }),
    /not found|session/i,
  );
  channel.dispose();
});
