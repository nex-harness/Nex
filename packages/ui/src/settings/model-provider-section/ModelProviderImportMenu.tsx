/**
 * 设置 → 模型 → 「从机器导入」（docs/next/model-onboarding.md 决策 2）。
 *
 * 迁移路径：把某台已连接 runner 的本地 pi 配置一次性导入 server 的共享
 * （global）作用域。只在 Web 平台暴露（Desktop 不实现两个平台方法）。
 *
 * 状态全部由平台层提供：菜单打开时拉一次机器状态，点击条目触发导入并
 * toast 摘要；provider 列表靠既有 provider-settings 变更事件刷新，这里
 * 额外调用 refresh() 兜底（事件丢失时仍能看到新 provider）。
 */
import { useCallback, useState } from "react";
import type { RunnerImportSummary, RunnerModelStatus } from "@nex/shared";
import { Loader2, Server } from "lucide-react";
import { Button } from "@/components/ui/button.js";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu.js";
import { toast } from "@/components/ui/toast.js";
import { useNexIntl } from "@/i18n/IntlProvider.js";
import { usePlatform } from "@/hooks/usePlatform.js";
import { logger } from "@/logger.js";

export function ModelProviderImportMenu({ onImported }: { onImported?: () => void }) {
  const { intl } = useNexIntl();
  const platform = usePlatform();
  const [statuses, setStatuses] = useState<RunnerModelStatus[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [importingRunnerId, setImportingRunnerId] = useState<string | null>(null);

  const supported = Boolean(platform.listRunnerModelStatus && platform.importRunnerModels);

  const loadStatuses = useCallback(async () => {
    if (!platform.listRunnerModelStatus) return;
    setLoading(true);
    try {
      setStatuses(await platform.listRunnerModelStatus());
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      logger.warn("[model-provider-import] 读取机器模型状态失败", { error: message });
      setStatuses([]);
      toast(message);
    } finally {
      setLoading(false);
    }
  }, [platform]);

  const reportSummary = useCallback(
    (summary: RunnerImportSummary) => {
      const skipped = summary.errors?.length ?? 0;
      const imported = summary.providers.join(", ");
      const message =
        summary.providers.length > 0
          ? intl.formatMessage(
              { id: "settings.modelProvider.importFromMachine.imported" },
              { providers: imported, skipped },
            )
          : intl.formatMessage(
              { id: "settings.modelProvider.importFromMachine.importedNone" },
              { skipped },
            );
      toast(message);
    },
    [intl],
  );

  const handleImport = useCallback(
    async (runnerId: string) => {
      if (!platform.importRunnerModels) return;
      setImportingRunnerId(runnerId);
      try {
        // 设置页只读写 global 层（spec 决策 3）。
        const summary = await platform.importRunnerModels(runnerId, "global");
        reportSummary(summary);
        onImported?.();
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        logger.warn("[model-provider-import] 导入模型失败", { error: message });
        toast(message);
      } finally {
        setImportingRunnerId(null);
      }
    },
    [platform, reportSummary, onImported],
  );

  // Desktop 不实现平台方法：整块入口不渲染。
  if (!supported) return null;

  const connected = (statuses ?? []).filter((status) => status.connected);

  return (
    <DropdownMenu
      onOpenChange={(next) => {
        if (next) void loadStatuses();
      }}
    >
      <DropdownMenuTrigger asChild>
        <Button
          type="button"
          variant="outline"
          size="default"
          className="rounded-lg"
          data-testid="model-provider-import-trigger"
        >
          <Server data-icon="inline-start" aria-hidden="true" />
          {intl.formatMessage({ id: "settings.modelProvider.importFromMachine.trigger" })}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-56">
        <DropdownMenuLabel className="text-ui-sm text-foreground-subtlest">
          {intl.formatMessage({ id: "settings.modelProvider.importFromMachine.title" })}
        </DropdownMenuLabel>
        {loading ? (
          <div className="flex items-center gap-2 px-2 py-1.5 text-ui-base text-foreground-subtle">
            <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
            {intl.formatMessage({ id: "settings.modelProvider.importFromMachine.loading" })}
          </div>
        ) : connected.length === 0 ? (
          <div className="px-2 py-1.5 text-ui-base text-foreground-subtlest">
            {intl.formatMessage({ id: "settings.modelProvider.importFromMachine.empty" })}
          </div>
        ) : (
          connected.map((status) => (
            <DropdownMenuItem
              key={status.runnerId}
              data-testid={`model-provider-import-item-${status.runnerId}`}
              disabled={importingRunnerId !== null}
              onSelect={() => void handleImport(status.runnerId)}
            >
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate">{status.name}</span>
                <span className="text-ui-sm text-foreground-subtle">
                  {intl.formatMessage(
                    { id: "settings.modelProvider.importFromMachine.itemMeta" },
                    {
                      mode: intl.formatMessage({
                        id: `settings.modelProvider.importFromMachine.mode.${status.mode}`,
                      }),
                      count: status.modelCount,
                    },
                  )}
                </span>
              </span>
              {importingRunnerId === status.runnerId ? (
                <Loader2 className="animate-spin" aria-hidden="true" />
              ) : null}
            </DropdownMenuItem>
          ))
        )}
        <DropdownMenuSeparator />
        <div className="px-2 py-1 text-ui-xs text-foreground-subtlest">
          {intl.formatMessage({ id: "settings.modelProvider.importFromMachine.hint" })}
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
