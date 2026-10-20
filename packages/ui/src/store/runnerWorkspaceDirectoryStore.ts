import { create } from "zustand";
import type { ServerWorkspaceOption } from "@nex/shared";

/**
 * Server runner workspace 目录（web 平台）。
 *
 * 数据唯一来源是 `IPlatformService.listServerWorkspaces()`；组件不直接 fetch。
 * 这个 store 只缓存展示所需的机器名映射，不承担 workspace/tab 状态：
 * - tab 的真实归属仍是 tabStore 的 `workspaceIdentity`；
 * - runner 离线/重启后列表会刷新，header 读到的最新名字仅作展示。
 */
interface RunnerWorkspaceDirectoryState {
  /** 最近一次成功读取的列表；null 表示尚未读取或平台不支持。 */
  workspaces: ServerWorkspaceOption[] | null;
  loading: boolean;
  /** 按完整 identity 索引的机器展示名。 */
  runnerNameByIdentity: Record<string, string>;
  setWorkspaces(workspaces: readonly ServerWorkspaceOption[] | null): void;
  setLoading(loading: boolean): void;
}

function buildRunnerNameByIdentity(
  workspaces: readonly ServerWorkspaceOption[] | null,
): Record<string, string> {
  if (!workspaces) {
    return {};
  }
  const byIdentity: Record<string, string> = {};
  for (const workspace of workspaces) {
    const identity = workspace.workspaceIdentity?.trim();
    if (!identity) {
      continue;
    }
    const runnerName = workspace.runnerName?.trim() || workspace.runnerId?.trim();
    if (runnerName) {
      byIdentity[identity] = runnerName;
    }
  }
  return byIdentity;
}

export const useRunnerWorkspaceDirectoryStore = create<RunnerWorkspaceDirectoryState>()((set) => ({
  workspaces: null,
  loading: false,
  runnerNameByIdentity: {},
  setWorkspaces(workspaces) {
    set({
      workspaces: workspaces ? [...workspaces] : null,
      runnerNameByIdentity: buildRunnerNameByIdentity(workspaces),
    });
  },
  setLoading(loading) {
    set({ loading });
  },
}));
