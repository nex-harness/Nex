import assert from "node:assert/strict";
import test from "node:test";
import type { IServiceAccessor } from "@nex/services";
import {
  bindServicesToWorkspace,
  injectWorkspaceTargetIntoParams,
  normalizeWorkspaceServiceTarget,
  WORKSPACE_BOUND_CHANNELS,
} from "../src/lib/workspaceBoundServices.js";

// 运行：pnpm --dir packages/ui exec tsx --tsconfig tsconfig.json --test test/workspaceBoundServices.test.ts

type Call = { channel: string; method: string; args: unknown[] };

/** 最小 accessor double：记录每次调用，返回固定 Promise。 */
function createRecordingAccessor() {
  const calls: Call[] = [];
  const makeService = (channel: string) => {
    // 普通对象 + 显式事件成员：Proxy 自身没有 Reflect.get 会返回 undefined。
    const service: Record<string, unknown> = {
      // 事件工厂本身返回一个订阅函数（Event），而不是 RPC 结果。
      onDynamicChange: () => () => undefined,
      onDynamicData: () => () => undefined,
      onDynamicExit: () => () => undefined,
    };
    return new Proxy(service, {
      get(source, property) {
        if (typeof property !== "string") return Reflect.get(source, property);
        if (property.startsWith("onDynamic") || property.startsWith("onDid")) {
          // 事件成员必须保持原样，包装层不能把 onDynamic* 当成普通方法调用。
          return Reflect.get(source, property);
        }
        return (...args: unknown[]) => {
          calls.push({ channel, method: property, args });
          return Promise.resolve(undefined);
        };
      },
    });
  };
  const services = {
    fileService: makeService("file"),
    fileWatcherService: makeService("file-watcher"),
    mediaPreviewService: makeService("media-preview"),
    gitService: makeService("git"),
    gitCheckpointService: makeService("git-checkpoint"),
    terminalService: makeService("terminal"),
    systemService: makeService("system"),
  } as unknown as IServiceAccessor;
  return { services, calls };
}

const target = { workspacePath: "/srv/app", workspaceIdentity: "runner:alpha:/srv/app" };

test("injects both fields only when absent", () => {
  assert.deepEqual(injectWorkspaceTargetIntoParams({ path: "/srv/app/a.ts" }, target), {
    path: "/srv/app/a.ts",
    workspacePath: "/srv/app",
    workspaceIdentity: "runner:alpha:/srv/app",
  });

  // 显式给出的非空值不覆盖；空白字符串按缺失处理（server trim 后会丢弃，桌面也不区分）。
  assert.deepEqual(injectWorkspaceTargetIntoParams({ workspacePath: "/other" }, target), {
    workspacePath: "/other",
    workspaceIdentity: "runner:alpha:/srv/app",
  });
  assert.deepEqual(
    injectWorkspaceTargetIntoParams({ workspacePath: "  ", workspaceIdentity: "" }, target),
    { workspacePath: "/srv/app", workspaceIdentity: "runner:alpha:/srv/app" },
  );

  // 非对象参数原样返回，不伪造目标。
  assert.equal(injectWorkspaceTargetIntoParams(undefined, target), undefined);
  assert.deepEqual(injectWorkspaceTargetIntoParams(["/x"], target), ["/x"]);

  // 不修改调用方对象。
  const original = { path: "/a" };
  injectWorkspaceTargetIntoParams(original, target);
  assert.deepEqual(original, { path: "/a" });
});

test("normalizes blank identity away and trims the path", () => {
  assert.deepEqual(
    normalizeWorkspaceServiceTarget({ workspacePath: " /srv/app ", workspaceIdentity: "   " }),
    { workspacePath: "/srv/app" },
  );
});

test("injects targets for every listed channel method and leaves others untouched", async () => {
  const { services, calls } = createRecordingAccessor();
  const bound = bindServicesToWorkspace(services, target);
  assert.ok(bound);

  await bound.fileService.readdir({ path: "/srv/app" });
  await bound.fileService.readFileRange({ path: "/srv/app/a.pdf", offset: 0, length: 10 });
  await bound.fileService.searchWorkspaceFiles({ rootPath: "/srv/app", query: "a" });
  await bound.fileWatcherService.watch({ path: "/srv/app" });
  await bound.mediaPreviewService!.prepare!({ path: "/srv/app/a.png", expectedKind: "image" });
  await bound.gitService.refresh({ workspacePath: "/srv/app" });
  await bound.gitService.getDiff({ workspacePath: "/srv/app", path: "a", sourceId: "unstaged" });
  await bound.gitCheckpointService.createCheckpoint({ workspacePath: "/srv/app" });
  await bound.terminalService.create({ cols: 80, rows: 24, cwd: "/srv/app" });

  for (const call of calls) {
    const first = call.args[0] as Record<string, unknown>;
    assert.equal(first.workspacePath, "/srv/app", `${call.channel}.${call.method}`);
    assert.equal(
      first.workspaceIdentity,
      "runner:alpha:/srv/app",
      `${call.channel}.${call.method}`,
    );
  }

  calls.length = 0;
  // 非路径方法不注入额外字段。
  await bound.fileWatcherService.unwatch({ id: "w1" });
  await bound.mediaPreviewService!.release!({ previewId: "p1" });
  await bound.terminalService.resize({ id: "t1", cols: 80, rows: 24 });
  await bound.terminalService.write({ id: "t1", data: "x" });
  await bound.terminalService.dispose({ id: "t1" });
  await bound.fileService.createDefaultWorkspace();
  assert.equal(calls.length, 6);
  for (const call of calls) {
    const params = (call.args[0] ?? {}) as Record<string, unknown>;
    assert.equal("workspacePath" in params, false, `${call.channel}.${call.method}`);
    assert.equal("workspaceIdentity" in params, false, `${call.channel}.${call.method}`);
  }

  calls.length = 0;
  // system.info 无参但有机器语义：绑定后必须注入目标，server 才能问对机器。
  // 清理性检查：除 info 外的 system 方法仍原样透传。
  assert.notEqual(bound.systemService, services.systemService);
  await bound.systemService.info();
  assert.deepEqual(calls, [
    {
      channel: "system",
      method: "info",
      args: [{ workspacePath: "/srv/app", workspaceIdentity: "runner:alpha:/srv/app" }],
    },
  ]);

  calls.length = 0;
  await bound.systemService.listIntegratedTerminalShells();
  assert.deepEqual(calls, [
    { channel: "system", method: "listIntegratedTerminalShells", args: [] },
  ]);

  calls.length = 0;
  // 只有路径、没有 runner 身份时不注入：server 应沿用“第一台 runner”的既有回落，
  // 按 path 报错会打断 server 兜底创建的会话目录。
  const pathOnly = bindServicesToWorkspace(services, { workspacePath: "/srv/app" });
  assert.ok(pathOnly);
  await pathOnly.systemService.info();
  assert.deepEqual(calls, [{ channel: "system", method: "info", args: [] }]);
});

test("keeps event members as passthrough functions", () => {
  const { services } = createRecordingAccessor();
  const bound = bindServicesToWorkspace(services, target);
  assert.ok(bound);
  // onDynamicChange 本身是事件工厂：包装层必须原样透传，不能把它当 RPC 方法调用。
  const listened = bound.fileWatcherService.onDynamicChange("w1") as unknown as
    | (() => void)
    | undefined;
  assert.equal(
    typeof listened,
    "function",
    `onDynamicChange 不是 RPC 方法，不能注入参数 (${typeof listened})`,
  );
});

test("memoizes per accessor + target and separates machines sharing a path", () => {
  const { services } = createRecordingAccessor();
  const first = bindServicesToWorkspace(services, target);
  const second = bindServicesToWorkspace(services, target);
  assert.equal(first, second);

  const otherMachine = bindServicesToWorkspace(services, {
    workspacePath: "/srv/app",
    workspaceIdentity: "runner:beta:/srv/app",
  });
  assert.notEqual(first, otherMachine);

  const { services: otherAccessor } = createRecordingAccessor();
  assert.notEqual(bindServicesToWorkspace(otherAccessor, target), first);

  // 无目标 / 无 accessor：原样返回 base accessor（host-global 调用保持原行为）。
  assert.equal(bindServicesToWorkspace(services, null), services);
  assert.equal(
    bindServicesToWorkspace(services, { workspacePath: "  ", workspaceIdentity: null }),
    services,
  );
  assert.equal(bindServicesToWorkspace(null, target), null);
});

test("binding table covers the seven routing channels", () => {
  // system 只绑定 info（机器级浏览要问对 homedir），其余系统方法保持透传。
  const channels = WORKSPACE_BOUND_CHANNELS.map((binding) => binding.channel).sort();
  assert.deepEqual(channels, [
    "file",
    "file-watcher",
    "git",
    "git-checkpoint",
    "media-preview",
    "system",
    "terminal",
  ]);
});
