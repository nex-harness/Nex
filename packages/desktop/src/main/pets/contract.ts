/**
 * pets-desktop 对外契约：main 进程中的宠物悬浮窗单例控制器与动作通道。
 * 调用方只从 `petWindow.ts` 引入；Electron 依赖由调用方注入（便于在无窗口环境构造）。
 */
import type { PetWindowAction, PetWindowPlacement, PetWindowState } from "@nex/shared";

/** 窗口尺寸与右键菜单位置的纯计算；不依赖 Electron。 */
export interface PetWindowLayoutContract {
  petWindowSize(sizePx: number): { width: number; height: number };
  resolvePetMenuOffset(
    windowBounds: { x: number; y: number; width: number; height: number },
    workArea: { x: number; y: number; width: number; height: number } | null,
  ): { x: number; y: number };
}

/** 悬浮窗控制器：状态单向推送，动作只回传用户手势，落点交回调用方持久化。 */
export interface PetWindowControllerContract {
  /** 同步状态；null 表示销毁窗口并注销全局快捷键。幂等。 */
  syncState(state: PetWindowState | null): void;
  /** 处理 pet window 回传的拖拽动作（已通过 zod 校验）。 */
  handleDragAction(
    action: Extract<PetWindowAction, { kind: "drag-start" | "drag-move" | "drag-end" }>,
  ): void;
  destroy(): void;
  isActive(): boolean;
  ownsWindow(candidate: unknown): boolean;
  /** 贴着宠物窗弹出原生菜单（宠物窗层级更高，光标锚定会被自身遮挡）。 */
  popupMenu(menu: { popup(options?: Record<string, unknown>): void }): void;
  /** 落点确定（拖拽落定 / 显示器变化重算）后由调用方持久化；模块自身不写 setting.json。 */
  onPlacementPersist(placement: PetWindowPlacement): void;
}
