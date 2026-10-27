import type { ServerWorkspaceOption } from "@nex/shared";
import { FolderOpen, Laptop, Monitor, Server } from "lucide-react";
import { Badge } from "@/components/ui/badge.js";
import { Button } from "@/components/ui/button.js";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog.js";
import { useNexIntl } from "@/i18n/IntlProvider.js";
import {
  groupRunnerWorkspaceOptions,
  resolveRunnerWorkspaceTitle,
} from "@/lib/runnerWorkspaceOptions.js";

function resolveRunnerOsLabel(
  os: string | undefined,
  intl: ReturnType<typeof useNexIntl>["intl"],
): string | null {
  const normalized = os?.trim().toLowerCase();
  if (!normalized) {
    return null;
  }
  if (normalized.includes("darwin") || normalized.includes("mac")) {
    return intl.formatMessage({ id: "runnerWorkspacePicker.os.macos" });
  }
  if (normalized.includes("win")) {
    return intl.formatMessage({ id: "runnerWorkspacePicker.os.windows" });
  }
  if (normalized.includes("linux")) {
    return intl.formatMessage({ id: "runnerWorkspacePicker.os.linux" });
  }
  // 未知 OS 名直接用 server 原值，不猜平台。
  return os?.trim() || null;
}

function RunnerOsIcon({ os }: { os?: string }) {
  const normalized = os?.trim().toLowerCase() ?? "";
  const Icon = normalized.includes("win")
    ? Monitor
    : normalized.includes("linux")
      ? Server
      : Laptop;
  return <Icon className="size-3.5 shrink-0 text-foreground-subtle" aria-hidden="true" />;
}

/**
 * 机器 → workspace 选择器。
 *
 * web 的目录浏览器只能浏览 server 本机目录，无法表达“哪台 runner”；当 server-info
 * 上报 runner workspaces 时用它先选机器，再带上 `runner:` 身份打开 workspace。
 * 列表为空或平台不提供数据时不渲染，由调用方回落 DirectoryBrowser。
 */
export function RunnerWorkspacePicker({
  open,
  workspaces,
  loading = false,
  onOpenChange,
  onSelect,
  onBrowseDirectory,
  onInstallRunner,
}: {
  open: boolean;
  workspaces: readonly ServerWorkspaceOption[];
  loading?: boolean;
  onOpenChange: (open: boolean) => void;
  onSelect: (workspace: ServerWorkspaceOption) => void;
  onBrowseDirectory: () => void;
  /** 平台支持 SSH 安装（web）时提供；触发「安装新机器」向导。 */
  onInstallRunner?: () => void;
}) {
  const { intl } = useNexIntl();
  const groups = groupRunnerWorkspaceOptions(workspaces);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        data-testid="runner-workspace-picker"
        className="flex h-[min(36rem,calc(100vh-4rem))] w-[min(44rem,calc(100vw-2rem))] max-w-none flex-col gap-3 overflow-hidden p-4"
      >
        <DialogHeader className="shrink-0 pr-8">
          <DialogTitle className="text-ui-base font-medium">
            {intl.formatMessage({ id: "runnerWorkspacePicker.title" })}
          </DialogTitle>
          <DialogDescription className="text-ui-sm">
            {intl.formatMessage({ id: "runnerWorkspacePicker.description" })}
          </DialogDescription>
        </DialogHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto rounded-lg border border-border bg-background p-2">
          {loading && groups.length === 0 ? (
            <div className="p-4 text-center text-ui-base text-foreground-subtle">
              {intl.formatMessage({ id: "runnerWorkspacePicker.loading" })}
            </div>
          ) : null}
          {!loading && groups.length === 0 ? (
            <div className="p-4 text-center text-ui-base text-foreground-subtle">
              {intl.formatMessage({ id: "runnerWorkspacePicker.empty" })}
            </div>
          ) : null}
          {groups.map((group) => {
            const osLabel = resolveRunnerOsLabel(group.runnerOs, intl);
            return (
              <section key={group.key} className="flex min-w-0 flex-col gap-1">
                <div className="flex min-w-0 items-center gap-2 px-2 pt-1 pb-0.5">
                  <RunnerOsIcon os={group.runnerOs} />
                  <span className="min-w-0 truncate text-ui-sm font-medium text-foreground-subtle">
                    {group.runnerName ||
                      intl.formatMessage({ id: "runnerWorkspacePicker.localMachine" })}
                  </span>
                  {osLabel ? (
                    <Badge variant="outline" className="shrink-0">
                      {osLabel}
                    </Badge>
                  ) : null}
                </div>
                {group.workspaces.map((workspace) => (
                  <button
                    key={`${workspace.workspaceIdentity ?? workspace.runnerId ?? "local"}:${workspace.path}`}
                    type="button"
                    data-testid="runner-workspace-option"
                    onClick={() => onSelect(workspace)}
                    className="flex w-full min-w-0 cursor-pointer items-center gap-2 rounded-md px-2 py-2 text-left transition hover:bg-hover/50 focus-visible:bg-hover/50 focus-visible:outline-none"
                  >
                    <FolderOpen className="size-4 shrink-0 text-foreground-subtle" />
                    <span className="flex min-w-0 flex-1 flex-col">
                      <span className="min-w-0 truncate text-ui-base text-foreground">
                        {resolveRunnerWorkspaceTitle(workspace)}
                      </span>
                      <span
                        className="min-w-0 truncate text-ui-sm text-foreground-subtle"
                        title={workspace.path}
                      >
                        {workspace.path}
                      </span>
                    </span>
                  </button>
                ))}
              </section>
            );
          })}
        </div>

        {/* 手机宽度下按钮纵向铺满；sm 以上恢复横向布局。 */}
        <div className="flex shrink-0 flex-col-reverse gap-2 sm:flex-row sm:items-center sm:justify-between">
          <div className="flex min-w-0 flex-col gap-2 sm:flex-row sm:items-center">
            <Button
              type="button"
              variant="ghost"
              size="lg"
              className="h-10 min-w-0 justify-start px-3 text-ui-base"
              onClick={onBrowseDirectory}
            >
              {intl.formatMessage({ id: "runnerWorkspacePicker.browseDirectory" })}
            </Button>
            {onInstallRunner ? (
              <Button
                type="button"
                variant="ghost"
                size="lg"
                className="h-10 min-w-0 justify-start px-3 text-ui-base"
                onClick={onInstallRunner}
                data-testid="runner-install-entry"
              >
                {intl.formatMessage({ id: "runnerInstall.entry" })}
              </Button>
            ) : null}
          </div>
          <Button
            type="button"
            variant="secondary"
            size="lg"
            className="h-10 min-w-0 px-5 text-ui-base"
            onClick={() => onOpenChange(false)}
          >
            {intl.formatMessage({ id: "common.cancel" })}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
