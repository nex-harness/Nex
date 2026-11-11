/**
 * 宠物悬浮窗（单例）：透明、无边框、置顶、可点击、可拖拽。
 *
 * 窗口性质踩坑结论沿用 cuaPermissionDragPanel.ts：宠物窗需要接受拖拽/点击，
 * 因此 focusable 必须为 true——展示用 showInactive() 不抢焦点，窗口大小等于精灵大小，
 * 不存在需要穿透的透明区域，无需 input shape 命中测试。
 *
 * 状态流：renderer（PetRuntime）→ SyncPetState IPC → 本模块 → PetWindowState IPC → pet window。
 * 动作流：pet window → PetWindowAction IPC → 本模块（唯一的拖拽状态机所有者）→ onPlacementPersist。
 * 拖拽状态机：drag-start → drag-move 跟手 → drag-end：Alt 自由放置 / 有速度则动量 / 吸附 6 边缘区。
 * 几何与物理常量见 @nex/shared 的 petPhysics，详见 docs/specs/desktop-pets.md。
 */
import type { BrowserWindow, Display, Menu, screen as ElectronScreen } from "electron";
import {
  PET_FRAME_WIDTH,
  PET_MOMENTUM_MAX_MS,
  PET_MOMENTUM_TICK_MS,
  PET_SNAP_ANIMATION_MS,
  PET_TOGGLE_ACCELERATOR,
  PlatformChannels,
  classifyPetSnapZone,
  clampPetToWorkArea,
  computePetSnapPosition,
  isPetMomentumSettled,
  petWindowActionSchema,
  pickPetDisplay,
  restorePetPlacement,
  stepPetMomentum,
  type PetDisplayInfo,
  type PetMomentumState,
  type PetWindowAction,
  type PetWindowPlacement,
  type PetWindowState,
} from "@nex/shared";

import { petWindowSize, resolvePetMenuOffset } from "./petWindowLayout.js";

/** 诊断日志走 desktop main 的 logger 实现，由调用方注入（main 传 `./logger.js` 的 logger）。 */
interface PetWindowLogger {
  info(...args: unknown[]): void;
  warn(...args: unknown[]): void;
}

interface PetWindowDeps {
  logger: PetWindowLogger;
  BrowserWindow: typeof import("electron").BrowserWindow;
  screen: typeof ElectronScreen;
  app: Pick<typeof import("electron").app, "isPackaged">;
  /** on-demand 模式注册显隐快捷键；测试可注入。 */
  globalShortcut?: Pick<typeof import("electron").globalShortcut, "register" | "unregister">;
  preloadPath: string;
  rendererDir: string;
  rendererDevUrl?: string | undefined;
  /** 落点确定（拖拽落定 / 显示器变化重算）后由调用方持久化；模块自身不写 setting.json。 */
  onPlacementPersist: (placement: PetWindowPlacement) => void;
}

export type PetDragAction = Extract<
  PetWindowAction,
  { kind: "drag-start" | "drag-move" | "drag-end" }
>;

export interface PetWindowController {
  /** 同步状态；null 表示销毁窗口。 */
  syncState(state: PetWindowState | null): void;
  /** 处理 pet window 回传的拖拽动作。 */
  handleDragAction(action: PetDragAction): void;
  destroy(): void;
  isActive(): boolean;
  /** 判断某窗口是否为宠物悬浮窗（用于应用窗口列表排除）。 */
  ownsWindow(candidate: BrowserWindow): boolean;
  /**
   * 在宠物旁弹出菜单。宠物窗处于更高的窗口层级，菜单若在光标处弹出会被宠物自己盖住一半，
   * 所以固定贴着宠物窗口的左/右侧弹出（右侧放不下才放左侧）。
   */
  popupMenu(menu: Pick<Menu, "popup">): void;
}

function toDisplayInfo(display: Display): PetDisplayInfo {
  return { id: display.id, workArea: display.workArea };
}

export function createPetWindowController(deps: PetWindowDeps): PetWindowController {
  let win: BrowserWindow | null = null;
  let lastState: PetWindowState | null = null;
  /** 窗口当前的规范落点；窗口存在期间以用户拖拽结果为准，不被状态推送覆盖。 */
  let placement: PetWindowPlacement | undefined;
  let drag: { offsetX: number; offsetY: number } | null = null;
  let stopMotion: (() => void) | null = null;
  let displayListenersAttached = false;
  let shortcutRegistered = false;
  /** on-demand 模式下窗口是否被用户唤出；always 模式恒为 true。 */
  let revealed = true;
  let revealedAt = 0;

  const listDisplays = () => deps.screen.getAllDisplays().map(toDisplayInfo);

  const currentSize = () => petWindowSize(lastState?.sizePx ?? PET_FRAME_WIDTH);

  const cancelMotion = () => {
    stopMotion?.();
    stopMotion = null;
  };

  const setWindowPosition = (x: number, y: number) => {
    if (win && !win.isDestroyed()) win.setPosition(Math.round(x), Math.round(y));
  };

  const commit = (next: PetWindowPlacement) => {
    placement = next;
    deps.onPlacementPersist(next);
  };

  /** 缓动到目标位置（ease-out）；期间被新拖拽打断则取消。 */
  const animateTo = (target: { x: number; y: number }, onDone: () => void) => {
    cancelMotion();
    if (!win || win.isDestroyed()) return;
    const from = win.getBounds();
    if (from.x === target.x && from.y === target.y) {
      onDone();
      return;
    }
    const startedAt = Date.now();
    const timer = setInterval(() => {
      const progress = Math.min(1, (Date.now() - startedAt) / PET_SNAP_ANIMATION_MS);
      const eased = 1 - Math.pow(1 - progress, 3);
      setWindowPosition(from.x + (target.x - from.x) * eased, from.y + (target.y - from.y) * eased);
      if (progress >= 1) {
        cancelMotion();
        onDone();
      }
    }, PET_MOMENTUM_TICK_MS);
    stopMotion = () => clearInterval(timer);
  };

  const snapToNearestZone = () => {
    if (!win || win.isDestroyed()) return;
    const size = currentSize();
    const bounds = win.getBounds();
    const display = pickPetDisplay({ ...bounds, ...size }, listDisplays());
    if (!display) return;
    const zone = classifyPetSnapZone(
      { x: bounds.x + size.width / 2, y: bounds.y + size.height / 2 },
      display.workArea,
    );
    const target = computePetSnapPosition(zone, display.workArea, size);
    animateTo(target, () => commit({ ...target, displayId: display.id, snapZone: zone }));
  };

  const placeFreely = () => {
    if (!win || win.isDestroyed()) return;
    const size = currentSize();
    const bounds = win.getBounds();
    const display = pickPetDisplay({ ...bounds, ...size }, listDisplays());
    if (!display) return;
    const target = clampPetToWorkArea(bounds, size, display.workArea);
    animateTo(target, () => commit({ ...target, displayId: display.id }));
  };

  /** 落定：Alt 松手吸附到边缘区，默认自由放置（仅夹回可见区）。 */
  const settle = (snap: boolean) => {
    if (snap) snapToNearestZone();
    else placeFreely();
  };

  const startMomentum = (velocity: { x: number; y: number }, snap: boolean) => {
    cancelMotion();
    if (!win || win.isDestroyed()) return;
    const size = currentSize();
    const bounds = win.getBounds();
    const display = pickPetDisplay({ ...bounds, ...size }, listDisplays());
    if (!display) {
      settle(snap);
      return;
    }
    let state: PetMomentumState = { x: bounds.x, y: bounds.y, vx: velocity.x, vy: velocity.y };
    const startedAt = Date.now();
    let lastTickAt = startedAt;
    const timer = setInterval(() => {
      const now = Date.now();
      state = stepPetMomentum(state, now - lastTickAt, size, display.workArea);
      lastTickAt = now;
      setWindowPosition(state.x, state.y);
      if (isPetMomentumSettled(state, Math.min(now - startedAt, PET_MOMENTUM_MAX_MS))) {
        cancelMotion();
        settle(snap);
      }
    }, PET_MOMENTUM_TICK_MS);
    stopMotion = () => clearInterval(timer);
  };

  /** 显示器增删/分辨率变化或宠物大小变化：按新工作区重算落点（吸附态重新吸附，自由态夹回可见区）。 */
  const reflow = () => {
    if (!win || win.isDestroyed() || drag) return;
    cancelMotion();
    const size = currentSize();
    const bounds = win.getBounds();
    const restored = restorePetPlacement({
      placement: placement ?? { x: bounds.x, y: bounds.y },
      size,
      displays: listDisplays(),
      primaryDisplayId: deps.screen.getPrimaryDisplay().id,
    });
    if (!restored) return;
    win.setBounds({ x: restored.x, y: restored.y, ...size });
    if (
      !placement ||
      placement.x !== restored.x ||
      placement.y !== restored.y ||
      placement.displayId !== restored.displayId
    ) {
      commit(restored);
    }
  };

  const attachDisplayListeners = () => {
    if (displayListenersAttached) return;
    displayListenersAttached = true;
    deps.screen.on("display-added", reflow);
    deps.screen.on("display-removed", reflow);
    deps.screen.on("display-metrics-changed", reflow);
  };

  const detachDisplayListeners = () => {
    if (!displayListenersAttached) return;
    displayListenersAttached = false;
    deps.screen.removeListener("display-added", reflow);
    deps.screen.removeListener("display-removed", reflow);
    deps.screen.removeListener("display-metrics-changed", reflow);
  };

  const showWindow = (target: BrowserWindow) => {
    if (target.isDestroyed()) return;
    target.showInactive();
    // 修复原因（1.x 29a5db50）：visibleOnFullScreen: true 会让 Electron 调用 DockHide，
    // 把整个进程转成 accessory（UIElement）应用，导致 Dock 图标消失、Nex 窗口盖在全屏 app 上，
    // 并在从全屏 app 返回后丢失该屏幕的菜单栏（关闭宠物即恢复，已实测）。Codex 的悬浮窗也只是
    // floating 层级。skipTransformProcessType 保证无论如何都不改变应用激活策略。
    // 2.0 的 Dock 可见性由 desktopDockVisibility 控制器唯一持有，宠物窗不得调用 app.dock.show/hide
    // 或 setActivationPolicy；这里只设置窗口层级。
    target.setAlwaysOnTop(true, "floating");
    target.setVisibleOnAllWorkspaces(true, { skipTransformProcessType: true });
  };

  const toggleRevealed = () => {
    if (!win || win.isDestroyed()) return;
    if (revealed) {
      revealed = false;
      win.hide();
      return;
    }
    revealed = true;
    revealedAt = Date.now();
    // on-demand 的「点击窗口外隐藏」依赖 blur，窗口必须真正拿到焦点才会在失焦时触发。
    win.show();
    win.focus();
  };

  const syncShortcut = (visibility: PetWindowState["visibility"]) => {
    const register = deps.globalShortcut;
    if (!register) return;
    if (visibility === "on-demand" && !shortcutRegistered) {
      shortcutRegistered = register.register(PET_TOGGLE_ACCELERATOR, toggleRevealed);
      if (shortcutRegistered) {
        deps.logger.info(`[pets] 已注册全局快捷键 ${PET_TOGGLE_ACCELERATOR}`);
      } else {
        deps.logger.warn(`[pets] 全局快捷键 ${PET_TOGGLE_ACCELERATOR} 注册失败（可能被占用）`);
      }
    } else if (visibility !== "on-demand" && shortcutRegistered) {
      register.unregister(PET_TOGGLE_ACCELERATOR);
      shortcutRegistered = false;
    }
  };

  const applyVisibility = (target: BrowserWindow, state: PetWindowState) => {
    syncShortcut(state.visibility);
    if (state.visibility === "always") {
      if (!revealed || !target.isVisible()) {
        revealed = true;
        showWindow(target);
      }
      return;
    }
    // 快捷键注册失败时回退为常显，避免宠物永远无法唤出。
    if (!shortcutRegistered && deps.globalShortcut) {
      revealed = true;
      if (!target.isVisible()) showWindow(target);
    }
  };

  const ensureWindow = (state: PetWindowState): BrowserWindow => {
    if (win && !win.isDestroyed()) return win;

    const size = petWindowSize(state.sizePx);
    const restored = restorePetPlacement({
      placement: state.placement,
      size,
      displays: listDisplays(),
      primaryDisplayId: deps.screen.getPrimaryDisplay().id,
    });
    placement = restored ?? state.placement;

    const created = new deps.BrowserWindow({
      x: restored?.x ?? 0,
      y: restored?.y ?? 0,
      ...size,
      frame: false,
      transparent: true,
      hasShadow: false,
      resizable: false,
      minimizable: false,
      maximizable: false,
      fullscreenable: false,
      // 可点击可拖拽，但展示时不抢焦点。
      focusable: true,
      skipTaskbar: true,
      show: false,
      alwaysOnTop: true,
      webPreferences: {
        preload: deps.preloadPath,
        contextIsolation: true,
        nodeIntegration: false,
        sandbox: true,
      },
    });

    // renderer 多入口按源文件相对路径输出：pets/pet-window.html → out/renderer/pets/pet-window.html。
    if (!deps.app.isPackaged && deps.rendererDevUrl) {
      void created.loadURL(`${deps.rendererDevUrl}/pets/pet-window.html`);
    } else {
      void created.loadFile(`${deps.rendererDir}/pets/pet-window.html`);
    }

    revealed = state.visibility === "always";
    created.once("ready-to-show", () => {
      if (created.isDestroyed() || !lastState) return;
      if (
        lastState.visibility === "always" ||
        (lastState.visibility === "on-demand" && !shortcutRegistered)
      ) {
        revealed = true;
        showWindow(created);
      }
    });

    // on-demand：窗口失焦（点击窗口外）即隐藏；刚唤出的 300ms 内忽略，避免焦点交接误触发。
    created.on("blur", () => {
      if (lastState?.visibility !== "on-demand" || !revealed) return;
      if (Date.now() - revealedAt < 300) return;
      revealed = false;
      if (!created.isDestroyed()) created.hide();
    });

    created.on("closed", () => {
      win = null;
      drag = null;
      cancelMotion();
      detachDisplayListeners();
      syncShortcut("always");
    });

    attachDisplayListeners();
    win = created;
    return created;
  };

  const destroyWindow = () => {
    cancelMotion();
    drag = null;
    detachDisplayListeners();
    syncShortcut("always");
    if (win && !win.isDestroyed()) win.destroy();
    win = null;
  };

  return {
    syncState(state) {
      const previousSize = lastState?.sizePx;
      lastState = state;
      if (!state) {
        destroyWindow();
        return;
      }
      const target = ensureWindow(state);
      if (target.isDestroyed()) return;
      if (previousSize !== undefined && previousSize !== state.sizePx) {
        reflow();
      }
      applyVisibility(target, state);
      if (!target.webContents.isLoading()) {
        target.webContents.send(PlatformChannels.PetWindowState, state);
      } else {
        target.webContents.once("did-finish-load", () => {
          if (!target.isDestroyed() && lastState) {
            target.webContents.send(PlatformChannels.PetWindowState, lastState);
          }
        });
      }
    },
    handleDragAction(action) {
      if (!win || win.isDestroyed()) return;
      if (action.kind === "drag-start") {
        cancelMotion();
        const bounds = win.getBounds();
        drag = { offsetX: action.pointerX - bounds.x, offsetY: action.pointerY - bounds.y };
        return;
      }
      if (!drag) return;
      setWindowPosition(action.pointerX - drag.offsetX, action.pointerY - drag.offsetY);
      if (action.kind === "drag-move") return;
      drag = null;
      // 默认自由放置；按住 Alt 松手才吸附到 6 个边缘区。
      if (action.velocity) {
        startMomentum(action.velocity, action.altKey);
      } else {
        settle(action.altKey);
      }
    },
    destroy: destroyWindow,
    isActive() {
      return Boolean(win && !win.isDestroyed());
    },
    popupMenu(menu) {
      if (!win || win.isDestroyed()) {
        menu.popup();
        return;
      }
      const bounds = win.getBounds();
      const offset = resolvePetMenuOffset(
        bounds,
        pickPetDisplay(bounds, listDisplays())?.workArea ?? null,
      );
      menu.popup({ window: win, ...offset });
    },
    ownsWindow(candidate) {
      return Boolean(win && !win.isDestroyed() && win === candidate);
    },
  };
}

/** 注册 pet-window → main 的动作通道（载荷经 zod 校验，非法动作丢弃）。 */
export function registerPetWindowActionHandler(deps: {
  logger: PetWindowLogger;
  ipcMain: typeof import("electron").ipcMain;
  focusPrimaryWindow: () => void;
  /** 右键菜单（隐藏 / 设置）。 */
  showContextMenu: () => void;
  getPetWindowController: () => PetWindowController | null;
}): void {
  deps.ipcMain.on(PlatformChannels.PetWindowAction, (_event, payload: unknown) => {
    const parsed = petWindowActionSchema.safeParse(payload);
    if (!parsed.success) {
      deps.logger.warn("[pets] 丢弃非法的 pet window 动作");
      return;
    }
    const action = parsed.data;
    if (action.kind === "focus-main-window") {
      deps.focusPrimaryWindow();
      return;
    }
    if (action.kind === "show-context-menu") {
      deps.showContextMenu();
      return;
    }
    deps.getPetWindowController()?.handleDragAction(action);
  });
  deps.logger.info("[pets] pet window action channel registered");
}
