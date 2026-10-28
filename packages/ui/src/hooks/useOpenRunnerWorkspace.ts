import { useCallback } from "react";
import type { ServerWorkspaceOption } from "@nex/shared";
import { findRunnerWorkspaceToOpen, pollRunnerWorkspaceToOpen } from "@/lib/runnerWorkspaceOpen.js";
import { logger } from "@/logger.js";

const OPEN_WORKSPACE_POLL_TIMEOUT_MS = 5_000;
const OPEN_WORKSPACE_POLL_INTERVAL_MS = 500;

/**
 * 「打开某台机器的 workspace」（docs/next/runner-folders.md 决策 4/5）。
 *
 * 只负责一件事：找到该机器当前 served 的第一个 workspace 并交给 `onOpen`
 * （Root 用 handleSelectProject 打开 tab）。`load` 由调用方注入成 runner 目录的
 * reload，因此轮询同时会刷新 store：选择器和机器下拉立刻看到新机器。
 *
 * 返回 false 表示超时仍未发现该机器，调用方负责提示。
 */
export function useOpenRunnerWorkspace({
  load,
  readCached,
  onOpen,
}: {
  load: () => Promise<readonly ServerWorkspaceOption[] | null>;
  /** 当前缓存快照；已有该机器时直接打开，不必先等一次网络刷新。 */
  readCached: () => readonly ServerWorkspaceOption[] | null;
  onOpen: (path: string, workspaceIdentity: string) => void;
}): (runnerId: string) => Promise<boolean> {
  return useCallback(
    async (runnerId: string) => {
      const cached = findRunnerWorkspaceToOpen(readCached(), runnerId);
      const workspace =
        cached ??
        (await pollRunnerWorkspaceToOpen({
          load,
          runnerId,
          timeoutMs: OPEN_WORKSPACE_POLL_TIMEOUT_MS,
          intervalMs: OPEN_WORKSPACE_POLL_INTERVAL_MS,
        }));
      if (!workspace?.workspaceIdentity?.trim()) {
        logger.warn("[runner-workspace] 超时未在 server-info 中看到新机器", { runnerId });
        return false;
      }
      // 缓存命中也要刷新一次：其它入口（机器下拉、选择器）读的是 store。
      void load();
      onOpen(workspace.path, workspace.workspaceIdentity.trim());
      return true;
    },
    [load, onOpen, readCached],
  );
}
