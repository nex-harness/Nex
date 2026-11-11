import type { TaskNotificationPayload, TaskNotificationPermission } from "@nex/shared";

interface BrowserNotificationInstance {
  onclick: ((event: unknown) => void) | null;
  close(): void;
}

export interface BrowserNotificationApi {
  new (
    title: string,
    options?: { body?: string; silent?: boolean; tag?: string },
  ): BrowserNotificationInstance;
  readonly permission: NotificationPermission;
  requestPermission(): Promise<NotificationPermission>;
}

export interface WebTaskNotifierDeps {
  /** 每次调用时解析，Notification 在非安全上下文中不存在。 */
  getNotificationApi: () => BrowserNotificationApi | undefined;
  hasFocus: () => boolean;
  focusWindow: () => void;
  playSound: () => Promise<void> | void;
  logger: { warn: (...args: unknown[]) => void };
}

function resolveNotificationTag(payload: TaskNotificationPayload): string {
  return ["nex-task", payload.taskId, payload.status, payload.requestId ?? ""].join(":");
}

export function createWebTaskNotifier(deps: WebTaskNotifierDeps) {
  const clickHandlers = new Set<(taskId: string) => void>();

  const getPermission = (): TaskNotificationPermission => {
    const api = deps.getNotificationApi();
    return api ? api.permission : "unsupported";
  };

  const requestPermission = async (): Promise<TaskNotificationPermission> => {
    const api = deps.getNotificationApi();
    if (!api) {
      return "unsupported";
    }
    try {
      return await api.requestPermission();
    } catch (error) {
      deps.logger.warn("[web-task-notification] requestPermission failed", error);
      return api.permission;
    }
  };

  const dispatchClick = (taskId: string) => {
    for (const handler of clickHandlers) {
      try {
        handler(taskId);
      } catch (error) {
        deps.logger.warn("[web-task-notification] click handler failed", error);
      }
    }
  };

  const show = (payload: TaskNotificationPayload) => {
    // 用户正在看这个页面时不需要再弹系统通知。
    if (deps.hasFocus()) {
      return;
    }

    const api = deps.getNotificationApi();
    if (!api || api.permission !== "granted") {
      return;
    }

    let notification: BrowserNotificationInstance;
    try {
      notification = new api(payload.title, {
        body: payload.body,
        silent: true,
        // 同一任务的同类通知互相替换，重复事件不会堆叠。
        tag: resolveNotificationTag(payload),
      });
    } catch (error) {
      // 例如 Android Chrome 只允许经 Service Worker 展示通知；此时也不应响提示音。
      deps.logger.warn("[web-task-notification] failed to show notification", error);
      return;
    }

    notification.onclick = () => {
      notification.close();
      deps.focusWindow();
      dispatchClick(payload.taskId);
    };
    void Promise.resolve(deps.playSound()).catch(() => {
      // 提示音失败（如自动播放限制）不影响通知本身。
    });
  };

  const onClick = (handler: (taskId: string) => void) => {
    clickHandlers.add(handler);
    return () => {
      clickHandlers.delete(handler);
    };
  };

  return { show, getPermission, requestPermission, onClick };
}
