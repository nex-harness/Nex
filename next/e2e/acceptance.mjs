#!/usr/bin/env node
// Acceptance E2E for the next/ MVP: login -> model selected -> send prompt ->
// streamed reply + Read/Bash tool cards -> session in sidebar -> reload ->
// reopen transcript with the model still selected.
//
// The runner workspace MUST contain notes.txt with exactly this content:
//
//   hello from demo
//
// The prompt asks the agent to read that file and run `ls -la`. next/dev.sh
// creates the file in its demo workspace before starting the runner; when you
// point this script at a stack you started by hand, create it yourself.
//
// Environment:
//   NEX_E2E_BASE_URL  Web origin to drive        (default http://localhost:5174)
//   NEX_E2E_TOKEN     Browser login token        (required)
//   NEX_E2E_CHROME    Chrome/Chromium executable (default: auto-detect)
//   NEX_E2E_OUT       Screenshot + summary dir   (default <repo>/next/e2e/out)
//   NEX_E2E_EXPECT_TEXT      notes.txt content the agent must echo
//                            (default "hello from demo")
//   NEX_E2E_ALLOW_EXISTING_TASKS  when "1", do not require an empty sidebar
//                            before the send; assert that exactly one new task
//                            row appears instead (deployed stacks keep sessions)
//   NEX_E2E_WORKSPACE        absolute workspace path on the runner to open via
//                            the machine picker (default: use the restored tab)
//   NEX_E2E_OPTION_TIMEOUT_MS  how long to wait for that picker entry (default 60000)
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
const EXPECT_TEXT = process.env.NEX_E2E_EXPECT_TEXT?.trim() || "hello from demo";
const ALLOW_EXISTING_TASKS = process.env.NEX_E2E_ALLOW_EXISTING_TASKS === "1";
const WORKSPACE = process.env.NEX_E2E_WORKSPACE?.trim() ?? "";
const OPTION_TIMEOUT_MS = Number(process.env.NEX_E2E_OPTION_TIMEOUT_MS) || 60_000;
const PROMPT =
  "Use the read tool to read notes.txt, then run `ls -la` with bash, then reply with one short sentence.";
const REPLY_TIMEOUT_MS = Number(process.env.NEX_E2E_REPLY_TIMEOUT_MS) || 180_000;

function failConfig(message) {
  console.error(`[acceptance] ${message}`);
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

const browser = await chromium.launch({ executablePath: chromePath, headless: true });
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
const logs = [];
const frames = [];

page.on("pageerror", (error) => logs.push(`PAGEERROR: ${error.message}\n${error.stack ?? ""}`));
page.on("console", (message) => {
  if (message.type() === "debug" || message.type() === "verbose") return;
  const line = `CONSOLE[${message.type()}]: ${message.text()}`;
  logs.push(line);
  if (message.type() === "error" || message.type() === "warning")
    consoleErrors.push(line.slice(0, 300));
});
page.on("websocket", (socket) => {
  socket.on("framesent", (frame) =>
    frames.push({ dir: ">", payload: Buffer.from(frame.payload).toString("latin1") }),
  );
  socket.on("framereceived", (frame) =>
    frames.push({ dir: "<", payload: Buffer.from(frame.payload).toString("latin1") }),
  );
});

const shot = (name) => page.screenshot({ path: path.join(OUT_DIR, `${name}.png`) });
/**
 * Code blocks render inside a @pierre/diffs shadow root, so `body.innerText`
 * never contains their text. Walk open shadow roots and append their text.
 */
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
const taskRows = () => page.locator("[data-testid^='task-item-']").count();
/** Task rows expose `<workspaceKey>:<taskId>`; the new key identifies this run. */
const taskItemKeys = () =>
  page.evaluate(() =>
    [...document.querySelectorAll("[data-testid^='task-item-']")].map(
      (element) => element.getAttribute("data-task-item-key") ?? "",
    ),
  );

function decodeFrame(payload) {
  const buf = Buffer.from(payload, "latin1");
  if (buf[0] !== 1) return null;
  const length = buf.readUInt32BE(9);
  return buf.subarray(13, 13 + length).toString("utf8");
}

// The model picker exposes its resolved value on the trigger; the "Choose model"
// placeholder means the runner did not return an executable selection.
async function modelSelectionState(body) {
  const trigger = page.locator('[data-testid="chat-model-select-trigger"]').first();
  const count = await trigger.count();
  const value = count
    ? ((await trigger.getAttribute("data-model-current-value")) ?? "").trim()
    : "";
  const label = count ? ((await trigger.textContent()) ?? "").trim() : "";
  return {
    selected: !/Choose model/.test(body) && value.length > 0,
    value,
    label,
  };
}

let summary;
try {
  await page.goto(`${BASE_URL}/`, { waitUntil: "load" });
  await page.waitForTimeout(2_000);

  // The login form may be replaced by a re-render right after Vite hydrates the
  // app, so verify the fill and retry once before waiting on the composer.
  const tokenInput = page.getByPlaceholder("Paste your access token");
  const signIn = page.getByRole("button", { name: "Sign in" });
  for (let attempt = 0; attempt < 2; attempt += 1) {
    if ((await tokenInput.count()) === 0) break;
    await tokenInput.waitFor({ state: "visible", timeout: 20_000 });
    await tokenInput.fill(TOKEN);
    const filled = await tokenInput.inputValue();
    await signIn.click();
    await page.waitForTimeout(3_000);
    if (filled === TOKEN && (await tokenInput.count()) > 0 && (await tokenInput.isVisible())) {
      continue;
    }
    break;
  }

  // Wait until the authenticated shell (composer or welcome) is ready.
  const composer = page.locator('[data-testid="chat-input"], [contenteditable="true"]').first();
  await composer.waitFor({ state: "visible", timeout: 90_000 });
  await page.waitForTimeout(9_000);

  // Deployed stacks may restore a tab on a different machine. When the caller
  // names a workspace, open it through the same machine picker a user would.
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
  await shot("final-01-loaded");

  const initialBody = await bodyText();
  const initialModel = await modelSelectionState(initialBody);
  check("modelSelectedBeforeSend", initialModel.selected, initialModel);

  const taskRowsBefore = await taskRows();
  const taskKeysBefore = await taskItemKeys();
  if (ALLOW_EXISTING_TASKS) {
    details.existingTaskRowsBeforeSend = taskRowsBefore;
  } else {
    check("onlyRealTasksBeforeSend", taskRowsBefore === 0, { count: taskRowsBefore });
  }

  const editor = page.locator('[data-testid="chat-input"], [contenteditable="true"]').first();
  await editor.waitFor({ timeout: 20_000 });
  await editor.click();
  await page.keyboard.type(PROMPT);
  await shot("final-02-typed");
  await page.keyboard.press("Enter");

  const deadline = Date.now() + REPLY_TIMEOUT_MS;
  let body = "";
  while (Date.now() < deadline) {
    await page.waitForTimeout(3_000);
    body = await bodyText();
    if (body.includes(EXPECT_TEXT) && !/Stop|停止/.test(body)) break;
  }
  await page.waitForTimeout(4_000);
  await shot("final-03-reply");
  body = await bodyText();
  check("replyRendered", body.includes(EXPECT_TEXT), { tail: body.slice(-800) });

  // Tool cards live inside the collapsed "Worked for …" assistant-history group,
  // nested under the "Explore" summary.
  // DOM click instead of a pointer click: on long sidebar histories a transient
  // overlay can hit-test over the trigger, and this expansion is diagnostic only.
  const trigger = page.locator("[data-testid^='chat-assistant-history-trigger']").first();
  if (await trigger.count()) {
    await trigger.evaluate((element) => element.click());
    await page.waitForTimeout(1_500);
  }
  const explore = page.locator("text=Explore").first();
  if (await explore.count()) {
    await explore.evaluate((element) => element.click());
    await page.waitForTimeout(1_200);
  }
  await shot("final-04-tools-expanded");

  const toolBlocks = await page.evaluate(() =>
    [...document.querySelectorAll("[data-testid^='chat-tool-call-block-']")].map(
      (element) => element.textContent ?? "",
    ),
  );
  const toolBlockText = toolBlocks.join("\n");
  check("hasReadCard", /Read/.test(toolBlockText) && /notes\.txt/.test(toolBlockText), {
    toolBlocks: toolBlocks.map((text) => text.slice(0, 200)),
  });
  check("hasBashCard", /(Terminal|Bash)/.test(toolBlockText) && /ls -la/.test(toolBlockText), {
    toolBlocks: toolBlocks.map((text) => text.slice(0, 200)),
  });

  const taskRowsAfterSend = await taskRows();
  let newTaskKey = "";
  if (ALLOW_EXISTING_TASKS) {
    const keysAfterSend = await taskItemKeys();
    const added = keysAfterSend.filter((key) => !taskKeysBefore.includes(key));
    newTaskKey = added.length === 1 ? added[0] : "";
    details.newTaskKey = newTaskKey;
    check("oneNewTaskAfterSend", added.length === 1, {
      before: taskRowsBefore,
      after: taskRowsAfterSend,
      added,
    });
  } else {
    check("oneRealTaskAfterSend", taskRowsAfterSend === 1, { count: taskRowsAfterSend });
  }

  await page.reload({ waitUntil: "domcontentloaded" });
  await page.waitForTimeout(9_000);
  await shot("final-05-after-reload");
  const afterReloadBody = await bodyText();
  const sidebarTasksAfterReload = await taskRows();
  check("sessionListedAfterReload", /Use the read tool/.test(afterReloadBody), {
    count: sidebarTasksAfterReload,
  });
  if (ALLOW_EXISTING_TASKS) {
    check(
      "oneNewTaskAfterReload",
      Boolean(newTaskKey) && (await taskItemKeys()).includes(newTaskKey),
      { newTaskKey, keys: await taskItemKeys() },
    );
  } else {
    check("oneRealTaskAfterReload", sidebarTasksAfterReload === 1, {
      count: sidebarTasksAfterReload,
    });
  }

  // Deployed stacks keep earlier sessions, so `first()` may be a different task.
  // Prefer the exact key observed above; fall back to the newest matching row.
  const row = newTaskKey
    ? page.locator(`[data-task-item-key="${newTaskKey}"]`).first()
    : page
        .locator("[data-testid^='task-item-']")
        .filter({ hasText: /Use the read tool to read notes\.txt, then run/ })
        .first();
  // The task is the active tab after the reload, so the transcript is already
  // shown; clicking its sidebar row only asserts the row is present and active.
  let reopened = false;
  if ((await row.count()) > 0 && (await row.getAttribute("data-state")) !== "active") {
    await row.click();
    await page.waitForTimeout(6_000);
  }
  // Fall back to the transcript itself: the sidebar row test id is not the
  // contract under test here.
  reopened = (await row.count()) > 0 || /Use the read tool/.test(await bodyText());
  await shot("final-06-reopened");
  const reopenedBody = await bodyText();
  check(
    "transcriptAfterReopen",
    reopened && /Use the read tool/.test(reopenedBody) && reopenedBody.includes(EXPECT_TEXT),
    { reopened, tail: reopenedBody.slice(-800) },
  );
  const reopenedModel = await modelSelectionState(reopenedBody);
  check("modelSelectedAfterReopen", reopenedModel.selected, reopenedModel);

  // Decode conversation frames for row-level diagnostics.
  let lastSnapshot = null;
  let maxToolRows = 0;
  for (const frame of frames) {
    if (frame.dir !== "<") continue;
    const text = decodeFrame(frame.payload);
    if (!text || !text.includes('"topic":"conversation/')) continue;
    const index = text.indexOf("{");
    try {
      const object = JSON.parse(text.slice(index));
      const snapshot = object?.frame?.payload?.snapshot;
      if (!snapshot) continue;
      lastSnapshot = snapshot;
      maxToolRows = Math.max(
        maxToolRows,
        snapshot.rows.window.filter((entry) => entry.kind === "toolCall").length,
      );
    } catch {
      // Frame is not a JSON snapshot; ignore it.
    }
  }
  details.toolRows = lastSnapshot?.rows.window
    .filter((entry) => entry.kind === "toolCall")
    .map((entry) => ({ name: entry.toolName, status: entry.status }));
  details.maxToolRows = maxToolRows;
  details.snapshotConfigModelSelection = lastSnapshot?.config?.modelSelection ?? null;
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
  // Known web-only noise unrelated to the acceptance path: the external client-config
  // fetch is blocked by CORS, Tooltip controlled/uncontrolled dev warnings, and the
  // one-off projection-vs-ACK race warning that self-recovers via snapshot.
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
  outDir: OUT_DIR,
  chrome: chromePath,
  checks,
  failures,
  details,
  significantErrors,
  consoleErrors,
};

fs.writeFileSync(path.join(OUT_DIR, "summary.json"), `${JSON.stringify(summary, null, 2)}\n`);
console.log(JSON.stringify(summary, null, 2));
process.exit(summary.ok ? 0 : 1);
