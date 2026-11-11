interface DockLike {
  show(): Promise<void> | void;
  hide(): void;
}

export interface DockVisibilityController {
  /** 用户设置：是否在 Dock 显示图标。 */
  setPreference(showDockIcon: boolean): void;
  /** 菜单栏图标是否可用；不可用时即使用户关闭 Dock 图标也必须保持显示，避免应用失去入口。 */
  setMenuBarEntryAvailable(available: boolean): void;
  /** 需要把应用带到前台（通知点击等）时调用；Dock 被用户隐藏时不会重新显示。 */
  revealForForeground(): void;
}

export function createDockVisibilityController(options: {
  platform: NodeJS.Platform;
  dock: DockLike | undefined;
  logger: { warn: (...args: unknown[]) => void };
}): DockVisibilityController {
  const dock = options.platform === "darwin" ? options.dock : undefined;
  let preference = true;
  let menuBarEntryAvailable = false;
  // 应用启动时 Dock 图标默认可见；以此为初值，避免首次应用同一状态时多余的原生调用。
  let applied: boolean | null = true;

  const shouldShow = () => preference || !menuBarEntryAvailable;

  const apply = (force = false) => {
    if (!dock) {
      return;
    }
    const next = shouldShow();
    if (!force && applied === next) {
      return;
    }
    applied = next;
    try {
      if (next) {
        void Promise.resolve(dock.show()).catch((error) => {
          options.logger.warn("[dock] failed to show dock icon", error);
        });
      } else {
        dock.hide();
      }
    } catch (error) {
      applied = null;
      options.logger.warn("[dock] failed to update dock icon visibility", error);
    }
  };

  return {
    setPreference(showDockIcon) {
      preference = showDockIcon;
      apply();
    },
    setMenuBarEntryAvailable(available) {
      menuBarEntryAvailable = available;
      apply();
    },
    revealForForeground() {
      if (shouldShow()) {
        apply(true);
      }
    },
  };
}
