/* 二次开发：左下角头像菜单已移除。
 * 语言/界面模式在设置页「常规」、主题/字号在设置页「外观」、界面缩放在桌面端原生菜单均有入口；
 * 退出登录、套餐徽标与用量汇总随账号体系一并裁剪。footer 仅保留远端控制触发与设置/返回按钮。 */
import { memo } from "react";
import { TID_TASK_SETTINGS_BUTTON } from "@nex/shared/test-ids";
import { ControlHintTooltip } from "@/ControlHintTooltip.js";
import { cn } from "@/components/lib/utils.js";
import { Button } from "@/components/ui/button.js";
import { Settings } from "lucide-react";
import { useNexIntl } from "@/i18n/IntlProvider.js";
import { WorkspaceWebRemoteControlTrigger } from "@/WorkspaceWebRemoteControlTrigger.js";

export const WorkspaceSidebarFooter = memo(function WorkspaceSidebarFooterComponent({
  onSettingsButtonClick,
  settingsButtonMode = "settings",
  workspacePath,
  workspaceIdentity,
  isDesktop = false,
  className,
}: {
  onSettingsButtonClick?: () => void;
  settingsButtonMode?: "settings" | "back";
  workspacePath?: string;
  workspaceIdentity?: string;
  isDesktop?: boolean;
  className?: string;
}) {
  const { intl } = useNexIntl();
  const settingsButtonLabel =
    settingsButtonMode === "back"
      ? intl.formatMessage({ id: "workspace.backToWorkspace" })
      : intl.formatMessage({ id: "settings.title" });

  return (
    // footer 被 Settings 复用，页面专属边距由调用方传入，避免修改共享默认样式。
    <footer className={cn("flex shrink-0 flex-col gap-2.5 px-4 pt-2 pb-4", className)}>
      <div className="flex min-w-0 items-center justify-end gap-1.5">
        {isDesktop && workspacePath ? (
          <WorkspaceWebRemoteControlTrigger
            workspacePath={workspacePath}
            workspaceIdentity={workspaceIdentity}
            compact
          />
        ) : null}
        <ControlHintTooltip title={settingsButtonLabel}>
          <Button
            type="button"
            variant="ghost"
            size="icon-lg"
            data-testid={TID_TASK_SETTINGS_BUTTON}
            aria-label={settingsButtonLabel}
            disabled={!onSettingsButtonClick}
            onClick={onSettingsButtonClick}
          >
            <Settings className="size-4" />
          </Button>
        </ControlHintTooltip>
      </div>
    </footer>
  );
});
