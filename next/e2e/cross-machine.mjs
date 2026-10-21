#!/usr/bin/env node
// Cross-machine E2E: one Go server, web UI served by that server, two runners on
// different machines with **different** workspaces (Linux + macOS).
//
// The difference from multi-runner.mjs: that one proves the same path on two
// machines routes by identity. This one proves the deployed topology works end
// to end across a network:
//
//   1. login against the production web build served by the Go server;
//   2. the picker lists both machines with their OS badges;
//   3. open the Linux workspace, the model comes from the Linux runner's pi
//      config, and the read tool returns the Linux file content;
//   4. same for the macOS workspace (different content, different machine);
//   5. every task row stays under its own machine;
//   6. reload restores both tabs on the right machines;
//   7. resilience: stop the Linux runner → the UI must not crash and calls for
//      that workspace must fail gracefully; start it again → the session list
//      must come back without a page reload (server re-issues listens).
//
// Environment:
//   NEX_E2E_BASE_URL        Web origin to drive        (default http://localhost:5174)
//   NEX_E2E_TOKEN           Browser login token        (required)
//   NEX_E2E_CHROME          Chrome/Chromium executable (default: auto-detect)
//   NEX_E2E_OUT             Screenshot + summary dir   (default <repo>/next/e2e/out)
//   NEX_E2E_LINUX_NAME      Linux runner name          (default remotebuilder)
//   NEX_E2E_MAC_NAME        macOS runner name          (default mac)
//   NEX_E2E_LINUX_TEXT      notes.txt content on Linux (default "hello from linux")
//   NEX_E2E_MAC_TEXT        notes.txt content on macOS (default "hello from mac")
//   NEX_E2E_STOP_LINUX_CMD  shell command that stops the Linux runner (optional)
//   NEX_E2E_START_LINUX_CMD shell command that starts it again (optional)
//   NEX_E2E_REPLY_TIMEOUT_MS per-reply timeout          (default 180000)
//   NEX_E2E_RECOVERY_TIMEOUT_MS recovery wait           (default 60000)
//
// Resilience checks run only when both stop/start commands are provided; without
// them the summary says so instead of pretending they passed.
//
// Exit code 0 only when every check passes. A JSON summary is always printed.

import { execSync } from "node:child_process";
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
const LINUX_NAME = process.env.NEX_E2E_LINUX_NAME?.trim() || "remotebuilder";
const MAC_NAME = process.env.NEX_E2E_MAC_NAME?.trim() || "mac";
const LINUX_TEXT = process.env.NEX_E2E_LINUX_TEXT?.trim() || "hello from linux";
const MAC_TEXT = process.env.NEX_E2E_MAC_TEXT?.trim() || "hello from mac";
const STOP_LINUX_CMD = process.env.NEX_E2E_STOP_LINUX_CMD?.trim() ?? "";
const START_LINUX_CMD = process.env.NEX_E2E_START_LINUX_CMD?.trim() ?? "";
const REPLY_TIMEOUT_MS = Number(process.env.NEX_E2E_REPLY_TIMEOUT_MS) || 180_000;
const RECOVERY_TIMEOUT_MS = Number(process.env.NEX_E2E_RECOVERY_TIMEOUT_MS) || 60_000;
const PROMPT = "Use the read tool to read notes.txt and reply with its exact content.";
const CAN_TEST_RESILIENCE = STOP_LINUX_CMD.length > 0 && START_LINUX_CMD.length > 0;

function failConfig(message) {
  console.error(`[cross-machine] ${message}`);
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
const shadowText = () =>
  page.evaluate(() => {
    const parts = [];
    const visit = (root) => {
      for (const element of root.querySelectorAll("*")) {
        if (!element.shadowRoot) continue;
        parts.push(element.shadowRoot.textContent ?? "");
        visit(element.shadowRoot);
      }
    };
    visit(document);
    return parts.join("\n");
  });
const composer = () =>
  page
    .locator(
      '[data-testid="v4-composer-input"], [data-testid="chat-input"], [contenteditable="true"]',
    )
    .first();

/** Polls until the exact file content is visible in the DOM or a shadow root. */
function waitForFileContent(text, timeoutMs = REPLY_TIMEOUT_MS) {
  return (async () => {
    const deadline = Date.now() + timeoutMs;
    let combined = "";
    while (Date.now() < deadline) {
      await page.waitForTimeout(1_500);
      combined = `${await bodyText()}\n${await shadowText()}`;
      if (combined.includes(text) && !/Stop|停止/.test(combined)) return combined;
    }
    return combined;
  })();
}

async function taskItemKeys() {
  return page.evaluate(() =>
    [...document.querySelectorAll("[data-testid^='task-item-']")].map(
      (element) => element.getAttribute("data-task-item-key") ?? "",
    ),
  );
}

async function workspaceIdentities() {
  return page.evaluate(() =>
    [...document.querySelectorAll("[data-workspace-identity]")].map((element) =>
      element.getAttribute("data-workspace-identity"),
    ),
  );
}

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
  await page.waitForTimeout(1_500);
}

/** Picker rows with their machine section text (name + OS badge). */
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
  await page.waitForTimeout(3_000);
}

async function modelSelectionState() {
  const trigger = page.locator('[data-testid="chat-model-select-trigger"]').first();
  const count = await trigger.count();
  const value = count
    ? ((await trigger.getAttribute("data-model-current-value")) ?? "").trim()
    : "";
  const label = count ? ((await trigger.textContent()) ?? "").trim() : "";
  return { selected: value.length > 0 && !/Choose model/.test(label), value, label };
}

/**
 * The workspace row is a Collapsible trigger: clicking it toggles the task group,
 * so clicking an already-expanded row *hides* its task rows. Expand only when the
 * row reports `aria-expanded="false"`.
 */
async function ensureWorkspaceExpanded(path) {
  const item = page.locator(`[data-testid="workspace-item-${path}"]`).first();
  await item.waitFor({ state: "visible", timeout: 20_000 });
  const expanded = (await item.getAttribute("aria-expanded")) === "true";
  if (!expanded) {
    await item.click();
    await page.waitForTimeout(2_500);
  }
  return expanded;
}

/**
 * Activate the sidebar tab for one machine by clicking one of its task rows.
 * Task rows navigate without toggling the group (unlike the workspace row).
 */
async function activateWorkspaceByTask(identity) {
  const row = page.locator(`[data-task-item-key^="${identity}:"]`).first();
  await row.waitFor({ state: "visible", timeout: 30_000 });
  await row.click();
  await page.waitForTimeout(5_000);
}

async function sendPrompt(text) {
  const editor = composer();
  await editor.waitFor({ state: "visible", timeout: 30_000 });
  await editor.click();
  await page.keyboard.press("ControlOrMeta+a");
  await page.keyboard.press("Backspace");
  await page.keyboard.type(text);
  await page.keyboard.press("Enter");
}

function runShell(command) {
  // Generic escape hatch for the resilience section: the caller decides how the
  // runner is supervised (systemd unit, pid file, container, …).
  return execSync(command, { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
}

let summary;
// Tracks the STOP/START pair so a failure in between cannot leave the runner down.
let runnerStopped = false;
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
  await composer().waitFor({ state: "visible", timeout: 90_000 });
  await page.waitForTimeout(8_000);
  await shot("cross-00-loaded");
  check("loginSucceeded", (await composer().count()) === 1);

  // Authoritative mapping from the same endpoint the picker uses.
  const serverInfo = await page.evaluate(async () => {
    const response = await fetch("/api/server-info", { cache: "no-store" });
    return response.ok ? await response.json() : null;
  });
  const workspaces = Array.isArray(serverInfo?.workspaces) ? serverInfo.workspaces : [];
  const linuxWs = workspaces.find((entry) => entry.runnerName === LINUX_NAME);
  const macWs = workspaces.find((entry) => entry.runnerName === MAC_NAME);
  details.serverWorkspaces = workspaces.map((entry) => ({
    path: entry.path,
    label: entry.label,
    runnerName: entry.runnerName,
    runnerOs: entry.runnerOs,
    workspaceIdentity: entry.workspaceIdentity,
  }));
  check("serverInfoListsLinuxRunner", Boolean(linuxWs), { LINUX_NAME });
  check("serverInfoListsMacRunner", Boolean(macWs), { MAC_NAME });
  if (!linuxWs || !macWs) {
    throw new Error(
      `both runners must be connected; saw ${JSON.stringify(details.serverWorkspaces)}`,
    );
  }
  const linuxPath = linuxWs.path;
  const macPath = macWs.path;
  const linuxIdentity = linuxWs.workspaceIdentity ?? "";
  const macIdentity = macWs.workspaceIdentity ?? "";
  details.identities = { linux: linuxIdentity, mac: macIdentity };
  check("linuxRunnerReportsLinuxOs", /linux/i.test(linuxWs.runnerOs ?? ""), linuxWs.runnerOs);
  check("macRunnerReportsDarwinOs", /darwin|mac/i.test(macWs.runnerOs ?? ""), macWs.runnerOs);
  check("workspacesAreOnDifferentPaths", linuxPath !== macPath, { linuxPath, macPath });

  // ── 1. picker lists both machines with OS badges ──
  await openPicker();
  const options = await pickerOptions();
  details.pickerOptions = options;
  const linuxOption = options.find((option) => option.path === linuxPath);
  const macOption = options.find((option) => option.path === macPath);
  check("pickerListsLinuxWorkspace", Boolean(linuxOption), { linuxPath });
  check("pickerListsMacWorkspace", Boolean(macOption), { macPath });
  check("pickerShowsLinuxOsBadge", /linux/i.test(linuxOption?.section ?? ""), {
    section: linuxOption?.section,
  });
  check("pickerShowsMacOsBadge", /macos|darwin/i.test(macOption?.section ?? ""), {
    section: macOption?.section,
  });
  check("pickerGroupsByMachine", (linuxOption?.section ?? "") !== (macOption?.section ?? ""));
  await shot("cross-01-picker");

  // ── 2. Linux workspace: model, read tool, exact content ──
  await selectPickerOption((option) => option.path === linuxPath, "linux workspace");
  await composer().waitFor({ state: "visible", timeout: 30_000 });
  await page.waitForTimeout(5_000);
  const linuxModel = await modelSelectionState();
  details.linuxModel = linuxModel;
  check("modelSelectedOnLinux", linuxModel.selected, linuxModel);
  check("linuxTabOpen", (await workspaceIdentities()).includes(linuxIdentity), {
    identities: await workspaceIdentities(),
  });
  await shot("cross-02-linux-workspace");

  await sendPrompt(PROMPT);
  const linuxCombined = await waitForFileContent(LINUX_TEXT);
  await page.waitForTimeout(3_000);
  await shot("cross-03-linux-reply");
  check("linuxReplyContainsFileContent", linuxCombined.includes(LINUX_TEXT), {
    tail: linuxCombined.slice(-700),
  });

  // ── 3. macOS workspace: same prompt, different machine and content ──
  await openPicker();
  await selectPickerOption((option) => option.path === macPath, "mac workspace");
  await composer().waitFor({ state: "visible", timeout: 30_000 });
  await page.waitForTimeout(5_000);
  const macModel = await modelSelectionState();
  details.macModel = macModel;
  check("modelSelectedOnMac", macModel.selected, macModel);
  check("macTabOpen", (await workspaceIdentities()).includes(macIdentity), {
    identities: await workspaceIdentities(),
  });
  await shot("cross-04-mac-workspace");

  await sendPrompt(PROMPT);
  const macCombined = await waitForFileContent(MAC_TEXT);
  await page.waitForTimeout(3_000);
  await shot("cross-05-mac-reply");
  check("macReplyContainsFileContent", macCombined.includes(MAC_TEXT), {
    tail: macCombined.slice(-700),
  });

  // ── 4. each task row stays under its own machine ──
  const keys = await taskItemKeys();
  details.taskKeys = keys;
  const linuxKeys = keys.filter((key) => key.startsWith(`${linuxIdentity}:`));
  const macKeys = keys.filter((key) => key.startsWith(`${macIdentity}:`));
  // The deployment keeps sessions between runs, so counts can exceed one; what
  // matters is that every row is attributed to its own machine and no row claims
  // a path that this machine does not serve.
  const unattributed = keys.filter(
    (key) => !key.startsWith(`${linuxIdentity}:`) && !key.startsWith(`${macIdentity}:`),
  );
  check("taskRowsUnderLinuxWorkspace", linuxKeys.length >= 1, {
    linuxKeys,
    count: linuxKeys.length,
  });
  check("taskRowsUnderMacWorkspace", macKeys.length >= 1, { macKeys, count: macKeys.length });
  check("noTaskRowOutsideItsMachine", unattributed.length === 0, { keys, unattributed });

  // ── 5. reload restores both tabs on the right machines ──
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.waitForTimeout(11_000);
  await shot("cross-06-after-reload");
  const reloadedIdentities = await workspaceIdentities();
  // Collapsed workspace groups do not render their task rows, and a reload starts
  // with every group collapsed. Expand both machines before counting.
  await ensureWorkspaceExpanded(linuxPath);
  await ensureWorkspaceExpanded(macPath);
  const reloadedKeys = await taskItemKeys();
  details.afterReload = { identities: reloadedIdentities, keys: reloadedKeys };
  check(
    "reloadKeepsBothMachines",
    reloadedIdentities.includes(linuxIdentity) && reloadedIdentities.includes(macIdentity),
    { identities: reloadedIdentities },
  );
  check(
    "reloadKeepsBothSessions",
    reloadedKeys.some((key) => key.startsWith(`${linuxIdentity}:`)) &&
      reloadedKeys.some((key) => key.startsWith(`${macIdentity}:`)),
    { keys: reloadedKeys },
  );

  // ── 6. resilience: stop the Linux runner, then bring it back ──
  if (!CAN_TEST_RESILIENCE) {
    details.resilience = "skipped: NEX_E2E_STOP_LINUX_CMD/NEX_E2E_START_LINUX_CMD not set";
    console.log(`[cross-machine] ${details.resilience}`);
  } else {
    await ensureWorkspaceExpanded(linuxPath);
    const rowsBeforeStop = await taskItemKeys();
    check(
      "linuxTaskVisibleBeforeStop",
      rowsBeforeStop.some((key) => key.startsWith(`${linuxIdentity}:`)),
      { keys: rowsBeforeStop },
    );
    await activateWorkspaceByTask(linuxIdentity);

    details.stopOutput = runShell(STOP_LINUX_CMD);
    runnerStopped = true;
    // Wait until the server has noticed the disconnect.
    const offlineDeadline = Date.now() + 30_000;
    let offline = false;
    while (Date.now() < offlineDeadline) {
      const info = await page.evaluate(async () => {
        const response = await fetch("/api/server-info", { cache: "no-store" });
        return response.ok ? await response.json() : null;
      });
      offline = !(info?.workspaces ?? []).some((entry) => entry.runnerName === LINUX_NAME);
      if (offline) break;
      await page.waitForTimeout(1_000);
    }
    check("serverDroppedStoppedRunner", offline);

    // Force calls for the offline workspace: switch to the other tab and back so
    // the composer re-requests the model catalog for the stopped runner. The UI
    // must surface an error, not crash.
    await ensureWorkspaceExpanded(macPath);
    await activateWorkspaceByTask(macIdentity);
    await ensureWorkspaceExpanded(linuxPath);
    await activateWorkspaceByTask(linuxIdentity);
    await page.waitForTimeout(4_000);
    const offlineBody = await bodyText();
    details.offlineTail = offlineBody.slice(-900);
    // Observed UI signal while a runner is offline: the model picker renders
    // "Models failed to load. Retry" (workspace calls reject with "runner offline").
    const hasGracefulError =
      /Models failed to load|Retry|offline|unavailable|not connected|无法|离线|失败/i.test(
        offlineBody,
      );
    check("offlineWorkspaceShowsError", hasGracefulError, { tail: details.offlineTail });
    const crashAfterStop = logs.filter((line) =>
      /PAGEERROR|React subtree crashed|change in the order of Hooks/u.test(line),
    );
    check("noCrashWhileRunnerOffline", crashAfterStop.length === 0, {
      crashes: crashAfterStop.slice(0, 5),
    });
    await shot("cross-07-linux-offline");

    // Bring it back and wait for the sidebar to recover **without** a reload.
    details.startOutput = runShell(START_LINUX_CMD);
    runnerStopped = false;
    const recoveryDeadline = Date.now() + RECOVERY_TIMEOUT_MS;
    let recovered = false;
    let recoveryBody = "";
    while (Date.now() < recoveryDeadline) {
      await page.waitForTimeout(2_000);
      const keysNow = await taskItemKeys();
      recoveryBody = await bodyText();
      if (keysNow.some((key) => key.startsWith(`${linuxIdentity}:`))) {
        recovered = true;
        break;
      }
    }
    details.recoveryTail = recoveryBody.slice(-900);
    check("linuxSessionListRecoversWithoutReload", recovered, {
      tail: details.recoveryTail,
    });
    await page.waitForTimeout(4_000);
    await shot("cross-08-linux-recovered");

    // Both groups must be expanded before counting: only expanded groups render
    // their task rows.
    await ensureWorkspaceExpanded(macPath);
    await ensureWorkspaceExpanded(linuxPath);
    const recoveredKeys = await taskItemKeys();
    details.recoveredKeys = recoveredKeys;
    check(
      "macSessionSurvivesLinuxRestart",
      recoveredKeys.some((key) => key.startsWith(`${macIdentity}:`)),
      { keys: recoveredKeys },
    );
    check(
      "linuxTaskStillUnderLinuxAfterRecovery",
      recoveredKeys.some((key) => key.startsWith(`${linuxIdentity}:`)),
      { keys: recoveredKeys },
    );
  }
} catch (error) {
  failures.push("exception");
  details.exception = error instanceof Error ? (error.stack ?? error.message) : String(error);
} finally {
  try {
    await browser.close();
  } catch {
    // Browser teardown failure must not hide the check result.
  }
  // A failure between STOP and START would leave the Linux runner down for the
  // next run (and for the user). Restore it unconditionally; the start script is
  // idempotent.
  if (CAN_TEST_RESILIENCE && runnerStopped) {
    try {
      runShell(START_LINUX_CMD);
      details.resilienceCleanup = "restarted the Linux runner";
    } catch (error) {
      details.resilienceCleanup = `failed to restart the Linux runner: ${String(error)}`;
      failures.push("resilienceCleanup");
    }
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
  linux: { name: LINUX_NAME, text: LINUX_TEXT },
  mac: { name: MAC_NAME, text: MAC_TEXT },
  outDir: OUT_DIR,
  chrome: chromePath,
  resilienceTested: CAN_TEST_RESILIENCE,
  checks,
  failures,
  details,
  significantErrors,
  consoleErrors,
};

fs.writeFileSync(
  path.join(OUT_DIR, "cross-machine-summary.json"),
  `${JSON.stringify(summary, null, 2)}\n`,
);
console.log(JSON.stringify(summary, null, 2));
process.exit(summary.ok ? 0 : 1);
