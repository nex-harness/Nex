import type { IPlatformService } from "@nex/shared";

type DesktopBrowserPlatformBridge = Pick<
  IPlatformService,
  | "onBrowserViewDownloadStarted"
  | "onBrowserViewReady"
  | "onBrowserViewOperation"
  | "onBrowserViewViewportChanged"
  | "onBrowserViewVisibility"
  | "onBrowserViewCloseTab"
  | "onBrowserViewSuspend"
  | "onBrowserViewRestore"
  | "browserViewLoadUrl"
  | "browserViewAttachGuest"
  | "browserViewDetachGuest"
  | "browserViewCloseTab"
  | "browserViewReportResidency"
  | "browserViewPromoteDraftTabs"
  | "browserViewSuspendReady"
  | "browserViewEnsureResident"
  | "browserViewRestoreTabs"
  | "browserViewUpdateViewport"
  | "importChromeBrowserData"
  | "clearEmbeddedBrowserData"
  | "getPathForFile"
  | "saveFile"
  | "printPageToPdf"
  | "microphone"
  | "captureBrowserGuestPage"
>;

// Rebase 集成：browser bridge 若继续内联在 renderer 入口，会让入口越过 max-lines 门禁。
// 独立对象只做 preload 委托与旧 bridge 兼容兜底，不持有 Browser 业务状态。
export const desktopBrowserPlatformBridge = {
  getPathForFile: (file) => window.nex.getPathForFile?.(file) ?? null,
  saveFile: (payload) =>
    window.nex.saveFile?.(payload) ?? Promise.resolve({ success: false, error: "not_supported" }),
  // 条件定义而非兜底返回失败：UI 靠方法是否存在做能力检测，旧 preload 下必须保持 undefined
  printPageToPdf: window.nex.printPageToPdf ? () => window.nex.printPageToPdf!() : undefined,
  // 与 printPageToPdf 一样按 preload 是否提供决定能力是否存在，UI 据此决定是否显示语音输入。
  microphone:
    window.nex.ensureMicrophoneAccess &&
    window.nex.resetMicrophoneAccess &&
    window.nex.openMicrophoneSettings
      ? {
          ensureAccess: () => window.nex.ensureMicrophoneAccess!(),
          resetAccess: () => window.nex.resetMicrophoneAccess!(),
          openSettings: (target) => window.nex.openMicrophoneSettings!(target),
        }
      : undefined,
  onBrowserViewDownloadStarted: (handler) =>
    window.nex.onBrowserViewDownloadStarted?.(handler) ?? (() => {}),
  captureBrowserGuestPage: window.nex.captureBrowserGuestPage
    ? (payload) => window.nex.captureBrowserGuestPage!(payload)
    : undefined,
  onBrowserViewReady: (handler) => window.nex.onBrowserViewReady?.(handler) ?? (() => {}),
  onBrowserViewOperation: (handler) => window.nex.onBrowserViewOperation?.(handler) ?? (() => {}),
  onBrowserViewViewportChanged: (handler) =>
    window.nex.onBrowserViewViewportChanged?.(handler) ?? (() => {}),
  onBrowserViewVisibility: (handler) =>
    window.nex.onBrowserViewVisibility?.(handler) ?? (() => {}),
  onBrowserViewCloseTab: (handler) => window.nex.onBrowserViewCloseTab?.(handler) ?? (() => {}),
  onBrowserViewSuspend: (handler) => window.nex.onBrowserViewSuspend?.(handler) ?? (() => {}),
  onBrowserViewRestore: (handler) => window.nex.onBrowserViewRestore?.(handler) ?? (() => {}),
  browserViewLoadUrl: (payload) => window.nex.browserViewLoadUrl!(payload),
  browserViewAttachGuest: (payload) =>
    window.nex.browserViewAttachGuest?.(payload) ??
    Promise.resolve({ ok: false, reason: "not-found", recoveryRequested: false }),
  browserViewDetachGuest: (payload) =>
    window.nex.browserViewDetachGuest?.(payload) ?? Promise.resolve(false),
  browserViewCloseTab: (payload) =>
    window.nex.browserViewCloseTab?.(payload) ?? Promise.resolve(),
  browserViewReportResidency: (payload) =>
    window.nex.browserViewReportResidency?.(payload) ?? Promise.resolve(),
  browserViewPromoteDraftTabs: (payload) =>
    window.nex.browserViewPromoteDraftTabs?.(payload) ?? Promise.resolve([]),
  browserViewSuspendReady: (payload) =>
    window.nex.browserViewSuspendReady?.(payload) ?? Promise.resolve(),
  browserViewEnsureResident: (payload) =>
    window.nex.browserViewEnsureResident?.(payload) ?? Promise.resolve(),
  browserViewRestoreTabs: (payload) =>
    window.nex.browserViewRestoreTabs?.(payload) ?? Promise.resolve([]),
  browserViewUpdateViewport: (payload) =>
    window.nex.browserViewUpdateViewport?.(payload) ?? Promise.resolve(),
  importChromeBrowserData: (options) =>
    window.nex.importChromeBrowserData?.(options) ??
    Promise.resolve({
      success: false,
      cookies: { imported: 0, skipped: 0, failed: 0 },
      localStorage: {
        originsImported: 0,
        entriesImported: 0,
        originsSkipped: 0,
        originsFailed: 0,
      },
      error: "unsupported",
    }),
  clearEmbeddedBrowserData: (mode) =>
    window.nex.clearEmbeddedBrowserData?.(mode) ??
    Promise.resolve({ success: false, error: "unsupported" }),
} satisfies DesktopBrowserPlatformBridge;
