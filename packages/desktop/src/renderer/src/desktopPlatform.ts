import {
  DesktopCommandIds,
  buildLocalMediaPreviewUrl,
  unwrapRemoteWorkspaceBindResult,
  type IPlatformService,
} from "@nex/shared";

import { desktopBrowserPlatformBridge } from "./desktopBrowserPlatformBridge.js";
import { createDesktopPluginSandboxPlatform } from "./plugin-sandbox/desktopPluginSandboxPlatform.js";

export function createDesktopPlatform(options: {
  isLocalDevelopmentRuntime: boolean;
}): IPlatformService {
  return {
    canSelectFilePath: true,
    canOpenLocalFiles: true,
    pluginSandbox: createDesktopPluginSandboxPlatform(),
    createLocalMediaPreviewUrl: buildLocalMediaPreviewUrl,
    isLocalDevelopmentRuntime: options.isLocalDevelopmentRuntime,
    selectDirectory: () => window.nex.selectDirectory(),
    selectDirectories: () => window.nex.selectDirectories(),
    selectFile: () => window.nex.selectFile(),
    selectFiles: () => window.nex.selectFiles?.() ?? Promise.resolve([]),
    createTempTextAttachment: (payload) => window.nex.createTempTextAttachment(payload),
    materializeWorkflowArtifactFile: (payload) =>
      window.nex.materializeWorkflowArtifactFile(payload),
    onRemoteConnectionLog: (handler) => window.nex.onRemoteConnectionLog(handler),
    onRemoteSessionClosed: (handler) => window.nex.onRemoteSessionClosed(handler),
    // Source 自动重连会替换 logical session；统一由 preload bridge 转发换代事件。
    onRemoteSessionReconnected: (handler) =>
      window.nex.onRemoteSessionReconnected?.(handler) ?? (() => {}),
    activateOrSetWorkspace: (path) =>
      window.nex.activateOrSetWorkspace?.(path) ?? Promise.resolve({ activated: false }),
    connectRemote: (remoteOptions, requestId, context) =>
      window.nex.connectRemote(remoteOptions, requestId, context),
    requestSourceSettingsAttachment: (sourceServerId) =>
      window.nex.requestSourceSettingsAttachment?.(sourceServerId) ??
      Promise.reject(new Error("Source settings attachment bridge unavailable")),
    cancelPendingRemoteConnection: (requestId) =>
      window.nex.cancelPendingRemoteConnection?.(requestId) ?? Promise.resolve(),
    bindRemoteWorkspaceSessionContext: async (context) => {
      if (!window.nex.bindRemoteWorkspaceSessionContext) {
        throw new Error("Desktop bindRemoteWorkspaceSessionContext bridge unavailable");
      }
      // preload 返回结构化结果，在页面一侧还原错误才能保留 code/retryable。
      return unwrapRemoteWorkspaceBindResult(
        await window.nex.bindRemoteWorkspaceSessionContext(context),
      );
    },
    startWebRemoteControl: (context) => window.nex.startWebRemoteControl(context),
    stopWebRemoteControl: () => window.nex.stopWebRemoteControl(),
    getWebRemoteControlStatus: () => window.nex.getWebRemoteControlStatus(),
    onWebRemoteControlStatusChanged: (handler) =>
      window.nex.onWebRemoteControlStatusChanged?.(handler) ?? (() => {}),
    disposeRemoteSession: (sessionId) => window.nex.disposeRemoteSession(sessionId),
    isDockerAvailable: () => window.nex.isDockerAvailable(),
    listWSLDistros: () => window.nex.listWSLDistros(),
    listDockerContainers: () => window.nex.listDockerContainers(),
    listSSHConfigAliases: () => window.nex.listSSHConfigAliases(),
    loadMcpFromUserDirectory: (payload) => window.nex.loadMcpFromUserDirectory(payload),
    saveMcpToUserDirectory: (payload) => window.nex.saveMcpToUserDirectory(payload),
    migrateLegacyCommonMcp: (payload) => window.nex.migrateLegacyCommonMcp(payload),
    openExternal: (url) => window.nex.openExternal(url),
    openFeedback: () => window.nex.executeDesktopCommand(DesktopCommandIds.OpenFeedback),
    openCommunity: () => window.nex.executeDesktopCommand(DesktopCommandIds.OpenCommunity),
    canOpenCommunity: (locale) => window.nex.canOpenCommunity(locale),
    openInFileManager: (path) => window.nex.openInFileManager(path),
    openExternalFile: (path) => window.nex.openExternalFile(path),
    openCuaPermissionOnboarding: window.nex.openCuaPermissionOnboarding
      ? (permissionOptions) =>
          window.nex.openCuaPermissionOnboarding?.(permissionOptions) ??
          Promise.resolve({ success: false, error: "not_supported" })
      : undefined,
    prepareCuaHelperPermissionDrag: window.nex.prepareCuaHelperPermissionDrag
      ? () =>
          window.nex.prepareCuaHelperPermissionDrag?.() ??
          Promise.resolve({ success: false, error: "not_supported" })
      : undefined,
    startCuaHelperPermissionDrag: window.nex.startCuaHelperPermissionDrag
      ? () => window.nex.startCuaHelperPermissionDrag?.()
      : undefined,
    notifyWebRemoteControlAuthStateChanged: (change) =>
      window.nex.notifyWebRemoteControlAuthStateChanged(change),
    onPaymentCallback: (callback) => window.nex.onPaymentCallback(callback),
    notifyRendererReady: () => window.nex.notifyRendererReady(),
    showTaskNotification: (payload) => window.nex.showTaskNotification(payload),
    syncWindowTabs: (paths) => window.nex.syncWindowTabs(paths),
    syncWebRemoteControlWorkspaces: (workspaces) =>
      window.nex.syncWebRemoteControlWorkspaces?.(workspaces),
    onWebRemoteControlReconnectWorkspace: (handler) =>
      window.nex.onWebRemoteControlReconnectWorkspace?.(handler) ?? (() => {}),
    onWebRemoteControlRendererAction: (handler) =>
      window.nex.onWebRemoteControlRendererAction?.(handler) ?? (() => {}),
    syncWindowUnreadCount: (count) => window.nex.syncWindowUnreadCount(count),
    syncActiveTaskSession: (sessionId) => window.nex.syncActiveTaskSession(sessionId),
    syncAppSettings: (patch) => window.nex.syncAppSettings?.(patch),
    syncPetState: (state) => window.nex.syncPetState?.(state),
    // 桥缺失（旧 preload 升级窗口）时按 supported 兜底，与 Windows 现状一致，设置开关可用。
    getCloseToTrayCapability: () =>
      window.nex.getCloseToTrayCapability?.() ??
      Promise.resolve({ supported: true, gnomeLikeWithoutTray: false }),
    setShortcutRecordingActive: (active) => window.nex.setShortcutRecordingActive?.(active),
    onFocusTab: (handler) => window.nex.onFocusTab(handler),
    onNewTab: (handler) => window.nex.onNewTab(handler),
    onCloseActiveContextRequest: (handler) =>
      window.nex.onCloseActiveContextRequest?.(handler) ?? (() => {}),
    onOpenBrowserUrl: (handler) => window.nex.onOpenBrowserUrl?.(handler) ?? (() => {}),
    onEmbeddedBrowserPermissionPrompt: (handler) =>
      window.nex.onEmbeddedBrowserPermissionPrompt?.(handler) ?? (() => {}),
    resolveEmbeddedBrowserPermissionPrompt: (request) =>
      window.nex.resolveEmbeddedBrowserPermissionPrompt?.(request),
    getEmbeddedBrowserSitePermissions: () =>
      window.nex.getEmbeddedBrowserSitePermissions?.() ?? Promise.resolve({}),
    setEmbeddedBrowserSitePermission: (request) =>
      window.nex.setEmbeddedBrowserSitePermission?.(request) ?? Promise.resolve({}),
    clearEmbeddedBrowserSitePermissions: () =>
      window.nex.clearEmbeddedBrowserSitePermissions?.() ?? Promise.resolve({}),
    resetEmbeddedBrowserSitePermission: (request) =>
      window.nex.resetEmbeddedBrowserSitePermission?.(request) ?? Promise.resolve({}),
    onBrowserViewScreenshotSurfacePrepare: (handler) =>
      window.nex.onBrowserViewScreenshotSurfacePrepare?.(handler) ?? (() => {}),
    onBrowserViewScreenshotSurfaceRelease: (handler) =>
      window.nex.onBrowserViewScreenshotSurfaceRelease?.(handler) ?? (() => {}),
    browserViewScreenshotSurfaceReady: (payload) =>
      window.nex.browserViewScreenshotSurfaceReady?.(payload),
    ...desktopBrowserPlatformBridge,
    onNewTask: (handler) => window.nex.onNewTask(handler),
    onOpenWorkspace: (handler) => {
      // 开发态或升级后的旧窗口可能仍运行未暴露 onOpenWorkspace 的 preload，
      // renderer 直接调用会在启动时崩溃。这里和 activateOrSetWorkspace 一样做兼容兜底，
      // 缺少该 bridge 时只禁用原生菜单回调，不影响应用继续打开。
      return window.nex.onOpenWorkspace?.(handler) ?? (() => {});
    },
    onOpenWorkspacePath: (handler) => window.nex.onOpenWorkspacePath?.(handler) ?? (() => {}),
    onOpenSettingsSection: (handler) => window.nex.onOpenSettingsSection?.(handler) ?? (() => {}),
    onWindowFullscreenChanged: (handler) => window.nex.onWindowFullscreenChanged(handler),
    getDesktopWindowChromeState: window.nex.getDesktopWindowChromeState
      ? () => window.nex.getDesktopWindowChromeState!()
      : undefined,
    onDesktopWindowChromeStateChanged: window.nex.onDesktopWindowChromeStateChanged
      ? (handler) => window.nex.onDesktopWindowChromeStateChanged!(handler)
      : undefined,
    getWindowControlsOverlayMetrics: () => window.nex.getWindowControlsOverlayMetrics?.() ?? null,
    onWindowControlsOverlayChanged: (handler) =>
      window.nex.onWindowControlsOverlayChanged?.(handler) ?? (() => {}),
    getDesktopZoomLevel: () =>
      window.nex.getDesktopZoomLevel?.() ?? Promise.resolve({ zoomLevel: 0 }),
    onDesktopZoomLevelChanged: (handler) =>
      window.nex.onDesktopZoomLevelChanged?.(handler) ?? (() => {}),
    onTaskNotificationClick: (handler) => window.nex.onTaskNotificationClick(handler),
    exportLogs: () => window.nex.exportLogs(),
    captureWindowScreenshot: () =>
      window.nex.captureWindowScreenshot?.() ?? Promise.resolve(null),
    onUpdateReady: (callback) => window.nex.onUpdateReady(callback),
    onUpdateCheckResult: (callback) => window.nex.onUpdateCheckResult(callback),
    onUpdateStateChanged: (callback) => window.nex.onUpdateStateChanged?.(callback) ?? (() => {}),
    getUpdateState: () =>
      window.nex.getUpdateState?.() ?? Promise.resolve({ kind: "idle", enabled: true }),
    downloadUpdate: () => window.nex.downloadUpdate?.() ?? Promise.resolve(),
    cancelUpdateDownload: () => window.nex.cancelUpdateDownload?.() ?? Promise.resolve(),
    openUpdateStatusWindow: () => window.nex.openUpdateStatusWindow?.() ?? Promise.resolve(),
    getAutoUpdatePreferences: () =>
      window.nex.getAutoUpdatePreferences?.() ??
      Promise.resolve({ autoDownloadAndInstallUpdates: false }),
    setAutoDownloadAndInstallUpdates: (enabled) =>
      window.nex.setAutoDownloadAndInstallUpdates?.(enabled) ?? Promise.resolve(),
    getDesktopSessionActivity: () =>
      window.nex.getDesktopSessionActivity?.() ??
      Promise.resolve({ runningAgentSessionCount: 0 }),
    getNexStdioTapDevState: () =>
      window.nex.getNexStdioTapDevState?.() ??
      Promise.resolve({ enabled: false, visible: false, logDir: "", statePath: "" }),
    onSettingsChanged: (callback) => window.nex.onSettingsChanged?.(callback) ?? (() => {}),
    onApplicationLocaleChanged: (callback) =>
      window.nex.onApplicationLocaleChanged?.(callback) ?? (() => {}),
    onPostUpdateReleaseNotes: (callback) => window.nex.onPostUpdateReleaseNotes(callback),
    acknowledgePostUpdateReleaseNotes: (version) =>
      window.nex.acknowledgePostUpdateReleaseNotes(version),
    skipUpdateVersion: (version) => window.nex.skipUpdateVersion?.(version) ?? Promise.resolve(),
    quitAndInstallUpdate: () => window.nex.quitAndInstallUpdate(),
    getInstalledEditors: (filePath) => window.nex.getInstalledEditors(filePath),
    getFileApplications: (target) => window.nex.getFileApplications(target),
    openFileWithApplication: (applicationId, target) =>
      window.nex.openFileWithApplication(applicationId, target),
    getApplicationIcon: (bundleId) =>
      window.nex.getApplicationIcon?.(bundleId) ?? Promise.resolve(null),
    openInEditor: (editorId, path, editorOptions) =>
      window.nex.openInEditor(editorId, path, editorOptions),
    executeDesktopCommand: (command) => window.nex.executeDesktopCommand(command),
    setApplicationLocale: (locale) => window.nex.setApplicationLocale(locale),
    getSystemLocale: () =>
      window.nex.getSystemLocale?.() ??
      Promise.resolve(navigator.language.toLowerCase().startsWith("zh") ? "zh-CN" : "en-US"),
    setTitleBarTheme: (theme) => window.nex.setTitleBarTheme(theme),
    getDeviceId: () =>
      (window as Window & { __NEX_DEVICE_ID__?: string }).__NEX_DEVICE_ID__ ?? "",
  };
}
