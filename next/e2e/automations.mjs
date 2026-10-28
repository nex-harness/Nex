// Automations E2E for the next/ stack (docs/next/automations-views-search.md §A):
//
//   1. Settings → Automations lists automations through the server-owned
//      channel (previously the page was empty: nothing served the methods).
//   2. Create manually: fill name + instructions, save (custom minute
//      schedule), the card appears in the list.
//   3. Run now from the card menu → status queued; the run history shows a
//      dispatched run with a sessionId (the runner received the
//      createSession command and answered).
//   4. The dispatched session appears as a task row (faux provider replies).
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

const AUTOMATION_NAME = `e2e-auto-${Math.floor(Math.random() * 1_000_000)}`;
const PROMPT_TEXT = "Reply with exactly: automation-ok";

if (!TOKEN) {
  console.error("[automations] NEX_E2E_TOKEN is required (the browser login token).");
  process.exit(2);
}
if (!WORKSPACE) {
  console.error("[automations] NEX_E2E_WORKSPACE is required (the runner workspace).");
  process.exit(2);
}

const playwrightEntry = path.join(REPO_ROOT, "node_modules", "playwright-core", "index.mjs");
if (!fs.existsSync(playwrightEntry)) {
  console.error(
    `[automations] playwright-core is missing at ${playwrightEntry}; run pnpm install at the repo root.`,
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
    console.error("[automations] no Chrome/Chromium executable found; set NEX_E2E_CHROME.");
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

const browser = await chromium.launch({ executablePath: chromePath, headless: true });
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
page.on("pageerror", (error) => consoleErrors.push(`PAGEERROR: ${error.message}`));
page.on("console", (message) => {
  if (message.type() !== "error" && message.type() !== "warning") return;
  consoleErrors.push(`CONSOLE[${message.type()}]: ${message.text()}`.slice(0, 300));
});

const shot = (name) => page.screenshot({ path: path.join(OUT_DIR, `${name}.png`) });
fs.mkdirSync(OUT_DIR, { recursive: true });

try {
  // ── login ──
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

  // The automations channel answers for any workspace; the page is global.
  // The E2E stack starts with wiped sessions: create a task first so the
  // workspace shell (settings entry) is mounted, like the other suites.
  if ((await page.locator('[data-testid^="task-item-"]').count()) === 0) {
    await composer.click();
    await page.waitForTimeout(1_000);
    await page.keyboard.type("Reply with exactly: ready");
    await page.keyboard.press("Enter");
    await waitFor(
      async () => ((await page.locator('[data-testid^="task-item-"]').count()) > 0 ? true : null),
      60_000,
      1_000,
    );
  }
  const firstRow = page.locator('[data-testid="task-item-"]').first();
  if ((await firstRow.count()) > 0) {
    await firstRow.click({ timeout: 5_000 }).catch(() => undefined);
    await page.waitForTimeout(1_500);
  }

  // ── 1. Automations main view opens and answers (the wiring this suite locks) ──
  // 入口是侧栏的 automations-open 按钮：设置页分区被 HIDDEN_SETTINGS_SECTIONS
  // 有意隐藏（Automations 是 workspace 主视图，packages/ui/src/lib/
  // settingsNavigation.ts），不是缺接线。
  await page.evaluate(() => {
    for (const host of document.querySelectorAll("#nex-toast-host")) {
      host.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
      const dismiss = host.querySelector("button[aria-label]");
      if (dismiss) dismiss.click();
    }
  });
  await page.waitForTimeout(300);
  await page.locator('[data-testid="automations-open"]').first().click({ timeout: 15_000 });
  await page.waitForTimeout(2_500);
  await shot("automations-01-page");
  // automations-list 只在列表非空时渲染；空态与非空态都有创建菜单，用它判定页面已加载。
  const listVisible = await page
    .locator('[data-testid="automation-create-menu"], [data-testid="automations-list"]')
    .first()
    .waitFor({ state: "visible", timeout: 15_000 })
    .then(() => true)
    .catch(() => false);
  check("automationsViewLoaded", listVisible, {
    tail: (await bodyText()).slice(0, 300),
  });

  // ── 2. Create manually ──
  const createMenu = page.locator('[data-testid="automation-create-menu"]').first();
  const createManually = page.locator('[data-testid="automation-create-manually"]').first();
  await createMenu.waitFor({ state: "visible", timeout: 10_000 });
  await createMenu.click();
  await page.waitForTimeout(600);
  await createManually.waitFor({ state: "visible", timeout: 10_000 });
  // 弹层/下拉菜单可能拦截 pointer events（元素可见但 actionability 卡住）：
  // toast 清理 + Escape 关菜单重试 + DOM 派发点击兜底（task-actions 套件同款）。
  await createManually.click({ timeout: 8_000 }).catch(async () => {
    await page.evaluate(() => {
      for (const host of document.querySelectorAll("#nex-toast-host")) {
        host.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
        const dismiss = host.querySelector("button[aria-label]");
        if (dismiss) dismiss.click();
      }
    });
    await page.waitForTimeout(300);
    await createManually.click({ timeout: 8_000 }).catch(async () => {
      await createManually.evaluate((element) => {
        element.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
      });
    });
  });
  await page.waitForTimeout(2_000);
  await shot("automations-02-create-form");

  const titleInput = page.locator('[data-testid="automation-form-title"]');
  await titleInput.waitFor({ state: "visible", timeout: 10_000 });
  await titleInput.fill(AUTOMATION_NAME);
  const promptInput = page.locator('[data-testid="automation-form-prompt"]');
  await promptInput.fill(PROMPT_TEXT);

  // Schedule is REQUIRED (cronExpr 必填；新建默认不带日程，submit 一直 disabled
  // 直到「Add schedule」里选一个预设)。「Add schedule」→ daily 预设。
  const addSchedule = page
    .getByRole("button")
    .filter({ hasText: /^(Add schedule|添加计划)$/u })
    .first();
  const addScheduleFound = await addSchedule.waitFor({ state: "visible", timeout: 8_000 }).then(
    () => true,
    () => false,
  );
  check("addScheduleButtonVisible", addScheduleFound);
  await addSchedule.click().catch(async () => {
    await addSchedule.evaluate((element) => {
      element.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });
  });
  await page.waitForTimeout(600);
  const dailyOption = page.locator('[data-testid="automation-frequency-option-daily"]').first();
  const dailyFound = await dailyOption.waitFor({ state: "visible", timeout: 8_000 }).then(
    () => true,
    () => false,
  );
  check("dailyPresetOffered", dailyFound, {
    options: await page
      .locator('[data-testid^="automation-frequency-option-"]')
      .evaluateAll((elements) => elements.map((el) => el.getAttribute("data-testid"))),
  });
  if (dailyFound) await dailyOption.click();
  await page.waitForTimeout(800);

  await shot("automations-03-filled");
  const submit = page.locator('[data-testid="automation-form-submit"]').first();
  await page.waitForTimeout(1_500);
  const submitEnabled = await submit.isEnabled();
  check("submitEnabledAfterSchedule", submitEnabled, {
    disabled: await submit.isDisabled(),
  });
  if (submitEnabled) {
    await submit.click();
  } else {
    // 诊断性兜底：DOM 断言缺失的必填状态。
    check("submitClickable", false, { bodyTail: (await bodyText()).slice(0, 500) });
  }
  await page.waitForTimeout(2_500);

  const cardAppeared = await waitFor(
    async () => {
      const cards = page.locator('[data-testid="automation-card"]');
      const count = await cards.count();
      if (count === 0) return null;
      return (await cards.filter({ hasText: AUTOMATION_NAME }).count()) > 0 ? true : null;
    },
    15_000,
    1_000,
  );
  check("automationCardListed", Boolean(cardAppeared), {
    tail: (await bodyText()).slice(0, 400),
  });
  await shot("automations-04-listed");

  // ── 3. Run now from the card menu ──
  const card = page
    .locator('[data-testid="automation-card"]')
    .filter({ hasText: AUTOMATION_NAME })
    .first();
  await card.hover();
  await page.waitForTimeout(400);
  const cardMenu = page.locator('[data-testid="automation-card-menu"]').first();
  await cardMenu.click().catch(async () => {
    // The menu button may sit under a hover layer; open it via the card.
    await card.click();
  });
  await page.waitForTimeout(600);
  const runNowItem = page
    .getByRole("menuitem")
    .filter({ hasText: /^(Run now|立即运行)$/u })
    .first();
  const runNowFound = await runNowItem.waitFor({ state: "visible", timeout: 8_000 }).then(
    () => true,
    () => false,
  );
  check("runNowMenuItemVisible", runNowFound);
  if (runNowFound) {
    await runNowItem.click();
    await page.waitForTimeout(1_500);
    // The queued toast / busy state: the run must reach "queued" then dispatch.
    // Run history: open the card's run list (click the card) and look for a
    // dispatched run with a sessionId.
    await page.keyboard.press("Escape").catch(() => undefined);
    await card.click().catch(() => undefined);
    await page.waitForTimeout(1_500);
    await shot("automations-05-run-started");
  }

  // Run history: the edit view's History tab lists runs; a dispatched run that
  // got its session reads In progress / Succeeded (never Failure — that is the
  // failed_to_dispatch state). The round-trip server → runner createSession
  // needs a moment, and the tab refreshes while it stays open.
  await page
    .getByRole("tab", { name: /^(History|历史)$/u })
    .first()
    .click({ timeout: 8_000 })
    .catch(() => undefined);
  const dispatchedRun = await waitFor(
    async () => {
      const rows = await page
        .locator("tbody tr")
        .evaluateAll((elements) => elements.map((el) => el.textContent ?? ""));
      if (rows.some((text) => /Failure|失败/u.test(text))) return false;
      return rows.some((text) => /In progress|Succeeded|进行中|成功/u.test(text)) ? true : null;
    },
    30_000,
    1_500,
  );
  check("runDispatchedWithSession", Boolean(dispatchedRun), {
    tail: (await bodyText()).slice(0, 500),
  });
  await shot("automations-06-run-history");

  // ── 4. The session appears as a task row with the cron badge ──
  // Back to the workspace: the automation session shows in the task list.
  await page
    .locator('[data-testid="automations-open"]')
    .first()
    .click()
    .catch(() => undefined);
  await page.waitForTimeout(2_000);
  await page.keyboard.press("Escape").catch(() => undefined);
  const taskRows = page.locator('[data-testid^="task-item-"]');
  const automationTaskFound = await waitFor(
    async () => {
      const count = await taskRows.count();
      if (count === 0) return null;
      // runner 给 automation 会话打 cronAutomationId，任务行渲染为时钟图标
      // （TaskListItem 的 data-cron-task-icon），行文本里没有 automation 名称。
      const cronRows = await taskRows.evaluateAll(
        (elements) =>
          elements.filter((el) => el.querySelector('[data-cron-task-icon="true"]')).length,
      );
      return cronRows > 0 ? true : null;
    },
    20_000,
    1_000,
  );
  check("automationTaskRowAppears", Boolean(automationTaskFound), {
    rowCount: await taskRows.count(),
  });
  await shot("automations-07-task-row");

  // Clean up: delete the automation (leaves the stack for the next suite).
  await page
    .locator('[data-testid="automations-open"]')
    .first()
    .click()
    .catch(() => undefined);
  await page.waitForTimeout(1_500);
  const cardAgain = page
    .locator('[data-testid="automation-card"]')
    .filter({ hasText: AUTOMATION_NAME })
    .first();
  if ((await cardAgain.count()) > 0) {
    await cardAgain.hover();
    await page
      .locator('[data-testid="automation-card-menu"]')
      .first()
      .click()
      .catch(() => undefined);
    await page.waitForTimeout(600);
    const deleteItem = page.locator('[data-testid="automation-action-delete"]').first();
    if ((await deleteItem.count()) > 0) {
      await deleteItem.click().catch(() => undefined);
      await page.waitForTimeout(1_500);
      // The delete confirm (destructive) if rendered.
      const confirm = page.getByRole("button", { name: /^(确认|Confirm|删除|Delete)$/u }).first();
      if ((await confirm.count()) > 0) {
        await confirm.click().catch(() => undefined);
        await page.waitForTimeout(1_000);
      }
    }
  }

  const relevantConsoleErrors = consoleErrors.filter(
    (entry) => !/zcode\.z\.ai|ERR_FAILED|Tooltip is changing|v4-store/.test(entry),
  );
  check("noConsoleErrors", relevantConsoleErrors.length === 0, relevantConsoleErrors.slice(0, 10));
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

const summary = {
  ok: failures.length === 0,
  failures,
  checks,
  details,
};
fs.mkdirSync(OUT_DIR, { recursive: true });
fs.writeFileSync(
  path.join(OUT_DIR, "automations-summary.json"),
  `${JSON.stringify(summary, null, 2)}\n`,
);
console.log(JSON.stringify(summary, null, 2));
process.exit(failures.length === 0 ? 0 : 1);
