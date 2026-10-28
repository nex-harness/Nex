import { test } from "node:test";
import assert from "node:assert/strict";
import { nexWorkspacePresentationSchema, nexTaskMetaSchema } from "@nex/shared";
import { sessionsIndexTopicWireFrameSchema } from "@nex/shared/nex-protocol-v4";
import { ChannelRegistry } from "../src/link/registry.js";
import { registerChannels } from "../src/channels/register.js";
import { readWorkspacePresentation } from "../src/channels/nexSession.js";
import { ConversationPublisher } from "../src/v4/publisher.js";
import { FakeEngine } from "./helpers/fakeEngine.js";
import { testRoots } from "./helpers/testRoots.js";
import { createLogger } from "../src/logger.js";

/**
 * Multi-runner workspace scoping (docs/next/multi-runner.md "Runner changes").
 *
 * The server routes by `runner:<runnerId>:<path>` identity. The runner must (a) echo the
 * identity in every workspace-scoped object so the UI's `identity ?? path` key matches,
 * and (b) refuse a path it never advertised in `hello`, because a stale identity can be
 * forwarded for a path this machine does not serve.
 */

const WORKSPACE = "/workspace/demo";
const OTHER_WORKSPACE = "/workspace/other";
const IDENTITY = "runner:runner_alpha:/workspace/demo";
const logger = createLogger("error");

function harness(workspacePaths: readonly string[] = [WORKSPACE]) {
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const registry = new ChannelRegistry();
  const channels = registerChannels(registry, {
    engine,
    publisher,
    logger,
    roots: testRoots(workspacePaths),
    runnerId: "runner_alpha",
  });
  return { engine, publisher, registry, channels };
}

function seedRunning(engine: FakeEngine, sessionId: string, workspacePath = WORKSPACE): void {
  engine.seedSession(
    {
      sessionId,
      workspacePath,
      title: "List notes",
      titleSource: "default",
      createdAt: 1_700_000_000_000,
      lastActivityAt: 1_700_000_000_000,
      phase: "running",
      model: null,
      usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      isRunning: false,
    },
    [],
  );
}

function waitFor(check: () => boolean, label: string, timeoutMs = 3000): Promise<void> {
  return new Promise((resolve, reject) => {
    const deadline = Date.now() + timeoutMs;
    const timer = setInterval(() => {
      if (check()) {
        clearInterval(timer);
        resolve();
      } else if (Date.now() > deadline) {
        clearInterval(timer);
        reject(new Error(`timed out waiting for ${label}`));
      }
    }, 5);
    timer.unref?.();
  });
}

test("readWorkspacePresentation echoes workspaceIdentity and parses with the real schema", () => {
  const withoutIdentity = readWorkspacePresentation({ workspacePath: WORKSPACE });
  nexWorkspacePresentationSchema.parse(withoutIdentity);
  assert.deepEqual(withoutIdentity.workspace, {
    workspacePath: WORKSPACE,
    workspaceKey: WORKSPACE,
  });

  const withIdentity = readWorkspacePresentation({
    workspacePath: WORKSPACE,
    workspaceIdentity: IDENTITY,
  });
  const parsed = nexWorkspacePresentationSchema.parse(withIdentity);
  assert.equal(parsed.workspace.workspaceIdentity, IDENTITY);
  // UI grouping key is `identity?.trim() || path`; the presentation must match it.
  assert.equal(parsed.workspace.workspaceKey, IDENTITY);
  assert.equal(parsed.workspace.workspacePath, WORKSPACE);
});

test("sessions-index snapshot and topic use the identity, matching the UI workspace key", async () => {
  const { engine, publisher, registry } = harness();
  seedRunning(engine, "sess-1");
  const frames: unknown[] = [];
  const subscribeIndex = registry.getCall("nex-agent", "subscribeSessionsIndexV4");
  const listenIndex = registry.getListen("nex-agent", "onDynamicSessionsIndexFrame");
  assert.ok(subscribeIndex && listenIndex);

  const subscription = listenIndex(
    { workspacePath: WORKSPACE, workspaceIdentity: IDENTITY },
    (frame) => frames.push(frame),
  );
  const ack = (await subscribeIndex({ workspacePath: WORKSPACE, workspaceIdentity: IDENTITY })) as {
    ack: { subscriptionId: string; mode: string };
  };
  assert.equal(ack.ack.mode, "snapshot");
  await waitFor(() => frames.length > 0, "initial sessions-index frame");

  const parsed = sessionsIndexTopicWireFrameSchema.parse(frames[0]);
  assert.equal(parsed.kind, "complete");
  if (parsed.kind !== "complete") return;
  assert.equal(parsed.topic, `sessions-index/${IDENTITY}`);
  assert.equal(parsed.frame.payload.kind, "snapshot");
  if (parsed.frame.payload.kind !== "snapshot") return;
  const snapshot = parsed.frame.payload.snapshot;
  assert.equal(snapshot.workspaceId, IDENTITY);
  assert.equal(snapshot.sessions[0]?.workspaceId, IDENTITY);
  assert.equal(snapshot.sessions[0]?.sessionId, "sess-1");

  subscription?.dispose?.();
  publisher.dispose();
});

test("sessions-index keeps path topic and workspaceId when no identity is sent (legacy clients)", async () => {
  const { engine, publisher, registry } = harness();
  seedRunning(engine, "sess-1");
  const frames: unknown[] = [];
  const subscription = registry.getListen("nex-agent", "onDynamicSessionsIndexFrame")?.(
    { workspacePath: WORKSPACE },
    (frame) => frames.push(frame),
  );
  await registry.getCall("nex-agent", "subscribeSessionsIndexV4")?.({ workspacePath: WORKSPACE });
  await waitFor(() => frames.length > 0, "initial sessions-index frame");
  const parsed = sessionsIndexTopicWireFrameSchema.parse(frames[0]);
  if (parsed.kind !== "complete") return;
  assert.equal(parsed.topic, `sessions-index/${WORKSPACE}`);
  assert.equal(
    parsed.frame.payload.kind === "snapshot" ? parsed.frame.payload.snapshot.workspaceId : "",
    WORKSPACE,
  );
  subscription?.dispose?.();
  publisher.dispose();
});

test("nex-task rows echo the identity and parse with the real schema", async () => {
  const { engine, publisher, registry } = harness();
  seedRunning(engine, "sess-1");
  const tasks = (await registry.getCall(
    "nex-task",
    "listTasks",
  )?.({
    workspacePath: WORKSPACE,
    workspaceIdentity: IDENTITY,
  })) as unknown[];
  const parsed = nexTaskMetaSchema.parse(tasks[0]);
  assert.equal(parsed.workspaceIdentity, IDENTITY);
  assert.equal(parsed.workspacePath, WORKSPACE);
  publisher.dispose();
});

test("workspace-scoped calls reject a path this runner never advertised", async () => {
  const { engine, publisher, registry } = harness([WORKSPACE]);
  const listTasks = registry.getCall("nex-task", "listTasks");
  const subscribeIndex = registry.getCall("nex-agent", "subscribeSessionsIndexV4");
  assert.ok(listTasks && subscribeIndex);

  // A forwarded `runner:<id>:<other path>` identity: the server can reach this runner
  // but the path is not in its hello list, so it must fail closed.
  await assert.rejects(Promise.resolve(listTasks({ workspacePath: OTHER_WORKSPACE })), {
    message: `workspace not served by this runner: ${OTHER_WORKSPACE}`,
  });
  await assert.rejects(
    Promise.resolve(
      subscribeIndex({
        workspacePath: OTHER_WORKSPACE,
        workspaceIdentity: `runner:runner_alpha:${OTHER_WORKSPACE}`,
      }),
    ),
    { message: `workspace not served by this runner: ${OTHER_WORKSPACE}` },
  );
  // No session may be created/listed for the rejected path.
  assert.deepEqual(await engine.listSessions(OTHER_WORKSPACE), []);
  publisher.dispose();
});

test("workspace-scoped listens reject a path this runner never advertised", async () => {
  const { publisher, registry } = harness([WORKSPACE]);
  const listenIndex = registry.getListen("nex-agent", "onDynamicSessionsIndexFrame");
  const listenTasks = registry.getListen("nex-task", "onDynamicWorkspaceEvent");
  assert.ok(listenIndex && listenTasks);
  const noop = () => undefined;
  assert.throws(
    () => listenIndex({ workspacePath: OTHER_WORKSPACE }, noop),
    new RegExp(`^Error: workspace not served by this runner: ${OTHER_WORKSPACE}$`, "u"),
  );
  assert.throws(
    () =>
      listenTasks(
        {
          workspacePath: OTHER_WORKSPACE,
          workspaceIdentity: `runner:runner_alpha:${OTHER_WORKSPACE}`,
        },
        noop,
      ),
    new RegExp(`^Error: workspace not served by this runner: ${OTHER_WORKSPACE}$`, "u"),
  );
  publisher.dispose();
});

test("guard normalizes trailing slashes and leaves workspace-less calls alone", async () => {
  const { engine, publisher, registry } = harness([WORKSPACE]);
  seedRunning(engine, "sess-1");
  // A served path with a trailing separator is still the same directory: the guard
  // accepts it (the engine itself keys by the exact string, so only the guard is tested).
  const tasks = await registry.getCall(
    "nex-task",
    "listTasks",
  )?.({
    workspacePath: `${WORKSPACE}/`,
  });
  assert.ok(Array.isArray(tasks));
  // The model catalog has no workspacePath in its arg and must stay reachable.
  const view = await registry.getCall("model-selection", "getView")?.(undefined);
  assert.ok(view);
  // `listPinnedTaskIds` is routed to the first runner without a workspace argument.
  assert.deepEqual(await registry.getCall("nex-task", "listPinnedTaskIds")?.(undefined), []);
  publisher.dispose();
});
