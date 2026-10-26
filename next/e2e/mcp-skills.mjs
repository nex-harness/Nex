#!/usr/bin/env node
// MCP + skills E2E for the next/ stack (docs/next/config-mcp-skills.md P1/P2):
//
//   1. Settings → MCP servers → add the repo's stdio echo server (type + command +
//      args, saved through `mcp-sync.saveMcpToUserDirectory` into the server doc)
//      → its row shows "connected" with one tool (the runner's live status).
//   2. Chat: the scripted provider receives an instruction naming the echo tool and
//      calls it → the tool card shows the tool's answer.
//   3. A skill uploaded through `POST /api/config/skills` (there is no upload button
//      in the unchanged Skills page) appears in Settings → Skills, and in the chat
//      `/` menu; sending `/skill:<name> …` reaches the model as pi's expanded skill
//      block — asserted from the deterministic faux provider's request log.
//
// The fake provider is packages/next-runner/test/fixtures/e2e-provider/server.mjs
// started by next/dev.sh; its `/prompts` API returns the prompts it received.
//
// Environment:
//   NEX_E2E_BASE_URL       Web origin to drive         (default http://localhost:5174)
//   NEX_E2E_TOKEN          Browser login token         (required)
//   NEX_E2E_CHROME         Chrome/Chromium executable  (default: auto-detect)
//   NEX_E2E_OUT            Screenshot + summary dir    (default <repo>/next/e2e/out)
//   NEX_E2E_WORKSPACE      Runner workspace to open    (default: restored tab)
//   NEX_E2E_FAUX_URL       Faux provider origin        (default http://127.0.0.1:18080)
//   NEX_E2E_MODEL_TIMEOUT_MS / NEX_E2E_REPLY_TIMEOUT_MS
//
// Exit code 0 only when every check passes. A JSON summary is always printed.

import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";
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
const FAUX_URL = (process.env.NEX_E2E_FAUX_URL?.trim() || "http://127.0.0.1:18080").replace(
  /\/+$/u,
  "",
);
const MODEL_TIMEOUT_MS = Number(process.env.NEX_E2E_MODEL_TIMEOUT_MS) || 120_000;
const REPLY_TIMEOUT_MS = Number(process.env.NEX_E2E_REPLY_TIMEOUT_MS) || 180_000;

const MCP_NAME = "echo-server";
const MCP_PREFIX = "mcp-e2e-"; // proves the tool answer came from the fixture process
const ECHO_TEXT = "pong from e2e";
/**
 * The echo server advertises a resource: pi then reaches the server through the
 * standard `read_mcp_resource` tool, the only MCP path a model has for a server
 * whose tools are not declared natively (same as the multi-runner E2E).
 */
const ECHO_RESOURCE_URI = "echo://e2e-note";
const SKILL_NAME = "echo-helper";
const SKILL_DESCRIPTION = "Explains how to use the echo tool.";
const PROMPT_TEXT = "greet the user";

function failConfig(message) {
  console.error(`[mcp-skills] ${message}`);
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
  failConfig(
    "No Chrome/Chromium executable found. Install Google Chrome or Chromium, or set NEX_E2E_CHROME.",
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

/** Server-owned browser state lives in `<server data>/settings.json`; each run starts clean. */
const SERVER_SETTINGS = process.env.NEX_E2E_SERVER_DATA
  ? path.join(process.env.NEX_E2E_SERVER_DATA, "settings.json")
  : "";

// ── fixtures ──

const NODE = process.execPath;

const MCP_SERVER = path.join(
  REPO_ROOT,
  "packages",
  "next-runner",
  "test",
  "fixtures",
  "mcp-echo",
  "server.mjs",
);
if (!fs.existsSync(MCP_SERVER)) failConfig(`missing fixture ${MCP_SERVER}`);

/** `/prompts` of the scripted provider: every prompt the model received. */
async function fauxPrompts() {
  try {
    const response = await fetch(`${FAUX_URL}/prompts`);
    const body = await response.json();
    return Array.isArray(body.prompts) ? body.prompts : [];
  } catch {
    return [];
  }
}

/** Server-owned config check (NEX_SERVER_CONFIG_DIR): the doc, never a plaintext key. */
function serverDoc() {
  const configDir = process.env.NEX_E2E_SERVER_CONFIG?.trim();
  if (!configDir) return null;
  const configPath = path.join(configDir, "config.json");
  if (!fs.existsSync(configPath)) return null;
  try {
    return JSON.parse(fs.readFileSync(configPath, "utf8"));
  } catch {
    return null;
  }
}

// ── browser harness ──

const checks = {};
const failures = [];
const details = {};
const consoleErrors = [];

function check(name, ok, detail) {
  checks[name] = Boolean(ok);
  if (detail !== undefined) details[name] = detail;
  if (!ok) failures.push(name);
}

// A restored tab would reopen the previous run's chat (a session opened before
// this run's MCP push). The suite starts from the server's empty state instead.
if (SERVER_SETTINGS && fs.existsSync(SERVER_SETTINGS)) fs.rmSync(SERVER_SETTINGS, { force: true });
const browser = await chromium.launch({ executablePath: chromePath, headless: true });
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
page.on("pageerror", (error) => consoleErrors.push(`PAGEERROR: ${error.message}`));
page.on("console", (message) => {
  if (message.type() !== "error" && message.type() !== "warning") return;
  consoleErrors.push(`CONSOLE[${message.type()}]: ${message.text()}`.slice(0, 300));
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

async function waitFor(predicate, timeoutMs, pollMs = 500) {
  const deadline = Date.now() + timeoutMs;
  let last;

  while (Date.now() < deadline) {
    last = await predicate();
    if (last) return last;
    await page.waitForTimeout(pollMs);
  }
  return last;
}

/**
 * Picks the scripted provider's model in the composer's model picker (the same
 * gestures a user makes: open the trigger, hover the provider row, click the
 * model radio). A composer without a chosen model does not send.
 */
async function selectComposerModel() {
  const trigger = page.locator('[data-testid="chat-model-select-trigger"]').first();
  await trigger.waitFor({ state: "visible", timeout: 30_000 });
  const label = (await trigger.textContent()) ?? "";
  if (/faux/iu.test(label)) return;
  await trigger.click();
  await page.waitForTimeout(600);
  const group = page.locator("[data-testid^='chat-model-select-group-']").first();
  if ((await group.count()) > 0) {
    await group.hover();
    await page.waitForTimeout(400);
  }
  const item = page.getByRole("menuitemradio").filter({ hasText: /faux/iu }).first();
  if ((await item.count()) > 0) await item.click();
  else await page.keyboard.press("Escape");
  await page.waitForTimeout(600);
}

/**
 * Full access (yolo): MCP tools run without an approval card, as in the
 * permissions suite's auto-approval mode; the turn's tool call and answer land in
 * the transcript without the E2E having to press Allow.
 */
async function setFullAccessMode() {
  const trigger = page.locator('[data-testid="chat-mode-select-trigger"]').first();
  await trigger.waitFor({ state: "visible", timeout: 30_000 });
  if ((await trigger.textContent())?.includes("Full")) return;
  await trigger.click();
  const item = page.locator('[data-testid="chat-mode-select-item-yolo"]').first();
  await item.waitFor({ state: "visible", timeout: 15_000 });
  await item.click();
  await page.waitForTimeout(800);
}

/** The v4 chat composer's editable input. */
const composerInput = () =>
  page.locator('[data-testid="v4-composer-input"], [data-testid="chat-input"]').first();

/**
 * What covers the page's centre right now: the element at that point, the
 * `<html>` classes (a Radix modal sets `pointer-events: none` on `body` and
 * `aria-hidden` on the app), and the dialogs/portals on screen.
 */
async function overlayProbe() {
  try {
    return await page.evaluate(() => {
      const at = document.elementFromPoint(window.innerWidth / 2, window.innerHeight / 2);
      const describe = (node) =>
        node
          ? `${node.tagName.toLowerCase()}#${node.id ?? ""}.${String(node.className ?? "").slice(0, 60)}[${node.getAttribute("role") ?? node.getAttribute("data-testid") ?? ""}]`
          : null;
      return {
        center: describe(at),
        bodyPointerEvents: getComputedStyle(document.body).pointerEvents,
        dialogs: [...document.querySelectorAll("[role='dialog'], dialog[open]")].map((d) => ({
          role: d.getAttribute("role"),
          state: d.getAttribute("data-state"),
          hidden: d.getAttribute("aria-hidden"),
          inert: d.hasAttribute("inert"),
          text: (d.textContent ?? "").replace(/\s+/gu, " ").slice(0, 160),
        })),
        openMenus: [
          ...document.querySelectorAll("[role='menu'], [role='listbox'], [data-state='open']"),
        ].length,
        toasts: document.querySelector("#nex-toast-host")?.textContent?.slice(0, 160) ?? null,
        htmlPointerEvents: getComputedStyle(document.documentElement).pointerEvents,
        modal: document.querySelectorAll(
          "[data-radix-portal], [data-state='open'][role='dialog'], dialog[open]",
        ).length,
        title: document.querySelector("[role='dialog'] h2, dialog h2")?.textContent ?? null,
        text: (document.querySelector("[role='dialog'], dialog[open]")?.textContent ?? "").slice(
          0,
          200,
        ),
      };
    });
  } catch {
    return null;
  }
}

/**
 * Clicks an element that a body-level overlay may cover (the notifications toast,
 * the runner picker, an onboarding dialog). Nothing here forces a click through
 * `<html>`: it dismisses what is on top, waits for it to go, and only then clicks
 * the real element (other suites treat these overlays as "wait it out", too).
 */
/** True when a modal layer (dialog, Radix portal) is on screen. */
async function overlayOpen() {
  return page.evaluate(
    () =>
      document.querySelectorAll(
        "dialog[open], [role='dialog'][data-state='open'], [data-radix-portal]",
      ).length > 0,
  );
}

async function clickWhenFree(locator, label) {
  const target = locator.first();
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    try {
      await target.click({ timeout: 2_500 });
      // A click can land on an "Allow" that a permission toast carries; the
      // caller's own approval step waits for the transcript instead.
      return;
    } catch (error) {
      if (Date.now() >= deadline) throw error;
    }
    await dismissOverlays();
    await clearToasts();
    if (Date.now() >= deadline) {
      // Report what still covers the target instead of a bare timeout.
      const probe = await overlayProbe();
      throw new Error(`${label}: pointer events intercepted; overlay=${JSON.stringify(probe)}`);
    }
  }
}

/** Closes overlays left by workspace opening (the picker is portalled and
 *  keeps covering the toolbar otherwise, exactly as in the other suites). */
async function dismissOverlays() {
  const picker = page.locator('[data-testid="runner-workspace-picker"]');
  if ((await picker.count()) > 0) {
    await page.keyboard.press("Escape").catch(() => undefined);
    await picker.waitFor({ state: "hidden", timeout: 10_000 }).catch(() => undefined);
  }
  // A dialog left open (onboarding, a confirm) or a transitive popover (the model
  // picker's submenus) can keep the page uninteractive; dismiss it as a user does.
  if (await overlayOpen()) {
    await page.keyboard.press("Escape").catch(() => undefined);
    await page.waitForTimeout(300);
  }
  // A transitive popover (the model picker's submenus) can leave the body
  // uninteractive after it closes; dismiss it the way a user does.
  if ((await page.evaluate(() => getComputedStyle(document.body).pointerEvents)) === "none") {
    const open = page.locator("[data-state='open']").first();
    if ((await open.count()) > 0) {
      await page.keyboard.press("Escape").catch(() => undefined);
      await page.mouse.click(5, 5);
      await page.waitForTimeout(500);
    }
  }
}

/** The toast stack can cover the composer; dismiss it rather than clicking through. */
async function clearToasts() {
  const host = page.locator("#nex-toast-host");
  if ((await host.count()) === 0) return;
  // The notification prompt sits at the bottom centre, exactly over the composer
  // and the sidebar footer. Its close button ("Not now") has no aria-label; use
  // whichever dismiss button the stack renders and give the exit animation time.
  await page.evaluate(() => {
    for (const toasts of document.querySelectorAll("#nex-toast-host button[aria-label]")) {
      toasts.click();
    }
    for (const stack of document.querySelectorAll("#nex-toast-host > div")) {
      const buttons = [...stack.querySelectorAll("button")];
      buttons.at(-1)?.click();
    }
  });
  await host
    .locator("button")
    .first()
    .waitFor({ state: "detached", timeout: 5_000 })
    .catch(() => undefined);
  await page.waitForTimeout(250);
}

async function openSettings(section) {
  details.settingsProbeBefore = await overlayProbe();
  await dismissOverlays();
  await clearToasts();
  details.settingsProbeAfter = await overlayProbe();
  await clickWhenFree(page.locator('[data-testid="task-settings-button"]'), "settings button");
  if ((await page.locator('[data-testid="settings-page"]').count()) === 0) {
    // The sidebar footer button is the second entry point; the composer toolbar
    // one is not rendered when the task list owns the footer.
    await clickWhenFree(
      page.locator('[data-testid="task-settings-button"]').last(),
      "sidebar settings button",
    );
  }
  await page
    .locator('[data-testid="settings-page"]')
    .waitFor({ state: "visible", timeout: 20_000 });
  await page.locator(`[data-testid="settings-section-nav-${section}"]`).first().click();
  await page.waitForTimeout(1_500);
}

let summary;
try {
  // This suite does not test the notification prompt (the web-notification E2E
  // does). Mark it as already asked so its bottom-centre toast never sits over the
  // composer and sidebar footer; clearing the flag made every step race the toast.
  await page.addInitScript(() => {
    try {
      window.localStorage.setItem("nex-notification-permission-prompted", "1");
    } catch {
      // Storage can be unavailable; the overlay helpers still dismiss the toast.
    }
  });
  await page.goto(`${BASE_URL}/`, { waitUntil: "load" });
  await page.waitForTimeout(2_000);

  const tokenInput = page.getByPlaceholder("Paste your access token");
  for (let attempt = 0; attempt < 2; attempt += 1) {
    if ((await tokenInput.count()) === 0) break;
    await tokenInput.waitFor({ state: "visible", timeout: 20_000 });
    await tokenInput.fill(TOKEN);
    await page.getByRole("button", { name: "Sign in" }).click();
    await page.waitForTimeout(3_000);
    if ((await tokenInput.count()) > 0 && (await tokenInput.isVisible())) continue;
    break;
  }
  await composerInput().waitFor({ state: "visible", timeout: 90_000 });
  await page.waitForTimeout(8_000);
  // A fresh chat needs an explicit model choice: the E2E agent dir has a default,
  // but the production path (server doc) leaves the composer on "Choose model",
  // where Send does nothing. Pick the faux model like a user does.
  await selectComposerModel();

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
    await option.waitFor({ state: "visible", timeout: 60_000 });
    await option.click();
    await page
      .locator('[data-testid="runner-workspace-picker"]')
      .waitFor({ state: "hidden", timeout: 15_000 });
    await page.waitForTimeout(6_000);
  }
  await shot("mcp-skills-01-loaded");
  if (
    await page.evaluate(
      () =>
        document.querySelectorAll("dialog[open], [data-testid='runner-workspace-picker']").length >
        0,
    )
  ) {
    await shot("mcp-skills-01b-dialog");
  }

  // No chat yet: the runner's hidden status session connects the applied list, so
  // Settings below reports "connected" before the first turn (previously the row
  // stayed "connecting" forever until a chat existed).
  // ── Settings → MCP → add the echo server ──
  await openSettings("mcp");
  // The header action and the empty state both open the editor ("New MCP
  // server" in this build).
  const addButton = page.getByRole("button", { name: /Add MCP server|New MCP server/u }).first();
  await addButton.waitFor({ state: "visible", timeout: 20_000 });
  await addButton.click();
  await page
    .getByText("New MCP server", { exact: true })
    .first()
    .waitFor({ state: "visible", timeout: 15_000 });
  // Form mode (the default): the fields the E2E drives are the same ones a
  // user fills in.
  // Env is a JSON object of name → value (the field parses it as JSON).
  const nameInput = page.getByPlaceholder("my-mcp-server").first();
  await nameInput.waitFor({ state: "visible", timeout: 15_000 });
  await nameInput.fill(MCP_NAME);
  await page.getByPlaceholder("npx", { exact: true }).first().fill(NODE);
  await page
    .getByPlaceholder("-y @modelcontextprotocol/server-memory", { exact: true })
    .first()
    .fill(MCP_SERVER);
  await page
    .getByRole("button", { name: /Environment variables/u })
    .first()
    .click();
  const envField = page.locator('textarea[placeholder*="MY_API_KEY"]').first();
  await envField.waitFor({ state: "visible", timeout: 10_000 });
  await envField.fill(
    JSON.stringify({ MCP_ECHO_PREFIX: MCP_PREFIX, RESOURCE_URI: ECHO_RESOURCE_URI }, null, 2),
  );
  // 修复：pi 的 MCP 工具默认 exposure 是 codemode，只能在 codemode 脚本里调用，模型直接调用
  // `mcp__echo_server__echo` 会得到 "Tool not found"；且 pi 只为 direct 工具在首个 prompt 前等待连接。
  // 场景是"模型直接调用 echo"，所以按真实用户操作在表单里选 Direct。
  await page.locator('[data-testid="settings-mcp-exposure-select"]').first().click();
  await page
    .getByRole("option", { name: /^Direct/u })
    .first()
    .click();
  await shot("mcp-skills-02-mcp-form");
  const save = page.getByRole("button", { name: "Save", exact: true }).last();
  await save.click();
  await page.waitForTimeout(3_000);

  const row = page.locator(`[data-testid="mcp-server-row-${MCP_NAME}"]`).first();
  // The status wait below can take a minute; a single locator call must not
  // spend its own 30 s default on top of it.
  page.setDefaultTimeout(Number(process.env.NEX_E2E_LOCATOR_TIMEOUT_MS ?? 60_000));
  // Ask for live statuses explicitly (the Settings refresh button), then wait for
  // the row. The runner opens/keeps a status connection for the applied list, so
  // "connected" with the tool count can be true before any chat has used it.
  await page
    .getByRole("button", { name: /^Refresh$/u })
    .first()
    .click()
    .catch(() => undefined);
  const connected = await waitFor(
    async () => {
      if ((await row.count()) === 0) return null;
      const status = (await row.getAttribute("data-mcp-status")) ?? "";
      const tools = (await row.getAttribute("data-mcp-tool-count")) ?? "";
      return status === "connected" && tools === "1" ? { status, tools } : null;
    },
    MODEL_TIMEOUT_MS,
    1_000,
  );
  check("mcpServerListed", (await row.count()) > 0, {
    rows: await page.locator('[data-testid^="mcp-server-row-"]').count(),
  });
  check("mcpConnectedWithOneTool", Boolean(connected), {
    status: (await row.getAttribute("data-mcp-status").catch(() => null)) ?? null,
    toolCount: (await row.getAttribute("data-mcp-tool-count").catch(() => null)) ?? null,
    text: (await row.innerText().catch(() => ""))?.slice(0, 300) ?? null,
  });
  await shot("mcp-skills-03-mcp-connected");

  // The server stored the entry (no secret in the doc).
  const docAfterMcp = serverDoc();
  if (docAfterMcp) {
    const stored = docAfterMcp.scopes?.global?.mcp?.[MCP_NAME];
    check("serverDocHasMcp", Boolean(stored) && stored.transport?.command === NODE, stored);
  }

  // ── upload the skill through the documented HTTP helper ──
  const skillMd = `---\nname: ${SKILL_NAME}\ndescription: ${SKILL_DESCRIPTION}\n---\n\n# Echo helper\n\nWhen asked to greet, call the echo tool with the greeting text.\n`;
  // The body goes through curl directly (no temp file: nothing new is left in
  // the repo, and `git status` stays clean after a run).
  const upload = execFileSync(
    "curl",
    [
      "-sS",
      "-X",
      "POST",
      "--data-binary",
      "@-",
      "--header",
      "content-type: text/markdown",
      `${BASE_URL}/api/config/skills?name=${encodeURIComponent(SKILL_NAME)}&token=${TOKEN}`,
    ],
    { input: skillMd, encoding: "utf8" },
  );
  let uploaded = null;
  try {
    uploaded = JSON.parse(upload);
  } catch {
    uploaded = null;
  }
  check("skillUploaded", Boolean(uploaded?.digest?.startsWith("sha256:")), uploaded ?? upload);

  // Settings → Skills lists it (the server-owned doc).
  await page.locator('[data-testid="settings-back-button"]').first().click();
  await page.waitForTimeout(1_500);
  await openSettings("skill");
  const skillListed = await waitFor(
    async () => {
      const text = await bodyText();
      return text.includes(SKILL_NAME) && text.includes(SKILL_DESCRIPTION);
    },
    30_000,
    1_000,
  );
  check("skillListedInSettings", Boolean(skillListed), { tail: (await bodyText()).slice(-400) });
  await shot("mcp-skills-04-skills-settings");

  // ── chat: the model calls the echo tool ──
  await page.locator('[data-testid="settings-back-button"]').first().click();
  await page.waitForTimeout(3_000);
  await dismissOverlays();
  await clearToasts();
  const prompt = composerInput();
  // 修复依据（E2E）：设置页返回会激活工作区 tab，但不会回到某个会话；没有打开的
  // 会话时，欢迎页的输入框只是草稿，提交不会变成任务。像用户一样先建任务、再在
  // 侧栏选中它，后面的输入才落在真正的会话里。
  if ((await page.locator("[data-testid='chat-empty']").count()) === 0) {
    await page.locator('[data-testid="task-new-button"]').first().click();
    await page.waitForTimeout(3_000);
    await page
      .locator("[data-testid='task-item-']")
      .first()
      .click({ timeout: 5_000 })
      .catch(() => undefined);
    await page.waitForTimeout(2_000);
    await dismissOverlays();
    await clearToasts();
  }
  details.chatOpened = (await page.locator("[data-testid='chat-empty']").count()) > 0;
  await prompt.click();
  // A draft is created as soon as the chat is new; give the config push (and the
  // draft rebuild it triggers) a moment before the first prompt is sent.
  await page.waitForTimeout(1_000);
  await selectComposerModel();
  await setFullAccessMode();
  // The model is told to name either route: the MCP tool when it is declared,
  // otherwise pi's `read_mcp_resource` for the server's advertised resource.
  const echoPrompt = `Echo the text "${ECHO_TEXT}" using the echo tool, or read the MCP resource ${ECHO_RESOURCE_URI} if the echo tool is unavailable`;
  // Type in the editor (real key events: the editor's Enter handler starts the
  // turn; replacing the text in one update skips it).
  await prompt.evaluate((element) => element.__nexLexicalInputE2E?.focus?.());
  await page.keyboard.type(echoPrompt);
  details.sendDisabledBeforeSubmit = await page
    .locator("[data-testid='v4-composer'] button")
    .last()
    .evaluate((element) => ({
      disabled: element.hasAttribute("disabled"),
      label: element.getAttribute("aria-label"),
    }))
    .catch(() => null);
  details.composerText = await prompt.evaluate(
    (element) => element.__nexLexicalInputE2E?.getText() ?? element.textContent ?? "",
  );
  details.debugModelTrigger = await page
    .locator('[data-testid="chat-model-select-trigger"]')
    .first()
    .textContent()
    .catch(() => null);
  await page.keyboard.press("Enter");
  details.timelineAfter2s = await waitFor(
    async () => {
      await page.waitForTimeout(1_000);
      const text = await page.evaluate(() => {
        const timeline = document.querySelector("[data-testid='v4-timeline']");
        return (
          (timeline?.innerText ?? timeline?.textContent ?? "") +
          " ||composer:" +
          (document.querySelector("[data-testid='v4-composer-input']")?.textContent ?? "")
        );
      });
      return /Echo the text/iu.test(text) && text.length > 0 ? text.slice(0, 300) : null;
    },
    20_000,
    1_000,
  );
  details.debugPending = await page.evaluate(() =>
    [...document.querySelectorAll("[data-testid='v4-composer'] button")].map(
      (b) => `${b.getAttribute("aria-label") ?? b.getAttribute("data-slot")}:${b.disabled}`,
    ),
  );
  details.debugSendError = await page.evaluate(
    () => (document.querySelector("[role='alert']")?.textContent ?? "").slice(0, 200) || null,
  );
  // The MCP tool call reaches the transcript. The MCP tool passes pi's approval
  // gate like any other tool, so approve the card before it can run (the tool's
  // call is in the transcript either way; its answer needs the approval).
  const approve = page.locator('[data-permission-option-kind="allowOnce"]').first();
  // Background approver: the page may close before it gives up, which must not crash the run.
  void waitFor(
    async () => ((await approve.count()) > 0 ? approve.click() : null),
    30_000,
    500,
  ).catch(() => undefined);
  const toolBlock = () =>
    page.locator("[data-testid^='chat-tool-call-block-']").evaluateAll((elements) =>
      elements.map((element) => ({
        name: element.getAttribute("data-tool-name") ?? "",
        status: element.getAttribute("data-status") ?? "",
        text: (element.textContent ?? "").slice(0, 200),
      })),
    );
  const toolCard = await waitFor(
    async () => {
      const text = await bodyText();
      if (process.env.NEX_E2E_DEBUG) {
        console.log(
          "[prompts]",
          JSON.stringify(await fauxPrompts()),
          "blocks",
          JSON.stringify(
            await page.evaluate(() =>
              [...document.querySelectorAll("[data-testid^='chat-tool-call-block-']")].map((el) =>
                (el.textContent ?? "").slice(0, 120),
              ),
            ),
          ),
        );
      }
      // A finished turn folds its tool cards into the "Worked for …" group (as in
      // acceptance.mjs); expand it so the echo card is in the DOM.
      await page.evaluate(() => {
        for (const trigger of document.querySelectorAll(
          "[data-testid^='chat-assistant-history-trigger'][aria-expanded='false']",
        )) {
          trigger.click();
        }
      });
      const blocks = await toolBlock();
      const called = blocks.some(
        (block) => block.name === "mcp__echo_server__echo" || block.name === "read_mcp_resource",
      );
      return text.includes(`${MCP_PREFIX}${ECHO_TEXT}`) || called ? { text, blocks } : null;
    },
    REPLY_TIMEOUT_MS,
    2_000,
  );
  if (!toolCard) {
    // Debug: what the runner saw (prompts + journal) on a failing run.
    details.debugPrompts = await fauxPrompts();
    details.debugJournal = await page.evaluate(() =>
      [...document.querySelectorAll("[data-session-id]")]
        .map((el) => el.getAttribute("data-session-id"))
        .slice(0, 5),
    );
  }
  if (!toolCard) {
    details.debugRendered = await page.evaluate(() => ({
      dataRowCount:
        document.querySelector("[data-testid='v4-timeline']")?.getAttribute("data-row-count") ??
        null,
      route: location.pathname + location.search,
      activeTask:
        document
          .querySelector(
            "[data-testid^='task-item-'][data-active='true'], [data-testid^='task-item-'][aria-current='true']",
          )
          ?.getAttribute("data-testid") ?? null,
      tasks: [...document.querySelectorAll("[data-testid^='task-item-']")].map(
        (e) =>
          `${e.getAttribute("data-testid")}:${e.getAttribute("data-active") ?? e.getAttribute("aria-current") ?? ""}`,
      ),
      sessionTitle: (
        document.querySelector("[data-testid='v4-session-title']")?.textContent ?? ""
      ).slice(0, 80),
      column: (
        document.querySelector("[data-testid='conversation-column']")?.textContent ?? ""
      ).slice(0, 500),
    }));
  }
  check("mcpToolResultRendered", Boolean(toolCard), {
    tail: (await bodyText()).slice(-600),
    toolBlocks: await toolBlock(),
    watched: await page.evaluate(() =>
      [...document.querySelectorAll("[data-cua-approval-card], [data-testid^='approval']")].map(
        (element) => (element.textContent ?? "").slice(0, 200),
      ),
    ),
  });
  await shot("mcp-skills-05-mcp-tool");

  // ── chat: the `/` menu lists the skill and `/skill:` expands ──
  await clearToasts();
  const prompt2 = composerInput();
  await prompt2.click();
  const before = (await fauxPrompts()).length;
  // The `/` panel matches skills by name (the old app's behaviour); `/skill:<name>` is
  // pi's own expansion syntax, typed after the panel check. No trailing space: a space
  // ends the `/` token and closes the panel before it is checked.
  const slash = "/echo-helper";
  // The `/` panel needs real typing: the E2E bridge replaces text in one update,
  // which never produces the editor's slash trigger.
  await prompt2.evaluate((element) => element.__nexLexicalInputE2E?.focus?.());
  await page.keyboard.type(slash);
  const panelVisible = await waitFor(
    async () =>
      (await page.locator('[data-testid="prompt-suggestion-panel"]').count()) > 0 ? true : null,
    10_000,
    300,
  );
  check("slashPanelOpened", Boolean(panelVisible));
  // Option ids are `skill:<catalog id>` (the catalog id carries the skill path), so match the
  // skill option by its visible name.
  //
  // 修复依据：useSkills 只在 slash 面板打开（trigger === "/"）后才 enabled 并发出
  // getSkillReferenceCatalog 请求（packages/ui/src/SlashCommandPlugin.tsx 的 enabled 条件），
  // catalog 在面板出现后数秒才回来，Skills 分组先显示 "Searching..."。此前面板一打开
  // 就立即断言 skill 选项存在，永远失败。这里按 UI 的真实时序等待选项出现（与
  // skillExpandedInPrompt 的等待一致），超时才判定失败。
  const skillOption = page
    .locator('[data-testid^="prompt-suggestion-option-skill:"]')
    .filter({ hasText: "echo-helper" });
  const skillOptionFound = await waitFor(
    async () => ((await skillOption.count()) > 0 ? true : null),
    15_000,
    500,
  );
  check("skillInSlashMenu", Boolean(skillOptionFound), {
    options: await page
      .locator('[data-testid^="prompt-suggestion-option-"]')
      .evaluateAll((elements) => elements.slice(0, 20).map((el) => el.getAttribute("data-testid"))),
  });
  await shot("mcp-skills-06-slash-menu");
  // Escape only closes the panel; the typed command stays in the editor.
  await page.keyboard.press("Escape");
  // Replace the panel query with pi's expansion syntax and send it as typed text.
  for (let i = 0; i < slash.length; i += 1) await page.keyboard.press("Backspace");
  await page.keyboard.type(`/skill:echo-helper ${PROMPT_TEXT}`);
  await page.keyboard.press("Escape");
  await page.keyboard.press("Enter");

  const expanded = await waitFor(
    async () => {
      const prompts = await fauxPrompts();
      const newest = prompts.slice(before);
      return newest.some((entry) => entry.includes(`<skill name="${SKILL_NAME}"`)) ? newest : null;
    },
    REPLY_TIMEOUT_MS,
    2_000,
  );
  check("skillExpandedInPrompt", Boolean(expanded), {
    prompts: (await fauxPrompts()).slice(before).map((entry) => entry.slice(0, 400)),
  });
  await shot("mcp-skills-07-skill-expanded");
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

const significantErrors = consoleErrors.filter(
  (line) =>
    !/zcode\.z\.ai|ERR_FAILED|Tooltip is changing|accepted input projection silent|ECONNREFUSED|\[plugins\]/u.test(
      line,
    ),
);
check("noConsoleErrors", significantErrors.length === 0, significantErrors.slice(0, 10));

summary = { ok: failures.length === 0, failures, checks, details };
console.log(JSON.stringify(summary, null, 2));
process.exit(failures.length === 0 ? 0 : 1);
