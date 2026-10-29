import assert from "node:assert/strict";
import test from "node:test";
import {
  RUNNER_CAPABILITY_WORKSPACES,
  resolveRunnerChooseFolderState,
  runnerSupports,
} from "../src/lib/runnerCapabilities.js";

// 运行：pnpm --dir packages/ui exec tsx --tsconfig tsconfig.json --test test/runnerCapabilities.test.ts

test("runnerSupports matches exact capability names and ignores case/space", () => {
  assert.equal(runnerSupports(["runner-workspaces"], RUNNER_CAPABILITY_WORKSPACES), true);
  assert.equal(runnerSupports(["  Runner-Workspaces  "], RUNNER_CAPABILITY_WORKSPACES), true);
  assert.equal(runnerSupports(["search", "self-update"], RUNNER_CAPABILITY_WORKSPACES), false);
});

test("missing capabilities mean a legacy runner with no optional features", () => {
  // 旧 runner 没有 hello.capabilities：不能当成“全部支持”，否则会回 Unknown method。
  assert.equal(runnerSupports(undefined, RUNNER_CAPABILITY_WORKSPACES), false);
  assert.equal(runnerSupports(null, RUNNER_CAPABILITY_WORKSPACES), false);
  assert.equal(runnerSupports([], RUNNER_CAPABILITY_WORKSPACES), false);
  assert.equal(runnerSupports(["runner-workspaces"], ""), false);
});

test("choose-folder is enabled when the machine reports runner-workspaces", () => {
  assert.deepEqual(
    resolveRunnerChooseFolderState({
      runnerId: "alpha",
      capabilities: ["runner-workspaces"],
      canChooseFolder: true,
      canUpdate: true,
    }),
    { kind: "enabled" },
  );
});

test("choose-folder is disabled with an update offer when the capability is missing", () => {
  assert.deepEqual(
    resolveRunnerChooseFolderState({
      runnerId: "alpha",
      capabilities: ["search"],
      canChooseFolder: true,
      canUpdate: true,
    }),
    { kind: "disabled", showUpdate: true },
  );
  // 旧 server 的 runner 条目没有 capabilities，同样走禁用分支。
  assert.deepEqual(
    resolveRunnerChooseFolderState({
      runnerId: "alpha",
      canChooseFolder: true,
      canUpdate: true,
    }),
    { kind: "disabled", showUpdate: true },
  );
});

test("choose-folder is disabled without an update action when the platform cannot update", () => {
  assert.deepEqual(
    resolveRunnerChooseFolderState({
      runnerId: "alpha",
      capabilities: [],
      canChooseFolder: true,
      canUpdate: false,
    }),
    { kind: "disabled", showUpdate: false },
  );
});

test("choose-folder stays hidden without a runnerId or platform support", () => {
  // 旧 server 的“本机”分组没有 runnerId，无法构造 `runner:<id>:~`。
  assert.deepEqual(
    resolveRunnerChooseFolderState({
      capabilities: ["runner-workspaces"],
      canChooseFolder: true,
      canUpdate: true,
    }),
    { kind: "hidden" },
  );
  assert.deepEqual(
    resolveRunnerChooseFolderState({
      runnerId: "alpha",
      capabilities: ["runner-workspaces"],
      canChooseFolder: false,
      canUpdate: true,
    }),
    { kind: "hidden" },
  );
  assert.deepEqual(
    resolveRunnerChooseFolderState({
      runnerId: "   ",
      capabilities: ["runner-workspaces"],
      canChooseFolder: true,
      canUpdate: true,
    }),
    { kind: "hidden" },
  );
});
