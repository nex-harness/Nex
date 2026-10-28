// Batch 1 workspace tool channels (docs/next/workspace-tools.md) 的消险单测：
// 路径包含校验（traversal / symlink 逃逸 / 顶层路径）、`$buf` 编码、
// terminal 重挂（决策 5）与首个 listener 的 serialize 回放、两个 viewer
// 收到同一份数据、exit 分发与 entry 回收。
//
// 终端走真实 pty-host 子进程（与 pty-host.test.ts 相同的入口），断言
// channel 层的包装而不是 pty 本身的稳定性。

import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdtemp, mkdir, writeFile, symlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { ChannelRegistry } from "../src/link/registry.js";
import {
  encodeBufferForWire,
  decodeWireBuffer,
  registerWorkspaceTools,
  resolveInsideWorkspace,
} from "../src/channels/workspaceTools.js";
import type { Logger } from "../src/logger.js";
import { testRoots } from "./helpers/testRoots.js";

const logger: Logger = {
  debug: () => undefined,
  info: () => undefined,
  warn: () => undefined,
  error: () => undefined,
  child: () => logger,
} as unknown as Logger;

test("resolveInsideWorkspace: prefix, traversal and sibling rejection", () => {
  const roots = ["/srv/work"];
  assert.equal(resolveInsideWorkspace("/srv/work", roots), "/srv/work");
  assert.equal(resolveInsideWorkspace("/srv/work/sub/file.ts", roots), "/srv/work/sub/file.ts");
  assert.equal(resolveInsideWorkspace("/srv/work/../work/x", roots), "/srv/work/x");
  // Traversal escaping the root, sibling directories and absolute paths outside.
  assert.equal(resolveInsideWorkspace("/srv/work/../../etc/passwd", roots), null);
  assert.equal(resolveInsideWorkspace("/srv/other", roots), null);
  assert.equal(resolveInsideWorkspace("/etc/passwd", roots), null);
  // Relative paths are refused outright (decision 2: absolute only).
  assert.equal(resolveInsideWorkspace("relative/file.ts", roots), null);
  // Prefix-collision trap: /srv/work2 is not inside /srv/work.
  assert.equal(resolveInsideWorkspace("/srv/work2", roots), null);
});

test("$buf wire codec round trip", () => {
  const data = new Uint8Array([0, 1, 2, 255, 254]);
  const encoded = encodeBufferForWire(data);
  assert.equal(encoded.$buf, Buffer.from(data).toString("base64"));
  assert.equal(encoded.$vs, false);
  const decoded = decodeWireBuffer(encoded);
  assert.ok(decoded);
  assert.deepEqual(Array.from(decoded), [0, 1, 2, 255, 254]);
  assert.equal(decodeWireBuffer({ nope: 1 }), null);
  assert.equal(decodeWireBuffer(null), null);
});

test("file channel: readdir/stat/readFileRange with path containment", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-tools-"));
  const workspace = join(root, "workspace");
  await mkdir(workspace);
  await writeFile(join(workspace, "hello.txt"), "hello world");
  await mkdir(join(root, "outside"));
  await writeFile(join(root, "outside", "secret.txt"), "nope");

  const registry = new ChannelRegistry();
  const tools = registerWorkspaceTools(registry, { logger, roots: testRoots([workspace]) });

  try {
    const readdir = registry.getCall("file", "readdir");
    assert.ok(readdir);
    const entries = (await readdir({ path: workspace, includeHidden: true })) as Array<{
      name: string;
    }>;
    assert.ok(entries.some((entry) => entry.name === "hello.txt"));

    // A path outside the served root is refused.
    await assert.rejects(
      async () => readdir({ path: join(root, "outside") }),
      /outside this runner's workspaces/,
    );

    // readFileRange returns the $buf sentinel (the Go bridge converts it to a
    // real buffer for the browser).
    const readRange = registry.getCall("file", "readFileRange");
    assert.ok(readRange);
    const range = (await readRange({
      path: join(workspace, "hello.txt"),
      offset: 0,
      length: 5,
    })) as {
      $buf: string;
      $vs: boolean;
    };
    assert.equal(Buffer.from(range.$buf, "base64").toString("utf8"), "hello");
    assert.equal(range.$vs, false);
  } finally {
    await tools.dispose();
  }
});

test("file channel: symlink escaping the workspace root is refused", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-symlink-"));
  const workspace = join(root, "workspace");
  const outside = join(root, "outside");
  await mkdir(workspace);
  await mkdir(outside);
  await writeFile(join(outside, "secret.txt"), "nope");
  // A symlink inside the workspace pointing outside the root.
  await symlink(join(outside, "secret.txt"), join(workspace, "leak.txt"));

  const registry = new ChannelRegistry();
  const tools = registerWorkspaceTools(registry, { logger, roots: testRoots([workspace]) });
  try {
    const readdir = registry.getCall("file", "readdir");
    assert.ok(readdir);
    await assert.rejects(
      async () => readdir({ path: join(workspace, "leak.txt").toString() }) as never,
      /outside this runner's workspaces/,
    );
  } finally {
    await tools.dispose();
  }
});

test(
  "terminal channel: create, replay on first listen, reattach, two viewers, exit",
  {
    timeout: 60_000,
  },
  async () => {
    const root = await mkdtemp(join(tmpdir(), "nex-term-"));
    const workspace = join(root, "workspace");
    await mkdir(workspace);

    const registry = new ChannelRegistry();
    const tools = registerWorkspaceTools(registry, { logger, roots: testRoots([workspace]) });

    const create = registry.getCall("terminal", "create");
    const write = registry.getCall("terminal", "write");
    const resize = registry.getCall("terminal", "resize");
    const disposeCall = registry.getCall("terminal", "dispose");
    assert.ok(create && write && resize && disposeCall);

    try {
      // create with cwd inside the workspace.
      const created = (await create({
        cols: 80,
        rows: 24,
        cwd: workspace,
        workspacePath: workspace,
      })) as { id: string; shell: string; fontFamily: string };
      assert.ok(created.id);
      assert.ok(created.fontFamily !== undefined);

      // A cwd outside the served roots is refused.
      await assert.rejects(
        async () => create({ cols: 80, rows: 24, cwd: tmpdir(), workspacePath: workspace }),
        /outside this runner's workspaces/,
      );

      // First data listener gets the serialize replay, then live output.
      const marker = `term-ok-${Math.floor(Math.random() * 1_000_000)}`;
      let viewerA = "";
      let viewerB = "";
      let replayA: string | null = null;
      let replayB: string | null = null;
      const listenA = registry.getListen("terminal", "onDynamicData");
      const listenB = registry.getListen("terminal", "onDynamicData");
      assert.ok(listenA && listenB);
      const subscriptionA = listenA(created.id, (data) => {
        if (typeof data === "string") {
          if (replayA === null) replayA = data;
          viewerA += data;
        }
      });
      const subscriptionB = listenB(created.id, (data) => {
        if (typeof data === "string") {
          if (replayB === null) replayB = data;
          viewerB += data;
        }
      });

      await write({ id: created.id, data: `echo ${marker}\n` });
      const deadline = Date.now() + 20_000;
      while (!viewerA.includes(marker) || !viewerB.includes(marker)) {
        assert.ok(
          Date.now() < deadline,
          `timed out; A=${JSON.stringify(viewerA.slice(-200))} B=${JSON.stringify(viewerB.slice(-200))}`,
        );
        await new Promise((resolve) => setTimeout(resolve, 200));
      }
      // Both viewers saw the marker; the replay snapshot preceded live output.
      assert.ok(replayA !== null && replayB !== null);

      // Second-listen semantics: both listeners receive live chunks (single
      // writer, multiple viewers).

      // Reattach: create with the same terminalId returns the same id.
      const reattached = (await create({
        cols: 100,
        rows: 30,
        cwd: workspace,
        terminalId: created.id,
        workspacePath: workspace,
      })) as { id: string };
      assert.equal(reattached.id, created.id);

      // Exit listener fires on dispose and the id is reaped.
      let exitCode: number | null = null;
      const exitListen = registry.getListen("terminal", "onDynamicExit");
      assert.ok(exitListen);
      const exitSubscription = exitListen(created.id, (code) => {
        if (typeof code === "number") exitCode = code;
      });
      await disposeCall({ id: created.id });
      const exitDeadline = Date.now() + 10_000;
      while (exitCode === null) {
        assert.ok(Date.now() < exitDeadline, "timed out waiting for exit event");
        await new Promise((resolve) => setTimeout(resolve, 100));
      }
      // SIGTERM 下的 shell 可能以非零 code 退出（或以信号终止）；这里只锁「exit
      // 事件确实分发且 entry 被回收」。
      assert.equal(typeof exitCode, "number");
      // After exit the id is gone: a further write is rejected.
      await assert.rejects(async () => write({ id: created.id, data: "x" }), /Terminal not found/);
      subscriptionA?.dispose?.();
      subscriptionB?.dispose?.();
      exitSubscription?.dispose?.();
    } finally {
      await tools.dispose();
    }
  },
);

test("git channel: getRepositorySummary on a temp repo (workspacePath routed)", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-git-"));
  const workspace = join(root, "workspace");
  await mkdir(workspace);
  await writeFile(join(workspace, "a.txt"), "a\n");

  const registry = new ChannelRegistry();
  const tools = registerWorkspaceTools(registry, { logger, roots: testRoots([workspace]) });
  try {
    const summary = registry.getCall("git", "getRepositorySummary");
    assert.ok(summary);
    // Not a repository yet: the service reports isRepository false (git CLI may
    // be missing on the host; both shapes must not throw).
    const result = (await summary({ workspacePath: workspace })) as {
      summary?: { isRepository?: boolean; isGitAvailable?: boolean };
    };
    assert.ok(result && typeof result === "object");
    // A workspacePath outside the roots is refused before git runs.
    await assert.rejects(
      async () => summary({ workspacePath: join(root, "elsewhere") }),
      /outside this runner's workspaces/,
    );
  } finally {
    await tools.dispose();
  }
});

test("system channel: info reports the runner platform and first served root", async () => {
  const root = await mkdtemp(join(tmpdir(), "nex-sys-"));
  const workspace = join(root, "workspace");
  await mkdir(workspace);
  const registry = new ChannelRegistry();
  const tools = registerWorkspaceTools(registry, { logger, roots: testRoots([workspace]) });
  try {
    const info = registry.getCall("system", "info");
    assert.ok(info);
    const result = (await info({})) as { homedir: string; platform: string };
    assert.equal(result.homedir, workspace);
    assert.equal(result.platform, process.platform);
  } finally {
    await tools.dispose();
  }
});
