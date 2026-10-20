#!/usr/bin/env node
// Permissions E2E for the next/ runner: build-mode approval card, allow/deny, "edit"
// mode auto-approval, and a read-only `ls` that never asks.
//
// The runner workspace MUST be a fresh demo directory (next/dev.sh creates it). The
// model is asked to use a specific tool so the card appears deterministically.
//
// Environment:
//   NEX_E2E_BASE_URL  Web origin to drive        (default http://localhost:5174)
//   NEX_E2E_TOKEN     Browser login token        (required)
//   NEX_E2E_CHROME    Chrome/Chromium executable (default: auto-detect)
//   NEX_E2E_OUT       Screenshot + summary dir   (default <repo>/next/e2e/out)
//   NEX_E2E_WORKSPACE Runner workspace path      (default $NEX_DEMO_WORKSPACE or
//                                                  $TMPDIR/nex-next-demo)
//
// Exit code 0 only when every check passes. A JSON summary is always printed.

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
const WORKSPACE = path.resolve(
  process.env.NEX_E2E_WORKSPACE?.trim() ||
    path.join(process.env.TMPDIR?.trim() || "/tmp", "nex-next-demo"),
);
const REPLY_TIMEOUT_MS = Number(process.env.NEX_E2E_REPLY_TIMEOUT_MS) || 180_000;
const CARD_TIMEOUT_MS = Number(process.env.NEX_E2E_CARD_TIMEOUT_MS) || 180_000;

const ALLOW_FILE = path.join(WORKSPACE, "perm-ok.txt");
const DENY_FILE = path.join(WORKSPACE, "perm-denied.txt");
const EDIT_FILE = path.join(WORKSPACE, "perm-edit.txt");

function failConfig(message) {
  console.error(`[permissions] ${message}`);
  console.log(JSON.stringify({ ok: false, failures: ["config"], error: message }, null, 2));
  process.exit(1);
}

if (!TOKEN) failConfig("NEX_E2E_TOKEN is required (the browser login token).");

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

function removeIfExists(file) {
  try {
    fs.rmSync(file, { force: true });
  } catch {
    // Best effort: the assertion below reports the real state.
  }
}

// The demo workspace is shared with acceptance.mjs; reset only the files this run owns.
for (const file of [ALLOW_FILE, DENY_FILE, EDIT_FILE]) removeIfExists(file);

const browser = await chromium.launch({ executablePath: chromePath, headless: true });
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
// The app shows an "Allow browser notifications" toast while permission is "default";
// it overlays the composer toolbar and intercepts clicks. Report "granted" so the
// prompt never renders (no OS-level permission is involved).
await page.addInitScript(() => {
  try {
    Object.defineProperty(window.Notification, "permission", {
      configurable: true,
      get: () => "granted",
    });
  } catch {
    // Older engines may expose a non-configurable property; the toast is only a nuisance.
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

/** The permission dialog is a listbox of role=option buttons; select by normalized kind. */
function optionButton(kind) {
  return page.locator(`[data-permission-option-kind="${kind}"]`).first();
}

async function waitForCard(timeoutMs = CARD_TIMEOUT_MS) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if ((await optionButton("allowOnce").count()) > 0) return true;
    await page.waitForTimeout(1_000);
  }
  return false;
}

async function waitForNoCard(timeoutMs = 20_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if ((await optionButton("allowOnce").count()) === 0) return true;
    await page.waitForTimeout(500);
  }
  return false;
}

async function waitForText(re, timeoutMs = REPLY_TIMEOUT_MS) {
  const deadline = Date.now() + timeoutMs;
  let body = "";
  while (Date.now() < deadline) {
    await page.waitForTimeout(1_500);
    body = await bodyText();
    if (re.test(body) && !/Stop|停止/.test(body)) return body;
  }
  return body;
}

async function setMode(mode) {
  const trigger = page.locator('[data-testid="chat-mode-select-trigger"]').first();
  await trigger.waitFor({ state: "visible", timeout: 30_000 });
  // Toasts (notifications/onboarding) can overlay the toolbar; retry a few times rather
  // than failing the whole run on a transient overlay.
  for (let attempt = 0; attempt < 6; attempt += 1) {
    try {
      await trigger.click({ timeout: 5_000 });
      break;
    } catch (error) {
      if (attempt === 5) throw error;
      await page.waitForTimeout(1_500);
    }
  }
  const item = page.locator(`[data-testid="chat-mode-select-item-${mode}"]`).first();
  await item.waitFor({ state: "visible", timeout: 15_000 });
  await item.click();
  await page.waitForTimeout(800);
  const actual = await page
    .locator('[data-testid="v4-model-config"]')
    .first()
    .getAttribute("data-mode");
  check(`modeIs_${mode}`, actual === mode, { actual });
}

/**
 * The composer must be bound to the runner workspace; sending from the default
 * `/workspace/conversation` scope fails with "no runner connected for workspace".
 * The workspace picker is optional UI, so only interact when it is rendered.
 *
 * Never click `conversation-new-task`: it switches the composer to the
 * conversation scope, which no runner serves.
 */
async function ensureRunnerWorkspace() {
  const expectedName = path.basename(WORKSPACE);
  const deadline = Date.now() + 90_000;
  while (Date.now() < deadline) {
    const options = page.locator('[data-testid="runner-workspace-option"]');
    if ((await options.count()) > 0) {
      const matching = options.filter({ hasText: expectedName }).first();
      if ((await matching.count()) > 0) {
        await matching.click();
        await page.waitForTimeout(4_000);
      }
      break;
    }
    const body = await bodyText();
    if (body.includes(expectedName)) break;
    await page.waitForTimeout(1_000);
  }
  const body = await bodyText();
  check("runnerWorkspaceSelected", body.includes(expectedName), {
    expectedName,
    tail: body.slice(-500),
  });
}

/** Waits until the previous turn settles so the next send starts a clean turn. */
async function waitForIdle(timeoutMs = REPLY_TIMEOUT_MS) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const body = await bodyText();
    if (!/Stop|停止/.test(body)) return true;
    await page.waitForTimeout(1_000);
  }
  return false;
}

async function send(text) {
  await waitForIdle();
  await waitForNoCard();
  await ensureRunnerWorkspace();
  const editor = page
    .locator('[data-testid="v4-composer-input"], [contenteditable="true"]')
    .first();
  await editor.waitFor({ state: "visible", timeout: 30_000 });
  await editor.click();
  // A previous turn may leave a draft behind; clear it so prompts never concatenate.
  await page.keyboard.press("ControlOrMeta+a");
  await page.keyboard.press("Backspace");
  await page.keyboard.type(text);
  await page.keyboard.press("Enter");
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

  // ── 1. build mode: write asks, Allow once writes the file ──
  const composer = page
    .locator('[data-testid="v4-composer-input"], [contenteditable="true"]')
    .first();
  await composer.waitFor({ state: "visible", timeout: 90_000 });
  await page.waitForTimeout(8_000);
  await setMode("build");

  await send(
    "Use the write tool to create perm-ok.txt containing exactly OK. Do not use bash, do not use any other tool.",
  );
  const cardShown = await waitForCard();
  await shot("perm-01-card-allow");
  check("approvalCardAppearsInBuildMode", cardShown, { body: (await bodyText()).slice(-600) });
  check("cardMentionsWrite", /Write|perm-ok\.txt/i.test(await bodyText()));
  check("fileNotCreatedWhilePending", !fs.existsSync(ALLOW_FILE));

  if (cardShown) {
    await optionButton("allowOnce").click();
  }
  const allowed = await waitForText(/OK|created|done/i);
  await shot("perm-02-allowed");
  // The write happens after the allow; wait for the file instead of the reply text.
  const allowDeadline = Date.now() + 60_000;
  while (Date.now() < allowDeadline && !fs.existsSync(ALLOW_FILE)) {
    await page.waitForTimeout(500);
  }
  check("fileCreatedAfterAllow", fs.existsSync(ALLOW_FILE), { path: ALLOW_FILE });
  check(
    "fileContentIsOK",
    fs.existsSync(ALLOW_FILE) && fs.readFileSync(ALLOW_FILE, "utf8").trim() === "OK",
  );
  check("cardDismissedAfterAllow", await waitForNoCard(), { tail: allowed.slice(-400) });

  // ── 2. build mode: write asks, Deny leaves the file absent ──
  await setMode("build");
  await send(
    "Use the write tool to create perm-denied.txt containing exactly NOPE. Do not use bash, do not use any other tool.",
  );
  const denyCard = await waitForCard();
  await shot("perm-03-card-deny");
  check("approvalCardAppearsForSecondWrite", denyCard);
  if (denyCard) {
    await optionButton("rejectOnce").click();
  }
  await waitForNoCard();
  await page.waitForTimeout(6_000);
  await shot("perm-04-denied");
  check("fileNotCreatedAfterDeny", !fs.existsSync(DENY_FILE), { path: DENY_FILE });

  // ── 3. edit mode: write runs without a card ──
  await setMode("edit");
  await send(
    "Use the write tool to create perm-edit.txt containing exactly AUTO. Do not use bash, do not use any other tool.",
  );
  const editDeadline = Date.now() + REPLY_TIMEOUT_MS;
  while (Date.now() < editDeadline && !fs.existsSync(EDIT_FILE)) {
    await page.waitForTimeout(1_000);
  }
  await shot("perm-05-edit-mode");
  check("fileCreatedInEditModeWithoutCard", fs.existsSync(EDIT_FILE), { path: EDIT_FILE });
  check("noCardInEditMode", (await optionButton("allowOnce").count()) === 0);

  // ── 4. read-only ls runs without a card in build mode ──
  await setMode("build");
  await send("Use the bash tool to run exactly `ls -la`. Then reply with one short sentence.");
  const lsDeadline = Date.now() + REPLY_TIMEOUT_MS;
  let sawBash = false;
  while (Date.now() < lsDeadline) {
    await page.waitForTimeout(2_000);
    const body = await bodyText();
    if (/(Terminal|Bash|ls -la)/.test(body) && !/Stop|停止/.test(body)) {
      sawBash = true;
      break;
    }
    if ((await optionButton("allowOnce").count()) > 0) break;
  }
  await shot("perm-06-readonly-bash");
  check("readOnlyBashRan", sawBash, { tail: (await bodyText()).slice(-600) });
  check("readOnlyBashHadNoCard", (await optionButton("allowOnce").count()) === 0);
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
  outDir: OUT_DIR,
  chrome: chromePath,
  checks,
  failures,
  details,
  significantErrors,
  consoleErrors,
};

fs.writeFileSync(
  path.join(OUT_DIR, "permissions-summary.json"),
  `${JSON.stringify(summary, null, 2)}\n`,
);
console.log(JSON.stringify(summary, null, 2));
process.exit(summary.ok ? 0 : 1);
