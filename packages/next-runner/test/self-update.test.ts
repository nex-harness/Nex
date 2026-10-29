import { test } from "node:test";
import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { WebSocketServer } from "ws";
import { RunnerLink } from "../src/link/client.js";
import { ChannelRegistry } from "../src/link/registry.js";
import { RUNNER_CAPABILITIES } from "../src/link/frames.js";
import {
  BUILD_ID_FILES,
  computeBuildId,
  currentBuildId,
  DEV_BUILD_ID,
} from "../src/update/buildId.js";
import { SelfUpdater, RESTART_EXIT_CODE, bundleOrigin } from "../src/update/selfUpdate.js";
import {
  PENDING_FILE,
  PREV_DIR,
  UPDATE_DIR,
  UPDATE_FILES,
  rollbackPendingUpdate,
  ROLLBACK_ATTEMPTS,
} from "../src/update/rollback.js";
import { resolveSupervisorMode } from "../src/update/supervisor.js";
import type { Logger } from "../src/logger.js";

const quietLogger: Logger = {
  debug: () => undefined,
  info: () => undefined,
  warn: () => undefined,
  error: () => undefined,
  child: () => quietLogger,
};

const PLATFORM = "darwin-arm64";

/** Deterministic fake bundle contents; `tag` distinguishes builds. */
function bundleFiles(tag: string): Map<string, Buffer> {
  return new Map<string, Buffer>([
    ["runner.mjs", Buffer.from(`// runner ${tag}\nexport const build = ${JSON.stringify(tag)};\n`)],
    ["pty-host.mjs", Buffer.from(`// pty-host ${tag}\n`)],
    ["lib/ptyHostAssets.mjs", Buffer.from(`// assets ${tag}\n`)],
    ["build.json", Buffer.from(JSON.stringify({ buildId: tag }) + "\n")],
    ["package.json", Buffer.from(JSON.stringify({ dependencies: { "node-pty": "1.1.0" } }) + "\n")],
  ]);
}

function buildIdOf(files: Map<string, Buffer>): string {
  return computeBuildId(BUILD_ID_FILES.map((file) => files.get(file) as Buffer));
}

async function makeInstallDir(files: Map<string, Buffer>): Promise<string> {
  const dir = await mkdtemp(join(tmpdir(), "nex-runner-update-"));
  for (const [file, content] of files) {
    const target = join(dir, file);
    await mkdir(dirname(target), { recursive: true });
    await writeFile(target, content);
  }
  return dir;
}

async function exists(path: string): Promise<boolean> {
  try {
    await readFile(path);
    return true;
  } catch {
    return false;
  }
}

interface BundleServer {
  origin: string;
  requests: string[];
  close: () => Promise<void>;
}

/** Serves the allow-listed bundle files at /api/runner-bundle/<platform>/<file>. */
async function startBundleServer(files: Map<string, Buffer>): Promise<BundleServer> {
  const requests: string[] = [];
  const server: Server = createServer((request, response) => {
    requests.push(request.url ?? "");
    const prefix = `/api/runner-bundle/${PLATFORM}/`;
    const url = request.url ?? "";
    if (request.headers.authorization !== "Bearer runner-token") {
      response.writeHead(401).end("unauthorized");
      return;
    }
    if (!url.startsWith(prefix)) {
      response.writeHead(404).end("not found");
      return;
    }
    const content = files.get(url.slice(prefix.length));
    if (!content) {
      response.writeHead(404).end("not found");
      return;
    }
    response.writeHead(200, { "content-type": "application/octet-stream" });
    response.end(content);
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const { port } = server.address() as AddressInfo;
  return {
    origin: `http://127.0.0.1:${port}`,
    requests,
    close: () =>
      new Promise<void>((resolve) => {
        server.close(() => resolve());
      }),
  };
}

interface UpdaterHarness {
  statuses: Array<{ state: string; buildId: string; error?: string }>;
  exits: number[];
  spawns: Array<{ command: string; args: readonly string[] }>;
  stopLinkCalls: number;
  updater: SelfUpdater;
}

function makeUpdater(
  installDir: string,
  origin: string,
  buildId: string,
  env: NodeJS.ProcessEnv = { NEX_RUNNER_SUPERVISOR: "loop" },
): UpdaterHarness {
  const statuses: UpdaterHarness["statuses"] = [];
  const exits: number[] = [];
  const spawns: UpdaterHarness["spawns"] = [];
  const harness: UpdaterHarness = {
    statuses,
    exits,
    spawns,
    stopLinkCalls: 0,
    updater: new SelfUpdater({
      installDir,
      buildId,
      serverUrl: `${origin.replace("http:", "ws:")}/runner`,
      token: "runner-token",
      logger: quietLogger,
      sendStatus: (status) => statuses.push(status),
      stopLink: async () => {
        harness.stopLinkCalls += 1;
      },
      platform: PLATFORM,
      env,
      exit: (code) => exits.push(code),
      spawn: (command, args) => {
        spawns.push({ command, args });
        return { unref: () => undefined };
      },
      execPath: "/usr/local/bin/node",
      argv: ["/usr/local/bin/node", "runner.mjs"],
      cwd: "/install",
    }),
  };
  return harness;
}

test("buildId: bundle script and runtime implementation agree", async () => {
  const scriptModule = (await import("../scripts/buildId.mjs")) as {
    BUILD_ID_FILES: readonly string[];
    computeBuildId: (files: ReadonlyArray<Uint8Array>) => string;
  };
  assert.deepEqual([...scriptModule.BUILD_ID_FILES], [...BUILD_ID_FILES]);
  for (const tag of ["alpha", "beta", "gamma"]) {
    const files = BUILD_ID_FILES.map((file) => bundleFiles(tag).get(file) as Buffer);
    assert.equal(scriptModule.computeBuildId(files), computeBuildId(files));
  }
  // Content-derived and order-sensitive.
  const sample = BUILD_ID_FILES.map((file) => bundleFiles("x").get(file) as Buffer);
  const [first, second, third] = [sample[0] as Buffer, sample[1] as Buffer, sample[2] as Buffer];
  assert.equal(computeBuildId([first, second, third]), computeBuildId([first, second, third]));
  assert.notEqual(computeBuildId([first, second, third]), computeBuildId([second, first, third]));
  assert.match(computeBuildId([first, second, third]), /^[a-f0-9]{16}$/u);
});

test("buildId: currentBuildId hashes installed files and reports dev when absent", async () => {
  const files = bundleFiles("installed");
  const dir = await makeInstallDir(files);
  try {
    assert.equal(await currentBuildId(dir), buildIdOf(files));
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
  assert.equal(await currentBuildId(join(tmpdir(), "nex-missing-bundle-dir")), DEV_BUILD_ID);
});

test("hello carries buildId and capabilities", async () => {
  const server = new WebSocketServer({ port: 0, host: "127.0.0.1" });
  await once(server, "listening");
  const { port } = server.address() as AddressInfo;
  const helloReceived = new Promise<Record<string, unknown>>((resolve) => {
    server.on("connection", (socket) => {
      socket.on("message", (data) => {
        const frame = JSON.parse(data.toString("utf8")) as Record<string, unknown>;
        if (frame["t"] === "hello") resolve(frame);
      });
    });
  });
  const link = new RunnerLink({
    url: `ws://127.0.0.1:${port}/runner`,
    token: "runner-token",
    logger: quietLogger,
    registry: new ChannelRegistry(),
    hello: {
      runnerId: "runner_test",
      name: "test",
      version: "0.1.0",
      os: "darwin",
      arch: "arm64",
      buildId: "0123456789abcdef",
      capabilities: [...RUNNER_CAPABILITIES],
      workspaces: [],
    },
    pingIntervalMs: 60_000,
  });
  link.start();
  try {
    const hello = await helloReceived;
    assert.equal(hello["buildId"], "0123456789abcdef");
    assert.deepEqual(hello["capabilities"], [...RUNNER_CAPABILITIES]);
  } finally {
    await link.stop();
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test("self-update: downloads, verifies, swaps, keeps .prev and exits 75", async () => {
  const current = bundleFiles("old");
  const incoming = bundleFiles("new");
  const targetBuildId = buildIdOf(incoming);
  const installDir = await makeInstallDir(current);
  const bundleServer = await startBundleServer(incoming);
  const harness = makeUpdater(installDir, bundleServer.origin, buildIdOf(current));
  try {
    await harness.updater.handleUpdate(targetBuildId);

    for (const file of UPDATE_FILES) {
      assert.equal(
        await readFile(join(installDir, file), "utf8"),
        (incoming.get(file) as Buffer).toString("utf8"),
        `${file} should be replaced`,
      );
      assert.equal(
        await readFile(join(installDir, PREV_DIR, file), "utf8"),
        (current.get(file) as Buffer).toString("utf8"),
        `${file} should be backed up`,
      );
    }
    const pending = JSON.parse(await readFile(join(installDir, PENDING_FILE), "utf8")) as {
      buildId: string;
      previousBuildId: string;
      attempts: number;
    };
    assert.equal(pending.buildId, targetBuildId);
    assert.equal(pending.previousBuildId, buildIdOf(current));
    assert.equal(pending.attempts, 0);
    assert.deepEqual(
      harness.statuses.map((status) => status.state),
      ["downloading", "restarting"],
    );
    assert.deepEqual(harness.exits, [RESTART_EXIT_CODE]);
    assert.equal(harness.stopLinkCalls, 1);
    // One request per allow-listed file, with the runner token.
    assert.equal(bundleServer.requests.length, 5);
    assert.ok(
      bundleServer.requests.every((url) => url.startsWith(`/api/runner-bundle/${PLATFORM}/`)),
    );
  } finally {
    await bundleServer.close();
    await rm(installDir, { recursive: true, force: true });
  }
});

test("self-update: wrong buildId aborts without swapping", async () => {
  const current = bundleFiles("old");
  const incoming = bundleFiles("new");
  const installDir = await makeInstallDir(current);
  const bundleServer = await startBundleServer(incoming);
  const harness = makeUpdater(installDir, bundleServer.origin, buildIdOf(current));
  try {
    // The frame promises a different id than the served content hashes to.
    await harness.updater.handleUpdate("ffffffffffffffff");

    assert.equal(
      await readFile(join(installDir, "runner.mjs"), "utf8"),
      (current.get("runner.mjs") as Buffer).toString("utf8"),
    );
    assert.equal(await exists(join(installDir, PREV_DIR)), false);
    assert.equal(await exists(join(installDir, PENDING_FILE)), false);
    assert.equal(harness.exits.length, 0);
    assert.deepEqual(
      harness.statuses.map((status) => status.state),
      ["downloading", "failed"],
    );
    assert.match(harness.statuses[1]?.error ?? "", /does not match/u);
  } finally {
    await bundleServer.close();
    await rm(installDir, { recursive: true, force: true });
  }
});

test("self-update: changed dependencies abort with reinstall required", async () => {
  const current = bundleFiles("old");
  const incoming = bundleFiles("new");
  incoming.set(
    "package.json",
    Buffer.from(JSON.stringify({ dependencies: { "node-pty": "2.0.0" } }) + "\n"),
  );
  const installDir = await makeInstallDir(current);
  const bundleServer = await startBundleServer(incoming);
  const harness = makeUpdater(installDir, bundleServer.origin, buildIdOf(current));
  try {
    await harness.updater.handleUpdate(buildIdOf(incoming));

    assert.equal(
      await readFile(join(installDir, "runner.mjs"), "utf8"),
      (current.get("runner.mjs") as Buffer).toString("utf8"),
    );
    assert.equal(await exists(join(installDir, PENDING_FILE)), false);
    assert.equal(harness.exits.length, 0);
    assert.deepEqual(
      harness.statuses.map((status) => status.state),
      ["downloading", "failed"],
    );
    assert.match(harness.statuses[1]?.error ?? "", /dependencies changed; reinstall required/u);
  } finally {
    await bundleServer.close();
    await rm(installDir, { recursive: true, force: true });
  }
});

test("self-update: same buildId is ignored without downloading", async () => {
  const current = bundleFiles("same");
  const installDir = await makeInstallDir(current);
  const bundleServer = await startBundleServer(current);
  const harness = makeUpdater(installDir, bundleServer.origin, buildIdOf(current));
  try {
    await harness.updater.handleUpdate(buildIdOf(current));
    assert.equal(bundleServer.requests.length, 0);
    assert.deepEqual(harness.statuses, []);
    assert.deepEqual(harness.exits, []);
  } finally {
    await bundleServer.close();
    await rm(installDir, { recursive: true, force: true });
  }
});

test("self-update: dev build never updates", async () => {
  const incoming = bundleFiles("new");
  const installDir = await makeInstallDir(bundleFiles("old"));
  const bundleServer = await startBundleServer(incoming);
  const harness = makeUpdater(installDir, bundleServer.origin, DEV_BUILD_ID);
  try {
    await harness.updater.handleUpdate(buildIdOf(incoming));
    assert.equal(bundleServer.requests.length, 0);
    assert.deepEqual(harness.statuses, []);
  } finally {
    await bundleServer.close();
    await rm(installDir, { recursive: true, force: true });
  }
});

test("self-update: unsupervised install spawns a detached successor", async () => {
  const current = bundleFiles("old");
  const incoming = bundleFiles("new");
  const installDir = await makeInstallDir(current);
  const bundleServer = await startBundleServer(incoming);
  const harness = makeUpdater(installDir, bundleServer.origin, buildIdOf(current), {});
  try {
    await harness.updater.handleUpdate(buildIdOf(incoming));
    assert.equal(harness.spawns.length, 1);
    assert.equal(harness.spawns[0]?.command, "/usr/local/bin/node");
    assert.deepEqual([...(harness.spawns[0]?.args ?? [])], ["runner.mjs"]);
    assert.deepEqual(harness.exits, [RESTART_EXIT_CODE]);
  } finally {
    await bundleServer.close();
    await rm(installDir, { recursive: true, force: true });
  }
});

test("supervisor: declared modes and platform detection", () => {
  assert.equal(resolveSupervisorMode({ NEX_RUNNER_SUPERVISOR: "systemd" }), "managed");
  assert.equal(resolveSupervisorMode({ NEX_RUNNER_SUPERVISOR: "launchd" }), "managed");
  assert.equal(resolveSupervisorMode({ NEX_RUNNER_SUPERVISOR: "loop" }), "managed");
  assert.equal(resolveSupervisorMode({ NEX_RUNNER_SUPERVISOR: "none" }), "respawn");
  assert.equal(resolveSupervisorMode({}, "linux"), "respawn");
  assert.equal(resolveSupervisorMode({ INVOCATION_ID: "abc" }, "linux"), "managed");
  assert.equal(resolveSupervisorMode({ XPC_SERVICE_NAME: "cn.nex.runner" }, "darwin"), "managed");
  assert.equal(resolveSupervisorMode({ XPC_SERVICE_NAME: "com.apple.other" }, "darwin"), "respawn");
  assert.equal(resolveSupervisorMode({}, "win32"), "respawn");
});

test("bundleOrigin maps ws/wss to http/https", () => {
  assert.equal(bundleOrigin("wss://nex.example.com/runner"), "https://nex.example.com");
  assert.equal(bundleOrigin("ws://127.0.0.1:3030/runner"), "http://127.0.0.1:3030");
});

test("rollback: restores .prev after the attempt budget is spent", async () => {
  const previous = bundleFiles("previous");
  const broken = bundleFiles("broken");
  const installDir = await makeInstallDir(broken);
  try {
    for (const [file, content] of previous) {
      const target = join(installDir, PREV_DIR, file);
      await mkdir(dirname(target), { recursive: true });
      await writeFile(target, content);
    }
    await writeFile(
      join(installDir, PENDING_FILE),
      JSON.stringify({
        buildId: buildIdOf(broken),
        previousBuildId: buildIdOf(previous),
        at: Date.now() - 120_000,
        attempts: 0,
      }),
    );
    // The first two starts only count; the third rolls back.
    for (let attempt = 1; attempt < ROLLBACK_ATTEMPTS; attempt += 1) {
      await rollbackPendingUpdate({ installDir, logger: quietLogger });
      assert.equal(await exists(join(installDir, PENDING_FILE)), true);
      assert.equal(
        await readFile(join(installDir, "runner.mjs"), "utf8"),
        (broken.get("runner.mjs") as Buffer).toString("utf8"),
      );
    }
    await rollbackPendingUpdate({ installDir, logger: quietLogger });
    assert.equal(await exists(join(installDir, PENDING_FILE)), false);
    assert.equal(
      await readFile(join(installDir, "runner.mjs"), "utf8"),
      (previous.get("runner.mjs") as Buffer).toString("utf8"),
    );
    assert.deepEqual(await readdir(join(installDir, UPDATE_DIR)).catch(() => []), []);
  } finally {
    await rm(installDir, { recursive: true, force: true });
  }
});

test("rollback: a fresh pending marker is not rolled back", async () => {
  const broken = bundleFiles("broken");
  const installDir = await makeInstallDir(broken);
  try {
    await writeFile(
      join(installDir, PENDING_FILE),
      JSON.stringify({
        buildId: buildIdOf(broken),
        previousBuildId: "old",
        at: Date.now(),
        attempts: ROLLBACK_ATTEMPTS,
      }),
    );
    await rollbackPendingUpdate({ installDir, logger: quietLogger });
    // Still pending: the update may simply be restarting.
    assert.equal(await exists(join(installDir, PENDING_FILE)), true);
  } finally {
    await rm(installDir, { recursive: true, force: true });
  }
});

test("self-update: markStartupComplete clears the pending marker", async () => {
  const files = bundleFiles("new");
  const installDir = await makeInstallDir(files);
  const harness = makeUpdater(installDir, "http://127.0.0.1:1", buildIdOf(files));
  try {
    await writeFile(
      join(installDir, PENDING_FILE),
      JSON.stringify({ buildId: buildIdOf(files), previousBuildId: "old", at: Date.now() }),
    );
    await mkdir(join(installDir, UPDATE_DIR, buildIdOf(files)), { recursive: true });
    await harness.updater.markStartupComplete();
    assert.equal(await exists(join(installDir, PENDING_FILE)), false);
    assert.equal(await exists(join(installDir, UPDATE_DIR)), false);
  } finally {
    await rm(installDir, { recursive: true, force: true });
  }
});
