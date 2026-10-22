#!/usr/bin/env node
// Multi-runner E2E (docs/next/multi-runner.md §Acceptance):
// two runners (alpha, beta) both serving the SAME absolute path →
// picker lists both machines with that path → open beta's workspace → send a prompt →
// reply renders → the task row belongs to beta only (UI key + runner data dirs) →
// header shows beta's machine name → reload restores the tab on beta with the session
// still listed → open alpha's workspace for the same path → its task list is empty.
//
// Environment:
//   NEX_E2E_BASE_URL         Web origin to drive        (default http://localhost:5174)
//   NEX_E2E_TOKEN            Browser login token        (required)
//   NEX_E2E_CHROME           Chrome/Chromium executable (default: auto-detect)
//   NEX_E2E_OUT              Screenshot + summary dir   (default <repo>/next/e2e/out)
//   NEX_E2E_MULTI_WORKSPACE  Path served by alpha+beta  (required)
//   NEX_E2E_RUNNER_DATA_ALPHA  Alpha runner data dir    (required)
//   NEX_E2E_RUNNER_DATA_BETA   Beta runner data dir     (required)
//   NEX_E2E_ALPHA_NAME / NEX_E2E_BETA_NAME  Runner names (default nex-next-alpha/beta)
//
// Exit code 0 only when every check passes. A JSON summary is always printed.

import { createHash } from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(HERE, "../..");
const BASE_URL = (process.env.NEX_E2E_BASE_URL?.trim() || "http://localhost:5174").replace(
  /\/+$/u,
  "",
);
const TOKEN = process.env.NEX_E2E_TOKEN?.trim() ?? "";
const OUT_DIR = path.resolve(
  REPO_ROOT,
  process.env.NEX_E2E_OUT?.trim() || path.join("next", "e2e", "out"),
);
const WORKSPACE = path.resolve(process.env.NEX_E2E_MULTI_WORKSPACE?.trim() || "");
const ALPHA_DATA = process.env.NEX_E2E_RUNNER_DATA_ALPHA?.trim() ?? "";
const BETA_DATA = process.env.NEX_E2E_RUNNER_DATA_BETA?.trim() ?? "";
const ALPHA_NAME = process.env.NEX_E2E_ALPHA_NAME?.trim() || "nex-next-alpha";
const BETA_NAME = process.env.NEX_E2E_BETA_NAME?.trim() || "nex-next-beta";
const PROMPT = "Reply with exactly: beta-ok";
const REPLY_TIMEOUT_MS = Number(process.env.NEX_E2E_REPLY_TIMEOUT_MS) || 180_000;

function failConfig(message) {
  console.error(`[multi-runner] ${message}`);
  console.log(JSON.stringify({ ok: false, failures: ["config"], error: message }, null, 2));
  process.exit(1);
}

if (!TOKEN) failConfig("NEX_E2E_TOKEN is required (the browser login token).");
if (!process.env.NEX_E2E_MULTI_WORKSPACE?.trim()) {
  failConfig("NEX_E2E_MULTI_WORKSPACE is required (the path both runners serve).");
}
if (!ALPHA_DATA || !BETA_DATA) {
  failConfig("NEX_E2E_RUNNER_DATA_ALPHA and NEX_E2E_RUNNER_DATA_BETA are required.");
}

function isExecutable(file) {
  try {
    fs.accessSync(file, fs.constants.X_OK);
    return fs.statSync(file).isFile();
  } catch {
    return false;
  }
}

function findOnPath(names) {
  const dirs = (process.env.PATH ?? "").split(path.delimiter).filter(Boolean);
  for (const name of names) {
    for (const dir of dirs) {
      const candidate = path.join(dir, name);
      if (isExecutable(candidate)) return candidate;
    }
  }
  return null;
}

function resolveChrome() {
  const explicit = process.env.NEX_E2E_CHROME?.trim();
  if (explicit) {
    if (!isExecutable(explicit)) {
      failConfig(`NEX_E2E_CHROME does not point at an executable file: ${explicit}`);
    }
    return explicit;
  }
  const candidates = [
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/Applications/Chromium.app/Contents/MacOS/Chromium",
  ];
  for (const candidate of candidates) {
    if (isExecutable(candidate)) return candidate;
  }
  const fromPath = findOnPath([
    "google-chrome",
    "google-chrome-stable",
    "chromium",
    "chromium-browser",
  ]);
  if (fromPath) return fromPath;
  failConfig(
    "No Chrome/Chromium executable found. Install Google Chrome or Chromium, " +
      "or set NEX_E2E_CHROME to the executable path.",
  );
}

const chromePath = resolveChrome();
const playwrightEntry = path.join(REPO_ROOT, "node_modules", "playwright-core", "index.mjs");
if (!fs.existsSync(playwrightEntry)) {
  failConfig(
    `playwright-core is missing at ${playwrightEntry}; run pnpm install at the repo root.`,
  );
}
const { chromium } = await import(pathToFileURL(playwrightEntry).href);

fs.mkdirSync(OUT_DIR, { recursive: true });

const checks = {};
const failures = [];
const details = {};
const consoleErrors = [];

function check(name, ok, detail) {
  checks[name] = Boolean(ok);
  if (detail !== undefined) details[name] = detail;
  if (!ok) failures.push(name);
}

const browser = await chromium.launch({ executablePath: chromePath, headless: true });
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
// Same nuisance as the other E2Es: the notification-permission toast overlays the
// composer toolbar and intercepts clicks.
await page.addInitScript(() => {
  try {
    Object.defineProperty(window.Notification, "permission", {
      configurable: true,
      get: () => "granted",
    });
  } catch {
    // Non-configurable in older engines; the toast is only a nuisance.
  }
});
const logs = [];

page.on("pageerror", (error) => logs.push(`PAGEERROR: ${error.message}\n${error.stack ?? ""}`));
page.on("console", (message) => {
  if (message.type() === "debug" || message.type() === "verbose") return;
  const line = `CONSOLE[${message.type()}]: ${message.text()}`;
  logs.push(line);
  if (message.type() === "error" || message.type() === "warning")
    consoleErrors.push(line.slice(0, 300));
});

const shot = (name) => page.screenshot({ path: path.join(OUT_DIR, `${name}.png`) });
const bodyText = () => page.locator("body").innerText();

function waitForText(re, timeoutMs = REPLY_TIMEOUT_MS) {
  return (async () => {
    const deadline = Date.now() + timeoutMs;
    let body = "";
    while (Date.now() < deadline) {
      await page.waitForTimeout(1_500);
      body = await bodyText();
      if (re.test(body) && !/Stop|停止/.test(body)) return body;
    }
    return body;
  })();
}

/** Task rows expose `<workspaceKey>:<taskId>`; the key prefix identifies the machine. */
async function taskItemKeys() {
  return page.evaluate(() =>
    [...document.querySelectorAll("[data-testid^='task-item-']")].map(
      (element) => element.getAttribute("data-task-item-key") ?? "",
    ),
  );
}

async function countWorkspaceItems(workspacePath) {
  return page.locator(`[data-testid="workspace-item-${workspacePath}"]`).count();
}

/** Workspace rows carry their identity: the same path can be open on two machines. */
async function workspaceIdentities() {
  return page.evaluate(() =>
    [...document.querySelectorAll("[data-workspace-identity]")].map((element) =>
      element.getAttribute("data-workspace-identity"),
    ),
  );
}

/** Sidebar "Add project" → "Open folder" opens the runner workspace picker on web. */
async function openPicker() {
  const addProject = page.locator('[data-testid="project-add"]').first();
  await addProject.waitFor({ state: "visible", timeout: 30_000 });
  await addProject.click();
  const openFolder = page.getByText("Open folder", { exact: true }).first();
  await openFolder.waitFor({ state: "visible", timeout: 15_000 });
  await openFolder.click();
  await page
    .locator('[data-testid="runner-workspace-picker"]')
    .waitFor({ state: "visible", timeout: 20_000 });
  // The dialog refreshes the directory when it opens; give the list a moment to settle.
  await page.waitForTimeout(1_500);
}

/** Reads picker rows with their machine group so options can be matched by identity. */
async function pickerOptions() {
  return page.evaluate(() =>
    [...document.querySelectorAll('[data-testid="runner-workspace-option"]')].map(
      (element, index) => ({
        index,
        section: element.closest("section")?.innerText ?? "",
        path: element.querySelector("span[title]")?.getAttribute("title") ?? "",
        text: element.innerText,
      }),
    ),
  );
}

async function selectPickerOption(predicate, label) {
  const options = await pickerOptions();
  const match = options.find(predicate);
  if (!match) {
    throw new Error(`${label}: picker option not found among ${JSON.stringify(options)}`);
  }
  await page.locator('[data-testid="runner-workspace-option"]').nth(match.index).click();
  await page
    .locator('[data-testid="runner-workspace-picker"]')
    .waitFor({ state: "hidden", timeout: 15_000 });
  await page.waitForTimeout(2_500);
}

/** Header machine label only renders inside the workspace-context tooltip. */
async function workspaceMachineLabel() {
  const trigger = page.locator('[data-testid="workspace-path"]').first();
  await trigger.waitFor({ state: "visible", timeout: 30_000 });
  await trigger.click();
  const machine = page.locator('[data-testid="workspace-header-runner-machine"]').first();
  try {
    await machine.waitFor({ state: "visible", timeout: 10_000 });
  } catch {
    return "";
  }
  const text = ((await machine.textContent()) ?? "").trim();
  // Close the controlled tooltip so it cannot intercept later clicks.
  await trigger.click();
  await page.waitForTimeout(300);
  return text;
}

function workspaceSessionDir(dataDir) {
  const digest = createHash("sha1").update(WORKSPACE).digest("hex");
  return path.join(dataDir, "sessions", digest);
}

function listSessionFiles(dataDir) {
  const dir = workspaceSessionDir(dataDir);
  try {
    return fs.readdirSync(dir).filter((name) => name.endsWith(".jsonl"));
  } catch {
    return [];
  }
}

function hasSessionFile(dataDir, taskId) {
  return listSessionFiles(dataDir).some((name) => name.endsWith(`_${taskId}.jsonl`));
}

let summary;
try {
  await page.goto(`${BASE_URL}/`, { waitUntil: "load" });
  await page.waitForTimeout(2_000);

  const tokenInput = page.getByPlaceholder("Paste your access token");
  const signIn = page.getByRole("button", { name: "Sign in" });
  for (let attempt = 0; attempt < 2; attempt += 1) {
    if ((await tokenInput.count()) === 0) break;
    await tokenInput.waitFor({ state: "visible", timeout: 20_000 });
    await tokenInput.fill(TOKEN);
    await signIn.click();
    await page.waitForTimeout(3_000);
  }

  const composer = page
    .locator(
      '[data-testid="v4-composer-input"], [data-testid="chat-input"], [contenteditable="true"]',
    )
    .first();
  await composer.waitFor({ state: "visible", timeout: 90_000 });
  await page.waitForTimeout(8_000);
  await shot("multi-00-initial");

  // Authoritative machine/identity mapping from the same endpoint the picker uses.
  const serverInfo = await page.evaluate(async () => {
    const response = await fetch("/api/server-info", { cache: "no-store" });
    return response.ok ? await response.json() : null;
  });
  const workspaces = Array.isArray(serverInfo?.workspaces) ? serverInfo.workspaces : [];
  const alphaMulti = workspaces.find(
    (entry) => entry.runnerName === ALPHA_NAME && entry.path === WORKSPACE,
  );
  const betaMulti = workspaces.find(
    (entry) => entry.runnerName === BETA_NAME && entry.path === WORKSPACE,
  );
  details.serverWorkspaces = workspaces.map((entry) => ({
    path: entry.path,
    label: entry.label,
    runnerId: entry.runnerId,
    runnerName: entry.runnerName,
    workspaceIdentity: entry.workspaceIdentity,
  }));
  check("serverInfoAlphaServesWorkspace", Boolean(alphaMulti), { ALPHA_NAME, WORKSPACE });
  check("serverInfoBetaServesWorkspace", Boolean(betaMulti), { BETA_NAME, WORKSPACE });
  const alphaIdentity = alphaMulti?.workspaceIdentity ?? "";
  const betaIdentity = betaMulti?.workspaceIdentity ?? "";
  details.alphaIdentity = alphaIdentity;
  details.betaIdentity = betaIdentity;

  // ── 1. picker lists both machines for the same path ──
  await openPicker();
  const options = await pickerOptions();
  details.pickerOptions = options;
  const alphaOption = options.find(
    (option) => option.path === WORKSPACE && option.section.includes(ALPHA_NAME),
  );
  const betaOption = options.find(
    (option) => option.path === WORKSPACE && option.section.includes(BETA_NAME),
  );
  check("pickerListsAlphaWorkspace", Boolean(alphaOption));
  check("pickerListsBetaWorkspace", Boolean(betaOption));
  check(
    "pickerShowsSamePathOnTwoMachines",
    Boolean(alphaOption && betaOption && alphaOption.path === betaOption.path),
  );
  await shot("multi-01-picker");

  // ── 2. open beta's workspace ──
  await selectPickerOption(
    (option) => option.path === WORKSPACE && option.section.includes(BETA_NAME),
    "beta workspace",
  );
  const editor = page
    .locator(
      '[data-testid="v4-composer-input"], [data-testid="chat-input"], [contenteditable="true"]',
    )
    .first();
  await editor.waitFor({ state: "visible", timeout: 30_000 });
  await page.waitForTimeout(6_000);
  const betaModelTrigger = page.locator('[data-testid="chat-model-select-trigger"]').first();
  const betaModelValue =
    (await betaModelTrigger.count()) > 0
      ? ((await betaModelTrigger.getAttribute("data-model-current-value")) ?? "").trim()
      : "";
  check("modelSelectedOnBeta", betaModelValue.length > 0, { value: betaModelValue });
  check("betaWorkspaceTabOpened", (await workspaceIdentities()).includes(betaIdentity), {
    identities: await workspaceIdentities(),
  });
  await shot("multi-02-beta-workspace");

  // ── 3. send a short prompt and wait for the reply ──
  await editor.click();
  await page.keyboard.press("ControlOrMeta+a");
  await page.keyboard.press("Backspace");
  await page.keyboard.type(PROMPT);
  await page.keyboard.press("Enter");
  const replyBody = await waitForText(/beta-ok/i);
  await page.waitForTimeout(3_000);
  await shot("multi-03-beta-reply");
  check("replyRenderedOnBeta", /beta-ok/i.test(replyBody), { tail: replyBody.slice(-600) });

  // ── 4. the task row belongs to beta only ──
  const keysAfterReply = await taskItemKeys();
  details.taskKeysAfterReply = keysAfterReply;
  const betaTaskKeys = keysAfterReply.filter((key) => key.startsWith(`${betaIdentity}:`));
  const alphaTaskKeys = keysAfterReply.filter((key) => key.startsWith(`${alphaIdentity}:`));
  check("taskRowUnderBetaWorkspace", betaTaskKeys.length === 1, {
    betaTaskKeys,
    count: betaTaskKeys.length,
  });
  check("noTaskRowUnderAlphaWorkspace", alphaTaskKeys.length === 0, { alphaTaskKeys });
  check("oneBetaWorkspaceTabOpen", (await countWorkspaceItems(WORKSPACE)) === 1, {
    count: await countWorkspaceItems(WORKSPACE),
  });
  check("openTabCarriesBetaIdentity", (await workspaceIdentities()).includes(betaIdentity), {
    identities: await workspaceIdentities(),
  });

  // ── 5. header shows the machine name (only a task-bearing workspace renders it) ──
  const betaMachineHeader = await workspaceMachineLabel();
  check("betaHeaderShowsMachineName", betaMachineHeader.includes(BETA_NAME), {
    header: betaMachineHeader,
  });
  await shot("multi-04-beta-header");

  // Runner-owned data dirs: the new session file exists under beta, never under alpha.
  const taskId = betaTaskKeys[0]?.slice(betaIdentity.length + 1) ?? "";
  const betaSessionFiles = listSessionFiles(BETA_DATA);
  const alphaSessionFiles = listSessionFiles(ALPHA_DATA);
  details.sessionFiles = {
    taskId,
    beta: { dataDir: BETA_DATA, files: betaSessionFiles },
    alpha: { dataDir: ALPHA_DATA, files: alphaSessionFiles },
  };
  check(
    "betaDataDirHasSession",
    Boolean(taskId) && hasSessionFile(BETA_DATA, taskId),
    details.sessionFiles.beta,
  );
  check(
    "alphaDataDirHasNoSession",
    Boolean(taskId) && !hasSessionFile(ALPHA_DATA, taskId),
    details.sessionFiles.alpha,
  );

  // ── 6. reload restores the tab on beta with the session still listed ──
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.waitForTimeout(10_000);
  await shot("multi-05-after-reload");
  const reloadedBody = await bodyText();
  const reloadedKeys = await taskItemKeys();
  details.taskKeysAfterReload = reloadedKeys;
  details.workspaceIdentitiesAfterReload = await workspaceIdentities();
  check(
    "sessionListedAfterReload",
    reloadedKeys.some((key) => key.startsWith(`${betaIdentity}:`)),
    { reloadedKeys },
  );
  // Titles are model-generated (e.g. "beta-4ok"), so assert that the same session row is
  // still listed under beta rather than matching title text.
  check(
    "taskTitleVisibleAfterReload",
    Boolean(taskId) && reloadedKeys.includes(`${betaIdentity}:${taskId}`),
    {
      tail: reloadedBody.slice(-600),
    },
  );
  check("reloadKeepsBetaMachine", (await workspaceIdentities()).includes(betaIdentity), {
    identities: await workspaceIdentities(),
  });

  // Reopen the session so the task-bearing header (and its machine label) renders.
  const reloadedRow = page.locator(`[data-task-item-key^="${betaIdentity}:"]`).first();
  if ((await reloadedRow.count()) > 0) {
    await reloadedRow.click();
    await page.waitForTimeout(6_000);
  }
  const reloadedHeader = await workspaceMachineLabel();
  check("reloadHeaderShowsBetaMachine", reloadedHeader.includes(BETA_NAME), {
    header: reloadedHeader,
  });
  await shot("multi-06-reload-header");

  // ── 7. open alpha's workspace for the same path: its task list stays empty ──
  await openPicker();
  await selectPickerOption(
    (option) => option.path === WORKSPACE && option.section.includes(ALPHA_NAME),
    "alpha workspace",
  );
  await page.waitForTimeout(7_000);
  await shot("multi-07-alpha-workspace");
  const alphaWorkspaceBody = await bodyText();
  const keysOnAlpha = await taskItemKeys();
  details.taskKeysOnAlpha = keysOnAlpha;
  const alphaKeysForPath = keysOnAlpha.filter((key) => key.startsWith(`${alphaIdentity}:`));
  const betaKeysStillThere = keysOnAlpha.filter((key) => key.startsWith(`${betaIdentity}:`));
  check("alphaTaskListIsEmpty", alphaKeysForPath.length === 0, {
    alphaKeysForPath,
    tail: alphaWorkspaceBody.slice(-400),
  });
  check("betaTaskStillListedUnderBeta", betaKeysStillThere.length === 1, {
    betaKeysStillThere,
  });
  check("twoMachineTabsForSamePath", (await countWorkspaceItems(WORKSPACE)) === 2, {
    count: await countWorkspaceItems(WORKSPACE),
  });
  check(
    "bothMachineIdentitiesOpen",
    (await workspaceIdentities()).includes(alphaIdentity) &&
      (await workspaceIdentities()).includes(betaIdentity),
    { identities: await workspaceIdentities() },
  );
} catch (error) {
  failures.push("exception");
  details.exception = error instanceof Error ? (error.stack ?? error.message) : String(error);
} finally {
  try {
    await browser.close();
  } catch {
    // Browser teardown failure must not hide the check result.
  }
}

const significantErrors = logs.filter((line) => {
  if (line.startsWith("PAGEERROR")) return true;
  if (!/CONSOLE\[(error|warning)\]/.test(line)) return false;
  return !/zcode\.z\.ai|ERR_FAILED|Tooltip is changing|accepted input projection silent/u.test(
    line,
  );
});
const crashes = significantErrors.filter((line) =>
  /PAGEERROR|React subtree crashed|change in the order of Hooks/u.test(line),
);
checks.noReactCrashes = crashes.length === 0;
if (!checks.noReactCrashes) failures.push("noReactCrashes");

summary = {
  ok: failures.length === 0,
  baseUrl: BASE_URL,
  workspace: WORKSPACE,
  alpha: { name: ALPHA_NAME, dataDir: ALPHA_DATA },
  beta: { name: BETA_NAME, dataDir: BETA_DATA },
  outDir: OUT_DIR,
  chrome: chromePath,
  checks,
  failures,
  details,
  significantErrors,
  consoleErrors,
};

fs.writeFileSync(
  path.join(OUT_DIR, "multi-runner-summary.json"),
  `${JSON.stringify(summary, null, 2)}\n`,
);
console.log(JSON.stringify(summary, null, 2));
process.exit(summary.ok ? 0 : 1);
