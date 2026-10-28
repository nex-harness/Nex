// Model onboarding E2E for the next/ stack (docs/next/model-onboarding.md):
//
//   1. The runner starts in fallback mode with its local agent catalog;
//      GET /api/config/runners reports it (mode, model count, name).
//   2. The suite adds an importable openai-completions provider (with a literal
//      apiKey) to the runner's agent models.json, the way a user's machine has one.
//   3. Settings → Models → "Import from machine" → the runner's pi config is
//      imported into the shared (global) scope: the response names the provider,
//      reports the E2E faux provider's custom api as skipped, never echoes the key;
//      the server config on disk holds a sealed secret reference, not the key.
//   4. The runner switches to server-owned mode and the imported model chats
//      through the faux server (scripted reply).
//
// Runs LAST in `all`: a global import makes every runner server-owned.
//
// Env (set by next/dev.sh):
//   NEX_E2E_BASE_URL       Web origin to drive         (default http://localhost:5174)
//   NEX_E2E_TOKEN          Browser login token         (required)
//   NEX_E2E_AGENT_DIR      Runner agent dir (models.json) (required)
//   NEX_E2E_SERVER_CONFIG  Server config dir           (optional: disk assertions)
//   NEX_E2E_FAUX_URL       Faux provider origin        (default http://127.0.0.1:18080)
//   NEX_E2E_OUT            Screenshot + summary dir    (default <repo>/next/e2e/out)

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
const AGENT_DIR = process.env.NEX_E2E_AGENT_DIR?.trim() ?? "";
const SERVER_CONFIG = process.env.NEX_E2E_SERVER_CONFIG?.trim() ?? "";
const FAUX_URL = (process.env.NEX_E2E_FAUX_URL?.trim() || "http://127.0.0.1:18080").replace(
  /\/+$/u,
  "",
);
const OUT_DIR = path.resolve(
  REPO_ROOT,
  process.env.NEX_E2E_OUT?.trim() || path.join("next", "e2e", "out"),
);

const PROVIDER_ID = "onboard-import";
const MODEL_ID = "onboard-model-1";
const API_KEY = `sk-onboard-${Math.floor(Math.random() * 1_000_000)}`;
const REPLY_WORD = "onboard-ok";

if (!TOKEN) {
  console.error("[onboarding] NEX_E2E_TOKEN is required (the browser login token).");
  process.exit(2);
}
if (!AGENT_DIR) {
  console.error("[onboarding] NEX_E2E_AGENT_DIR is required (the runner's agent dir).");
  process.exit(2);
}

const playwrightEntry = path.join(REPO_ROOT, "node_modules", "playwright-core", "index.mjs");
if (!fs.existsSync(playwrightEntry)) {
  console.error(`[onboarding] playwright-core is missing at ${playwrightEntry}; run pnpm install.`);
  process.exit(2);
}
const { chromium } = await import(pathToFileURL(playwrightEntry).href);

const chromePath =
  process.env.NEX_E2E_CHROME?.trim() ||
  [
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/usr/bin/google-chrome",
    "/usr/bin/chromium",
    "/usr/bin/chromium-browser",
  ].find((candidate) => fs.existsSync(candidate)) ||
  (() => {
    console.error("[onboarding] no Chrome/Chromium executable found; set NEX_E2E_CHROME.");
    process.exit(2);
    return "";
  })();

const checks = {};
const failures = [];
const details = {};
const consoleErrors = [];

function check(name, ok, detail) {
  checks[name] = Boolean(ok);
  if (detail !== undefined) details[name] = detail;
  if (!ok) failures.push(name);
}

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

const bodyText = () => page.evaluate(() => document.body.innerText);

// ── 2. the machine's own pi config gains an importable provider ──
// exportLocal reads models.json at import time, so editing it now is enough;
// the original file is restored at the end.
const modelsPath = path.join(AGENT_DIR, "models.json");
const originalModels = fs.existsSync(modelsPath) ? fs.readFileSync(modelsPath, "utf8") : null;
{
  const parsed = originalModels ? JSON.parse(originalModels) : {};
  parsed.providers = parsed.providers ?? {};
  parsed.providers[PROVIDER_ID] = {
    name: "Onboarding Import",
    api: "openai-completions",
    baseUrl: `${FAUX_URL}/v1`,
    apiKey: API_KEY,
    models: [{ id: MODEL_ID, name: "Onboard Model 1", contextWindow: 128000, maxTokens: 8192 }],
  };
  fs.writeFileSync(modelsPath, `${JSON.stringify(parsed, null, 2)}\n`);
}

const browser = await chromium.launch({ executablePath: chromePath, headless: true });
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
page.on("pageerror", (error) => consoleErrors.push(`PAGEERROR: ${error.message}`));
page.on("console", (message) => {
  if (message.type() !== "error" && message.type() !== "warning") return;
  consoleErrors.push(`CONSOLE[${message.type()}]: ${message.text()}`.slice(0, 300));
});
const importResponses = [];
page.on("response", async (response) => {
  if (!response.url().includes("/api/config/import-runner")) return;
  importResponses.push({
    url: response.url(),
    status: response.status(),
    body: await response.text().catch(() => ""),
  });
});

const shot = (name) => page.screenshot({ path: path.join(OUT_DIR, `${name}.png`) });
fs.mkdirSync(OUT_DIR, { recursive: true });

async function runnerStatuses() {
  const response = await page.request.get(`${BASE_URL}/api/config/runners`);
  if (!response.ok()) return { error: response.status() };
  return (await response.json()).runners ?? [];
}

async function openModelSettings() {
  await page.locator('[data-testid="task-settings-button"]').first().click();
  await page
    .locator('[data-testid="settings-page"]')
    .waitFor({ state: "visible", timeout: 20_000 });
  await page.locator('[data-testid="settings-section-nav-modelProvider"]').first().click();
  await page.waitForTimeout(1_500);
}

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
  const composer = page
    .locator('[data-testid="v4-composer-input"], [data-testid="chat-input"]')
    .first();
  await composer.waitFor({ state: "visible", timeout: 90_000 });
  await page.waitForTimeout(3_000);
  check("loginAndComposer", true);

  // ── 1. runner status before the import ──
  const before = await waitFor(
    async () => {
      const runners = await runnerStatuses();
      if (!Array.isArray(runners)) return null;
      const ready = runners.find((runner) => runner.connected && runner.mode !== "unknown");
      return ready ? runners : null;
    },
    30_000,
    1_000,
  );
  const source = Array.isArray(before)
    ? before.find((runner) => runner.connected && runner.mode !== "unknown")
    : undefined;
  // `all` runs the settings suite first, which leaves a server-owned provider:
  // only a fresh stack (NEX_E2E_ONBOARDING_FRESH=1) starts in fallback mode.
  const expectFallback = process.env.NEX_E2E_ONBOARDING_FRESH === "1";
  check(
    "runnerStatusReported",
    Boolean(source?.name) &&
      source.modelCount > 0 &&
      (!expectFallback || source.mode === "fallback"),
    before,
  );
  if (!source) throw new Error("no connected runner reported a model status");

  // ── 3. Settings → Models → Import from machine ──
  await openModelSettings();
  await shot("onboarding-01-settings");
  const trigger = page.locator('[data-testid="model-provider-import-trigger"]').first();
  const triggerVisible = await trigger
    .waitFor({ state: "visible", timeout: 15_000 })
    .then(() => true)
    .catch(() => false);
  check("importTriggerVisible", triggerVisible);
  await trigger.click();
  const item = page
    .locator(`[data-testid="model-provider-import-item-${source.runnerId}"]`)
    .first();
  const itemVisible = await item
    .waitFor({ state: "visible", timeout: 10_000 })
    .then(() => true)
    .catch(() => false);
  check("importItemListsRunner", itemVisible, {
    items: await page
      .locator('[data-testid^="model-provider-import-item-"]')
      .evaluateAll((elements) => elements.map((el) => el.textContent ?? "")),
  });
  await shot("onboarding-02-import-menu");
  await item.click();

  const imported = await waitFor(
    async () => (importResponses.length > 0 ? importResponses.at(-1) : null),
    20_000,
    500,
  );
  let summary = null;
  try {
    summary = imported ? JSON.parse(imported.body) : null;
  } catch {
    summary = null;
  }
  check("importUsesGlobalScope", Boolean(imported?.url.includes("scope=global")), imported?.url);
  check(
    "importSummaryNamesProvider",
    imported?.status === 200 && summary?.providers?.includes(PROVIDER_ID),
    summary ?? imported,
  );
  check(
    "importKeyStored",
    Boolean(summary?.keysStored?.includes(PROVIDER_ID)),
    summary?.keysStored,
  );
  check(
    "importReportsSkippedFaux",
    (summary?.errors ?? []).some((error) => error.code === "provider-api-unsupported"),
    summary?.errors,
  );
  check("importNeverEchoesKey", !(imported?.body ?? "").includes(API_KEY));
  await page.waitForTimeout(1_500);
  await shot("onboarding-03-imported");

  // The provider list refreshes from the provider-settings change event.
  const listed = await waitFor(
    async () => ((await bodyText()).includes("Onboarding Import") ? true : null),
    15_000,
    700,
  );
  check("importedProviderListed", Boolean(listed));

  if (SERVER_CONFIG) {
    const raw = fs.readFileSync(path.join(SERVER_CONFIG, "config.json"), "utf8");
    const doc = JSON.parse(raw);
    const stored = doc?.scopes?.global?.providers?.[PROVIDER_ID];
    check(
      "docGlobalProviderSealed",
      stored?.apiKey === `sec_prov_${PROVIDER_ID}_apikey` && !raw.includes(API_KEY),
      { apiKey: stored?.apiKey ?? null },
    );
  }

  // ── 4. server-owned mode, and the imported model chats ──
  const after = await waitFor(
    async () => {
      const runners = await runnerStatuses();
      if (!Array.isArray(runners)) return null;
      const runner = runners.find((entry) => entry.runnerId === source.runnerId);
      return runner?.mode === "server" && runner.modelCount > 0 ? runner : null;
    },
    30_000,
    1_000,
  );
  check("runnerServerOwnedAfterImport", Boolean(after), after ?? (await runnerStatuses()));

  await page.locator('[data-testid="settings-back-button"]').first().click();
  await page.waitForTimeout(2_500);
  const newTask = page.locator('[data-testid="task-new-button"]').first();
  if ((await newTask.count()) > 0) {
    await newTask.click({ timeout: 8_000 }).catch(() => undefined);
    await page.waitForTimeout(1_500);
  }
  let pickerDebug = null;
  const picked = await waitFor(
    async () => {
      const pickerTrigger = page.locator('[data-testid="chat-model-select-trigger"]').first();
      if ((await pickerTrigger.count()) === 0) return null;
      await pickerTrigger.click();
      await page.waitForTimeout(600);
      const groups = page.locator("[data-testid^='chat-model-select-group-']");
      for (let index = 0; index < (await groups.count()); index += 1) {
        await groups
          .nth(index)
          .hover()
          .catch(() => undefined);
        await page.waitForTimeout(300);
        // The picker labels a model by its display name, not its id.
        const option = page
          .getByRole("menuitemradio")
          .filter({ hasText: /onboard-model-1|Onboard Model 1/u })
          .first();
        if ((await option.count()) > 0) {
          await option.click();
          return true;
        }
      }
      pickerDebug = await page.evaluate(() => ({
        trigger:
          document.querySelector('[data-testid="chat-model-select-trigger"]')?.textContent ?? null,
        groups: [...document.querySelectorAll("[data-testid^='chat-model-select-group-']")].map(
          (el) => el.textContent ?? "",
        ),
        items: [...document.querySelectorAll("[role='menuitemradio'], [role='menuitem']")].map(
          (el) => el.textContent ?? "",
        ),
      }));
      await page.keyboard.press("Escape");
      return null;
    },
    30_000,
    1_500,
  );
  check("importedModelInPicker", Boolean(picked), picked ? undefined : pickerDebug);
  await page.waitForTimeout(1_000);
  await composer.click();
  await page.keyboard.type(`Reply with exactly ${REPLY_WORD}`);
  await page.keyboard.press("Enter");
  const replied = await waitFor(
    async () => ((await bodyText()).split(REPLY_WORD).length - 1 >= 2 ? true : null),
    60_000,
    1_000,
  );
  check("importedModelReplies", Boolean(replied), { tail: (await bodyText()).slice(-400) });
  await shot("onboarding-04-chat");

  const relevantConsoleErrors = consoleErrors.filter(
    (entry) => !/zcode\.z\.ai|ERR_FAILED|Tooltip is changing|v4-store/.test(entry),
  );
  check("noConsoleErrors", relevantConsoleErrors.length === 0, relevantConsoleErrors.slice(0, 10));
} catch (error) {
  failures.push("exception");
  details.exception = error instanceof Error ? (error.stack ?? error.message) : String(error);
  await shot("onboarding-99-exception").catch(() => undefined);
} finally {
  if (originalModels === null) fs.rmSync(modelsPath, { force: true });
  else fs.writeFileSync(modelsPath, originalModels);
  try {
    await browser.close();
  } catch {
    // Teardown failure must not hide the check result.
  }
}

const result = { ok: failures.length === 0, failures, checks, details };
fs.mkdirSync(OUT_DIR, { recursive: true });
fs.writeFileSync(
  path.join(OUT_DIR, "onboarding-summary.json"),
  `${JSON.stringify(result, null, 2)}\n`,
);
console.log(JSON.stringify(result, null, 2));
process.exit(failures.length === 0 ? 0 : 1);
