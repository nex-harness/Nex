#!/usr/bin/env node
// File changes and rewind E2E for the next/ runner (docs/next/attachments-rewind.md B).
//
//   1. "Edit automatically" mode: the model writes a file and then edits it.
//   2. The edit/write tool cards show a diff with +/- counts.
//   3. The turn's changed-files panel lists the file.
//   4. Undo ("Undo files" in the dialog) restores the file on disk; the panel says "Undone".
//   5. Edit-with-rewind: edit the user message with "Reset chat + files" -> the files are
//      restored and the turn re-runs with the new text.
//
// The runner workspace MUST be a writable demo directory (next/dev.sh creates it). The
// model is asked to use specific tools so the cards appear deterministically.
//
// Environment:
//   NEX_E2E_BASE_URL   Web origin to drive        (default http://localhost:5174)
//   NEX_E2E_TOKEN      Browser login token        (required)
//   NEX_E2E_CHROME     Chrome/Chromium executable (default: auto-detect)
//   NEX_E2E_OUT        Screenshot + summary dir   (default <repo>/next/e2e/out)
//   NEX_E2E_WORKSPACE  Runner workspace path      (required; files are asserted on disk)
//   NEX_E2E_REPLY_TIMEOUT_MS      per-reply budget (default 180000)
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
  console.error(`[files] ${message}`);
  console.log(JSON.stringify({ ok: false, failures: ["config"], error: message }, null, 2));
  process.exit(1);
}

if (!TOKEN) failConfig("NEX_E2E_TOKEN is required (the browser login token).");
if (!WORKSPACE) failConfig("NEX_E2E_WORKSPACE is required (the runner workspace on disk).");

const STAMP = Date.now();
const FILE = `e2e-file-${STAMP}.txt`;
const FILE_PATH = path.join(WORKSPACE, FILE);
const FIRST_LINE = `first line ${STAMP}`;
const SECOND_LINE = `second line ${STAMP}`;
const RERUN_LINE = `rerun line ${STAMP}`;

function isExecutable(file) {
  try {
    fs.accessSync(file, fs.constants.X_OK);
    return fs.statSync(file).isFile();
  } catch {
    return false;
  }
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
  for (const name of ["google-chrome", "google-chrome-stable", "chromium", "chromium-browser"]) {
    for (const dir of (process.env.PATH ?? "").split(path.delimiter).filter(Boolean)) {
      const candidate = path.join(dir, name);
      if (isExecutable(candidate)) return candidate;
    }
  }
  failConfig("No Chrome/Chromium found; set NEX_E2E_CHROME.");
}

const chromePath = resolveChrome();
const playwrightEntry = path.join(REPO_ROOT, "node_modules", "playwright-core", "index.mjs");
if (!fs.existsSync(playwrightEntry)) {
  failConfig(`playwright-core is missing at ${playwrightEntry}; run pnpm install.`);
}
const { chromium } = await import(pathToFileURL(playwrightEntry).href);

fs.mkdirSync(OUT_DIR, { recursive: true });
fs.rmSync(FILE_PATH, { force: true });

const checks = {};
const failures = [];
const details = {};
const logs = [];

function check(name, ok, detail) {
  checks[name] = Boolean(ok);
  if (detail !== undefined) details[name] = detail;
  if (!ok) failures.push(name);
}

function fileContent() {
  try {
    return fs.readFileSync(FILE_PATH, "utf8");
  } catch {
    return null;
  }
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

const bodyText = () =>
  page.evaluate(() => {
    let text = document.body.innerText;
    const visit = (root) => {
      for (const element of root.querySelectorAll("*")) {
        if (!element.shadowRoot) continue;
        text += `\n${element.shadowRoot.textContent ?? ""}`;
        visit(element.shadowRoot);
      }
    };
    visit(document);
    return text;
  });

async function waitUntil(label, predicate, timeoutMs = 30_000) {
  const deadline = Date.now() + timeoutMs;
  let last;
  while (Date.now() < deadline) {
    last = await predicate();
    if (last) return last;
    await page.waitForTimeout(400);
  }
  details[`last_${label}`] = last;
  return null;
}

const stopButton = () => page.getByRole("button", { name: /^(Stop|停止)$/u }).first();

async function waitForIdle(timeoutMs = REPLY_TIMEOUT_MS) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if ((await stopButton().count()) === 0) return true;
    await page.waitForTimeout(700);
  }
  return false;
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

async function typeAndSend(text) {
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
  await page.keyboard.press("Enter");
  await page.waitForTimeout(500);
}

/** Sets the composer permission mode (build | edit | plan | yolo). */
async function setMode(mode) {
  const trigger = page.locator('[data-testid="chat-mode-select-trigger"]').first();
  await trigger.waitFor({ state: "visible", timeout: 30_000 });
  for (let attempt = 0; attempt < 6; attempt += 1) {
    try {
      await trigger.click({ timeout: 5_000 });
      break;
    } catch (error) {
      if (attempt === 5) throw error;
      await page.waitForTimeout(1_200);
    }
  }
  const item = page.locator(`[data-testid="chat-mode-select-item-${mode}"]`).first();
  await item.waitFor({ state: "visible", timeout: 15_000 });
  await item.click();
  await page.waitForTimeout(900);
  const actual = await page
    .locator('[data-testid="v4-model-config"]')
    .first()
    .getAttribute("data-mode");
  check(`modeIs_${mode}`, actual === mode, { actual });
}

let workspaceEnsured = false;
async function ensureRunnerWorkspace() {
  if (workspaceEnsured) return;
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
    await exact.click();
    await page
      .locator('[data-testid="runner-workspace-picker"]')
      .waitFor({ state: "hidden", timeout: 15_000 })
      .catch(() => {});
    await page.waitForTimeout(5_000);
    workspaceEnsured = true;
    return;
  }
  if ((await bodyText()).includes(WORKSPACE)) workspaceEnsured = true;
  check("runnerWorkspaceSelected", workspaceEnsured, { workspace: WORKSPACE });
}

async function startNewChat() {
  const newTask = page.locator('[data-testid="task-new-button"]').first();
  if ((await newTask.count()) > 0) {
    await newTask.click();
  } else {
    const fallback = page.locator('[data-testid="conversation-new-task"]').first();
    if ((await fallback.count()) > 0) await fallback.click();
  }
  await page.waitForTimeout(2_500);
}

/**
 * The changed-files panel of the newest turn ("N file(s) changed", +/- and Undo). It renders
 * those statistics as separate inline elements, so match the "file changed" leaf text rather
 * than one normalized string. The panel lives in the turn group, outside the row-shell list.
 */
const filePanelText = () =>
  page.evaluate(() => {
    const leaves = [...document.querySelectorAll("span")].filter(
      (element) => element.children.length === 0,
    );
    const anchor = leaves.find((element) =>
      /file changed|files changed|个文件已更改/u.test(element.textContent ?? ""),
    );
    if (!anchor) return "";
    // Walk up to the panel (it owns the Undo button) so the assertion sees the whole card.
    let node = anchor;
    for (let depth = 0; depth < 6 && node; depth += 1) {
      if (
        [...node.querySelectorAll("button")].some((button) =>
          /^(Undo|撤销)$/u.test((button.textContent ?? "").trim()),
        )
      ) {
        return (node.textContent ?? "").replace(/\s+/gu, " ").trim();
      }
      node = node.parentElement;
    }
    return (anchor.closest("div")?.textContent ?? "").replace(/\s+/gu, " ").trim();
  });

/** Diff counts rendered on the edit/write cards ("+N" / "-M" aria/visible labels). */
const diffCounts = () =>
  page.evaluate(() => {
    const labels = new Set();
    for (const element of document.querySelectorAll("[aria-label]")) {
      const label = element.getAttribute("aria-label") ?? "";
      if (/^[+-]\d+$/u.test(label)) labels.add(label);
    }
    for (const element of document.querySelectorAll("span, div")) {
      const text = (element.textContent ?? "").trim();
      if (element.children.length === 0 && /^[+-]\d+$/u.test(text)) labels.add(text);
    }
    return [...labels];
  });

async function clickButton(pattern, timeoutMs = 20_000) {
  const button = page.getByRole("button", { name: pattern }).last();
  await waitUntil(
    "button",
    async () => ((await button.count()) > 0 && (await button.isVisible()) ? true : null),
    timeoutMs,
  );
  if ((await button.count()) === 0) return false;
  await button.click({ timeout: 10_000 }).catch(() => {});
  return true;
}

let summary;
try {
  await page.goto(`${BASE_URL}/`, { waitUntil: "load" });
  await page.waitForTimeout(2_000);

  const tokenInput = page.getByPlaceholder("Paste your access token");
  const signIn = page.getByRole("button", { name: "Sign in" });
  if ((await tokenInput.count()) > 0) {
    await tokenInput.fill(TOKEN);
    if ((await signIn.count()) > 0) await signIn.click();
    await page.waitForTimeout(3_000);
  } else {
    await page.evaluate(
      ([token]) => {
        localStorage.setItem("nex.token", token);
        localStorage.setItem("nexAccessToken", token);
      },
      [TOKEN],
    );
    await page.reload({ waitUntil: "load" });
    await page.waitForTimeout(3_000);
  }

  await ensureRunnerWorkspace();
  await startNewChat();
  await setMode("edit");

  // ═══════════════════════════════════════════════════════════════════════
  // WRITE + EDIT → diff cards, changed-files panel, Undo
  // ═══════════════════════════════════════════════════════════════════════
  await typeAndSend(
    `Use the write tool to create the file ${FILE} with exactly this content: ${FIRST_LINE}. ` +
      `Then use the edit tool to replace "${FIRST_LINE}" with "${SECOND_LINE}" in the same file. ` +
      `Use no other tools. When done, reply with exactly FILE-DONE-${STAMP}.`,
  );
  const turnDone = await waitUntil(
    "turnDone",
    async () => ((await bodyText()).includes(`FILE-DONE-${STAMP}`) ? true : null),
    REPLY_TIMEOUT_MS,
  );
  await waitForIdle(REPLY_TIMEOUT_MS);
  check("turnCompleted", Boolean(turnDone));
  const onDisk = fileContent();
  details.fileOnDisk = onDisk;
  check("fileWrittenAndEdited", Boolean(onDisk?.includes(SECOND_LINE)), { onDisk });
  await shot("files-01-diff-cards");

  const counts = await diffCounts();
  details.diffCounts = counts;
  check(
    "toolCardsShowDiff",
    counts.some((label) => /^\+\d+$/u.test(label ?? "")) &&
      counts.some((label) => /^-\d+$/u.test(label ?? "")),
    { counts },
  );

  const panelShown = await waitUntil(
    "filePanel",
    async () => {
      const text = await filePanelText();
      return /file changed|files changed|个文件已更改/u.test(text) ? text : null;
    },
    30_000,
  );
  check("changedFilesPanel", Boolean(panelShown), { text: panelShown });
  // The collapsed panel shows the count; expanding it (the chevron trigger, which also flips
  // its label to "Collapse changed files") lists each changed path.
  const expandTrigger = page
    .locator(
      'button[aria-label="Expand changed files"], button[aria-label="展开更改的文件"], button[aria-label="Collapse changed files"], button[aria-label="收起更改的文件"]',
    )
    .last();
  if ((await expandTrigger.count()) > 0) {
    const label = await expandTrigger.getAttribute("aria-label");
    if (/Expand|展开/u.test(label ?? "")) {
      await expandTrigger.click({ timeout: 10_000 }).catch(() => {});
    }
  }
  const expanded = await waitUntil(
    "expandedPanel",
    async () => {
      const text = await bodyText();
      return text.includes(FILE) ? text : null;
    },
    60_000,
  );
  check("changedFilesPanelListsFile", Boolean(expanded), { file: FILE });

  // Undo: the panel's Undo opens the rewind dialog; confirm restores the turn-start files.
  const undoClicked = await clickButton(/^(Undo|撤销)$/u, 20_000);
  check("undoVisible", undoClicked);
  const dialogOpen = await waitUntil(
    "rewindDialog",
    async () => {
      const text = await bodyText();
      return /Undo file changes|撤销文件更改/u.test(text) ? text : null;
    },
    30_000,
  );
  check("rewindDialogShown", Boolean(dialogOpen));
  await shot("files-03-undo-dialog");
  const confirmClicked = await clickButton(/^(Undo files|撤销文件)$/u, 20_000);
  check("undoConfirmed", confirmClicked);
  const restored = await waitUntil(
    "restored",
    async () => {
      const content = fileContent();
      const text = await bodyText();
      const undone = /Undone|已撤销/u.test(text);
      // The turn created the file, so Undo deletes it; a pre-existing edit would be
      // restored to its turn-start content.
      return content === null && undone ? { deleted: true } : null;
    },
    60_000,
  );
  check("undoRestoredFile", Boolean(restored), { content: fileContent() });
  await shot("files-04-undone");

  // ═══════════════════════════════════════════════════════════════════════
  // EDIT WITH REWIND — "Reset chat + files" restores the files and re-runs
  // ═══════════════════════════════════════════════════════════════════════
  await typeAndSend(
    `Use the write tool to create the file ${FILE} with exactly this content: ${FIRST_LINE}. ` +
      `Use no other tools. When done, reply with exactly RERUN-OLD-${STAMP}.`,
  );
  const rerunOld = await waitUntil(
    "rerunOld",
    async () => ((await bodyText()).includes(`RERUN-OLD-${STAMP}`) ? true : null),
    REPLY_TIMEOUT_MS,
  );
  await waitForIdle(REPLY_TIMEOUT_MS);
  check("rerunTurnCompleted", Boolean(rerunOld));
  check("rerunFileWritten", fileContent()?.includes(FIRST_LINE), { content: fileContent() });

  const userRow = page.locator('[data-v4-user-input-bubble="true"]').last();
  if ((await userRow.count()) > 0) await userRow.hover();
  await page.waitForTimeout(300);
  const editActions = await page.evaluate(() =>
    [...document.querySelectorAll('[data-testid^="v4-edit-"]')]
      .filter((element) => /^v4-edit-\d+$/u.test(element.getAttribute("data-testid") ?? ""))
      .map((element) => element.getAttribute("data-testid")),
  );
  check("rerunEditAffordance", editActions.length > 0);
  if (editActions.length > 0) {
    await page
      .locator(`[data-testid="${editActions[editActions.length - 1]}"]`)
      .first()
      .click();
    await page.waitForTimeout(500);
  }
  const editInput = page.locator('[data-testid^="v4-edit-input-"]').first();
  if ((await editInput.count()) > 0) {
    const bridged = await editInput.evaluate((element, text) => {
      const bridge = element.__nexLexicalInputE2E;
      if (!bridge) return false;
      bridge.setText(text);
      return true;
    }, `Reply with exactly ${RERUN_LINE}. Use no tools.`);
    if (!bridged) {
      await editInput.click();
      await page.keyboard.press(process.platform === "darwin" ? "Meta+A" : "Control+A");
      await page.keyboard.type(`Reply with exactly ${RERUN_LINE}. Use no tools.`);
    }
    await page.waitForTimeout(300);
  }
  // The files-reset entry only enables while the turn's fileChanges are `active`.
  const rewindButton = page.locator('[data-testid^="v4-edit-rewind-workspace-"]').last();
  await waitUntil(
    "rewindWorkspaceButton",
    async () => ((await rewindButton.count()) > 0 ? true : null),
    20_000,
  );
  check("rewindWorkspaceAvailable", (await rewindButton.count()) > 0);
  if ((await rewindButton.count()) > 0) {
    await rewindButton.click({ timeout: 10_000 }).catch(() => {});
  }

  const rerunDone = await waitUntil(
    "rerunDone",
    async () => {
      const text = await bodyText();
      return text.includes(RERUN_LINE) && !text.includes(`RERUN-OLD-${STAMP}`) ? text : null;
    },
    REPLY_TIMEOUT_MS,
  );
  check("rewindReranTurn", Boolean(rerunDone));
  // 修复依据：文件还原发生在 runner 处理 editUserQuery{rewind} 时，重跑回复（rerunDone）
  // 与它并发到达浏览器；`rerunDone` 一出现就同步 stat，文件可能还没被删除（旧断言因此
  // 偶发读到删除前的 "first line"）。这里等磁盘状态真正到位（文件消失或内容不含首行），
  // 超时仍不对再判失败，而不是把「刚点完就去读」当成产品行为。
  const restoredFiles = await waitUntil(
    "rewindFilesRestored",
    async () => {
      const content = fileContent();
      return content === null || !content.includes(FIRST_LINE)
        ? { deleted: content === null }
        : null;
    },
    20_000,
  );
  const afterRewind = fileContent();
  details.afterRewind = afterRewind;
  details.rewindRestored = restoredFiles;
  check("rewindRestoredFiles", Boolean(restoredFiles), { content: afterRewind });
  await shot("files-05-rewind-rerun");

  // Known web-only noise (same filter as the other suites).
  const significantErrors = logs.filter(
    (line) =>
      line.startsWith("PAGEERROR") ||
      (/CONSOLE\[(error|warning)\]/.test(line) &&
        !/zcode\.z\.ai|ERR_FAILED|Tooltip is changing|accepted input projection silent|Missing `Description`|getSkillReferenceCatalog/u.test(
          line,
        )),
  );
  check("noAppErrors", significantErrors.length === 0, significantErrors.slice(0, 8));
} catch (error) {
  failures.push("exception");
  details.exception = error instanceof Error ? (error.stack ?? error.message) : String(error);
  try {
    await shot("files-99-exception");
  } catch {
    // Screenshot failure must not hide the exception detail.
  }
} finally {
  try {
    fs.rmSync(FILE_PATH, { force: true });
  } catch {
    // Best effort: the workspace is a throwaway demo directory.
  }
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
  workspace: WORKSPACE,
  file: FILE,
  checks,
  failures,
  details,
};
fs.writeFileSync(
  path.join(OUT_DIR, "file-changes-summary.json"),
  `${JSON.stringify(summary, null, 2)}\n`,
);
console.log(JSON.stringify(summary, null, 2));
process.exit(summary.ok ? 0 : 1);
