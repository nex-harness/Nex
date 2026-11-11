import { useCallback, useEffect, useState } from "react";
import type { IPlatformService, TaskNotificationPermission } from "@nex/shared";
import { Button } from "@/components/ui/button.js";
import { useNexIntl } from "@/i18n/IntlProvider.js";
import { SettingsRow } from "@/settings/SettingsPageParts.js";

type PermissionPlatform = Pick<
  IPlatformService,
  "getTaskNotificationPermission" | "requestTaskNotificationPermission"
>;

/**
 * Web 浏览器通知权限行，四种状态都展示：default 给「允许」按钮，granted 显示已允许，
 * denied / unsupported 给出处理指引（浏览器不允许脚本再次弹出已被拒绝的授权框）。
 * Desktop 由系统通知接管，平台不实现这两个接口，因此整行不会出现。
 */
export function TaskNotificationPermissionRow({
  platform,
}: {
  platform: PermissionPlatform | undefined;
}) {
  const { intl } = useNexIntl();
  const readPermission = useCallback(
    (): TaskNotificationPermission | null => platform?.getTaskNotificationPermission?.() ?? null,
    [platform],
  );
  const [permission, setPermission] = useState(readPermission);

  useEffect(() => {
    setPermission(readPermission());
    // 用户可能在浏览器站点设置里改了权限，回到页面时重新读取。
    const refresh = () => setPermission(readPermission());
    window.addEventListener("focus", refresh);
    return () => window.removeEventListener("focus", refresh);
  }, [readPermission]);

  const requestPermission = useCallback(async () => {
    const next = await platform?.requestTaskNotificationPermission?.();
    setPermission(next ?? readPermission());
  }, [platform, readPermission]);

  if (permission === null) {
    return null;
  }

  return (
    <SettingsRow
      label={intl.formatMessage({ id: "settings.notificationBrowserPermission" })}
      description={intl.formatMessage({
        id: `settings.notificationBrowserPermission.${permission}`,
      })}
      control={
        permission === "default" ? (
          <Button size="sm" onClick={() => void requestPermission()}>
            {intl.formatMessage({ id: "settings.notificationBrowserPermissionAllow" })}
          </Button>
        ) : null
      }
    />
  );
}
