import type { TaskNotificationPermission } from "@nex/shared";

/**
 * 浏览器只允许在用户手势里请求通知权限，且 `default` 之外的结果都不可再次弹窗，
 * 所以引导提示只在「总开关开 + 权限仍是 default + 从未提示过」时出现一次。
 */
export function shouldPromptTaskNotificationPermission(params: {
  notificationEnabled: boolean;
  permission: TaskNotificationPermission | null;
  alreadyPrompted: boolean;
}): boolean {
  return params.notificationEnabled && params.permission === "default" && !params.alreadyPrompted;
}
