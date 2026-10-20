import { useCallback, useEffect } from "react";
import type { IPlatformService } from "@nex/shared";
import { useRunnerWorkspaceDirectoryStore } from "@/store/runnerWorkspaceDirectoryStore.js";
import { logger } from "@/logger.js";

/**
 * 拉取 server runner workspace 目录（web 平台）。
 *
 * 只在平台实现了 `listServerWorkspaces` 时生效：desktop 不实现，保持 SSH/WSL/
 * Docker 原有入口；普通 Web 由 platform 读取 `/api/server-info`。
 * 失败时保持 null，由“打开文件夹”回落目录浏览器，不阻塞主流程。
 *
 * 返回 `reload`，供打开选择器时刷新：runner 可能在页面加载之后才连上 server。
 */
export function useRunnerWorkspaceDirectory(platform: IPlatformService): () => void {
  const load = useCallback(() => {
    const listServerWorkspaces = platform.listServerWorkspaces;
    if (!listServerWorkspaces) {
      useRunnerWorkspaceDirectoryStore.getState().setWorkspaces(null);
      return;
    }
    const store = useRunnerWorkspaceDirectoryStore.getState();
    store.setLoading(true);
    void listServerWorkspaces
      .call(platform)
      .then((workspaces) => {
        useRunnerWorkspaceDirectoryStore.getState().setWorkspaces(workspaces);
      })
      .catch((error: unknown) => {
        logger.warn("[runner-workspace-directory] 读取 server workspace 列表失败", { error });
        useRunnerWorkspaceDirectoryStore.getState().setWorkspaces(null);
      })
      .finally(() => {
        useRunnerWorkspaceDirectoryStore.getState().setLoading(false);
      });
  }, [platform]);

  useEffect(() => {
    load();
  }, [load]);

  return load;
}
