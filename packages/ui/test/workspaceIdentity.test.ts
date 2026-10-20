import assert from "node:assert/strict";
import test from "node:test";
import { isClientRemoteWorkspaceIdentity } from "../src/lib/workspaceIdentity.js";
import { shouldEnableWorkspaceRpc } from "../src/lib/workspaceRpcAvailability.js";
import { isSameWorkspaceTab } from "../src/store/tabWorkspaceIdentity.js";
import {
  buildPersistedWorkspaceSessionEntries,
  hasRemoteWorkspaceIdentity,
} from "../src/lib/remoteWorkspaceHistory.js";
import { appSettingsSchema } from "@nex/shared";
import type { WindowTabState } from "../src/store/tabStore.js";

// 运行：pnpm --dir packages/ui exec tsx --tsconfig tsconfig.json --test test/workspaceIdentity.test.ts

test("only remote: identities count as client remote", () => {
  assert.equal(isClientRemoteWorkspaceIdentity("remote:ssh:host:22:user:/w"), true);
  assert.equal(isClientRemoteWorkspaceIdentity("remote:wsl:Ubuntu:/w"), true);
  assert.equal(isClientRemoteWorkspaceIdentity("remote:docker:box:/w"), true);
  assert.equal(isClientRemoteWorkspaceIdentity("runner:alpha:/w"), false);
  assert.equal(isClientRemoteWorkspaceIdentity("  runner:alpha:/w  "), false);
  assert.equal(isClientRemoteWorkspaceIdentity(""), false);
  assert.equal(isClientRemoteWorkspaceIdentity("   "), false);
  assert.equal(isClientRemoteWorkspaceIdentity(undefined), false);
  assert.equal(isClientRemoteWorkspaceIdentity(null), false);
});

test("runner identity does not put workspace RPC into remote-waiting", () => {
  assert.equal(shouldEnableWorkspaceRpc({ workspaceIdentity: "runner:alpha:/w" }), true);
  assert.equal(shouldEnableWorkspaceRpc({ workspaceIdentity: "remote:ssh:h:22:u:/w" }), false);
  assert.equal(
    shouldEnableWorkspaceRpc({ workspaceIdentity: "remote:ssh:h:22:u:/w", remoteSessionId: "s1" }),
    true,
  );
});

test("legacy tab matching treats runner tabs as non-remote", () => {
  // 无 identity 的旧调用按 path 复用时，runner tab 不应被当成“远端 tab”排除。
  assert.equal(isSameWorkspaceTab({ workspacePath: "/w" }, "/w"), true);
  assert.equal(
    isSameWorkspaceTab({ workspacePath: "/w", workspaceIdentity: "runner:a:/w" }, "/w"),
    true,
  );
  assert.equal(
    isSameWorkspaceTab({ workspacePath: "/w", workspaceIdentity: "remote:ssh:h:22:u:/w" }, "/w"),
    false,
  );
});

test("hasRemoteWorkspaceIdentity keeps runner tabs as local entries", () => {
  assert.equal(hasRemoteWorkspaceIdentity({ workspaceIdentity: "runner:a:/w" }), false);
  assert.equal(hasRemoteWorkspaceIdentity({ workspaceIdentity: "remote:ssh:h:22:u:/w" }), true);
  assert.equal(hasRemoteWorkspaceIdentity({ remoteSessionId: "s1" }), true);
});

function workspaceTab(path: string, workspaceIdentity?: string): WindowTabState {
  return {
    id: `tab-${path}-${workspaceIdentity ?? "local"}`,
    kind: "workspace",
    workspacePath: path,
    label: path,
    ...(workspaceIdentity ? { workspaceIdentity } : {}),
  } as WindowTabState;
}

test("local persistence round-trip keeps runner identity", () => {
  // 保存：runner tab 必须写成 local entry 并携带 identity。
  const entries = buildPersistedWorkspaceSessionEntries(
    [workspaceTab("/srv/app", "runner:beta:/srv/app"), workspaceTab("/local/repo")],
    new Map(),
  );
  assert.deepEqual(entries, [
    { kind: "local", workspacePath: "/srv/app", workspaceIdentity: "runner:beta:/srv/app" },
    { kind: "local", workspacePath: "/local/repo" },
  ]);

  // 读取：schema + preprocess 迁移都不能丢掉 workspaceIdentity。
  // workspacePurpose 的默认值 project 是既有 schema 行为，这里只断言身份字段与路径。
  const settings = appSettingsSchema.parse({
    lastWorkspaceSession: entries,
    lastActiveTabIndex: 0,
  });
  assert.deepEqual(
    settings.lastWorkspaceSession.map((entry) => ({
      kind: entry.kind,
      workspacePath: entry.workspacePath,
      workspaceIdentity: entry.kind === "local" ? entry.workspaceIdentity : undefined,
      workspacePurpose: entry.kind === "local" ? entry.workspacePurpose : undefined,
    })),
    [
      {
        kind: "local",
        workspacePath: "/srv/app",
        workspaceIdentity: "runner:beta:/srv/app",
        workspacePurpose: "project",
      },
      {
        kind: "local",
        workspacePath: "/local/repo",
        workspaceIdentity: undefined,
        workspacePurpose: "project",
      },
    ],
  );
});

test("legacy settings without identity still parse", () => {
  const settings = appSettingsSchema.parse({
    lastWorkspaceSession: [{ kind: "local", workspacePath: "/legacy" }],
  });
  assert.deepEqual(settings.lastWorkspaceSession, [
    { kind: "local", workspacePath: "/legacy", workspacePurpose: "project" },
  ]);
});
