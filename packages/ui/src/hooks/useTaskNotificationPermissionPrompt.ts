import { useEffect } from "react";
import type { IPlatformService } from "@nex/shared";
import { toast } from "@/components/ui/toast.js";
import { useNexIntl } from "@/i18n/IntlProvider.js";
import { shouldPromptTaskNotificationPermission } from "@/lib/taskNotificationPermissionPrompt.js";
import {
  hasPromptedTaskNotificationPermission,
  persistTaskNotificationPermissionPrompted,
} from "@/lib/taskNotificationPreferences.js";
import { useNexStore } from "@/store/StoreProvider.js";

type PermissionPlatform = Pick<
  IPlatformService,
  "getTaskNotificationPermission" | "requestTaskNotificationPermission"
>;

/**
 * Web 首次进入时提示一次浏览器通知授权。设置页的权限行需要用户主动去找，
 * 没人点开设置就永远收不到通知；这里补上一个带「允许」按钮的入口，
 * 点击即用户手势，可以直接调用 requestPermission。Desktop 没有权限接口，不会提示。
 */
export function useTaskNotificationPermissionPrompt(platform: PermissionPlatform): void {
  const { intl } = useNexIntl();
  const notificationEnabled = useNexStore((state) => state.notificationEnabled);

  useEffect(() => {
    const permission = platform.getTaskNotificationPermission?.() ?? null;
    if (
      !shouldPromptTaskNotificationPermission({
        notificationEnabled,
        permission,
        alreadyPrompted: hasPromptedTaskNotificationPermission(),
      })
    ) {
      return;
    }
    persistTaskNotificationPermissionPrompted();
    toast(
      `${intl.formatMessage({ id: "notification.permissionPrompt.title" })}\n${intl.formatMessage({
        id: "notification.permissionPrompt.body",
      })}`,
      {
        variant: "info",
        position: "bottom-center",
        durationMs: 0,
        dismissible: true,
        dismissLabel: intl.formatMessage({ id: "notification.permissionPrompt.dismiss" }),
        actionLabel: intl.formatMessage({ id: "settings.notificationBrowserPermissionAllow" }),
        onAction: () => {
          void platform.requestTaskNotificationPermission?.();
        },
        dedupeKey: "task-notification-permission-prompt",
      },
    );
  }, [intl, notificationEnabled, platform]);
}
