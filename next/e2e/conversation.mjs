#!/usr/bin/env node
// Conversation-features E2E for the next/ MVP.
//
// Batch 2c (QUEUE) — the runner owns the queue (docs/next/conversation-features.md
// decision 4):
//
//   1. a long-running turn (bash `sleep`, run in Full-access mode so no approval card
//      appears) keeps the session busy deterministically;
//   2. three messages sent while busy land in the queue panel in order;
//   3. reorder the last item to the head by dragging its handle, delete another item;
//   4. the remaining items drain in the panel order and answer in that order;
//   5. Stop while busy with queued items pauses the queue (paused banner), and Resume
//      drains it.
//
// Sections reserved for batch 2d (compact marker, edit a sent message, fork a reply) are
// marked below; add them in place, they must not change the QUEUE section.
//
// Environment:
//   NEX_E2E_BASE_URL  Web origin to drive        (default http://localhost:5174)
//   NEX_E2E_TOKEN     Browser login token        (required)
//   NEX_E2E_CHROME    Chrome/Chromium executable (default: auto-detect)
//   NEX_E2E_OUT       Screenshot + summary dir   (default <repo>/next/e2e/out)
//   NEX_E2E_WORKSPACE absolute runner workspace to open via the machine picker (optional)
//   NEX_E2E_ALLOW_EXISTING_TASKS "1" keeps existing sidebar rows (deployed stacks)
//   NEX_E2E_REPLY_TIMEOUT_MS     per-reply budget (default 180000)
//   NEX_E2E_BUSY_SLEEP_SECONDS   seconds the busy turn sleeps (default 25)
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
const REPLY_TIMEOUT_MS = Number(process.env.NEX_E2E_REPLY_TIMEOUT_MS) || 180_000;
const BUSY_SLEEP_SECONDS = Number(process.env.NEX_E2E_BUSY_SLEEP_SECONDS) || 25;

function failConfig(message) {
  console.error(`[conversation] ${message}`);
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

async function isBusy() {
  return (await stopButton().count()) > 0;
}

async function waitForBusy(timeoutMs = 60_000) {
  return waitUntil("busy", isBusy, timeoutMs);
}

async function waitForIdle(timeoutMs = REPLY_TIMEOUT_MS) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (!(await isBusy())) return true;
    await page.waitForTimeout(700);
  }
  return false;
}

const composer = () =>
  page.locator('[data-testid="v4-composer-input"], [contenteditable="true"]').first();

/**
 * Onboarding/newsletter toasts ("Get notified when tasks finish") render above the
 * composer and swallow the click. Dismiss them before typing; the composer is focused
 * programmatically so a lingering toast cannot make the send flaky.
 */
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
  // Focus through the DOM: a toast overlay can intercept pointer events, but focus and
  // keyboard input still reach the editor.
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

/** Composer send while a turn is running: no idle wait (the queue must accept it). */
async function sendWhileBusy(text) {
  await typeAndSend(text);
}

/**
 * The panel unmounts entirely when the queue empties, so this must never block: a
 * `getAttribute` on a vanished locator waits the full action timeout. Read the attribute in
 * the DOM instead, which returns null immediately when nothing matches.
 */
async function queueItemCount() {
  return page.evaluate(() => {
    const panel = document.querySelector('[data-testid="v4-queue"]');
    return Number(panel?.getAttribute("data-queue-count") ?? 0);
  });
}

const queueItems = () =>
  page.evaluate(() =>
    [
      ...document.querySelectorAll(
        "[data-testid^='v4-queue-item-'][data-queue-item-id][data-index]",
      ),
    ].map((element) => ({
      queueItemId: element.getAttribute("data-queue-item-id") ?? "",
      text: (element.textContent ?? "").trim(),
      index: Number(element.getAttribute("data-index") ?? -1),
      dispatchState: element.getAttribute("data-dispatch-state") ?? "",
    })),
  );

async function waitForQueueCount(count, timeoutMs = 30_000) {
  return waitUntil(
    `queueCount${count}`,
    async () => ((await queueItemCount()) === count ? true : null),
    timeoutMs,
  );
}

/** Drags one queue row's handle onto another row (dnd-kit pointer sensor, 6px activation). */
async function dragQueueItemOnto(fromId, toId) {
  const handle = page
    .locator(`[data-v4-queue-drag-handle="true"][data-queue-item-id="${fromId}"]`)
    .first();
  const target = page
    .locator(`[data-testid^='v4-queue-item-'][data-queue-item-id="${toId}"][data-index]`)
    .first();
  if ((await handle.count()) === 0 || (await target.count()) === 0) return false;
  const from = await handle.boundingBox();
  const to = await target.boundingBox();
  if (!from || !to) return false;
  const startX = from.x + from.width / 2;
  const startY = from.y + from.height / 2;
  await page.mouse.move(startX, startY);
  await page.mouse.down();
  // Cross the 6px activation threshold inside the handle first.
  await page.mouse.move(startX, startY - 12, { steps: 5 });
  await page.mouse.move(to.x + to.width / 2, to.y + to.height / 2, { steps: 20 });
  await page.waitForTimeout(250);
  await page.mouse.up();
  await page.waitForTimeout(900);
  return true;
}

async function clickQueueDelete(queueItemId) {
  const button = page
    .locator(`[data-testid^="v4-queue-item-delete"][data-queue-item-id="${queueItemId}"]`)
    .first();
  if ((await button.count()) === 0) return false;
  await button.click();
  await page.waitForTimeout(900);
  return true;
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

/** Opens the runner workspace once (same picker flow as the other next/ E2Es). */
let workspaceEnsured = false;
async function ensureRunnerWorkspace() {
  if (workspaceEnsured || !WORKSPACE) return;
  const optionTimeoutMs = Number(process.env.NEX_E2E_OPTION_TIMEOUT_MS) || 60_000;
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
    await exact.waitFor({ state: "visible", timeout: optionTimeoutMs }).catch(() => {});
    await exact.click();
    await page
      .locator('[data-testid="runner-workspace-picker"]')
      .waitFor({ state: "hidden", timeout: 15_000 })
      .catch(() => {});
    await page.waitForTimeout(5_000);
    workspaceEnsured = true;
    return;
  }
  const body = await bodyText();
  if (body.includes(WORKSPACE)) workspaceEnsured = true;
}

/** Starts a fresh chat inside the connected runner workspace. */
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
 * The prompt that keeps a turn busy: Full-access mode lets `sleep` run without an
 * approval card, and the reply marker proves the turn reached the end.
 */
function busyPrompt(marker) {
  return (
    `First use the bash tool to run exactly this command: sleep ${BUSY_SLEEP_SECONDS}. ` +
    `Do not use any other tool. When it finishes, reply with exactly ${marker} and nothing else.`
  );
}

function markerIndex(body, marker) {
  return body.indexOf(marker);
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

  await composer().waitFor({ state: "visible", timeout: 90_000 });
  await page.waitForTimeout(8_000);
  await ensureRunnerWorkspace();
  await shot("conv-00-loaded");

  if (!ALLOW_EXISTING_TASKS) {
    const rows = await page.evaluate(
      () => document.querySelectorAll("[data-testid^='task-item-']").length,
    );
    check("startsEmpty", rows === 0, { rows });
  }
  await startNewChat();
  await setMode("yolo");

  // ═══════════════════════════════════════════════════════════════════════
  // QUEUE (batch 2c) — reorder + delete + drain order
  // ═══════════════════════════════════════════════════════════════════════
  const stamp = Date.now();
  const busyMarker = `BUSY-DONE-${stamp}`;
  const replyA = `queue-alpha-${stamp}`;
  const replyB = `queue-bravo-${stamp}`;
  const replyC = `queue-charlie-${stamp}`;

  await typeAndSend(busyPrompt(busyMarker));
  const busy = await waitForBusy(90_000);
  check("busyTurnStarted", Boolean(busy));
  await shot("conv-01-busy");

  // Three inputs while busy → three queue items, in admission order.
  await sendWhileBusy(`Reply with exactly ${replyA}`);
  await sendWhileBusy(`Reply with exactly ${replyB}`);
  await sendWhileBusy(`Reply with exactly ${replyC}`);
  check("queueShowsThree", Boolean(await waitForQueueCount(3, 30_000)));
  const queued = await queueItems();
  details.queued = queued;
  check(
    "queueOrderIsAdmissionOrder",
    queued
      .map((item) => (item.text.includes(replyA) ? "a" : item.text.includes(replyB) ? "b" : "c"))
      .join("") === "abc",
    queued.map((item) => item.text),
  );
  await shot("conv-02-queued");

  // Reorder charlie (last) before alpha (first) by dragging its handle.
  const ids = Object.fromEntries(
    queued.map((item) => [
      item.text.includes(replyA) ? "a" : item.text.includes(replyB) ? "b" : "c",
      item.queueItemId,
    ]),
  );
  const dragged = await dragQueueItemOnto(ids.c, ids.a);
  check("queueDragStarted", dragged);
  const reordered = await waitUntil(
    "reordered",
    async () => {
      const items = await queueItems();
      return items[0]?.text.includes(replyC) ? items : null;
    },
    20_000,
  );
  check(
    "queueReorderMovedCharlieToHead",
    Boolean(reordered),
    reordered?.map((item) => item.text),
  );
  await shot("conv-03-reordered");

  // Delete bravo; charlie must stay at the head and alpha last.
  check("queueDeleteClicked", await clickQueueDelete(ids.b));
  const afterDelete = await waitUntil(
    "afterDelete",
    async () => {
      const items = await queueItems();
      return items.length === 2 ? items : null;
    },
    20_000,
  );
  check(
    "queueDeleteLeftCharlieThenAlpha",
    Boolean(afterDelete) &&
      afterDelete[0].text.includes(replyC) &&
      afterDelete[1].text.includes(replyA),
    afterDelete?.map((item) => item.text),
  );
  await shot("conv-04-deleted");

  // Let the busy turn finish: the two remaining items drain in panel order.
  const drained = await waitUntil(
    "queueDrained",
    async () => ((await queueItemCount()) === 0 ? true : null),
    REPLY_TIMEOUT_MS,
  );
  check("queueDrained", Boolean(drained));
  await waitForIdle(REPLY_TIMEOUT_MS);
  await page.waitForTimeout(2_000);
  const bodyAfterDrain = await bodyText();
  const iBusy = markerIndex(bodyAfterDrain, busyMarker);
  const iC = markerIndex(bodyAfterDrain, replyC);
  const iA = markerIndex(bodyAfterDrain, replyA);
  check("drainedBusyReplyPresent", iBusy >= 0);
  check("drainedCharliePresent", iC >= 0);
  check("drainedAlphaPresent", iA >= 0);
  // Draining order = panel order after reorder: charlie before alpha; bravo was deleted.
  check("drainedOrderMatchesQueue", iC >= 0 && iA >= 0 && iC < iA, { iC, iA });
  check("deletedItemNeverRan", markerIndex(bodyAfterDrain, replyB) < 0);
  await shot("conv-05-drained");

  // ═══════════════════════════════════════════════════════════════════════
  // QUEUE (batch 2c) — Stop pauses the queue, Resume drains it
  // ═══════════════════════════════════════════════════════════════════════
  const stopStamp = Date.now();
  const stopBusyMarker = `STOP-BUSY-DONE-${stopStamp}`;
  const stopReplyA = `stop-alpha-${stopStamp}`;
  const stopReplyB = `stop-bravo-${stopStamp}`;

  await startNewChat();
  await setMode("yolo");
  await typeAndSend(busyPrompt(stopBusyMarker));
  check("stopBusyTurnStarted", Boolean(await waitForBusy(90_000)));
  await sendWhileBusy(`Reply with exactly ${stopReplyA}`);
  await sendWhileBusy(`Reply with exactly ${stopReplyB}`);
  check("stopQueueHasTwo", Boolean(await waitForQueueCount(2, 30_000)));
  await shot("conv-06-stop-queued");

  const stop = stopButton();
  if ((await stop.count()) > 0) await stop.click();
  const paused = await waitUntil(
    "pausedBanner",
    async () => {
      const banner = page.locator('[data-testid="v4-queue-paused-banner"]').first();
      return (await banner.count()) > 0 ? banner : null;
    },
    60_000,
  );
  check("queuePausedBannerAfterStop", Boolean(paused));
  const pausedCount = await queueItemCount();
  check("pausedQueueKeptItems", pausedCount === 2, { pausedCount });
  const autoDrainFlag = await page.evaluate(
    () =>
      document.querySelector('[data-testid="v4-queue"]')?.getAttribute("data-queue-auto-drain") ??
      null,
  );
  check("pausedQueueAutoDrainOff", autoDrainFlag === "false", { autoDrainFlag });
  await shot("conv-07-paused");

  const resume = page.locator('[data-testid="v4-queue-resume"]').first();
  check("resumeButtonVisible", (await resume.count()) > 0);
  if ((await resume.count()) > 0) await resume.click();
  const resumed = await waitUntil(
    "resumed",
    async () => ((await queueItemCount()) === 0 ? true : null),
    REPLY_TIMEOUT_MS,
  );
  check("queueResumeDrained", Boolean(resumed));
  await waitForIdle(REPLY_TIMEOUT_MS);
  await page.waitForTimeout(2_000);
  const bodyAfterResume = await bodyText();
  const rA = markerIndex(bodyAfterResume, stopReplyA);
  const rB = markerIndex(bodyAfterResume, stopReplyB);
  check("resumeRanAlpha", rA >= 0);
  check("resumeRanBravo", rB >= 0);
  check("resumeRanInOrder", rA >= 0 && rB >= 0 && rA < rB, { rA, rB });
  await shot("conv-08-resumed");

  // ═══════════════════════════════════════════════════════════════════════
  // Batch 2d — reserved: compact marker, edit a sent message, fork a reply.
  // Add those sections here; they must not change the QUEUE checks above.
  // ═══════════════════════════════════════════════════════════════════════

  // Known web-only noise unrelated to conversation state: the external client-config fetch
  // is blocked by CORS, Radix warns about a missing dialog description, and the one-off
  // projection-vs-ACK warning self-recovers via snapshot (same filter as the other suites).
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
    await shot("conv-99-exception");
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
  busySleepSeconds: BUSY_SLEEP_SECONDS,
  checks,
  failures,
  details,
};
fs.writeFileSync(
  path.join(OUT_DIR, "conversation-summary.json"),
  `${JSON.stringify(summary, null, 2)}\n`,
);
console.log(JSON.stringify(summary, null, 2));
process.exit(summary.ok ? 0 : 1);
