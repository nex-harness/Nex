// Runner self-update E2E for the next/ stack (docs/next/runner-update.md):
//
//   1. A bundled runner ("gamma") runs from an install dir under a restart loop that
//      restarts it on exit code 75 only (the installer's loop wrapper). Its install
//      dir holds build OLD; the server's bundle dir for this platform holds build NEW.
//   2. GET /api/config/runners reports gamma outdated with both build ids.
//   3. The server sends the update frame on connect: gamma downloads the bundle over
//      /api/runner-bundle (runner token), verifies it, swaps its files, exits 75, the
//      loop restarts it, and it reconnects with build NEW.
//   4. /api/config/runners shows gamma up to date with the self-update capabilities;
//      /api/server-info lists runnerBuildId NEW for gamma; the install dir keeps .prev.
//
// Env (set by next/dev.sh):
//   NEX_E2E_BASE_URL        Web origin (only used for login)  (default http://localhost:5174)
//   NEX_E2E_SERVER_ORIGIN   Server origin                     (required, e.g. http://127.0.0.1:3031)
//   NEX_E2E_TOKEN           Browser login token               (required)
//   NEX_E2E_RUNNER_TOKEN    Runner token                      (required)
//   NEX_E2E_BUNDLES_DIR     Server bundle dir (NEX_RUNNER_BUNDLES_DIR) (required)
//   NEX_E2E_GAMMA_DIR       Scratch dir for gamma's install    (required)
//   NEX_E2E_GAMMA_WORKSPACE Workspace gamma serves             (required)
//   NEX_E2E_OUT             Summary dir                        (default <repo>/next/e2e/out)

import { spawn, execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, "../..");
const SERVER = (process.env.NEX_E2E_SERVER_ORIGIN?.trim() ?? "").replace(/\/+$/u, "");
const TOKEN = process.env.NEX_E2E_TOKEN?.trim() ?? "";
const RUNNER_TOKEN = process.env.NEX_E2E_RUNNER_TOKEN?.trim() ?? "";
const BUNDLES_DIR = process.env.NEX_E2E_BUNDLES_DIR?.trim() ?? "";
const GAMMA_DIR = process.env.NEX_E2E_GAMMA_DIR?.trim() ?? "";
const GAMMA_WORKSPACE = process.env.NEX_E2E_GAMMA_WORKSPACE?.trim() ?? "";
const OUT_DIR = path.resolve(
  REPO_ROOT,
  process.env.NEX_E2E_OUT?.trim() || path.join("next", "e2e", "out"),
);
for (const [name, value] of Object.entries({
  NEX_E2E_SERVER_ORIGIN: SERVER,
  NEX_E2E_TOKEN: TOKEN,
  NEX_E2E_RUNNER_TOKEN: RUNNER_TOKEN,
  NEX_E2E_BUNDLES_DIR: BUNDLES_DIR,
  NEX_E2E_GAMMA_DIR: GAMMA_DIR,
  NEX_E2E_GAMMA_WORKSPACE: GAMMA_WORKSPACE,
})) {
  if (!value) {
    console.error(`[update] ${name} is required.`);
    process.exit(2);
  }
}

const PLATFORM = `${process.platform}-${process.arch}`;
const GAMMA_ID = "gamma";
const DIST = path.join(REPO_ROOT, "packages", "next-runner", "dist");
const BUILD_FILES = ["runner.mjs", "pty-host.mjs", path.join("lib", "ptyHostAssets.mjs")];

const checks = {};
const failures = [];
const details = {};
function check(name, ok, detail) {
  checks[name] = Boolean(ok);
  if (detail !== undefined) details[name] = detail;
  if (!ok) failures.push(name);
}
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
async function waitFor(predicate, timeoutMs, pollMs = 1_000) {
  const deadline = Date.now() + timeoutMs;
  let last;
  while (Date.now() < deadline) {
    last = await predicate();
    if (last) return last;
    await sleep(pollMs);
  }
  return last;
}

/** The runner's buildId algorithm (docs/next/runner-update.md decision 1). */
function buildIdOf(dir) {
  const hash = createHash("sha256");
  for (const file of BUILD_FILES) hash.update(fs.readFileSync(path.join(dir, file)));
  return hash.digest("hex").slice(0, 16);
}

function copyBuild(from, to) {
  fs.mkdirSync(path.join(to, "lib"), { recursive: true });
  for (const file of [...BUILD_FILES, "package.json", "node-pty-assets.tar.gz"]) {
    const source = path.join(from, file);
    if (fs.existsSync(source)) fs.copyFileSync(source, path.join(to, file));
  }
}

function writeBuildJson(dir) {
  const buildId = buildIdOf(dir);
  fs.writeFileSync(
    path.join(dir, "build.json"),
    `${JSON.stringify({ buildId, version: "0.1.0", builtAt: new Date().toISOString() })}\n`,
  );
  return buildId;
}

let cookie = "";
async function login() {
  const response = await fetch(`${SERVER}/api/auth/login`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ token: TOKEN }),
  });
  cookie = (response.headers.get("set-cookie") ?? "").split(";")[0];
  return response.ok;
}
async function gammaStatus() {
  const response = await fetch(`${SERVER}/api/config/runners`, { headers: { cookie } });
  if (!response.ok) return null;
  const body = await response.json();
  return (body.runners ?? []).find((runner) => runner.runnerId === GAMMA_ID) ?? null;
}

let loop;
try {
  fs.mkdirSync(OUT_DIR, { recursive: true });
  // ── build NEW (the current source) and OLD (NEW + a marker comment) ──
  execFileSync("node", ["scripts/bundle.mjs"], {
    cwd: path.join(REPO_ROOT, "packages", "next-runner"),
    stdio: "ignore",
  });
  const install = path.join(GAMMA_DIR, "install");
  fs.rmSync(GAMMA_DIR, { recursive: true, force: true });
  fs.mkdirSync(install, { recursive: true });
  fs.mkdirSync(GAMMA_WORKSPACE, { recursive: true });

  const platformBundle = path.join(BUNDLES_DIR, PLATFORM);
  fs.rmSync(platformBundle, { recursive: true, force: true });
  copyBuild(DIST, platformBundle);
  const newBuild = writeBuildJson(platformBundle);

  copyBuild(DIST, install);
  fs.appendFileSync(path.join(install, "runner.mjs"), "\n// e2e: old build marker\n");
  const oldBuild = writeBuildJson(install);
  check("buildsDiffer", oldBuild !== newBuild, { oldBuild, newBuild });

  // The install needs its runtime deps: link the repo's node_modules (the bundle keeps
  // pi and node-pty external, exactly like a real install's npm step).
  fs.symlinkSync(path.join(REPO_ROOT, "node_modules"), path.join(install, "node_modules"), "dir");

  // ── gamma under the installer's restart loop (restart on 75 only) ──
  const env = {
    ...process.env,
    NEX_SERVER_URL: `${SERVER.replace(/^http/u, "ws")}/runner`,
    NEX_RUNNER_TOKEN: RUNNER_TOKEN,
    NEX_RUNNER_ID: GAMMA_ID,
    NEX_RUNNER_NAME: "nex-next-gamma",
    NEX_RUNNER_WORKSPACES: GAMMA_WORKSPACE,
    NEX_RUNNER_DATA_DIR: path.join(GAMMA_DIR, "data"),
    NEX_RUNNER_SUPERVISOR: "loop",
  };
  const loopScript = `while :; do "${process.execPath}" runner.mjs; code=$?; [ "$code" -eq 75 ] || exit "$code"; done`;
  loop = spawn("sh", ["-c", loopScript], {
    cwd: install,
    env,
    stdio: ["ignore", "pipe", "pipe"],
    detached: true,
  });
  const logLines = [];
  for (const stream of [loop.stdout, loop.stderr]) {
    stream.on("data", (chunk) => {
      for (const line of chunk.toString().split("\n")) if (line.trim()) logLines.push(line);
    });
  }

  check("login", await login());

  // ── 2-3. the server asks gamma to update; gamma comes back with NEW ──
  const firstSeen = await waitFor(async () => {
    const status = await gammaStatus();
    return status?.connected ? status : null;
  }, 60_000);
  check("gammaConnected", Boolean(firstSeen), firstSeen);
  check("reportsLatestBuild", firstSeen?.latestBuildId === newBuild, {
    latestBuildId: firstSeen?.latestBuildId,
    newBuild,
  });

  const updated = await waitFor(async () => {
    const status = await gammaStatus();
    return status?.connected && status.buildId === newBuild ? status : null;
  }, 90_000);
  check("gammaUpdatedItself", Boolean(updated), {
    status: updated ?? (await gammaStatus()),
    log: logLines.filter((line) => /update|restart|exit/iu.test(line)).slice(-12),
  });
  check("upToDateAfterUpdate", updated?.outdated === false, updated);
  // The update finishes within a second of connecting, faster than any poll can
  // observe "outdated"; the swap log proves gamma started on OLD and moved to NEW.
  const swapLine = logLines.find((line) => /self-update swapped files/u.test(line)) ?? "";
  check(
    "swappedFromOldToNew",
    swapLine.includes(`from="${oldBuild}"`) && swapLine.includes(`to="${newBuild}"`),
    swapLine,
  );
  check(
    "capabilitiesReported",
    ["runner-workspaces", "self-update"].every((name) => updated?.capabilities?.includes(name)),
    updated?.capabilities,
  );
  check("installFilesSwapped", buildIdOf(install) === newBuild, { onDisk: buildIdOf(install) });
  check("previousBuildKept", fs.existsSync(path.join(install, ".prev", "runner.mjs")));
  const pendingCleared = await waitFor(
    async () => (!fs.existsSync(path.join(install, ".update-pending")) ? true : null),
    15_000,
  );
  check("updatePendingCleared", Boolean(pendingCleared));

  const info = await (await fetch(`${SERVER}/api/server-info`, { headers: { cookie } })).json();
  const gammaWorkspace = (info.workspaces ?? []).find((entry) => entry.runnerId === GAMMA_ID);
  check("serverInfoBuildId", gammaWorkspace?.runnerBuildId === newBuild, gammaWorkspace);

  // ── the bundle endpoint refuses requests without the runner token ──
  const anonymous = await fetch(`${SERVER}/api/runner-bundle/${PLATFORM}/runner.mjs`);
  check("bundleEndpointNeedsRunnerToken", anonymous.status === 401, { status: anonymous.status });
} catch (error) {
  failures.push("exception");
  details.exception = error instanceof Error ? (error.stack ?? error.message) : String(error);
} finally {
  if (loop?.pid) {
    try {
      process.kill(-loop.pid, "SIGTERM");
    } catch {
      // The loop may already be gone.
    }
  }
}

const summary = { ok: failures.length === 0, failures, checks, details };
fs.writeFileSync(
  path.join(OUT_DIR, "update-summary.json"),
  `${JSON.stringify(summary, null, 2)}\n`,
);
console.log(JSON.stringify(summary, null, 2));
process.exit(failures.length === 0 ? 0 : 1);
