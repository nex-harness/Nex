import assert from "node:assert/strict";
import test from "node:test";
import type { ServerWorkspaceOption } from "@nex/shared";
import {
  buildDirectoryBrowserMachineOptions,
  resolveDirectoryBrowserMachineStartPath,
  shouldShowDirectoryBrowserMachineSelector,
} from "../src/lib/directoryBrowserMachines.js";

// 运行：pnpm --dir packages/ui exec tsx --tsconfig tsconfig.json --test test/directoryBrowserMachines.test.ts

const twoMachines: ServerWorkspaceOption[] = [
  {
    path: "/home/me/app",
    workspaceIdentity: "runner:alpha:/home/me/app",
    runnerId: "alpha",
    runnerName: "mac-studio",
    runnerOs: "darwin",
  },
  {
    path: "/home/me/app",
    workspaceIdentity: "runner:beta:/home/me/app",
    runnerId: "beta",
    runnerName: "wsl-box",
    runnerOs: "linux",
  },
  {
    path: "/home/me/web",
    workspaceIdentity: "runner:alpha:/home/me/web",
    runnerId: "alpha",
    runnerName: "mac-studio",
    runnerOs: "darwin",
  },
];

test("groups one option per machine with name, os and workspace roots", () => {
  const options = buildDirectoryBrowserMachineOptions(twoMachines);
  assert.deepEqual(options, [
    {
      id: "alpha",
      name: "mac-studio",
      os: "darwin",
      workspacePaths: ["/home/me/app", "/home/me/web"],
    },
    { id: "beta", name: "wsl-box", os: "linux", workspacePaths: ["/home/me/app"] },
  ]);
});

test("selector only appears with more than one machine", () => {
  assert.equal(shouldShowDirectoryBrowserMachineSelector([]), false);
  assert.equal(
    shouldShowDirectoryBrowserMachineSelector(buildDirectoryBrowserMachineOptions(null)),
    false,
  );
  assert.equal(
    shouldShowDirectoryBrowserMachineSelector(
      buildDirectoryBrowserMachineOptions([twoMachines[0]]),
    ),
    false,
  );
  assert.equal(
    shouldShowDirectoryBrowserMachineSelector(buildDirectoryBrowserMachineOptions(twoMachines)),
    true,
  );
});

test("legacy server without runner info yields one local option", () => {
  const options = buildDirectoryBrowserMachineOptions([{ path: "/srv/legacy" }]);
  assert.deepEqual(options, [{ id: "__local__", name: "", workspacePaths: ["/srv/legacy"] }]);
});

test("derives runner id from identity when runnerId is missing", () => {
  const options = buildDirectoryBrowserMachineOptions([
    { path: "C:\\work", workspaceIdentity: "runner:zeta:C:\\work", runnerName: "win-box" },
  ]);
  assert.equal(options[0]?.id, "zeta");
  assert.equal(options[0]?.os, undefined);
});

test("start path prefers the machine's first workspace root and otherwise keeps the current path", () => {
  const [alpha] = buildDirectoryBrowserMachineOptions(twoMachines);
  assert.ok(alpha);
  assert.equal(resolveDirectoryBrowserMachineStartPath(alpha, "/fallback"), "/home/me/app");
  assert.equal(resolveDirectoryBrowserMachineStartPath(null, "/fallback"), "/fallback");
  assert.equal(
    resolveDirectoryBrowserMachineStartPath(
      { id: "x", name: "x", workspacePaths: ["  "] },
      "/keep",
    ),
    "/keep",
  );
});
