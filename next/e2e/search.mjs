// Full-text search E2E for the next/ stack (docs/next/automations-views-search.md §C):
//
//   1. A chat whose transcript holds a unique word (the faux provider strips the
//      "Reply with exactly X" marker from generated titles, so the word is not in
//      the title).
//   2. ⌘K → type the word: the command center lists the chat with a snippet
//      (snippets only come from the runner's transcript index, never from a
//      title match).
//   3. Selecting the result opens the chat.
//   4. After a reload the runner index is rebuilt and the search still hits.
//   5. A 1-character query stays title-only and does not error.
//
// Env (set by next/dev.sh):
//   NEX_E2E_BASE_URL       Web origin to drive         (default http://localhost:5174)
//   NEX_E2E_TOKEN          Browser login token         (required)
//   NEX_E2E_WORKSPACE      Runner workspace to open    (required)
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
const OUT_DIR = path.resolve(
  REPO_ROOT,
  process.env.NEX_E2E_OUT?.trim() || path.join("next", "e2e", "out"),
);
const WORKSPACE = process.env.NEX_E2E_WORKSPACE?.trim() ?? "";

const UNIQUE_WORD = `zqxsearch${Math.floor(Math.random() * 1_000_000)}`;
const PROMPT_TEXT = `Reply with exactly ${UNIQUE_WORD}`;

if (!TOKEN) {
  console.error("[search] NEX_E2E_TOKEN is required (the browser login token).");
  process.exit(2);
}
if (!WORKSPACE) {
  console.error("[search] NEX_E2E_WORKSPACE is required (the runner workspace).");
  process.exit(2);
}

const playwrightEntry = path.join(REPO_ROOT, "node_modules", "playwright-core", "index.mjs");
if (!fs.existsSync(playwrightEntry)) {
  console.error(
    `[search] playwright-core is missing at ${playwrightEntry}; run pnpm install at the repo root.`,
  );
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
    console.error("[search] no Chrome/Chromium executable found; set NEX_E2E_CHROME.");
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
  await page.waitForTimeout(2_000);
  return composer;
}

/** Starts a fresh chat so the prompt lands in its own session (task-actions 同款兜底). */
async function startNewChat() {
  const newTask = page.locator('[data-testid="task-new-button"]').first();
  if ((await newTask.count()) === 0) return;
  await dismissToasts();
  await newTask.click({ timeout: 8_000 }).catch(async () => {
    await page.keyboard.press("Escape");
    await page.waitForTimeout(500);
    await newTask.evaluate((element) => {
      element.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });
  });
  await page.waitForTimeout(2_000);
}

/** Opens ⌘K, types the query and returns the conversation items that mention `needle`. */
async function searchFor(query, needle) {
  await dismissToasts();
  await page.keyboard.press("Escape").catch(() => undefined);
  await page.keyboard.press(process.platform === "darwin" ? "Meta+k" : "Control+k");
  const palette = page.locator("[cmdk-root]").first();
  await palette.waitFor({ state: "visible", timeout: 10_000 });
  await page.waitForTimeout(400);
  await page.keyboard.type(query);
  // The controller fans out to the runner on every query change; give the
  // debounced list call and the runner index build time to land.
  return waitFor(
    async () => {
      const items = await page
        .locator("[cmdk-item]")
        .evaluateAll((elements) => elements.map((el) => el.textContent ?? ""));
      const hits = items.filter((text) => text.includes(needle));
      return hits.length > 0 ? hits : null;
    },
    20_000,
    700,
  );
}

try {
  // ── 1. A chat whose transcript (not title) holds the unique word ──
  await page.goto(`${BASE_URL}/`, { waitUntil: "load" });
  await page.waitForTimeout(2_000);
  const composer = await login();
  check("loginAndComposer", true);

  if ((await page.locator('[data-testid^="task-item-"]').count()) > 0) await startNewChat();
  await composer.click();
  await page.waitForTimeout(800);
  await page.keyboard.type(PROMPT_TEXT);
  await page.keyboard.press("Enter");
  // The prompt echo plus the faux reply: the word shows at least twice.
  const replied = await waitFor(
    async () => ((await bodyText()).split(UNIQUE_WORD).length - 1 >= 2 ? true : null),
    60_000,
    1_000,
  );
  check("chatReplied", Boolean(replied));
  // Let title generation settle so the row title is final before searching.
  await page.waitForTimeout(3_000);
  const rowTitles = await page
    .locator('[data-testid^="task-item-"]')
    .evaluateAll((elements) => elements.map((el) => el.textContent ?? ""));
  check(
    "wordNotInTitle",
    !rowTitles.some((text) => text.includes(UNIQUE_WORD)),
    rowTitles.slice(0, 5),
  );
  await shot("search-01-chat");

  // Move away so selecting the result is an observable navigation.
  await startNewChat();

  // ── 2. ⌘K finds the chat by a transcript-only word, with a snippet ──
  const hits = await searchFor(UNIQUE_WORD, UNIQUE_WORD);
  check("transcriptHitListed", Boolean(hits), {
    items: await page
      .locator("[cmdk-item]")
      .evaluateAll((elements) => elements.slice(0, 8).map((el) => el.textContent ?? "")),
  });
  // The title is not the word (checked above), so the word in the item can
  // only come from the snippet line.
  const snippetHighlighted = await page
    .locator("[cmdk-item]")
    .filter({ hasText: UNIQUE_WORD })
    .first()
    .locator("mark")
    .count()
    .catch(() => 0);
  check("snippetHighlighted", snippetHighlighted > 0, { marks: snippetHighlighted });
  await shot("search-02-results");

  // ── 3. Selecting the result opens the chat ──
  await page.locator("[cmdk-item]").filter({ hasText: UNIQUE_WORD }).first().click();
  const opened = await waitFor(
    async () => ((await bodyText()).split(UNIQUE_WORD).length - 1 >= 2 ? true : null),
    15_000,
    700,
  );
  check("resultOpensChat", Boolean(opened));
  await shot("search-03-opened");

  // ── 4. After a reload the index rebuilds from the session files ──
  await page.reload({ waitUntil: "load" });
  await page.waitForTimeout(2_000);
  await login();
  await startNewChat();
  const hitsAfterReload = await searchFor(UNIQUE_WORD, UNIQUE_WORD);
  check("transcriptHitAfterReload", Boolean(hitsAfterReload));
  await shot("search-04-after-reload");

  // ── 5. A 1-character query is title-only and does not error ──
  await page.keyboard.press("Escape");
  await page.waitForTimeout(400);
  const shortHits = await searchFor("z", UNIQUE_WORD);
  check("shortQueryNoSnippetHit", !shortHits, { hits: shortHits ?? [] });
  await page.keyboard.press("Escape");

  const relevantConsoleErrors = consoleErrors.filter(
    (entry) => !/zcode\.z\.ai|ERR_FAILED|Tooltip is changing|v4-store/.test(entry),
  );
  check("noConsoleErrors", relevantConsoleErrors.length === 0, relevantConsoleErrors.slice(0, 10));
} catch (error) {
  failures.push("exception");
  details.exception = error instanceof Error ? (error.stack ?? error.message) : String(error);
  await shot("search-99-exception").catch(() => undefined);
} finally {
  try {
    await browser.close();
  } catch {
    // Teardown failure must not hide the check result.
  }
}

const summary = {
  ok: failures.length === 0,
  failures,
  checks,
  details,
};
fs.mkdirSync(OUT_DIR, { recursive: true });
fs.writeFileSync(
  path.join(OUT_DIR, "search-summary.json"),
  `${JSON.stringify(summary, null, 2)}\n`,
);
console.log(JSON.stringify(summary, null, 2));
process.exit(failures.length === 0 ? 0 : 1);
