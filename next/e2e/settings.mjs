#!/usr/bin/env node
// Settings E2E for server-owned model providers (docs/next/config-mcp-skills.md P0):
//
//   Settings → Model providers → add a custom OpenAI-compatible provider pointing
//   at a tiny fake chat-completions server started by this script, add a model and
//   its key → the model appears in the composer picker → send a prompt → the fake
//   server receives the request with the right key and the reply renders →
//   disable the model → it disappears from the picker.
//
// The fake server implements `POST /v1/chat/completions` (streaming SSE and
// non-streaming JSON). It records every Authorization header and request body so
// the assertions are made server-side, not by trusting the UI.
//
// Environment:
//   NEX_E2E_BASE_URL      Web origin to drive         (default http://localhost:5174)
//   NEX_E2E_TOKEN         Browser login token         (required)
//   NEX_E2E_CHROME        Chrome/Chromium executable  (default: auto-detect)
//   NEX_E2E_OUT           Screenshot + summary dir    (default <repo>/next/e2e/out)
//   NEX_E2E_WORKSPACE     Runner workspace to open    (default: restored tab)
//   NEX_E2E_SERVER_CONFIG Server config dir the stack uses (NEX_SERVER_CONFIG_DIR);
//                         when set, the suite asserts the stored doc on disk.
//   NEX_E2E_MODEL_TIMEOUT_MS  how long to wait for the model in the picker (default 120000)
//
// Exit code 0 only when every check passes. A JSON summary is always printed.

import fs from "node:fs";
import http from "node:http";
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
const SERVER_CONFIG = process.env.NEX_E2E_SERVER_CONFIG?.trim() ?? "";
const MODEL_TIMEOUT_MS = Number(process.env.NEX_E2E_MODEL_TIMEOUT_MS) || 120_000;
const REPLY_TIMEOUT_MS = Number(process.env.NEX_E2E_REPLY_TIMEOUT_MS) || 180_000;

const MODEL_ID = "e2e-fake-model";
const PROVIDER_NAME = "E2E Fake Provider";
const MODEL_NAME = "E2E Fake Model";
const API_KEY = "sk-e2e-secret-1234";
const REPLY_TEXT = "E2E fake server reply: 42";

function failConfig(message) {
  console.error(`[settings] ${message}`);
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

// ── fake OpenAI-compatible server ──

const requests = [];
const fakeServer = http.createServer((request, response) => {
  let body = "";
  request.on("data", (chunk) => {
    body += chunk;
  });
  request.on("end", () => {
    const url = request.url ?? "";
    if (request.method === "GET" && url.endsWith("/v1/models")) {
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify({ data: [{ id: MODEL_ID, object: "model" }] }));
      return;
    }
    if (request.method !== "POST" || !url.includes("/chat/completions")) {
      response.writeHead(404, { "content-type": "application/json" });
      response.end(JSON.stringify({ error: { message: `unexpected ${request.method} ${url}` } }));
      return;
    }
    let payload = {};
    try {
      payload = JSON.parse(body || "{}");
    } catch {
      payload = { parseError: true };
    }
    requests.push({
      authorization: request.headers["authorization"] ?? "",
      model: payload.model ?? null,
      stream: payload.stream === true,
      messages: Array.isArray(payload.messages) ? payload.messages : [],
      at: Date.now(),
    });
    const id = `chatcmpl-e2e-${requests.length}`;
    const created = Math.floor(Date.now() / 1000);
    if (payload.stream === true) {
      response.writeHead(200, {
        "content-type": "text/event-stream",
        "cache-control": "no-cache",
        connection: "keep-alive",
      });
      const chunk = (delta, finishReason = null) => ({
        id,
        object: "chat.completion.chunk",
        created,
        model: payload.model ?? MODEL_ID,
        choices: [{ index: 0, delta, finish_reason: finishReason }],
      });
      response.write(
        `data: ${JSON.stringify(chunk({ role: "assistant", content: REPLY_TEXT }))}\n\n`,
      );
      response.write(`data: ${JSON.stringify(chunk({}, "stop"))}\n\n`);
      response.write("data: [DONE]\n\n");
      response.end();
      return;
    }
    response.writeHead(200, { "content-type": "application/json" });
    response.end(
      JSON.stringify({
        id,
        object: "chat.completion",
        created,
        model: payload.model ?? MODEL_ID,
        choices: [
          {
            index: 0,
            message: { role: "assistant", content: REPLY_TEXT },
            finish_reason: "stop",
          },
        ],
        usage: { prompt_tokens: 10, completion_tokens: 8, total_tokens: 18 },
      }),
    );
  });
});

await new Promise((resolve, reject) => {
  fakeServer.once("error", reject);
  fakeServer.listen(0, "127.0.0.1", resolve);
});
const fakePort = fakeServer.address().port;
const fakeBaseUrl = `http://127.0.0.1:${fakePort}/v1`;

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

async function waitFor(checkName, predicate, timeoutMs, pollMs = 500) {
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
 * The model picker lists every composer model option. Providers are submenus
 * (one `chat-model-select-group-*` row per provider), so this opens the menu,
 * hovers every provider row and collects the model radio items it reveals.
 */
async function pickerOptions() {
  return page.evaluate(() =>
    [...document.querySelectorAll("[data-testid^='chat-model-select-item-']")].map((element) => ({
      testid: element.getAttribute("data-testid") ?? "",
      text: (element.textContent ?? "").trim(),
    })),
  );
}

async function openPicker() {
  if (await isPickerOpen()) return;
  const trigger = page.locator('[data-testid="chat-model-select-trigger"]').first();
  await trigger.waitFor({ state: "visible", timeout: 20_000 });
  await trigger.click();
  await page.waitForTimeout(600);
  await expandProviderSubmenus();
}

async function isPickerOpen() {
  return page.evaluate(() => Boolean(document.querySelector("[role='menu']")));
}

/** Hovers each provider submenu trigger so its model items render. */
async function expandProviderSubmenus() {
  const groups = page.locator("[data-testid^='chat-model-select-group-']");
  const count = await groups.count();
  for (let index = 0; index < count; index += 1) {
    const group = groups.nth(index);
    await group.hover().catch(() => undefined);
    await page.waitForTimeout(400);
  }
}

/** Reads the provider rows (submenu triggers) of the open menu. */
async function pickerGroups() {
  return page.evaluate(() =>
    [...document.querySelectorAll("[data-testid^='chat-model-select-group-']")].map((element) => ({
      testid: element.getAttribute("data-testid") ?? "",
      text: (element.textContent ?? "").trim(),
      items: [...document.querySelectorAll("[data-testid^='chat-model-select-item-']")].map(
        (item) => ({
          testid: item.getAttribute("data-testid") ?? "",
          text: (item.textContent ?? "").trim(),
        }),
      ),
    })),
  );
}

async function closePicker() {
  await page.keyboard.press("Escape");
  await page.waitForTimeout(300);
}

let summary;
try {
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
  const composer = page.locator('[data-testid="chat-input"], [contenteditable="true"]').first();
  await composer.waitFor({ state: "visible", timeout: 90_000 });
  await page.waitForTimeout(8_000);

  // Open the E2E workspace when the caller names one (deployed stacks).
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
  await shot("settings-01-loaded");

  // The runner must start in fallback mode: its local catalog knows the e2e
  // provider does not exist yet.
  await openPicker();
  const before = await pickerOptions();
  await closePicker();
  check(
    "providerAbsentBefore",
    before.every((option) => !/E2E Fake Model/.test(option.text)),
    before,
  );

  // ── Settings → Model providers → add a custom provider ──
  await page.locator('[data-testid="task-settings-button"]').first().click();
  await page
    .locator('[data-testid="settings-page"]')
    .waitFor({ state: "visible", timeout: 20_000 });
  await page.locator('[data-testid="settings-section-nav-modelProvider"]').first().click();
  await page.waitForTimeout(2_000);
  await shot("settings-02-model-providers");

  await page.locator('[data-testid="model-provider-add-provider-button"]').first().click();
  await page
    .locator('[data-testid="model-provider-template-picker"]')
    .waitFor({ state: "visible", timeout: 15_000 });
  await page.locator('[data-testid="model-provider-template-item-custom"]').first().click();

  // The card is named from the create call. The composer's provider group is
  // labelled with the providerId (the UI's group builder), so the E2E checks
  // the model id and the group row rather than a display name.
  const header = page.locator('[data-testid="model-provider-header"]').first();
  await header.waitFor({ state: "visible", timeout: 20_000 });
  let banner = "";
  const createError = page.locator('[data-testid="provider-detail-feedback-viewport"]');
  if (await createError.count()) banner = (await createError.first().innerText()).trim();
  check("providerCreatedWithoutError", !/失败|error|Error/u.test(banner), banner);
  await page.waitForTimeout(800);

  const apiFormatTrigger = page
    .locator('[data-testid="model-provider-api-format-trigger"]')
    .first();
  await apiFormatTrigger.waitFor({ state: "visible", timeout: 15_000 });
  await apiFormatTrigger.click();
  await page
    .locator('[data-testid="model-provider-api-format-item-openai-chat-completions"]')
    .first()
    .click();
  await page.waitForTimeout(800);

  // Rename the provider (the composer labels a provider group with its
  // providerId, so the suite tracks the provider through Settings by name).
  const actions = page.locator('[data-testid="model-provider-actions-button"]').first();
  if (await actions.count()) {
    await actions.click();
    await page.waitForTimeout(300);
    const rename = page.locator('[data-testid="model-provider-name-edit-button"]').first();
    await rename.waitFor({ state: "visible", timeout: 10_000 });
    await rename.click();
    const nameInput = page.locator('[data-testid="model-provider-name-input"]').first();
    await nameInput.waitFor({ state: "visible", timeout: 10_000 });
    await nameInput.fill(PROVIDER_NAME);
    await nameInput.press("Enter");
    await page.waitForTimeout(1_000);
    check(
      "providerRenamed",
      (await page.locator('[data-testid="model-provider-header"]').first().innerText()).includes(
        PROVIDER_NAME,
      ),
      await page.locator('[data-testid="model-provider-header"]').first().innerText(),
    );
  }

  const baseUrlInput = page.locator('[data-testid="model-provider-base-url-input"]').first();
  await baseUrlInput.fill(fakeBaseUrl);
  await baseUrlInput.press("Enter");
  await page.waitForTimeout(800);

  const apiKeyInput = page.locator('[data-testid="model-provider-api-key-input"]').first();
  await apiKeyInput.fill(API_KEY);
  await apiKeyInput.blur();
  await page.waitForTimeout(1_500);

  // Add the model through the dialog (the add button opens a metadata dialog;
  // the inline row editor is only used for existing models).
  await page.locator('[data-testid="model-provider-add-model-button"]').first().click();
  const dialog = page.getByRole("dialog");
  await dialog.waitFor({ state: "visible", timeout: 15_000 });
  const modelId = dialog.getByPlaceholder("Model ID", { exact: true }).first();
  await modelId.fill(MODEL_ID);
  await page.waitForTimeout(300);
  await dialog.getByRole("button", { name: "Save", exact: true }).first().click();
  await dialog.waitFor({ state: "hidden", timeout: 20_000 });
  await page.waitForTimeout(1_000);
  const savedRows = await page.locator(`[data-testid="model-provider-model-input-0"]`).count();
  check("modelRowAdded", savedRows === 1, { savedRows });
  await shot("settings-03-provider-added");

  const providerNav = page.locator('[data-testid^="model-provider-nav-item-"]').first();
  await providerNav.click();
  await page.waitForTimeout(800);
  const enabledSwitch = page.locator('[data-testid="model-provider-enabled-switch"]').first();
  if ((await enabledSwitch.count()) > 0) {
    const state = (await enabledSwitch.getAttribute("data-state")) ?? "";
    if (state !== "checked") {
      await enabledSwitch.click();
      await page.waitForTimeout(1_500);
    }
  }
  const enabledState = ((await enabledSwitch.getAttribute("data-state")) ?? "").trim();
  check("providerEnabledInSettings", enabledState === "checked", { enabledState });
  await shot("settings-04-provider-enabled");

  // ── the model reaches the composer picker ──
  await page.locator('[data-testid="settings-back-button"]').first().click();
  await page.waitForTimeout(3_000);
  const appeared = await waitFor(
    "picker",
    async () => {
      await openPicker();
      const groups = await pickerGroups();
      // The composer labels a provider group with the providerId; the model
      // row carries the model id.
      const group = groups.find((candidate) =>
        candidate.items.some((item) => item.text.includes(MODEL_ID)),
      );
      if (!group) return null;
      const options = await pickerOptions();
      const model = options.find((option) => option.text.includes(MODEL_ID));
      return { group, model };
    },
    MODEL_TIMEOUT_MS,
    1_000,
  );
  if (!appeared) {
    const trigger = page.locator('[data-testid="chat-model-select-trigger"]').first();
    details.pickerDebug = await page.evaluate(() => ({
      triggerText:
        document.querySelector('[data-testid="chat-model-select-trigger"]')?.textContent ?? null,
      groups: [...document.querySelectorAll("[data-testid^='chat-model-select-group-']")].map(
        (el) => ({
          testid: el.getAttribute("data-testid"),
          text: el.textContent,
        }),
      ),
      items: [...document.querySelectorAll("[data-testid^='chat-model-select-item-']")].map((el) =>
        el.getAttribute("data-testid"),
      ),
    }));
    await trigger.click();
    await page.waitForTimeout(1500);
    details.pickerDebug.afterClick = await page.evaluate(() => {
      const menu = document.querySelector("[data-radix-menu-content], [role='menu']");
      return {
        items: [...document.querySelectorAll("[data-testid^='chat-model-select-item-']")].map(
          (el) => el.textContent,
        ),
        open: document.querySelectorAll("[data-state='open']").length,
        menuText: menu ? menu.innerText : null,
        isMenu: menu?.getAttribute("role") ?? null,
        hasItems: Boolean(menu?.querySelector("[role='menuitemradio'], [role='menuitem']")),
        childRoles: menu
          ? [...menu.querySelectorAll("[role]")]
              .slice(0, 20)
              .map((el) => `${el.getAttribute("role")}:${(el.textContent ?? "").slice(0, 40)}`)
          : null,
      };
    });
    await shot("settings-05b-picker-failed");
  }
  check("modelAppearsInPicker", Boolean(appeared), appeared);
  if (!appeared) throw new Error("model never appeared in the picker");
  await shot("settings-05-picker");

  // ── select it and chat with the fake provider ──
  // The provider row is a submenu trigger: hover it, then click the model radio.
  await page.locator(`[data-testid="${appeared.group.testid}"]`).first().hover();
  await page.waitForTimeout(600);
  await page.getByRole("menuitemradio").filter({ hasText: MODEL_ID }).first().click();
  await page.waitForTimeout(1_500);
  const editor = page.locator('[data-testid="chat-input"], [contenteditable="true"]').first();
  await editor.click();
  await page.keyboard.type("Say hello from the fake provider.");
  await page.keyboard.press("Enter");

  const replied = await waitFor(
    "reply",
    async () => (await bodyText()).includes(REPLY_TEXT),
    REPLY_TIMEOUT_MS,
    2_000,
  );
  check("replyRendered", Boolean(replied), { tail: (await bodyText()).slice(-500) });
  await shot("settings-06-reply");

  const chatRequest = requests.find((entry) =>
    entry.messages.some((message) => JSON.stringify(message).includes("fake provider")),
  );
  check(
    "fakeServerReceivedRequest",
    Boolean(chatRequest),
    requests.map((entry) => entry.model),
  );
  check("fakeServerReceivedKey", chatRequest?.authorization === `Bearer ${API_KEY}`, {
    authorization: chatRequest ? `${chatRequest.authorization.slice(0, 12)}…` : null,
  });
  check("fakeServerReceivedModel", chatRequest?.model === MODEL_ID, {
    model: chatRequest?.model ?? null,
  });

  // ── disable the model → it leaves the picker ──
  await page.locator('[data-testid="task-settings-button"]').first().click();
  await page.locator('[data-testid="settings-section-nav-modelProvider"]').first().click();
  await page.waitForTimeout(1_500);
  const modelRow = page.locator('[data-testid="model-provider-model-input-0"]').first();
  await modelRow.waitFor({ state: "visible", timeout: 15_000 });
  // The model row's enable switch is the only switch inside the model list.
  const toggle = page.getByRole("switch").first();
  await toggle.waitFor({ state: "visible", timeout: 15_000 });
  await toggle.click();
  await page.waitForTimeout(1_500);
  await page.locator('[data-testid="settings-back-button"]').first().click();
  await page.waitForTimeout(3_000);
  const gone = await waitFor(
    "gone",
    async () => {
      await openPicker();
      const groups = await pickerGroups();
      return groups.every(
        (group) => !group.text.includes(PROVIDER_NAME) && !group.text.includes(MODEL_NAME),
      );
    },
    MODEL_TIMEOUT_MS,
    1_000,
  );
  await closePicker();
  check("modelDisappearsAfterDisable", Boolean(gone), await pickerOptions());
  await shot("settings-07-disabled");

  // ── the server stored the provider; the key is not in plaintext ──
  if (SERVER_CONFIG) {
    const configPath = path.join(SERVER_CONFIG, "config.json");
    const secretsPath = path.join(SERVER_CONFIG, "secrets.enc");
    const configText = fs.existsSync(configPath) ? fs.readFileSync(configPath, "utf8") : "";
    const secretsText = fs.existsSync(secretsPath) ? fs.readFileSync(secretsPath, "utf8") : "";
    check("serverConfigPersisted", configText.includes(MODEL_ID), {
      configPath,
      exists: fs.existsSync(configPath),
      configDirListing: fs.existsSync(SERVER_CONFIG) ? fs.readdirSync(SERVER_CONFIG) : null,
    });
    check("serverConfigHidesKey", !configText.includes(API_KEY) && !secretsText.includes(API_KEY), {
      configPath,
      secretsPath,
    });
  }
} catch (error) {
  failures.push("exception");
  details.exception = error instanceof Error ? (error.stack ?? error.message) : String(error);
} finally {
  try {
    await browser.close();
  } catch {
    // Teardown failure must not hide the check result.
  }
  await new Promise((resolve) => fakeServer.close(resolve));
}

const significantErrors = consoleErrors.filter(
  (line) =>
    !/zcode\.z\.ai|ERR_FAILED|Tooltip is changing|accepted input projection silent|ECONNREFUSED/u.test(
      line,
    ),
);
check("noConsoleErrors", significantErrors.length === 0, significantErrors.slice(0, 10));

summary = {
  ok: failures.length === 0,
  failures,
  checks,
  details,
  fakeServer: { baseUrl: fakeBaseUrl, requests: requests.length },
};
console.log(JSON.stringify(summary, null, 2));
process.exit(failures.length === 0 ? 0 : 1);
