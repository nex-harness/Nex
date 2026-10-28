// Runner folders E2E for the next/ stack (docs/next/runner-folders.md):
//
//   1. Sidebar "Add project" → "Open folder" opens the machine picker; the runner's
//      group offers "Choose folder…".
//   2. The directory browser opens on that machine (machine scope) and can browse a
//      directory OUTSIDE the runner's served workspaces.
//   3. "Select this directory" adds the folder on the runner: it becomes a sidebar
//      project, and a chat in it gets the scripted reply (the runner serves it).
//   4. After a reload the folder is still offered by the picker (persisted on the
//      runner, catalogued by the server).
//
// Env (set by next/dev.sh):
//   NEX_E2E_BASE_URL       Web origin to drive         (default http://localhost:5174)
//   NEX_E2E_TOKEN          Browser login token         (required)
//   NEX_E2E_EXTRA_FOLDER   Absolute directory outside the runner workspaces (required)
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
const EXTRA_FOLDER = process.env.NEX_E2E_EXTRA_FOLDER?.trim() ?? "";
const OUT_DIR = path.resolve(
  REPO_ROOT,
  process.env.NEX_E2E_OUT?.trim() || path.join("next", "e2e", "out"),
);
const REPLY_WORD = "folder-ok";

if (!TOKEN) {
  console.error("[folders] NEX_E2E_TOKEN is required (the browser login token).");
  process.exit(2);
}
if (!EXTRA_FOLDER || !path.isAbsolute(EXTRA_FOLDER)) {
  console.error("[folders] NEX_E2E_EXTRA_FOLDER must be an absolute directory.");
  process.exit(2);
}
fs.mkdirSync(EXTRA_FOLDER, { recursive: true });
// A marker subdirectory proves the browser lists the folder's own children.
fs.mkdirSync(path.join(EXTRA_FOLDER, "inside-marker"), { recursive: true });
const EXTRA_REAL = fs.realpathSync(EXTRA_FOLDER);
const EXTRA_NAME = path.basename(EXTRA_REAL);

const playwrightEntry = path.join(REPO_ROOT, "node_modules", "playwright-core", "index.mjs");
if (!fs.existsSync(playwrightEntry)) {
  console.error(`[folders] playwright-core is missing at ${playwrightEntry}; run pnpm install.`);
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
    console.error("[folders] no Chrome/Chromium executable found; set NEX_E2E_CHROME.");
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

const browser = await chromium.launch({ executablePath: chromePath, headless: true });
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
page.on("pageerror", (error) => consoleErrors.push(`PAGEERROR: ${error.message}`));
page.on("console", (message) => {
  if (message.type() !== "error" && message.type() !== "warning") return;
  consoleErrors.push(`CONSOLE[${message.type()}]: ${message.text()}`.slice(0, 300));
});
const shot = (name) => page.screenshot({ path: path.join(OUT_DIR, `${name}.png`) });
fs.mkdirSync(OUT_DIR, { recursive: true });

async function login() {
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
  await page.waitForTimeout(2_500);
  return composer;
}

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

async function openPicker() {
  await dismissToasts();
  const addProject = page.locator('[data-testid="project-add"]').first();
  await addProject.waitFor({ state: "visible", timeout: 30_000 });
  await addProject.click();
  const openFolder = page.getByText(/^(Open folder|打开文件夹)$/u).first();
  await openFolder.waitFor({ state: "visible", timeout: 15_000 });
  await openFolder.click();
  await page
    .locator('[data-testid="runner-workspace-picker"]')
    .waitFor({ state: "visible", timeout: 20_000 });
  await page.waitForTimeout(1_500);
}

const pickerPaths = () =>
  page.evaluate(() =>
    [...document.querySelectorAll('[data-testid="runner-workspace-option"] span[title]')].map(
      (element) => element.getAttribute("title") ?? "",
    ),
  );

try {
  await page.goto(`${BASE_URL}/`, { waitUntil: "load" });
  await page.waitForTimeout(2_000);
  await login();
  check("loginAndComposer", true);

  // ── 1. picker offers "Choose folder…" on the runner ──
  await openPicker();
  const chooseButtons = page.locator('[data-testid^="runner-workspace-choose-folder-"]');
  const chooseCount = await chooseButtons.count();
  check("pickerOffersChooseFolder", chooseCount > 0, {
    before: await pickerPaths(),
  });
  check("extraFolderNotServedYet", !(await pickerPaths()).includes(EXTRA_REAL));
  await shot("folders-01-picker");
  await chooseButtons.first().click();

  // ── 2. browse outside the served workspaces on that machine ──
  // The directory browser is an overlay with a "Browse folder" header and its own
  // path form; it starts at the machine's home (system.info on the machine scope).
  const selectButton = page
    .getByRole("button", { name: /^(Select this directory|选择此目录|选择该目录)$/u })
    .first();
  await selectButton.waitFor({ state: "visible", timeout: 20_000 });
  const browserInput = page
    .locator("form", { has: page.getByRole("button", { name: /^(Go|前往)$/u }) })
    .locator("input[type='text']")
    .first();
  await browserInput.waitFor({ state: "visible", timeout: 10_000 });
  const startPath = await waitFor(
    async () => {
      const value = (await browserInput.inputValue()) ?? "";
      return value.length > 1 ? value : null;
    },
    15_000,
    300,
  );
  check("browserStartsAtMachineHome", Boolean(startPath), { start: startPath ?? "" });
  await browserInput.fill(EXTRA_REAL);
  await browserInput.press("Enter");
  const listed = await waitFor(
    async () => ((await bodyText()).includes("inside-marker") ? true : null),
    15_000,
    500,
  );
  check("browsesOutsideServedFolders", Boolean(listed), {
    tail: (await bodyText()).slice(0, 400),
  });
  await shot("folders-02-browser");

  // ── 3. select it → sidebar project, and a chat in it works ──
  await selectButton.click();
  const identity = await waitFor(
    async () => {
      const identities = await page.evaluate(() =>
        [...document.querySelectorAll("[data-workspace-identity]")].map(
          (element) => element.getAttribute("data-workspace-identity") ?? "",
        ),
      );
      return identities.find((value) => value.endsWith(`:${EXTRA_REAL}`)) ?? null;
    },
    20_000,
    700,
  );
  check("sidebarShowsAddedFolder", Boolean(identity), {
    identity,
    sidebarHasName: (await bodyText()).includes(EXTRA_NAME),
  });
  await shot("folders-03-sidebar");

  const editor = page
    .locator('[data-testid="v4-composer-input"], [data-testid="chat-input"]')
    .first();
  await editor.click();
  await page.keyboard.type(`Reply with exactly ${REPLY_WORD}`);
  await page.keyboard.press("Enter");
  // The prompt echo also contains the word: require the reply as its own line.
  const replyLine = new RegExp(`^${REPLY_WORD}$`, "mu");
  const replied = await waitFor(
    async () => (replyLine.test(await bodyText()) ? true : null),
    60_000,
    1_000,
  );
  check("chatInAddedFolderReplies", Boolean(replied), { tail: (await bodyText()).slice(-400) });
  await shot("folders-04-chat");

  // ── 4. persisted: still offered after a reload ──
  await page.reload({ waitUntil: "load" });
  await page.waitForTimeout(2_000);
  await login();
  await openPicker();
  const afterReload = await waitFor(
    async () => ((await pickerPaths()).includes(EXTRA_REAL) ? true : null),
    15_000,
    1_000,
  );
  check("folderPersistsAfterReload", Boolean(afterReload), { paths: await pickerPaths() });
  await shot("folders-05-after-reload");
  await page.keyboard.press("Escape");

  const relevantConsoleErrors = consoleErrors.filter(
    (entry) => !/zcode\.z\.ai|ERR_FAILED|Tooltip is changing|v4-store/.test(entry),
  );
  check("noConsoleErrors", relevantConsoleErrors.length === 0, relevantConsoleErrors.slice(0, 10));
} catch (error) {
  failures.push("exception");
  details.exception = error instanceof Error ? (error.stack ?? error.message) : String(error);
  await shot("folders-99-exception").catch(() => undefined);
} finally {
  try {
    await browser.close();
  } catch {
    // Teardown failure must not hide the check result.
  }
}

const result = { ok: failures.length === 0, failures, checks, details };
fs.writeFileSync(
  path.join(OUT_DIR, "folders-summary.json"),
  `${JSON.stringify(result, null, 2)}\n`,
);
console.log(JSON.stringify(result, null, 2));
process.exit(failures.length === 0 ? 0 : 1);
