import { useCallback, useEffect, useRef, useState } from "react";
import type { RunnerUpdateTriggerResult } from "@nex/shared";
import { toast } from "@/components/ui/toast.js";
import { useNexIntl } from "@/i18n/IntlProvider.js";
import { logger } from "@/logger.js";

/** runner 重启后重新 hello 的等待窗口：两次刷新覆盖慢机器。 */
const RUNNER_UPDATE_RELOAD_DELAYS_MS = [8_000, 20_000] as const;

/**
 * 手动触发 runner 自更新（docs/next/runner-update.md 决策 5）。
 *
 * UI 只负责发 `POST /api/runner-update` 并把结果反馈给用户；下载、校验、换文件
 * 和重启都由 runner 自己完成。触发成功后必须延迟重读目录：runner 会断开再
 * 重连，能力位（`runner-workspaces`）由服务端在新 hello 后重新计算，立即刷新
 * 只会读到旧的“不支持”状态。
 */
export function useRunnerUpdateAction({
  updateRunner,
  reload,
}: {
  /** 平台是否实现由调用方决定；undefined 时不显示 Update 入口。 */
  updateRunner?: (runnerId: string) => Promise<RunnerUpdateTriggerResult>;
  reload?: () => Promise<unknown>;
}): {
  updatingRunnerId: string | null;
  triggerUpdate: (runnerId: string, runnerName?: string) => Promise<void>;
} {
  const { intl } = useNexIntl();
  const [updatingRunnerId, setUpdatingRunnerId] = useState<string | null>(null);
  const timersRef = useRef<Array<ReturnType<typeof setTimeout>>>([]);

  useEffect(
    () => () => {
      // 组件卸载（例如平台不再挂载选择器）时清掉待触发的刷新，避免卸载后发请求。
      for (const timer of timersRef.current) {
        clearTimeout(timer);
      }
      timersRef.current = [];
    },
    [],
  );

  const triggerUpdate = useCallback(
    async (runnerId: string, runnerName?: string) => {
      if (!updateRunner) {
        return;
      }
      const name = runnerName?.trim() || runnerId;
      setUpdatingRunnerId(runnerId);
      try {
        const result = await updateRunner(runnerId);
        if (result.sent) {
          toast(intl.formatMessage({ id: "runnerWorkspacePicker.updateStarted" }, { name }), {
            variant: "update",
            dismissible: true,
          });
          // 重复点击不能叠加刷新：先清掉上一轮未触发的定时器。
          for (const timer of timersRef.current) {
            clearTimeout(timer);
          }
          timersRef.current = [];
          for (const delayMs of RUNNER_UPDATE_RELOAD_DELAYS_MS) {
            timersRef.current.push(
              setTimeout(() => {
                void reload?.();
              }, delayMs),
            );
          }
          return;
        }
        // sent=false 只表示服务端认为该机器已是当前构建，不需要重启。
        toast(intl.formatMessage({ id: "runnerWorkspacePicker.updateUpToDate" }, { name }), {
          variant: "info",
        });
      } catch (error: unknown) {
        // 409（runner 太旧无法自更新）的服务端文案要求重装，必须原样展示。
        const message = error instanceof Error ? error.message : String(error);
        logger.warn("[runner-workspace-update] 触发 runner 自更新失败", { runnerId, error });
        toast(message, { variant: "warning", dismissible: true });
      } finally {
        setUpdatingRunnerId(null);
      }
    },
    [updateRunner, intl, reload],
  );

  return { updatingRunnerId, triggerUpdate };
}
