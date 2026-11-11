import { app, Menu, nativeImage, Tray } from "electron";
import { join } from "node:path";
import {
  DesktopCommandIds,
  desktopMenuMessageIds,
  getDesktopMenuMessage,
  NEX_PRODUCT_FLAVOR,
  type DesktopCommandId,
  type Locale,
} from "@nex/shared";

let desktopTray: Tray | null = null;
let rebuildDesktopTrayContextMenu: (() => void) | null = null;

export function resolveDesktopTrayIconPath(platform: NodeJS.Platform = process.platform) {
  // macOS 使用单色 template 图（旁边的 @2x 由 Electron 按文件名自动选取），随菜单栏深浅反色。
  if (platform === "darwin") {
    return app.isPackaged
      ? join(process.resourcesPath, "trayTemplate.png")
      : join(import.meta.dirname, "../../build/trayTemplate.png");
  }
  // Linux 托盘只接受 PNG（.ico 不会渲染）；打包态 icon.png 已由 extraResources 无条件放入
  // resources 目录（electron-builder.config.js），Windows 继续用独立托盘 .ico 避免高 DPI 模糊。
  if (platform === "linux") {
    return app.isPackaged
      ? join(process.resourcesPath, "icon.png")
      : join(import.meta.dirname, "../../build/icon.png");
  }
  return app.isPackaged
    ? join(process.resourcesPath, "tray_icon.ico")
    : join(import.meta.dirname, "../../build/icon.ico");
}

function createDesktopTrayIcon(platform: NodeJS.Platform) {
  const iconPath = resolveDesktopTrayIconPath(platform);
  if (platform !== "darwin") {
    return iconPath;
  }
  const image = nativeImage.createFromPath(iconPath);
  if (image.isEmpty()) {
    throw new Error(`tray template icon missing or unreadable: ${iconPath}`);
  }
  image.setTemplateImage(true);
  return image;
}

/** Windows 通知区域、macOS 菜单栏与 Linux 托盘共用同一套菜单。 */
export function createDesktopTray(options: {
  platform?: NodeJS.Platform;
  getLocale: () => Locale;
  showCurrentWindow: () => Promise<void> | void;
  executeDesktopCommand: (command: DesktopCommandId) => Promise<unknown>;
  quitApp: () => void;
  logger: { warn: (...args: unknown[]) => void };
}) {
  const platform = options.platform ?? process.platform;
  // Linux 驻留能力由 createCloseToTrayCapabilityMonitor 探测决定（spec：
  // docs/desktop/linux-close-to-tray.md）；这里只挡平台，能力不过关时调用方不应传入 linux。
  if (platform !== "win32" && platform !== "linux" && platform !== "darwin") {
    return null;
  }

  if (desktopTray) {
    return desktopTray;
  }

  try {
    desktopTray = new Tray(createDesktopTrayIcon(platform));
  } catch (error) {
    options.logger.warn("[desktop-tray] failed to create tray icon", error);
    return null;
  }

  const getLabel = (id: (typeof desktopMenuMessageIds)[keyof typeof desktopMenuMessageIds]) =>
    getDesktopMenuMessage(options.getLocale(), id);
  const showTrayWindow = () => {
    void Promise.resolve(options.showCurrentWindow()).catch((error) => {
      options.logger.warn("[desktop-tray] failed to show current window", error);
    });
  };
  const executeTrayCommand = (command: DesktopCommandId) => {
    void Promise.resolve(options.showCurrentWindow())
      .then(() => options.executeDesktopCommand(command))
      .catch((error) => {
        options.logger.warn(`[desktop-tray] failed to execute tray command ${command}`, error);
      });
  };
  let contextMenu: Menu | null = null;
  const rebuildContextMenu = () => {
    desktopTray?.setToolTip(getLabel(desktopMenuMessageIds.trayTooltip));
    contextMenu = Menu.buildFromTemplate([
      {
        label: getLabel(desktopMenuMessageIds.trayOpenNex),
        click: showTrayWindow,
      },
      { type: "separator" },
      {
        label: getLabel(desktopMenuMessageIds.fileNewTask),
        click: () => executeTrayCommand(DesktopCommandIds.NewTask),
      },
      {
        label: getLabel(desktopMenuMessageIds.fileOpenWorkspace),
        click: () => executeTrayCommand(DesktopCommandIds.OpenWorkspace),
      },
      { type: "separator" },
      // 更新入口跟随产品身份：Preview（含生产后端的 Preview）禁用更新器，托盘也不能露出入口。
      ...(NEX_PRODUCT_FLAVOR === "production"
        ? [
            {
              label: getLabel(desktopMenuMessageIds.helpCheckForUpdates),
              click: () => executeTrayCommand(DesktopCommandIds.CheckForUpdates),
            },
          ]
        : []),
      {
        label: getLabel(desktopMenuMessageIds.helpAbout),
        click: () => executeTrayCommand(DesktopCommandIds.ShowAbout),
      },
      {
        // 托盘只提供日志诊断入口，避免右键菜单暴露清除全部数据操作；复用应用菜单导出命令。
        label: getLabel(desktopMenuMessageIds.helpExportLogs),
        click: () => executeTrayCommand(DesktopCommandIds.ExportLogs),
      },
      { type: "separator" },
      {
        label: getLabel(desktopMenuMessageIds.trayQuit),
        click: () => options.quitApp(),
      },
    ]);
    // macOS 上 setContextMenu 会让左键也弹菜单，而菜单栏图标的左键要直接显示主窗口，
    // 所以 macOS 不挂载菜单，改在 right-click 时手动弹出。
    if (platform !== "darwin") {
      desktopTray?.setContextMenu(contextMenu);
    }
  };

  rebuildDesktopTrayContextMenu = rebuildContextMenu;
  desktopTray.on("click", showTrayWindow);
  desktopTray.on("double-click", showTrayWindow);
  if (platform === "darwin") {
    desktopTray.on("right-click", () => {
      if (contextMenu) {
        desktopTray?.popUpContextMenu(contextMenu);
      }
    });
  }
  rebuildContextMenu();

  return desktopTray;
}

export function updateDesktopTrayMenu() {
  rebuildDesktopTrayContextMenu?.();
}
