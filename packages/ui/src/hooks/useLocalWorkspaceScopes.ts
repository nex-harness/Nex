import { useMemo } from "react";
import { isWorkspaceTab, type WorkspaceTabState } from "@/store/tabStore.js";
import { isClientRemoteWorkspaceIdentity } from "@/lib/workspaceIdentity.js";

function isLocalWorkspaceTab(tab: WorkspaceTabState): boolean {
  // `runner:` 身份走同一 server 连接，仍属于本地 shard：window-controller 会按
  // scope 里的 identity 交给 server 路由；把它排除会让 runner workspace 的
  // pinned/timeline 分组永远为空。
  return (
    !tab.remoteSessionId &&
    !tab.remoteTarget &&
    !isClientRemoteWorkspaceIdentity(tab.workspaceIdentity)
  );
}

export function useLocalWorkspaceScopes({
  workspaceTabs,
}: {
  workspaceTabs: WorkspaceTabState[];
}): WorkspaceTabState[] {
  return useMemo(
    () =>
      workspaceTabs.filter((tab) => {
        // pinned 查询只使用当前窗口已经打开的本地 workspace。
        // 远端 workspace 即使持久化在 lastWorkspaceSession 中，也不能兜底混入本地查询，
        // 否则没有 remoteSessionId 时会落到本地服务，导致同路径任务串读。
        return isWorkspaceTab(tab) && isLocalWorkspaceTab(tab);
      }),
    [workspaceTabs],
  );
}
