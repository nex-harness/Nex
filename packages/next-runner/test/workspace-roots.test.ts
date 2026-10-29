// Runner-owned served folders (docs/next/runner-folders.md decisions 1-3):
// WorkspaceRoots merging/persistence/validation, the live channel guard, the
// machine browse scope (`workspacePath: "~"`) and the `workspaces` link frame.

import assert from "node:assert/strict";
import { test } from "node:test";
import {
  mkdir,
  mkdtemp,
  readFile,
  readdir as fsReaddir,
  realpath,
  symlink,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { WebSocketServer, type WebSocket as WsSocket } from "ws";
import type { AddressInfo } from "node:net";
import { ChannelRegistry } from "../src/link/registry.js";
import { RunnerLink } from "../src/link/client.js";
import { registerChannels } from "../src/channels/register.js";
import { registerWorkspaceTools } from "../src/channels/workspaceTools.js";
import { ConversationPublisher } from "../src/v4/publisher.js";
import { WorkspaceRoots } from "../src/workspaces/workspaceRoots.js";
import { publishWorkspaceFrames } from "../src/workspaces/workspaceFrames.js";
import type { Logger } from "../src/logger.js";
import { FakeEngine } from "./helpers/fakeEngine.js";
import { testRoots } from "./helpers/testRoots.js";

const logger: Logger = {
  debug: () => undefined,
  info: () => undefined,
  warn: () => undefined,
  error: () => undefined,
  child: () => logger,
} as unknown as Logger;

async function tempDir(prefix: string): Promise<string> {
  // realpath: on macOS tmpdir() is /var/folders (a symlink into /private/var), and
  // WorkspaceRoots stores realpaths, so assertions must compare the same form.
  return realpath(await mkdtemp(join(tmpdir(), prefix)));
}

function makeRoots(envRoots: readonly string[], dataDir: string): WorkspaceRoots {
  return new WorkspaceRoots({ envRoots, dataDir, logger });
}

test("workspace roots: merges env roots and persisted folders, env first", async () => {
  const base = await tempDir("nex-roots-merge-");
  const envRoot = join(base, "env");
  const persisted = join(base, "persisted");
  await mkdir(envRoot);
  await mkdir(persisted);
  const dataDir = join(base, "data");
  await mkdir(dataDir, { recursive: true });
  await writeFile(
    join(dataDir, "workspaces.json"),
    JSON.stringify({ version: 1, folders: [{ path: persisted, addedAt: "2026-01-01T00:00:00Z" }] }),
  );

  const roots = makeRoots([envRoot], dataDir);
  await roots.load();
  assert.deepEqual(
    roots.list().map((folder) => ({ path: folder.path, removable: folder.removable })),
    [
      { path: envRoot, removable: false },
      { path: persisted, removable: true },
    ],
  );
  assert.equal(roots.list()[1]?.label, "persisted");
  assert.ok(roots.has(envRoot));
  assert.ok(roots.has(`${persisted}/`));
  assert.equal(roots.has(join(base, "other")), false);
});

test("workspace roots: a missing or corrupt file yields the env roots only", async () => {
  const base = await tempDir("nex-roots-empty-");
  const envRoot = join(base, "env");
  await mkdir(envRoot);
  const dataDir = join(base, "data");

  const missing = makeRoots([envRoot], dataDir);
  await missing.load();
  assert.deepEqual(
    missing.list().map((folder) => folder.path),
    [envRoot],
  );

  await mkdir(dataDir, { recursive: true });
  await writeFile(join(dataDir, "workspaces.json"), "{not json");
  const corrupt = makeRoots([envRoot], dataDir);
  await corrupt.load();
  assert.deepEqual(
    corrupt.list().map((folder) => folder.path),
    [envRoot],
  );
});

test("workspace roots: add persists atomically and survives a restart", async () => {
  const base = await tempDir("nex-roots-persist-");
  const envRoot = join(base, "env");
  const added = join(base, "added");
  await mkdir(envRoot);
  await mkdir(added);
  const dataDir = join(base, "data");

  const roots = makeRoots([envRoot], dataDir);
  await roots.load();
  const entry = await roots.add(added);
  assert.equal(entry.path, added);
  assert.equal(entry.label, "added");
  assert.equal(entry.removable, true);

  // Atomic write: valid JSON and no temp file left behind.
  const text = await readFile(join(dataDir, "workspaces.json"), "utf8");
  const parsed = JSON.parse(text) as { version: number; folders: Array<{ path: string }> };
  assert.equal(parsed.version, 1);
  assert.deepEqual(
    parsed.folders.map((folder) => folder.path),
    [added],
  );
  assert.deepEqual(
    (await fsReaddir(dataDir)).filter((name) => name.endsWith(".tmp")),
    [],
  );

  // A new instance (restart) reads the persisted folder back.
  const restarted = makeRoots([envRoot], dataDir);
  await restarted.load();
  assert.deepEqual(
    restarted.list().map((folder) => folder.path),
    [envRoot, added],
  );
  assert.equal(restarted.has(added), true);
});

test("workspace roots: add validates the path (relative, missing, file, symlink)", async () => {
  const base = await tempDir("nex-roots-validate-");
  const envRoot = join(base, "env");
  await mkdir(envRoot);
  const dataDir = join(base, "data");
  const roots = makeRoots([envRoot], dataDir);
  await roots.load();

  await assert.rejects(() => roots.add("relative/dir"), /must be absolute/u);
  await assert.rejects(() => roots.add(join(base, "missing")), /does not exist/u);
  const file = join(base, "file.txt");
  await writeFile(file, "x");
  await assert.rejects(() => roots.add(file), /not a directory/u);

  // A symlink is stored as its realpath, so containment matches the OS.
  const target = join(base, "target");
  const link = join(base, "link");
  await mkdir(target);
  await symlink(target, link);
  const entry = await roots.add(link);
  assert.equal(entry.path, target);
});

test("workspace roots: add is idempotent and remove refuses env roots", async () => {
  const base = await tempDir("nex-roots-idem-");
  const envRoot = join(base, "env");
  const added = join(base, "added");
  await mkdir(envRoot);
  await mkdir(added);
  const roots = makeRoots([envRoot], join(base, "data"));
  await roots.load();

  let changes = 0;
  roots.onChange(() => {
    changes += 1;
  });
  await roots.add(added);
  const again = await roots.add(added);
  assert.equal(again.path, added);
  assert.equal(roots.list().filter((folder) => folder.path === added).length, 1);
  // Only the first add changed the list.
  assert.equal(changes, 1);

  await assert.rejects(() => roots.remove(envRoot), /env workspace cannot be removed/u);
  await roots.remove(added);
  assert.equal(roots.has(added), false);
  assert.equal(changes, 2);
  // Removing a folder that is not served is a no-op.
  await roots.remove(added);
  assert.equal(changes, 2);
});

test("guard: a newly added folder is accepted without a restart", async () => {
  const base = await tempDir("nex-roots-guard-");
  const envRoot = join(base, "env");
  const added = join(base, "added");
  await mkdir(envRoot);
  await mkdir(added);

  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const registry = new ChannelRegistry();
  const roots = makeRoots([envRoot], join(base, "data"));
  await roots.load();
  registerChannels(registry, { engine, publisher, logger, roots, runnerId: "runner_test" });

  const listTasks = registry.getCall("nex-task", "listTasks");
  assert.ok(listTasks);
  await assert.rejects(
    () => Promise.resolve(listTasks({ workspacePath: added })),
    /workspace not served by this runner/u,
  );

  const add = registry.getCall("runner-workspaces", "add");
  const listed = registry.getCall("runner-workspaces", "list");
  assert.ok(add && listed);
  const addedResult = (await add({ path: added, workspacePath: "~" })) as {
    path: string;
    label: string;
    workspaceIdentity: string;
  };
  assert.deepEqual(addedResult, {
    path: added,
    label: "added",
    workspaceIdentity: `runner:runner_test:${added}`,
  });

  // The same guard now accepts the folder: no restart.
  const tasks = await listTasks({ workspacePath: added });
  assert.deepEqual(tasks, []);
  const folders = (await listed({ workspacePath: "~" })) as Array<{
    path: string;
    removable: boolean;
  }>;
  assert.deepEqual(folders, [
    { path: envRoot, label: "env", removable: false },
    { path: added, label: "added", removable: true },
  ]);
  // env roots cannot be removed through the channel either.
  await assert.rejects(
    () =>
      Promise.resolve(
        registry.getCall("runner-workspaces", "remove")?.({ path: envRoot, workspacePath: "~" }),
      ),
    /env workspace cannot be removed/u,
  );
  publisher.dispose();
});

test("guard: `~` is allowed for readdir/stat/info/runner-workspaces only", async () => {
  const base = await tempDir("nex-roots-scope-");
  const envRoot = join(base, "env");
  await mkdir(envRoot);
  const registry = new ChannelRegistry();
  const roots = makeRoots([envRoot], join(base, "data"));
  await roots.load();
  const tools = registerWorkspaceTools(registry, { logger, roots });
  try {
    const readdir = registry.getCall("file", "readdir");
    const stat = registry.getCall("file", "stat");
    const readText = registry.getCall("file", "readTextFile");
    const gitSummary = registry.getCall("git", "getRepositorySummary");
    const info = registry.getCall("system", "info");
    const createTerminal = registry.getCall("terminal", "create");
    assert.ok(readdir && stat && readText && gitSummary && info && createTerminal);

    // Browse methods accept the machine scope.
    assert.ok(Array.isArray(await readdir({ path: base, workspacePath: "~" })));
    const statResult = (await stat({ path: base, workspacePath: "~" })) as { type: string };
    assert.equal(statResult.type, "directory");
    const infoResult = (await info({ workspacePath: "~" })) as { homedir: string };
    assert.ok(infoResult.homedir.length > 0);

    // Every other method with `~` is rejected before the handler runs.
    await assert.rejects(
      () => Promise.resolve(readText({ path: join(base, "x.txt"), workspacePath: "~" })),
      /machine scope is not available for file.readTextFile/u,
    );
    await assert.rejects(
      () => Promise.resolve(gitSummary({ workspacePath: "~" })),
      /machine scope is not available for git.getRepositorySummary/u,
    );
    await assert.rejects(
      () => Promise.resolve(createTerminal({ cols: 80, rows: 24, workspacePath: "~" })),
      /machine scope is not available for terminal.create/u,
    );
  } finally {
    await tools.dispose();
  }
});

test("machine scope: readdir lists directories only, outside the served roots", async () => {
  const base = await tempDir("nex-roots-browse-");
  const envRoot = join(base, "env");
  const outside = join(base, "outside");
  await mkdir(envRoot);
  await mkdir(join(outside, "sub"), { recursive: true });
  await writeFile(join(outside, "file.txt"), "x");
  await writeFile(join(outside, "sub", "nested.txt"), "x");

  const registry = new ChannelRegistry();
  const roots = makeRoots([envRoot], join(base, "data"));
  await roots.load();
  const tools = registerWorkspaceTools(registry, { logger, roots });
  try {
    const readdir = registry.getCall("file", "readdir");
    assert.ok(readdir);
    const entries = (await readdir({
      path: outside,
      includeHidden: true,
      workspacePath: "~",
    })) as Array<{ name: string; type: string }>;
    assert.deepEqual(
      entries.map((entry) => entry.name),
      ["sub"],
    );
    assert.ok(entries.every((entry) => entry.type === "directory"));

    // Without the machine scope the same path stays outside the served roots.
    await assert.rejects(
      () => Promise.resolve(readdir({ path: outside })),
      /outside this runner's workspaces/u,
    );
    // Relative paths are still refused in the machine scope.
    await assert.rejects(
      () => Promise.resolve(readdir({ path: "relative", workspacePath: "~" })),
      /must be absolute/u,
    );
  } finally {
    await tools.dispose();
  }
});

test("guard: `~` is rejected for a workspace-scoped listen", async () => {
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const registry = new ChannelRegistry();
  registerChannels(registry, {
    engine,
    publisher,
    logger,
    roots: testRoots(["/workspace/demo"]),
    runnerId: "runner_test",
  });
  const listen = registry.getListen("nex-agent", "onDynamicSessionsIndexFrame");
  assert.ok(listen);
  assert.throws(
    () => listen({ workspacePath: "~" }, () => undefined),
    /machine scope is not available for nex-agent.onDynamicSessionsIndexFrame/u,
  );
  publisher.dispose();
});

function frameReader(socket: WsSocket): () => Promise<Record<string, unknown>> {
  const frames: Record<string, unknown>[] = [];
  socket.on("message", (data) => {
    frames.push(JSON.parse(data.toString("utf8")) as Record<string, unknown>);
  });
  return async () => {
    const deadline = Date.now() + 5000;
    for (;;) {
      const next = frames.shift();
      if (next) return next;
      assert.ok(Date.now() < deadline, "timed out waiting for a frame");
      await new Promise((resolve) => setTimeout(resolve, 5));
    }
  };
}

test("link: add sends a workspaces frame and the next hello carries the folder", async () => {
  const base = await tempDir("nex-roots-link-");
  const envRoot = join(base, "env");
  const added = join(base, "added");
  await mkdir(envRoot);
  await mkdir(added);
  const roots = makeRoots([envRoot], join(base, "data"));
  await roots.load();

  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const registry = new ChannelRegistry();
  registerChannels(registry, { engine, publisher, logger, roots, runnerId: "runner_link" });

  const helloWorkspaces = () =>
    roots.list().map((folder) => ({ path: folder.path, label: folder.label }));

  let firstDone: (() => void) | null = null;
  const firstScenario = new Promise<void>((resolve) => {
    firstDone = resolve;
  });
  let failure: Error | null = null;
  let observedFrame: Record<string, unknown> | null = null;

  const server = new WebSocketServer({ port: 0, host: "127.0.0.1" });
  await new Promise<void>((resolve) => server.once("listening", () => resolve()));
  server.on("connection", (socket) => {
    void (async () => {
      try {
        const next = frameReader(socket);
        const hello = await next();
        assert.equal(hello["t"], "hello");
        assert.deepEqual(hello["workspaces"], [{ path: envRoot, label: "env" }]);
        socket.send(JSON.stringify({ t: "welcome" }));
        socket.send(
          JSON.stringify({
            t: "call",
            id: "add-1",
            channel: "runner-workspaces",
            method: "add",
            arg: [{ path: added, workspacePath: "~" }],
          }),
        );
        // The result and the workspaces frame both arrive; order is not fixed,
        // so read until both are seen.
        let result: Record<string, unknown> | null = null;
        let workspacesFrame: Record<string, unknown> | null = null;
        while (!result || !workspacesFrame) {
          const frame = await next();
          if (frame["t"] === "result" && frame["id"] === "add-1") result = frame;
          if (frame["t"] === "workspaces") workspacesFrame = frame;
        }
        assert.equal(result["ok"], true);
        const value = result["value"] as { path: string; workspaceIdentity: string };
        assert.equal(value.path, added);
        assert.equal(value.workspaceIdentity, `runner:runner_link:${added}`);
        assert.deepEqual(workspacesFrame["workspaces"], [
          { path: envRoot, label: "env" },
          { path: added, label: "added" },
        ]);
        observedFrame = workspacesFrame;
        firstDone?.();
      } catch (error) {
        failure = error instanceof Error ? error : new Error(String(error));
        firstDone?.();
      }
    })();
  });
  const address = server.address() as AddressInfo;
  const url = `ws://127.0.0.1:${address.port}/runner`;
  const link = new RunnerLink({
    url,
    token: undefined,
    logger,
    registry,
    hello: {
      runnerId: "runner_link",
      name: "test",
      version: "0.1.0",
      os: "darwin",
      arch: "arm64",
      // docs/next/runner-update.md decision 2: hello now carries both fields.
      buildId: "0123456789abcdef",
      capabilities: ["runner-workspaces", "machine-scope", "search", "self-update"],
      workspaces: helloWorkspaces,
    },
    pingIntervalMs: 60_000,
  });
  // Same wiring as main.ts: add/remove pushes the workspaces frame.
  publishWorkspaceFrames(roots, link);
  link.start();
  try {
    await firstScenario;
    if (failure) throw failure;
    assert.ok(observedFrame);
  } finally {
    await link.stop();
    publisher.dispose();
    await new Promise<void>((resolve) => {
      for (const client of server.clients) client.terminate();
      server.close(() => resolve());
    });
  }

  // A fresh connection (reconnect) reports the added folder in its hello.
  const hellos: Array<Record<string, unknown>> = [];
  const server2 = new WebSocketServer({ port: 0, host: "127.0.0.1" });
  await new Promise<void>((resolve) => server2.once("listening", () => resolve()));
  let secondDone: (() => void) | null = null;
  const secondScenario = new Promise<void>((resolve) => {
    secondDone = resolve;
  });
  server2.on("connection", (socket) => {
    void (async () => {
      const next = frameReader(socket);
      hellos.push(await next());
      secondDone?.();
    })();
  });
  const address2 = server2.address() as AddressInfo;
  const link2 = new RunnerLink({
    url: `ws://127.0.0.1:${address2.port}/runner`,
    token: undefined,
    logger,
    registry,
    hello: {
      runnerId: "runner_link",
      name: "test",
      version: "0.1.0",
      os: "darwin",
      arch: "arm64",
      // docs/next/runner-update.md decision 2: hello now carries both fields.
      buildId: "0123456789abcdef",
      capabilities: ["runner-workspaces", "machine-scope", "search", "self-update"],
      workspaces: helloWorkspaces,
    },
    pingIntervalMs: 60_000,
  });
  link2.start();
  try {
    await secondScenario;
    assert.deepEqual(hellos[0]?.["workspaces"], [
      { path: envRoot, label: "env" },
      { path: added, label: "added" },
    ]);
  } finally {
    await link2.stop();
    await new Promise<void>((resolve) => {
      for (const client of server2.clients) client.terminate();
      server2.close(() => resolve());
    });
  }
});
