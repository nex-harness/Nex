import assert from "node:assert/strict";
import test from "node:test";
import {
  buildServerWorkspaceIdentity,
  isServerWorkspaceIdentity,
  parseServerWorkspaceIdentity,
} from "@nex/shared";

// 运行（在 packages/ui 下）：
//   pnpm exec tsx --tsconfig tsconfig.json --test test/workspaceIdentity.test.ts
// shared 的 server-workspace-identity 经包 barrel 引入，与业务侧走同一条解析路径。

test("build/parse round-trip keeps the path verbatim", () => {
  const identity = buildServerWorkspaceIdentity("runner_0123456789abcdef", "/home/me/app");
  assert.equal(identity, "runner:runner_0123456789abcdef:/home/me/app");
  assert.deepEqual(parseServerWorkspaceIdentity(identity!), {
    runnerId: "runner_0123456789abcdef",
    workspacePath: "/home/me/app",
  });
});

test("path may contain ':' because only the first two separators are structural", () => {
  // Windows 盘符与带冒号的 POSIX 路径都必须原样保留；Go ParseIdentity 同规则。
  const windows = buildServerWorkspaceIdentity("alpha", String.raw`C:\Users\me\app`);
  assert.deepEqual(parseServerWorkspaceIdentity(windows!), {
    runnerId: "alpha",
    workspacePath: String.raw`C:\Users\me\app`,
  });
  assert.deepEqual(parseServerWorkspaceIdentity("runner:beta:/home/me/a:b:c"), {
    runnerId: "beta",
    workspacePath: "/home/me/a:b:c",
  });
});

test("invalid identities return null", () => {
  const invalid = [
    "",
    "   ",
    "runner:alpha",
    "runner:alpha:",
    "runner::/w",
    "runner:a b:/w",
    "runner:a\tb:/w",
    "remote:ssh:host:22:user:/w",
    "/plain/path",
  ];
  for (const identity of invalid) {
    assert.equal(parseServerWorkspaceIdentity(identity), null, identity);
    assert.equal(isServerWorkspaceIdentity(identity), false, identity);
  }
});

test("build rejects unusable runner ids and empty paths", () => {
  assert.equal(buildServerWorkspaceIdentity("", "/w"), null);
  assert.equal(buildServerWorkspaceIdentity("a:b", "/w"), null);
  assert.equal(buildServerWorkspaceIdentity("a b", "/w"), null);
  assert.equal(buildServerWorkspaceIdentity("alpha", ""), null);
});

test("parse trims surrounding whitespace but not the path", () => {
  assert.deepEqual(parseServerWorkspaceIdentity("  runner:alpha:/w  "), {
    runnerId: "alpha",
    workspacePath: "/w",
  });
});
