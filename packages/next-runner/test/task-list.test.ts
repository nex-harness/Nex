import { test } from "node:test";
import assert from "node:assert/strict";
import { nexTaskMetaSchema } from "@nex/shared";
import { sessionsIndexSnapshotSchema } from "@nex/shared/nex-protocol-v4";
import { NexTaskChannel } from "../src/channels/nexTask.js";
import { buildSessionsIndexSnapshot } from "../src/v4/sessionsIndex.js";
import { FakeEngine } from "./helpers/fakeEngine.js";
import { createLogger } from "../src/logger.js";
import type { EngineSessionState } from "../src/engine/types.js";

/**
 * Regression: draft-prewarm sessions must never surface as sidebar tasks.
 *
 * The real UI starts a `createSession` prewarm as soon as a draft composer is ready
 * (useDraftSessionPrewarm) and treats `phase === "draft"` as "not a task yet"
 * (nexTaskIndexSyncer skips drafts for both live summaries and the initial baseline).
 * Before this fix the runner projected every session into both `nex-task.listTasks`
 * and `sessions-index`, so every prewarm/reload added another empty "New task" row.
 */

const WORKSPACE = "/workspace/demo";
const logger = createLogger("error");
/** refresh() is async; give each engine notification one turn to settle deterministically. */
const settle = () => new Promise<void>((resolve) => setTimeout(resolve, 20));

function session(
  sessionId: string,
  phase: EngineSessionState["phase"],
  overrides: Partial<EngineSessionState> = {},
): EngineSessionState {
  return {
    sessionId,
    workspacePath: WORKSPACE,
    title: phase === "draft" ? "" : "List notes",
    titleSource: "default",
    createdAt: 1_700_000_000_000,
    lastActivityAt: 1_700_000_000_000,
    phase,
    model: null,
    usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    isRunning: false,
    ...overrides,
  };
}

test("sessions-index omits draft sessions and keeps the schema valid", () => {
  const snapshot = buildSessionsIndexSnapshot(WORKSPACE, "epoch-1", [
    session("draft-1", "draft"),
    session("running-1", "running"),
    session("done-1", "completedSuccess"),
  ]);
  const parsed = sessionsIndexSnapshotSchema.parse(snapshot);
  assert.deepEqual(
    parsed.sessions.map((item) => item.sessionId),
    ["running-1", "done-1"],
  );
});

test("nex-task.listTasks omits draft sessions and emits tasks with valid meta", async () => {
  const engine = new FakeEngine();
  engine.seedSession(session("draft-1", "draft"), []);
  engine.seedSession(session("running-1", "running"), []);
  const channel = new NexTaskChannel(engine, logger);

  const tasks = await channel.listTasks({ workspacePath: WORKSPACE });
  assert.deepEqual(
    tasks.map((task) => task.taskId),
    ["running-1"],
  );
  for (const task of tasks) nexTaskMetaSchema.parse(task);
  channel.dispose();
});

test("nex-task workspace events skip the draft→draft transition and announce promotion once", async () => {
  const engine = new FakeEngine();
  const channel = new NexTaskChannel(engine, logger);
  const events: Array<Record<string, unknown>> = [];
  const subscription = channel.onDynamicWorkspaceEvent(WORKSPACE, (event) =>
    events.push(event as unknown as Record<string, unknown>),
  );
  // Let the initial refresh (no watcher snapshot yet) settle.
  await settle();

  // Prewarm: a draft session must not produce a task_created event.
  const draft = session("draft-1", "draft");
  engine.seedSession(draft, []);
  engine.emitSessionsChanged(WORKSPACE);
  await settle();
  assert.deepEqual(events, []);

  // Promotion: same session starts running after sendText → one task_created.
  engine.seedSession({ ...draft, phase: "running", title: "Use the read tool" }, []);
  engine.emitSessionsChanged(WORKSPACE);
  await settle();
  const created = events.filter((event) => event["reason"] === "task_created");
  assert.equal(created.length, 1);
  assert.equal(created[0]?.["taskId"], "draft-1");
  nexTaskMetaSchema.parse(created[0]?.["taskMeta"]);

  // Deleting removes it again (deleteSession records the tombstone and emits).
  await engine.deleteSession(WORKSPACE, "draft-1");
  engine.emitSessionsChanged(WORKSPACE);
  await settle();
  const deleted = events.filter((event) => event["reason"] === "task_deleted");
  assert.equal(deleted.length, 1);
  subscription.dispose();
  channel.dispose();
});

test("nex-task echoes workspaceIdentity so the UI's workspaceKey grouping matches", async () => {
  const engine = new FakeEngine();
  engine.seedSession(session("running-1", "running"), []);
  const channel = new NexTaskChannel(engine, logger);
  const identity = "runner:runner_abc:/workspace/demo";

  const tasks = await channel.listTasks({ workspacePath: WORKSPACE, workspaceIdentity: identity });
  assert.equal(tasks[0]?.workspaceIdentity, identity);
  nexTaskMetaSchema.parse(tasks[0]);

  const events: Array<Record<string, unknown>> = [];
  const subscription = channel.onDynamicWorkspaceEvent(
    WORKSPACE,
    (event) => events.push(event as unknown as Record<string, unknown>),
    identity,
  );
  await settle();
  engine.seedSession(session("running-2", "running", { title: "Second" }), []);
  engine.emitSessionsChanged(WORKSPACE);
  await settle();
  const created = events.find((event) => event["reason"] === "task_created");
  assert.equal(created?.["workspaceIdentity"], identity);
  assert.equal(
    created?.["taskMeta"] &&
      (created["taskMeta"] as { workspaceIdentity?: string }).workspaceIdentity,
    identity,
  );
  subscription.dispose();
  channel.dispose();
});
