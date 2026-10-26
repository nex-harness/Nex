#!/usr/bin/env node
// Task-state E2E for the next/ MVP (docs/next/task-state.md):
//
//   create two chats → pin one (moves to the pinned section) → archive the other
//   (leaves the project list, appears in the archived view) → unarchive it →
//   rename one → delete one → reload → every mutation survived.
//
// All mutations go through the real sidebar controls (row context menu / inline
// archive button / archived-view buttons), never through a service shortcut, so the
// channel wiring (nex-task + window-controller) is what gets exercised.
//
// Environment:
//   NEX_E2E_BASE_URL  Web origin to drive        (default http://localhost:5174)
//   NEX_E2E_TOKEN     Browser login token        (required)
//   NEX_E2E_CHROME    Chrome/Chromium executable (default: auto-detect)
//   NEX_E2E_OUT       Screenshot + summary dir   (default <repo>/next/e2e/out)
//   NEX_E2E_WORKSPACE     Workspace path to open via the machine picker (optional)
//   NEX_E2E_ALLOW_EXISTING_TASKS  "1" keeps existing rows (deployed stacks)
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
const ALLOW_EXISTING_TASKS = process.env.NEX_E2E_ALLOW_EXISTING_TASKS === "1";
const OPTION_TIMEOUT_MS = Number(process.env.NEX_E2E_OPTION_TIMEOUT_MS) || 60_000;
const REPLY_TIMEOUT_MS = Number(process.env.NEX_E2E_REPLY_TIMEOUT_MS) || 180_000;

function failConfig(message) {
  console.error(`[task-actions] ${message}`);
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

fs.mkdirSync(OUT_DIR, { recursive: true });

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
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
page.on("pageerror", (error) => logs.push(`PAGEERROR: ${error.message}`));
page.on("console", (message) => {
  if (message.type() === "error" || message.type() === "warning") {
    logs.push(`CONSOLE[${message.type()}]: ${message.text()}`);
  }
});

const shot = (name) => page.screenshot({ path: path.join(OUT_DIR, `${name}.png`) });

const PROJECT_SECTION_LABELS = new Set(["已置顶", "Pinned"]);

/**
 * Sidebar rows, partitioned. Pinned and project rows share the `task-item-<id>`
 * test id, so a row counts as pinned when the nearest ancestor section holds a
 * pinned header; everything else is a project row.
 */
const sidebarRows = () =>
  page.evaluate(
    (pinnedLabels) => {
      const labels = new Set(pinnedLabels);
      const rows = [...document.querySelectorAll("[data-testid^='task-item-']")].map((element) => {
        let pinned = false;
        for (let node = element.parentElement; node; node = node.parentElement) {
          const headers = [...node.querySelectorAll(":scope > h3")];
          if (headers.some((header) => labels.has((header.textContent ?? "").trim()))) {
            pinned = true;
            break;
          }
        }
        return {
          testId: element.getAttribute("data-testid") ?? "",
          key: element.getAttribute("data-task-item-key") ?? "",
          text: (element.textContent ?? "").trim(),
          pinned,
        };
      });
      return {
        project: rows.filter((row) => !row.pinned),
        pinned: rows.filter((row) => row.pinned),
      };
    },
    [...PROJECT_SECTION_LABELS],
  );

const projectRows = async () => (await sidebarRows()).project;
const pinnedRows = async () => (await sidebarRows()).pinned;

/** Archives rows are plain `<li>`s inside the archived view (no task-item test ids). */
const archivedRows = () =>
  page.evaluate(() => {
    const labels = new Set(["暂无归档任务", "No archived tasks"]);
    const empty = [...document.querySelectorAll("div")].some((element) =>
      labels.has((element.textContent ?? "").trim()),
    );
    const actionButtons = [...document.querySelectorAll("button")].filter((element) => {
      const label = element.getAttribute("aria-label") ?? "";
      return label === "取消归档任务" || label === "Unarchive task";
    });
    const rows = actionButtons.map((button) => {
      const item = button.closest("li");
      return {
        text: (item?.textContent ?? "").trim(),
        hasDelete:
          Boolean(item) &&
          [...(item?.querySelectorAll("button") ?? [])].some((candidate) => {
            const label = candidate.getAttribute("aria-label") ?? "";
            return label === "删除任务" || label === "Delete task";
          }),
      };
    });
    return { empty, rows };
  });

/** Opens/closes the archived view; the toggle becomes a Close button while open. */
async function toggleArchivedView(toggle, open) {
  if ((await toggle.count()) === 0) return false;
  const state = await toggle.getAttribute("data-state");
  const expanded = state === "on";
  if (expanded === open) return true;
  await toggle.click();
  await page.waitForTimeout(2_500);
  return true;
}

/** Unarchives one row from the open archived view (matched by its prompt text). */
async function unarchiveArchivedRow(matchText) {
  return page.evaluate((needle) => {
    const labels = new Set(["取消归档任务", "Unarchive task"]);
    const buttons = [...document.querySelectorAll("button")].filter((element) =>
      labels.has(element.getAttribute("aria-label") ?? ""),
    );
    for (const button of buttons) {
      const item = button.closest("li");
      if (item && (item.textContent ?? "").includes(needle)) {
        button.click();
        return true;
      }
    }
    return false;
  }, matchText);
}

/** Deletes one row from the open archived view after the confirm dialog. */
async function deleteArchivedRow(matchText) {
  const clicked = await page.evaluate((needle) => {
    const labels = new Set(["删除任务", "Delete task"]);
    const buttons = [...document.querySelectorAll("button")].filter((element) =>
      labels.has(element.getAttribute("aria-label") ?? ""),
    );
    for (const button of buttons) {
      const item = button.closest("li");
      if (item && (item.textContent ?? "").includes(needle)) {
        button.click();
        return true;
      }
    }
    return false;
  }, matchText);
  if (!clicked) return false;
  await page.waitForTimeout(1_000);
  // The confirm dialog reuses the same label as the row button; scope the click to the
  // dialog so the row button under the overlay cannot swallow it.
  const dialog = page.locator('[role="dialog"]').last();
  if ((await dialog.count()) > 0) {
    const dialogButtons = dialog.getByRole("button");
    const count = await dialogButtons.count();
    if (count > 0) await dialogButtons.nth(count - 1).click();
  }
  await page.waitForTimeout(1_500);
  return true;
}

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
    await page.waitForTimeout(500);
  }
  details[`last_${label}`] = last;
  return null;
}

/** Sends one prompt in the composer and waits for the reply text. */
async function startChat(prompt, expectText) {
  const editor = page.locator('[data-testid="chat-input"], [contenteditable="true"]').first();
  await editor.waitFor({ state: "visible", timeout: 30_000 });
  await editor.click();
  await page.keyboard.type(prompt);
  await page.keyboard.press("Enter");
  const deadline = Date.now() + REPLY_TIMEOUT_MS;
  while (Date.now() < deadline) {
    await page.waitForTimeout(3_000);
    const body = await bodyText();
    if (body.includes(expectText) && !/\bStop\b|停止/u.test(body)) return true;
  }
  return false;
}

/** Opens the row context menu and clicks one of its items by localized label. */
async function clickRowMenuItem(rowKey, labels) {
  const row = page.locator(`[data-testid^='task-item-'][data-task-item-key="${rowKey}"]`).first();
  if ((await row.count()) === 0) return false;
  // The row itself may sit under a transient overlay (hover toolbar/active highlight);
  // dispatch the contextmenu event directly so the menu opens deterministically.
  await row.evaluate((element) => {
    element.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
  });
  await page.waitForTimeout(700);
  for (const label of labels) {
    const item = page.getByRole("menuitem", { name: label }).first();
    if ((await item.count()) > 0) {
      await item.click();
      await page.waitForTimeout(800);
      return true;
    }
  }
  await page.keyboard.press("Escape");
  return false;
}

/** Starts a fresh chat (new task) so the next prompt lands in its own session. */
async function startNewChat() {
  const newTask = page.locator('[data-testid="task-new-button"]').first();
  if ((await newTask.count()) > 0) {
    // 修复依据：全量门禁在这里偶发超时——回复完成后的 toast 栈会短暂盖住侧栏
    // （click 日志报 html "intercepts pointer events"）。这里先按 conversation 套件
    // 的方式清掉 toast；若点击仍被全屏 overlay 拦住（命令面板/对话框），按 Escape
    // 关闭后重试一次；仍然失败才走 DOM 派发点击，避免瞬时 overlay 让整个套件判负。
    await page.evaluate(() => {
      for (const host of document.querySelectorAll("#nex-toast-host")) {
        host.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
        const dismiss = host.querySelector("button[aria-label]");
        if (dismiss) dismiss.click();
      }
    });
    await page.waitForTimeout(300);
    try {
      await newTask.click({ timeout: 8_000 });
    } catch {
      await page.keyboard.press("Escape");
      await page.waitForTimeout(500);
      try {
        await newTask.click({ timeout: 8_000 });
      } catch {
        await newTask.evaluate((element) => {
          element.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
        });
      }
    }
  } else {
    const fallback = page.locator('[data-testid="conversation-new-task"]').first();
    if ((await fallback.count()) > 0) await fallback.click();
  }
  await page.waitForTimeout(2_500);
}

/**
 * Inline archive: the row only mounts its hover actions after a pointer enters, so
 * hover first, then click Archive (which turns into a destructive Confirm button).
 */
async function archiveRow(rowKey) {
  const row = page.locator(`[data-testid^='task-item-'][data-task-item-key="${rowKey}"]`).first();
  if ((await row.count()) === 0) return false;
  await row.hover();
  await page.waitForTimeout(500);
  const taskId = rowKey.split(":").pop();
  const archiveButton = page.locator(`[data-testid="task-archive-${taskId}"]`).first();
  if ((await archiveButton.count()) === 0) return false;
  await archiveButton.click();
  await page.waitForTimeout(700);
  const confirm = page.getByRole("button", { name: /^(确认|Confirm)$/u }).first();
  if ((await confirm.count()) > 0) {
    await confirm.click();
  } else if ((await archiveButton.count()) > 0) {
    // Second click confirms when the confirm button is not a separate role match.
    await archiveButton.click();
  }
  await page.waitForTimeout(1_200);
  return true;
}

async function renameRow(rowKey, title) {
  if (!(await clickRowMenuItem(rowKey, ["重命名任务", "Rename task"]))) return false;
  const input = page.locator('[role="dialog"] input:not([type="file"])').first();
  await input.waitFor({ state: "visible", timeout: 15_000 });
  await input.fill(title);
  const confirm = page.getByRole("button", { name: /^(确认|Confirm)$/u }).first();
  if ((await confirm.count()) > 0) await confirm.click();
  else await page.keyboard.press("Enter");
  await page.waitForTimeout(1_500);
  return true;
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
    if ((await tokenInput.count()) > 0 && (await tokenInput.isVisible())) continue;
    break;
  }

  const composer = page.locator('[data-testid="chat-input"], [contenteditable="true"]').first();
  await composer.waitFor({ state: "visible", timeout: 90_000 });
  await page.waitForTimeout(8_000);

  if (WORKSPACE) {
    const addProject = page.locator('[data-testid="project-add"]').first();
    await addProject.waitFor({ state: "visible", timeout: 30_000 });
    await addProject.click();
    await page.getByText("Open folder", { exact: true }).first().click();
    await page
      .locator('[data-testid="runner-workspace-picker"]')
      .waitFor({ state: "visible", timeout: 20_000 });
    const option = page
      .locator('[data-testid="runner-workspace-option"]')
      .filter({ has: page.locator(`span[title="${WORKSPACE}"]`) })
      .first();
    await option.waitFor({ state: "visible", timeout: OPTION_TIMEOUT_MS });
    await option.click();
    await page
      .locator('[data-testid="runner-workspace-picker"]')
      .waitFor({ state: "hidden", timeout: 15_000 });
    await page.waitForTimeout(6_000);
  }
  await shot("tasks-01-loaded");

  const rowsBefore = await projectRows();
  details.projectRowsBefore = rowsBefore.length;
  if (!ALLOW_EXISTING_TASKS) {
    check("startsEmpty", rowsBefore.length === 0, { count: rowsBefore.length });
  }

  // ── two throwaway chats ──
  const stamp = Date.now();
  const promptA = `Reply with exactly: pin-me-${stamp}`;
  const promptB = `Reply with exactly: archive-me-${stamp}`;
  check("chatAReplied", await startChat(promptA, `pin-me-${stamp}`));
  await page.waitForTimeout(3_000);
  const rowsAfterA = await projectRows();
  const addedA = rowsAfterA.filter((row) => !rowsBefore.some((before) => before.key === row.key));
  check("chatAHasRow", addedA.length === 1, { added: addedA });
  const keyA = addedA[0]?.key ?? "";

  // A second chat needs a new composer/session; typing into the open one would
  // continue chat A instead of creating chat B.
  await startNewChat();
  check("chatBReplied", await startChat(promptB, `archive-me-${stamp}`));
  await page.waitForTimeout(3_000);
  const rowsAfterB = await projectRows();
  const addedB = rowsAfterB.filter((row) => !rowsAfterA.some((before) => before.key === row.key));
  check("chatBHasRow", addedB.length === 1, { added: addedB });
  const keyB = addedB[0]?.key ?? "";
  await shot("tasks-02-two-chats");

  // ── pin chat A: leaves the project list, appears in the pinned section ──
  check("pinMenuClicked", await clickRowMenuItem(keyA, ["置顶任务", "Pin task"]));
  const pinned = await waitUntil("pinnedRowA", async () => {
    const rows = await pinnedRows();
    return rows.some((row) => row.key === keyA) ? rows : null;
  });
  check("pinnedSectionShowsA", Boolean(pinned), { pinned });
  const projectAfterPin = await waitUntil("projectWithoutA", async () => {
    const rows = await projectRows();
    return rows.some((row) => row.key === keyA) ? null : rows;
  });
  check("pinnedRowLeftProjectList", Boolean(projectAfterPin), projectAfterPin);
  await shot("tasks-03-pinned");

  // ── archive chat B: inline archive button, then confirm ──
  check("archiveClicked", await archiveRow(keyB));
  const projectAfterArchive = await waitUntil("projectWithoutB", async () => {
    const rows = await projectRows();
    return rows.some((row) => row.key === keyB) ? null : rows;
  });
  check("archivedRowLeftProjectList", Boolean(projectAfterArchive), projectAfterArchive);

  // Open the archived view from the toolbar and find B there.
  const archivedToggle = page
    .getByRole("button", { name: /^(归档|Archived|关闭|Close)$/u })
    .first();
  check("archivedToggleVisible", (await archivedToggle.count()) > 0);
  await toggleArchivedView(archivedToggle, true);
  await shot("tasks-04-archived-view");
  const archived = await waitUntil("archivedRowB", async () => {
    const view = await archivedRows();
    const row = view.rows.find((item) => item.text.includes(`archive-me-${stamp}`));
    return row ?? null;
  });
  check("archivedViewShowsB", Boolean(archived), archived);

  // ── unarchive B from the archived view ──
  check("unarchiveClicked", await unarchiveArchivedRow(`archive-me-${stamp}`));
  // The archived view replaces the project list; close it before checking the sidebar.
  await toggleArchivedView(archivedToggle, false);
  const restored = await waitUntil("restoredB", async () => {
    const rows = await projectRows();
    return rows.some((row) => row.key === keyB) ? rows : null;
  });
  check("unarchivedRowBackInProject", Boolean(restored), restored);
  await shot("tasks-05-unarchived");

  // ── rename chat A from the pinned section (context menu) ──
  const renamedTitle = `renamed-${stamp}`;
  const renamed = await renameRow(keyA, renamedTitle);
  check("renameMenuClicked", renamed);
  const renameVisible = await waitUntil("renamedTitle", async () => {
    const rows = await pinnedRows();
    return rows.some((row) => row.text.includes(renamedTitle)) ? rows : null;
  });
  check("pinnedRowShowsNewTitle", Boolean(renameVisible), renameVisible);
  await shot("tasks-06-renamed");

  // ── delete chat B from the archived view (archive it again, then delete) ──
  check("rearchiveClicked", await archiveRow(keyB));
  await toggleArchivedView(archivedToggle, true);
  await waitUntil("archivedAgain", async () => {
    const view = await archivedRows();
    return view.rows.some((item) => item.text.includes(`archive-me-${stamp}`)) ? view : null;
  });
  check("deleteClicked", await deleteArchivedRow(`archive-me-${stamp}`));
  const deletedGone = await waitUntil("deletedB", async () => {
    const view = await archivedRows();
    return view.rows.some((item) => item.text.includes(`archive-me-${stamp}`)) ? null : view;
  });
  check("deletedRowGoneFromArchived", Boolean(deletedGone), deletedGone);
  await shot("tasks-07-deleted");

  // ── reload: every mutation survived ──
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.waitForTimeout(10_000);
  await shot("tasks-08-after-reload");
  // The archived view may still be open after reload; the project list is hidden then.
  const reloadedArchivedToggle = page
    .getByRole("button", { name: /^(归档|Archived|关闭|Close)$/u })
    .first();
  await toggleArchivedView(reloadedArchivedToggle, false);
  const reloadedPinned = await waitUntil(
    "pinnedAfterReload",
    async () => {
      const rows = await pinnedRows();
      return rows.some((row) => row.text.includes(renamedTitle)) ? rows : null;
    },
    40_000,
  );
  check("pinnedSurvivesReload", Boolean(reloadedPinned), reloadedPinned);
  const reloadedProject = await projectRows();
  check(
    "deletedStaysGoneAfterReload",
    !reloadedProject.some((row) => row.key === keyB),
    reloadedProject,
  );
  await toggleArchivedView(reloadedArchivedToggle, true);
  const reloadedArchived = await archivedRows();
  check(
    "archivedViewEmptyAfterReload",
    !reloadedArchived.rows.some((item) => item.text.includes(`archive-me-${stamp}`)),
    reloadedArchived,
  );

  // Known web-only noise unrelated to task state: the external client-config fetch is
  // blocked by CORS, Radix warns about a missing dialog description, and the one-off
  // projection-vs-ACK warning self-recovers via snapshot (same filter as acceptance.mjs).
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
  path.join(OUT_DIR, "task-actions-summary.json"),
  `${JSON.stringify(summary, null, 2)}\n`,
);
console.log(JSON.stringify(summary, null, 2));
process.exit(summary.ok ? 0 : 1);
