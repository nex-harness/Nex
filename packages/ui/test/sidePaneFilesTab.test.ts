import assert from "node:assert/strict";
import test from "node:test";
import { resolveOpenTabLauncherItemIds } from "../src/app-shell/animatedSidePanePanelModel.js";
import { activateFilesSidePane, getVisibleSidePaneTabs } from "../src/lib/workspaceSidePane.js";

// 运行：pnpm exec tsx --tsconfig packages/ui/tsconfig.json --test packages/ui/test/sidePaneFilesTab.test.ts
// workspaceSidePane 内部用 `@/` 别名，必须带 ui 的 tsconfig 才能解析。

test("launcher offers Files before Terminal on every surface", () => {
  const ids = resolveOpenTabLauncherItemIds({
    developerToolsEnabled: false,
    hasReviewTab: false,
  });
  assert.deepEqual(ids, ["review", "files", "terminal", "browser"]);
});

test("launcher keeps Files when the embedded browser is unavailable (web)", () => {
  const ids = resolveOpenTabLauncherItemIds({
    developerToolsEnabled: true,
    hasReviewTab: true,
    supportsEmbeddedBrowser: false,
  });
  assert.deepEqual(ids, ["files", "terminal", "developer-tools"]);
});

test("opening Files twice keeps a single tab and activates it", () => {
  const second = activateFilesSidePane(activateFilesSidePane(null));
  assert.equal(second.tabs.filter((tab) => tab.type === "files").length, 1);
  assert.equal(second.activeTabId, "files");
});

test("Files tab is visible from every conversation in the workspace", () => {
  const state = activateFilesSidePane(null);
  for (const ownerTaskId of ["task-a", "task-b", null]) {
    const visible = getVisibleSidePaneTabs(state.tabs, { workspaceKey: "/repo", ownerTaskId });
    assert.deepEqual(
      visible.map((tab) => tab.id),
      ["files"],
    );
  }
});
