/**
 * 安装向导的「模型」步骤（docs/next/model-onboarding.md 决策 1）。
 *
 * 安装成功后向导不关闭，先在这里轮询新机器的模型状态：约每秒一次、最多 15s。
 * - 有模型 → Ready + 完成；
 * - 没模型（含超时仍 unknown）→ 从其他机器导入 / 打开模型设置 / 稍后。
 *
 * 组件只读状态并触发一次性导入，不持有模型事实（spec 的 Owners 一节）。
 */
import { useCallback, useEffect, useMemo, useState } from "react";
import type { RunnerImportSummary, RunnerModelStatus } from "@nex/shared";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button.js";
import { useNexIntl } from "@/i18n/IntlProvider.js";
import { usePlatform } from "@/hooks/usePlatform.js";
import { resolveRunnerModelOnboardingState } from "@/lib/runnerModelOnboarding.js";
import { logger } from "@/logger.js";

const MODEL_POLL_INTERVAL_MS = 1_000;
const MODEL_POLL_TIMEOUT_MS = 15_000;

export function RunnerInstallModelsStep({
  runnerId,
  onClose,
  onOpenModelSettings,
}: {
  runnerId: string;
  onClose: () => void;
  onOpenModelSettings?: () => void;
}) {
  const { intl } = useNexIntl();
  const platform = usePlatform();
  const [statuses, setStatuses] = useState<RunnerModelStatus[]>([]);
  const [timedOut, setTimedOut] = useState(false);
  // 导入后服务端会重新下发配置，用 token 重开一个轮询窗口。
  const [pollToken, setPollToken] = useState(0);
  const [importingRunnerId, setImportingRunnerId] = useState<string | null>(null);
  const [importSummary, setImportSummary] = useState<RunnerImportSummary | null>(null);
  const [importError, setImportError] = useState<string | null>(null);

  const state = useMemo(
    () => resolveRunnerModelOnboardingState({ statuses, runnerId, timedOut }),
    [statuses, runnerId, timedOut],
  );

  useEffect(() => {
    if (!platform.listRunnerModelStatus) return;
    let cancelled = false;
    const startedAt = Date.now();
    let timer: number | undefined;

    const poll = async () => {
      try {
        const next = await platform.listRunnerModelStatus?.();
        if (cancelled || !next) return;
        setStatuses(next);
        const target = next.find((entry) => entry.runnerId === runnerId);
        // 只有明确拿到模型才提前结束；unknown 继续等到超时。
        if (target && target.modelCount > 0) {
          if (timer !== undefined) window.clearInterval(timer);
          return;
        }
      } catch (pollError) {
        logger.warn("[runner-install] 轮询机器模型状态失败", {
          error: pollError instanceof Error ? pollError.message : String(pollError),
        });
      }
      if (Date.now() - startedAt >= MODEL_POLL_TIMEOUT_MS) {
        if (timer !== undefined) window.clearInterval(timer);
        setTimedOut(true);
      }
    };

    timer = window.setInterval(poll, MODEL_POLL_INTERVAL_MS);
    void poll();
    return () => {
      cancelled = true;
      if (timer !== undefined) window.clearInterval(timer);
    };
  }, [runnerId, pollToken, platform]);

  const handleImport = useCallback(
    async (sourceRunnerId: string) => {
      if (!platform.importRunnerModels) return;
      setImportingRunnerId(sourceRunnerId);
      setImportError(null);
      try {
        // 设置页只读写 global 层（spec 决策 3），向导同样只写 global。
        const summary = await platform.importRunnerModels(sourceRunnerId, "global");
        setImportSummary(summary);
        setTimedOut(false);
        setPollToken((token) => token + 1);
      } catch (failure) {
        const message = failure instanceof Error ? failure.message : String(failure);
        setImportError(message);
        logger.warn("[runner-install] 导入模型失败", { error: message });
      } finally {
        setImportingRunnerId(null);
      }
    },
    [platform],
  );

  // 导入摘要同时出现在 ready 与 needs-models 两个分支：导入后机器可能立刻
  // 变成 ready，摘要不能因此消失（spec 决策 1 要求显示导入结果）。
  const importSummaryBlock = importSummary ? (
    <div className="rounded-lg bg-surface px-2.5 py-2 text-ui-sm">
      <p className="text-foreground">
        {importSummary.providers.length > 0
          ? intl.formatMessage(
              { id: "runnerInstall.models.imported" },
              { providers: importSummary.providers.join(", ") },
            )
          : intl.formatMessage({ id: "runnerInstall.models.importedNone" })}
      </p>
      {(importSummary.errors ?? []).length > 0 ? (
        <ul className="mt-1 flex flex-col gap-0.5 text-foreground-subtle">
          {(importSummary.errors ?? []).map((entry, index) => (
            <li key={`${entry.path ?? entry.code ?? "skip"}-${index}`}>
              {intl.formatMessage(
                { id: "runnerInstall.models.skipped" },
                { name: entry.path ?? entry.code ?? "", message: entry.message ?? "" },
              )}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  ) : null;

  if (state.kind === "checking") {
    return (
      <div className="flex flex-col gap-3" data-testid="runner-install-models-step">
        <div className="flex items-center gap-2 text-ui-base text-foreground-subtle">
          <Loader2 className="size-4 animate-spin" aria-hidden="true" />
          {intl.formatMessage({ id: "runnerInstall.models.checking" })}
        </div>
      </div>
    );
  }

  if (state.kind === "ready") {
    return (
      <div className="flex flex-col gap-3" data-testid="runner-install-models-step">
        <p className="text-ui-base text-foreground" data-testid="runner-install-models-ready">
          {intl.formatMessage({ id: "runnerInstall.models.ready" }, { count: state.modelCount })}
        </p>
        {importSummaryBlock}
        <div className="flex justify-end pt-1">
          <Button type="button" onClick={onClose} data-testid="runner-install-models-done">
            {intl.formatMessage({ id: "runnerInstall.models.done" })}
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3" data-testid="runner-install-models-step">
      <p className="text-ui-base text-foreground-subtle">
        {intl.formatMessage({ id: "runnerInstall.models.needsModels" })}
      </p>
      {state.sources.length > 0 ? (
        <div className="flex flex-col gap-1.5">
          <p className="text-ui-sm font-medium text-foreground-subtle">
            {intl.formatMessage({ id: "runnerInstall.models.sourcesTitle" })}
          </p>
          {state.sources.map((source) => (
            <div
              key={source.runnerId}
              className="flex items-center justify-between gap-2 rounded-lg border border-border bg-surface px-2.5 py-1.5"
              data-testid={`runner-install-models-source-${source.runnerId}`}
            >
              <span className="flex min-w-0 flex-col">
                <span className="truncate text-ui-base text-foreground">{source.name}</span>
                <span className="text-ui-sm text-foreground-subtle">
                  {intl.formatMessage(
                    { id: "runnerInstall.models.sourceMeta" },
                    {
                      mode: intl.formatMessage({ id: `runnerInstall.models.mode.${source.mode}` }),
                      count: source.modelCount,
                    },
                  )}
                </span>
              </span>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={importingRunnerId !== null}
                data-testid={`runner-install-models-import-${source.runnerId}`}
                onClick={() => void handleImport(source.runnerId)}
              >
                {importingRunnerId === source.runnerId ? (
                  <Loader2 className="animate-spin" aria-hidden="true" />
                ) : null}
                {intl.formatMessage({ id: "runnerInstall.models.import" })}
              </Button>
            </div>
          ))}
        </div>
      ) : (
        <p className="text-ui-sm text-foreground-subtlest">
          {intl.formatMessage({ id: "runnerInstall.models.noSources" })}
        </p>
      )}
      {/* 导入结果：成功的 provider id + 被跳过的条目与原因（spec 决策 5）。 */}
      {importSummaryBlock}
      {importError ? (
        <p className="text-ui-base text-red-500" role="alert">
          {importError}
        </p>
      ) : null}
      <div className="flex justify-end gap-2 pt-1">
        {onOpenModelSettings ? (
          <Button
            type="button"
            variant="outline"
            data-testid="runner-install-models-open-settings"
            onClick={() => {
              onClose();
              onOpenModelSettings();
            }}
          >
            {intl.formatMessage({ id: "runnerInstall.models.openSettings" })}
          </Button>
        ) : null}
        <Button
          type="button"
          variant="ghost"
          data-testid="runner-install-models-later"
          onClick={onClose}
        >
          {intl.formatMessage({ id: "runnerInstall.models.later" })}
        </Button>
      </div>
    </div>
  );
}
