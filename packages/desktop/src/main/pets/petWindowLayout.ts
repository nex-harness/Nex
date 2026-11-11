/**
 * 宠物悬浮窗的纯布局计算：窗口尺寸与右键菜单落点。不依赖 Electron，便于单测。
 */
import {
  PET_CONTROLS_HEIGHT_PX,
  PET_CONTROLS_MIN_WIDTH_PX,
  PET_FRAME_HEIGHT,
  PET_FRAME_WIDTH,
  type PetRect,
} from "@nex/shared";

/** 原生菜单的估计宽度（px）：实测英文「Hide / Settings」菜单约 83px；用于左侧放置的偏移和右侧空间判断。 */
const PET_MENU_ESTIMATED_WIDTH_PX = 96;
const PET_MENU_GAP_PX = 4;

/** 窗口 = 精灵 + 下方固定高度的控制行（悬停时显示控制条，常态透明）；宽度至少容纳控制条。 */
export function petWindowSize(sizePx: number): { width: number; height: number } {
  return {
    width: Math.max(Math.round(sizePx), PET_CONTROLS_MIN_WIDTH_PX),
    height: Math.round((sizePx * PET_FRAME_HEIGHT) / PET_FRAME_WIDTH) + PET_CONTROLS_HEIGHT_PX,
  };
}

/**
 * 右键菜单相对宠物窗口内容区的弹出位置。宠物窗层级更高，菜单在光标处弹出会被宠物自己盖住一半，
 * 所以贴着窗口右侧弹出，右侧放不下才放左侧；纵向与精灵上部对齐。
 */
export function resolvePetMenuOffset(
  windowBounds: PetRect,
  workArea: PetRect | null,
): { x: number; y: number } {
  const rightSpace = workArea
    ? workArea.x + workArea.width - (windowBounds.x + windowBounds.width)
    : Number.POSITIVE_INFINITY;
  const fitsRight = rightSpace >= PET_MENU_ESTIMATED_WIDTH_PX + PET_MENU_GAP_PX;
  return {
    x: fitsRight
      ? windowBounds.width + PET_MENU_GAP_PX
      : -(PET_MENU_ESTIMATED_WIDTH_PX + PET_MENU_GAP_PX),
    y: Math.round(windowBounds.height / 4),
  };
}
