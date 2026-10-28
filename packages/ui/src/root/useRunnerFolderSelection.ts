import { useCallback } from "react";
import type { IPlatformService, RunnerWorkspaceAddResult } from "@nex/shared";
import { toast } from "@/components/ui/toast.js";
import { useRunnerWorkspaceFolderAdd } from "@/hooks/useRunnerWorkspaceFolderAdd.js";
import type { useNexIntl } from "@/i18n/IntlProvider.js";
import { logger } from "@/logger.js";

/**
 * 目录浏览器在某台机器上选定目录后的动作（docs/next/runner-folders.md 决策 4）：
 * `runner-workspaces.add` → 刷新目录 → 用 runner 返回的身份打开 workspace。
 *
 * 顺序不能颠倒：add 之前打开会命中 server 里还不存在的 workspace（runner 会以
 * 「workspace not served」拒绝后续调用）；必须等 runner 持久化并回传 identity。
 * 失败只提示并留在浏览器里，不关闭对话框，用户可以换一个目录重试。
 */
export function useRunnerFolderSelection({
  platform,
  intl,
  reload,
  openWorkspace,
}: {
  platform: IPlatformService;
  intl: ReturnType<typeof useNexIntl>["intl"];
  reload: () => Promise<unknown>;
  openWorkspace: (path: string, workspaceIdentity: string) => void;
}): (runnerId: string, path: string) => Promise<RunnerWorkspaceAddResult | null> {
  const addFolder = useRunnerWorkspaceFolderAdd(platform);

  return useCallback(
    async (runnerId, path) => {
      try {
        const added = await addFolder(runnerId, path);
        if (!added) {
          toast(intl.formatMessage({ id: "runnerWorkspacePicker.addUnsupported" }));
          return null;
        }
        // 先刷新 /api/server-info 缓存，再打开：选择器与机器下拉立即能看到该文件夹。
        await reload();
        openWorkspace(added.path, added.workspaceIdentity);
        return added;
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        logger.warn("[runner-workspace] 添加文件夹失败", { runnerId, path, error: message });
        toast(intl.formatMessage({ id: "runnerWorkspacePicker.addFailed" }, { error: message }), {
          variant: "warning",
          dismissible: true,
        });
        return null;
      }
    },
    [addFolder, intl, openWorkspace, reload],
  );
}
