import { useCallback } from "react";
import type { IPlatformService, RunnerWorkspaceAddResult } from "@nex/shared";
import { logger } from "@/logger.js";

/**
 * 在指定机器上添加一个 served folder（docs/next/runner-folders.md 决策 3/4）。
 *
 * 真正的调用是 web 平台的 `runner-workspaces.add`（带机器级目标），UI 只经
 * IPlatformService 发起；desktop 不实现该方法，返回 null 让调用方保留原行为。
 *
 * 返回 null 表示「平台不支持」，抛错表示 runner 拒绝路径（不是目录、不可读、
 * 相对路径等）；两者语义不同，调用方不能当成同一种失败。
 */
export function useRunnerWorkspaceFolderAdd(
  platform: IPlatformService,
): (runnerId: string, path: string) => Promise<RunnerWorkspaceAddResult | null> {
  return useCallback(
    async (runnerId, path) => {
      const addRunnerWorkspace = platform.addRunnerWorkspace;
      if (!addRunnerWorkspace) {
        logger.warn("[runner-workspace] 平台不支持添加 runner 文件夹", { runnerId });
        return null;
      }
      return addRunnerWorkspace.call(platform, runnerId, path);
    },
    [platform],
  );
}
