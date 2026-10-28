import { useCallback, useEffect } from "react";
import type { IPlatformService, ServerWorkspaceOption } from "@nex/shared";
import { useRunnerWorkspaceDirectoryStore } from "@/store/runnerWorkspaceDirectoryStore.js";
import { logger } from "@/logger.js";

/**
 * 拉取 server runner workspace 目录（web 平台）。
 *
 * 只在平台实现了 `listServerWorkspaces` 时生效：desktop 不实现，保持 SSH/WSL/
 * Docker 原有入口；普通 Web 由 platform 读取 `/api/server-info`。
 * 失败时保持 null，由“打开文件夹”回落目录浏览器，不阻塞主流程。
 *
 * 返回 `reload`：既刷新 store（选择器、下拉框订阅它），也把最新列表交给调用方。
 * 「加完文件夹 / 装完新机器后立刻打开」需要同一份结果，不能再读一次旧快照。
 */
export function useRunnerWorkspaceDirectory(
  platform: IPlatformService,
): () => Promise<ServerWorkspaceOption[] | null> {
  const load = useCallback(async (): Promise<ServerWorkspaceOption[] | null> => {
    const listServerWorkspaces = platform.listServerWorkspaces;
    if (!listServerWorkspaces) {
      useRunnerWorkspaceDirectoryStore.getState().setWorkspaces(null);
      return null;
    }
    const store = useRunnerWorkspaceDirectoryStore.getState();
    store.setLoading(true);
    try {
      const workspaces = await listServerWorkspaces.call(platform);
      useRunnerWorkspaceDirectoryStore.getState().setWorkspaces(workspaces);
      return workspaces;
    } catch (error: unknown) {
      logger.warn("[runner-workspace-directory] 读取 server workspace 列表失败", { error });
      useRunnerWorkspaceDirectoryStore.getState().setWorkspaces(null);
      return null;
    } finally {
      useRunnerWorkspaceDirectoryStore.getState().setLoading(false);
    }
  }, [platform]);

  useEffect(() => {
    void load();
  }, [load]);

  return load;
}
