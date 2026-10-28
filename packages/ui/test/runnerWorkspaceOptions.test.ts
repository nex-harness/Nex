import assert from "node:assert/strict";
import test from "node:test";
import type { ServerWorkspaceOption } from "@nex/shared";
import {
  groupRunnerWorkspaceOptions,
  hasSelectableRunnerWorkspaces,
  resolveRunnerWorkspaceMachineLabel,
  resolveRunnerWorkspaceTitle,
} from "../src/lib/runnerWorkspaceOptions.js";

// 运行：pnpm --dir packages/ui exec tsx --tsconfig tsconfig.json --test test/runnerWorkspaceOptions.test.ts

const options: ServerWorkspaceOption[] = [
  {
    path: "/srv/app",
    label: "alpha: app",
    workspaceIdentity: "runner:alpha:/srv/app",
    runnerId: "alpha",
    runnerName: "mac-studio",
    runnerOs: "darwin",
  },
  {
    path: "/srv/web",
    label: "alpha: web",
    workspaceIdentity: "runner:alpha:/srv/web",
    runnerId: "alpha",
    runnerName: "mac-studio",
    runnerOs: "darwin",
  },
  {
    path: "/srv/app",
    label: "beta: app",
    workspaceIdentity: "runner:beta:/srv/app",
    runnerId: "beta",
    runnerName: "win-box",
    runnerOs: "windows",
  },
];

test("groups by runner and keeps stable machine order", () => {
  const groups = groupRunnerWorkspaceOptions(options);
  assert.deepEqual(
    groups.map((group) => group.runnerName),
    ["mac-studio", "win-box"],
  );
  assert.deepEqual(
    groups[0]!.workspaces.map((workspace) => workspace.path),
    ["/srv/app", "/srv/web"],
  );
  assert.equal(groups[1]!.runnerOs, "windows");
});

test("same path on two machines stays in two groups", () => {
  const groups = groupRunnerWorkspaceOptions(options);
  // 同路径切到不同机器是本次功能的核心：分组不能按 path 去重。
  assert.equal(groups[0]!.workspaces[0]!.path, groups[1]!.workspaces[0]!.path);
  assert.equal(groups[0]!.workspaces[0]!.workspaceIdentity, "runner:alpha:/srv/app");
  assert.equal(groups[1]!.workspaces[0]!.workspaceIdentity, "runner:beta:/srv/app");
});

test("groups carry the runnerId needed for the machine-scope choose-folder action", () => {
  // 「Choose folder…」用 runnerId 构造 `runner:<id>:~`，分组必须带出它。
  const groups = groupRunnerWorkspaceOptions(options);
  assert.deepEqual(
    groups.map((group) => group.runnerId),
    ["alpha", "beta"],
  );
  // 旧 server 的“本机”分组没有 runnerId，UI 据此隐藏该动作。
  const localOnly = groupRunnerWorkspaceOptions([{ path: "/a" }]);
  assert.equal(localOnly[0]!.runnerId, undefined);
});

test("missing runner name falls back to runner id and single-runner entries group together", () => {
  const groups = groupRunnerWorkspaceOptions([
    { path: "/w", runnerId: "runner_abc" },
    { path: "/x", runnerId: "runner_abc" },
  ]);
  assert.equal(groups.length, 1);
  assert.equal(groups[0]!.runnerName, "runner_abc");
});

test("entries without runner/identity form one local group", () => {
  const groups = groupRunnerWorkspaceOptions([{ path: "/a" }, { path: "/b" }]);
  assert.equal(groups.length, 1);
  assert.equal(groups[0]!.runnerName, "");
  assert.equal(groups[0]!.workspaces.length, 2);
});

test("selectable detection ignores blank paths", () => {
  assert.equal(hasSelectableRunnerWorkspaces(options), true);
  assert.equal(hasSelectableRunnerWorkspaces([]), false);
  assert.equal(hasSelectableRunnerWorkspaces(null), false);
  assert.equal(hasSelectableRunnerWorkspaces(undefined), false);
  assert.equal(hasSelectableRunnerWorkspaces([{ path: "   " }]), false);
});

test("title prefers server label and falls back to path leaf", () => {
  assert.equal(resolveRunnerWorkspaceTitle(options[0]!), "alpha: app");
  assert.equal(resolveRunnerWorkspaceTitle({ path: String.raw`C:\Users\me\repo` }), "repo");
});

test("machine label uses cached runner name, then runner id", () => {
  assert.equal(
    resolveRunnerWorkspaceMachineLabel("runner:alpha:/srv/app", {
      "runner:alpha:/srv/app": "mac-studio",
    }),
    "mac-studio",
  );
  // 缓存缺失（例如直接刷新页面且 server-info 尚未返回）时仍要能说明机器。
  assert.equal(resolveRunnerWorkspaceMachineLabel("runner:alpha:/srv/app", {}), "alpha");
  // 非 runner 身份不改变 header 现有展示。
  assert.equal(resolveRunnerWorkspaceMachineLabel("remote:ssh:h:22:u:/w", {}), null);
  assert.equal(resolveRunnerWorkspaceMachineLabel("/local", {}), null);
  assert.equal(resolveRunnerWorkspaceMachineLabel(null, {}), null);
});
