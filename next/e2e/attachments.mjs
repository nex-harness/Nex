#!/usr/bin/env node
// Attachments E2E for the next/ MVP (docs/next/attachments-rewind.md section A).
//
// A small PNG is attached through the composer's hidden file input; the row must render the
// image preview (chunked read from the runner) and the vision-capable model receives it as a
// prompt image. A .txt file attached the same way must reach the model as a path reference
// that it reads back. Both rows carry the refs the runner admitted.
//
// Environment:
//   NEX_E2E_BASE_URL  Web origin to drive        (default http://localhost:5174)
//   NEX_E2E_TOKEN     Browser login token        (required)
//   NEX_E2E_CHROME    Chrome/Chromium executable (default: auto-detect)
//   NEX_E2E_OUT       Screenshot + summary dir   (default <repo>/next/e2e/out)
//   NEX_E2E_WORKSPACE absolute runner workspace to open via the machine picker (optional)
//   NEX_E2E_REPLY_TIMEOUT_MS     per-reply budget (default 180000)
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
const WORKSPACE = process.env.NEX_E2E_WORKSPACE?.trim() ?? "";
const REPLY_TIMEOUT_MS = Number(process.env.NEX_E2E_REPLY_TIMEOUT_MS) || 180_000;

function failConfig(message) {
  console.error(`[attachments] ${message}`);
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
    if (!isExecutable(explicit)) failConfig(`NEX_E2E_CHROME is not executable: ${explicit}`);
    return explicit;
  }
  for (const candidate of [
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/Applications/Chromium.app/Contents/MacOS/Chromium",
  ]) {
    if (isExecutable(candidate)) return candidate;
  }
  const fromPath = findOnPath([
    "google-chrome",
    "google-chrome-stable",
    "chromium",
    "chromium-browser",
  ]);
  if (fromPath) return fromPath;
  failConfig("No Chrome/Chromium found; set NEX_E2E_CHROME.");
}

const chromePath = resolveChrome();
const playwrightEntry = path.join(REPO_ROOT, "node_modules", "playwright-core", "index.mjs");
if (!fs.existsSync(playwrightEntry)) {
  failConfig(`playwright-core is missing at ${playwrightEntry}; run pnpm install.`);
}
const { chromium } = await import(pathToFileURL(playwrightEntry).href);

const checks = {};
const failures = [];
const details = {};
const logs = [];

function check(name, ok, detail) {
  checks[name] = Boolean(ok);
  if (detail !== undefined) details[name] = detail;
  if (!ok) failures.push(name);
}

const browser = await chromium.launch({ executablePath: chromePath, headless: true });
const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
page.on("pageerror", (error) => logs.push(`PAGEERROR: ${error.message}`));
page.on("console", (message) => {
  if (message.type() === "error" || message.type() === "warning") {
    logs.push(`CONSOLE[${message.type()}]: ${message.text()}`);
  }
});

const shot = (name) => page.screenshot({ path: path.join(OUT_DIR, `${name}.png`) });

async function waitUntil(label, predicate, timeoutMs = 30_000) {
  const deadline = Date.now() + timeoutMs;
  let last;
  while (Date.now() < deadline) {
    last = await predicate();
    if (last) return last;
    await page.waitForTimeout(400);
  }
  details[`last_${label}`] = typeof last === "object" ? JSON.stringify(last) : last;
  return null;
}

const composer = () =>
  page.locator('[data-testid="v4-composer-input"], [contenteditable="true"]').first();

async function dismissToasts() {
  await page.evaluate(() => {
    for (const host of document.querySelectorAll("#nex-toast-host")) {
      host.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
      const dismiss = host.querySelector("button[aria-label]");
      if (dismiss) dismiss.click();
    }
  });
  await page.waitForTimeout(300);
}

/**
 * Types through the real editor. `send` = press Enter; attachments are added first via the
 * hidden file input so the send carries them.
 */
async function typeMessage(text, { send = true } = {}) {
  await dismissToasts();
  const editor = composer();
  await editor.waitFor({ state: "visible", timeout: 30_000 });
  await editor.evaluate((element) => {
    element.focus();
    const selection = window.getSelection();
    const range = document.createRange();
    range.selectNodeContents(element);
    selection?.removeAllRanges();
    selection?.addRange(range);
  });
  await page.keyboard.press("Backspace");
  await page.keyboard.type(text);
  if (send) await page.keyboard.press("Enter");
  await page.waitForTimeout(500);
}

/** The composer's hidden `<input type=file>` (web attachment picker). */
async function attachFile(filePath) {
  const input = page.locator('[data-testid="v4-composer"] input[type="file"]').first();
  await input.waitFor({ state: "attached", timeout: 30_000 });
  await input.setInputFiles(filePath);
  // Uploads run with concurrency 2; wait for the ready chip before sending.
  await waitUntil(
    "attachmentReady",
    () =>
      page
        .evaluate(() => {
          const scope = document.querySelector('[data-testid="v4-composer"]');
          return (scope?.textContent ?? "").length > 0;
        })
        .then((value) => (value ? true : null)),
    30_000,
  );
  await page.waitForTimeout(2_500);
}

/**
 * Attachment evidence of the newest user row: media tiles and file chips. Both live in
 * the row's own summary row (`data-v4-user-input-attachments`), keyed by `v4-row-<id>`;
 * the bubble itself only holds the prompt text.
 */
async function lastRowAttachments() {
  return page.evaluate(() => {
    const summaries = [...document.querySelectorAll("[data-v4-user-input-attachments='true']")];
    const summary = summaries[summaries.length - 1];
    if (!summary) return null;
    return {
      media: summary.querySelectorAll("[data-v4-user-input-media-attachments='true'] > *").length,
      images: summary.querySelectorAll("img").length,
      chips: [...summary.querySelectorAll("[data-v4-user-input-attachment-pill='true']")].map(
        (element) => (element.textContent ?? "").trim(),
      ),
      text: (summary.textContent ?? "").trim().slice(0, 200),
    };
  });
}

let workspaceEnsured = false;
async function ensureRunnerWorkspace() {
  if (workspaceEnsured || !WORKSPACE) return;
  const options = page.locator('[data-testid="runner-workspace-option"]');
  for (let attempt = 0; attempt < 3 && (await options.count()) === 0; attempt += 1) {
    const addProject = page.locator('[data-testid="project-add"]').first();
    if ((await addProject.count()) === 0) break;
    try {
      await addProject.click({ timeout: 5_000 });
      await page.getByText("Open folder", { exact: true }).first().click({ timeout: 5_000 });
      await page.waitForTimeout(1_500);
    } catch {
      // Transient overlay; retry.
    }
  }
  const exact = options.filter({ has: page.locator(`span[title="${WORKSPACE}"]`) }).first();
  if ((await exact.count()) > 0) {
    await exact.click({ timeout: 60_000 }).catch(() => {});
    await page
      .locator('[data-testid="runner-workspace-picker"]')
      .waitFor({ state: "hidden", timeout: 15_000 })
      .catch(() => {});
    await page.waitForTimeout(5_000);
  }
  workspaceEnsured = true;
}

async function startNewChat() {
  const newTask = page.locator('[data-testid="task-new-button"]').first();
  if ((await newTask.count()) > 0) await newTask.click();
  await page.waitForTimeout(2_500);
}

const PNG_BASE64 =
  "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAMElEQVR42mNkYPhfz0DFgK5gYGBgYGBgYGBgYGBgYGBgYGBgYGBgYGBgYGBgYAAAW0sL1m+Sz7wAAAAASUVORK5CYII=";
const TEXT_CONTENT = "attached-secret-2048\nsecond line\n";

let summary;
try {
  fs.mkdirSync(OUT_DIR, { recursive: true });
  const pngPath = path.join(OUT_DIR, "attachment-pixel.png");
  const txtPath = path.join(OUT_DIR, "attachment-notes.txt");
  fs.writeFileSync(pngPath, Buffer.from(PNG_BASE64, "base64"));
  fs.writeFileSync(txtPath, TEXT_CONTENT);

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
    if ((await tokenInput.count()) > 0 && (await tokenInput.isVisible())) continue;
    break;
  }
  await composer().waitFor({ state: "visible", timeout: 90_000 });
  await page.waitForTimeout(8_000);
  await ensureRunnerWorkspace();
  await startNewChat();
  await shot("attach-00-loaded");

  // ── 1. image attachment ────────────────────────────────────────────────
  // Uploads are session-scoped: a draft has no session id yet, so the file is attached to
  // the draft and sent with the first message (the composer uploads it at submit).
  await typeMessage("I attached a small PNG. Describe its colors in one short sentence.", {
    send: false,
  });
  await attachFile(pngPath);
  await page
    .getByRole("button", { name: /^(Send|发送)$/u })
    .first()
    .click();
  await page.waitForTimeout(3_000);
  await page.waitForTimeout(1_500);
  const imageRow = await waitUntil(
    "imageRow",
    async () => {
      const row = await lastRowAttachments();
      return row && row.media > 0 ? row : null;
    },
    REPLY_TIMEOUT_MS,
  );
  check("imageRowHasMedia", Boolean(imageRow), imageRow);
  await shot("attach-01-image-row");
  // The row preview needs the runner's authorized chunked read of the uploaded blob.
  const previewLoaded = await waitUntil(
    "imagePreview",
    async () => {
      const state = await lastRowAttachments();
      return state && state.images > 0 ? state : null;
    },
    60_000,
  );
  check("imagePreviewRendered", Boolean(previewLoaded), previewLoaded);
  await shot("attach-02-image-preview");

  // ── 2. text attachment ─────────────────────────────────────────────────
  await startNewChat();
  await page.waitForTimeout(2_000);
  await typeMessage(
    "Read the attached text file with the read tool and reply with its first line exactly.",
    { send: false },
  );
  await attachFile(txtPath);
  await composer().press("Enter");
  const fileRow = await waitUntil(
    "fileRow",
    async () => {
      const row = await lastRowAttachments();
      return row && row.chips.some((name) => name.includes("attachment-notes")) ? row : null;
    },
    REPLY_TIMEOUT_MS,
  );
  check("textRowHasChip", Boolean(fileRow), fileRow);
  await shot("attach-03-text-row");
  const replyRead = await waitUntil(
    "textReply",
    async () => {
      const text = await page.evaluate(() => document.body.innerText);
      return text.includes("attached-secret-2048") ? text : null;
    },
    REPLY_TIMEOUT_MS,
  );
  check("modelReadTextFile", Boolean(replyRead));
  await shot("attach-04-text-reply");

  const significantErrors = logs.filter(
    (line) =>
      line.startsWith("PAGEERROR") ||
      (/CONSOLE\[(error|warning)\]/.test(line) &&
        !/zcode\.z\.ai|ERR_FAILED|Tooltip is changing|accepted input projection silent|Missing `Description`/u.test(
          line,
        )),
  );
  check("noAppErrors", significantErrors.length === 0, significantErrors.slice(0, 8));
} catch (error) {
  failures.push("exception");
  details.exception = error instanceof Error ? (error.stack ?? error.message) : String(error);
  try {
    await shot("attach-99-exception");
  } catch {
    // Screenshot failure must not hide the exception detail.
  }
} finally {
  try {
    await browser.close();
  } catch {
    // Teardown failure must not hide the check result.
  }
}

summary = {
  ok: failures.length === 0,
  baseUrl: BASE_URL,
  outDir: OUT_DIR,
  chrome: chromePath,
  checks,
  failures,
  details,
};
fs.writeFileSync(
  path.join(OUT_DIR, "attachments-summary.json"),
  `${JSON.stringify(summary, null, 2)}\n`,
);
console.log(JSON.stringify(summary, null, 2));
process.exit(summary.ok ? 0 : 1);
