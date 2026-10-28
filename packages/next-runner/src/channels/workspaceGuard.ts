/**
 * The runner's single workspace boundary (docs/next/runner-folders.md decisions
 * 1-2): every call/listen is checked against the live `WorkspaceRoots`, and the
 * machine browse scope (`workspacePath: "~"`) is accepted only on the
 * browse-only channel/method pairs.
 *
 * The check lives in a registry decorator rather than in each handler so one
 * rule covers every call, including channels added later. Workspace-less calls
 * (model catalog, `listPinnedTaskIds`) are untouched.
 */
import type { ChannelRegistryLike } from "../link/registry.js";
import type { JsonValue } from "../link/frames.js";
import type { WorkspaceRoots } from "../workspaces/workspaceRoots.js";
import { MACHINE_SCOPE_PATH, assertMachineScopeAllowed } from "../workspaces/machineScope.js";

/** Reads the workspace target from an arg: top-level, then nested under `workspace`. */
export function workspacePathFromArg(arg: JsonValue): string | null {
  if (!isRecord(arg)) return null;
  if (typeof arg["workspacePath"] === "string" && arg["workspacePath"].length > 0) {
    return arg["workspacePath"];
  }
  const workspace = arg["workspace"];
  if (isRecord(workspace)) {
    const nested = workspace["workspacePath"];
    if (typeof nested === "string" && nested.length > 0) return nested;
  }
  return null;
}

export function createWorkspaceGuardedRegistry(
  registry: ChannelRegistryLike,
  roots: WorkspaceRoots,
): ChannelRegistryLike {
  /**
   * 服务端可能按 `runner:<id>:<path>` 把一个本 runner 从未声明过的路径转发过来
   * （例如跨 runner 的陈旧 tab）；只信任服务端会让 runner 在别人的 cwd 上执行。
   * 因此这里对每条 call 重新校验，并按方法限制机器级浏览范围。
   */
  const assertServedCall = (channel: string, method: string, arg: JsonValue): void => {
    const path = workspacePathFromArg(arg);
    if (!path) return;
    if (path === MACHINE_SCOPE_PATH) {
      // 修复依据（决策 2）：机器级作用域只开放目录浏览/stat/host 信息与
      // `runner-workspaces.*`；其他方法带 `~` 一律拒绝，避免会话/终端/写入
      // 落到任意目录。
      assertMachineScopeAllowed(channel, method, arg);
      return;
    }
    if (roots.has(path)) return;
    throw new Error(`workspace not served by this runner: ${path}`);
  };
  // 事件订阅没有浏览语义：机器级作用域永远不适用于 listen。
  const assertServedListen = (channel: string, event: string, arg: JsonValue): void => {
    const path = workspacePathFromArg(arg);
    if (!path) return;
    if (path === MACHINE_SCOPE_PATH) {
      throw new Error(`machine scope is not available for ${channel}.${event}`);
    }
    if (roots.has(path)) return;
    throw new Error(`workspace not served by this runner: ${path}`);
  };
  return {
    call(channel, method, handler) {
      // async 包装保证校验失败总是以 rejected promise 形式回到 link 的错误分支，
      // 与 handler 自身的异步失败路径一致（link 两者都会捕获，但调用方不该区分）。
      registry.call(channel, method, async (arg) => {
        assertServedCall(channel, method, arg);
        return handler(arg);
      });
    },
    listen(channel, event, handler) {
      registry.listen(channel, event, (arg, emit) => {
        assertServedListen(channel, event, arg);
        return handler(arg, emit);
      });
    },
    getCall: (channel, method) => registry.getCall(channel, method),
    getListen: (channel, event) => registry.getListen(channel, event),
    listRegistered: () => registry.listRegistered(),
  };
}

/** 请求侧的尾斜杠/`.` 段由 `WorkspaceRoots.has` 的 resolve 归一后比较。 */

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
