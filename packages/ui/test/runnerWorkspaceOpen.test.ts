import assert from "node:assert/strict";
import test from "node:test";
import type { ServerWorkspaceOption } from "@nex/shared";
import {
  findRunnerWorkspaceToOpen,
  pollRunnerWorkspaceToOpen,
} from "../src/lib/runnerWorkspaceOpen.js";

// 运行：pnpm --dir packages/ui exec tsx --tsconfig tsconfig.json --test test/runnerWorkspaceOpen.test.ts

const workspaces: ServerWorkspaceOption[] = [
  {
    path: "/home/me",
    label: "alpha: me",
    workspaceIdentity: "runner:alpha:/home/me",
    runnerId: "alpha",
    runnerName: "mac-studio",
  },
  {
    path: "/var/app",
    label: "beta: app",
    workspaceIdentity: "runner:beta:/var/app",
    runnerId: "beta",
    runnerName: "win-box",
  },
];

test("opens the first workspace of that exact machine", () => {
  assert.equal(findRunnerWorkspaceToOpen(workspaces, "alpha")?.path, "/home/me");
  assert.equal(findRunnerWorkspaceToOpen(workspaces, "beta")?.path, "/var/app");
  // 未知机器（hello 还没到）或空列表 → 继续等，不能随便打开一台。
  assert.equal(findRunnerWorkspaceToOpen(workspaces, "gamma"), null);
  assert.equal(findRunnerWorkspaceToOpen(null, "alpha"), null);
  assert.equal(findRunnerWorkspaceToOpen([], "alpha"), null);
});

test("same path on two machines still opens the requested machine", () => {
  const sharedPath: ServerWorkspaceOption[] = [
    { path: "/srv/app", workspaceIdentity: "runner:alpha:/srv/app", runnerId: "alpha" },
    { path: "/srv/app", workspaceIdentity: "runner:beta:/srv/app", runnerId: "beta" },
  ];
  assert.equal(
    findRunnerWorkspaceToOpen(sharedPath, "beta")?.workspaceIdentity,
    "runner:beta:/srv/app",
  );
  assert.equal(
    findRunnerWorkspaceToOpen(sharedPath, "alpha")?.workspaceIdentity,
    "runner:alpha:/srv/app",
  );
});

test("entries without an identity are not opened by path alone", () => {
  // 没有 identity 无法表达“哪台机器”；回落到 path-only 会打开 server 本地目录。
  assert.equal(findRunnerWorkspaceToOpen([{ path: "/w", runnerId: "alpha" }], "alpha"), null);
});

test("poll returns as soon as hello arrives and gives up after the timeout", async () => {
  let clock = 0;
  let loads = 0;
  const found = await pollRunnerWorkspaceToOpen({
    // 前两次仍是旧列表，第三次才出现新机器（hello 迟到）。
    load: async () => {
      loads += 1;
      return loads >= 3 ? workspaces : [workspaces[0]!];
    },
    runnerId: "beta",
    timeoutMs: 5_000,
    intervalMs: 500,
    now: () => clock,
    sleep: async (ms) => {
      clock += ms;
    },
  });
  assert.equal(found?.path, "/var/app");
  assert.equal(loads, 3);

  clock = 0;
  loads = 0;
  const missing = await pollRunnerWorkspaceToOpen({
    load: async () => {
      loads += 1;
      return [workspaces[0]!];
    },
    runnerId: "gamma",
    timeoutMs: 1_000,
    intervalMs: 500,
    now: () => clock,
    sleep: async (ms) => {
      clock += ms;
    },
  });
  assert.equal(missing, null);
  // 1s / 500ms → 第一次 + 两次重试后超时，不会无限轮询。
  assert.equal(loads, 3);
});
