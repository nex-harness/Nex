/**
 * 桌面悬浮窗拖拽的纯几何/物理：松手速度、动量、吸附区、落点还原。
 * 常量对齐 docs/research/codex-pets-implementation.md 第 4 节。
 */
import type { PetSnapZone, PetWindowPlacement } from "./petTypes.js";

export const PET_DRAG_DEAD_ZONE_PX = 4;
export const PET_VELOCITY_WINDOW_MS = 160;
export const PET_VELOCITY_MIN_DT_MS = 8;
export const PET_VELOCITY_JITTER_PX_PER_S = 320;
export const PET_VELOCITY_MAX_PX_PER_S = 1600;
export const PET_THROW_MULTIPLIER = 3;
export const PET_MOMENTUM_TICK_MS = 16;
export const PET_MOMENTUM_FRICTION_BASE = 0.88;
export const PET_MOMENTUM_RESTITUTION = 0.7;
export const PET_MOMENTUM_STOP_SPEED_PX_PER_S = 65;
export const PET_MOMENTUM_MAX_MS = 900;
export const PET_SNAP_MARGIN_PX = 16;
export const PET_SNAP_ANIMATION_MS = 160;

export interface PetRect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface PetDisplayInfo {
  id: number;
  workArea: PetRect;
}

export interface PetPointerSample {
  x: number;
  y: number;
  timeMs: number;
}

/**
 * 从指针采样估算松手速度（px/s，已乘抛掷倍率）。
 * 只取最近 160ms；Δt 最小 8ms；低于 320px/s 视为抖动返回 null；上限 1600px/s。
 */
export function computePetReleaseVelocity(
  samples: readonly PetPointerSample[],
): { x: number; y: number } | null {
  const last = samples[samples.length - 1];
  if (!last) return null;
  const recent = samples.filter((sample) => last.timeMs - sample.timeMs <= PET_VELOCITY_WINDOW_MS);
  const first = recent.find((sample) => Math.hypot(last.x - sample.x, last.y - sample.y) >= 4);
  if (!first || first === last) return null;
  const dtMs = Math.max(PET_VELOCITY_MIN_DT_MS, last.timeMs - first.timeMs);
  let vx = ((last.x - first.x) / dtMs) * 1000;
  let vy = ((last.y - first.y) / dtMs) * 1000;
  const speed = Math.hypot(vx, vy);
  if (speed < PET_VELOCITY_JITTER_PX_PER_S) return null;
  if (speed > PET_VELOCITY_MAX_PX_PER_S) {
    const scale = PET_VELOCITY_MAX_PX_PER_S / speed;
    vx *= scale;
    vy *= scale;
  }
  return { x: vx * PET_THROW_MULTIPLIER, y: vy * PET_THROW_MULTIPLIER };
}

export interface PetMomentumState {
  x: number;
  y: number;
  vx: number;
  vy: number;
}

/** 动量积分一步：位移 → 边缘弹性碰撞 → 帧率无关摩擦。 */
export function stepPetMomentum(
  state: PetMomentumState,
  dtMs: number,
  size: { width: number; height: number },
  bounds: PetRect,
): PetMomentumState {
  let { x, y, vx, vy } = state;
  x += (vx * dtMs) / 1000;
  y += (vy * dtMs) / 1000;

  const minX = bounds.x;
  const maxX = bounds.x + bounds.width - size.width;
  const minY = bounds.y;
  const maxY = bounds.y + bounds.height - size.height;
  if (x < minX) {
    x = minX;
    vx = Math.abs(vx) * PET_MOMENTUM_RESTITUTION;
  } else if (x > maxX) {
    x = maxX;
    vx = -Math.abs(vx) * PET_MOMENTUM_RESTITUTION;
  }
  if (y < minY) {
    y = minY;
    vy = Math.abs(vy) * PET_MOMENTUM_RESTITUTION;
  } else if (y > maxY) {
    y = maxY;
    vy = -Math.abs(vy) * PET_MOMENTUM_RESTITUTION;
  }

  const friction = Math.pow(PET_MOMENTUM_FRICTION_BASE, dtMs / PET_MOMENTUM_TICK_MS);
  return { x, y, vx: vx * friction, vy: vy * friction };
}

export function isPetMomentumSettled(state: PetMomentumState, elapsedMs: number): boolean {
  return (
    elapsedMs >= PET_MOMENTUM_MAX_MS ||
    Math.hypot(state.vx, state.vy) < PET_MOMENTUM_STOP_SPEED_PX_PER_S
  );
}

/** 窗口中心落在工作区的哪个吸附区：水平三等分 × 垂直二等分。 */
export function classifyPetSnapZone(
  center: { x: number; y: number },
  workArea: PetRect,
): PetSnapZone {
  const horizontal = (center.x - workArea.x) / workArea.width;
  const vertical = (center.y - workArea.y) / workArea.height;
  const column = horizontal < 1 / 3 ? "left" : horizontal < 2 / 3 ? "center" : "right";
  const row = vertical < 0.5 ? "top" : "bottom";
  return `${row}-${column}` as PetSnapZone;
}

export function computePetSnapPosition(
  zone: PetSnapZone,
  workArea: PetRect,
  size: { width: number; height: number },
  margin: number = PET_SNAP_MARGIN_PX,
): { x: number; y: number } {
  const [row, column] = zone.split("-") as ["top" | "bottom", "left" | "center" | "right"];
  const x =
    column === "left"
      ? workArea.x + margin
      : column === "right"
        ? workArea.x + workArea.width - size.width - margin
        : workArea.x + Math.round((workArea.width - size.width) / 2);
  const y =
    row === "top" ? workArea.y + margin : workArea.y + workArea.height - size.height - margin;
  return { x, y };
}

export function clampPetToWorkArea(
  position: { x: number; y: number },
  size: { width: number; height: number },
  workArea: PetRect,
): { x: number; y: number } {
  const maxX = workArea.x + workArea.width - size.width;
  const maxY = workArea.y + workArea.height - size.height;
  return {
    x: Math.round(Math.min(Math.max(position.x, workArea.x), Math.max(workArea.x, maxX))),
    y: Math.round(Math.min(Math.max(position.y, workArea.y), Math.max(workArea.y, maxY))),
  };
}

function intersectionArea(a: PetRect, b: PetRect): number {
  const width = Math.min(a.x + a.width, b.x + b.width) - Math.max(a.x, b.x);
  const height = Math.min(a.y + a.height, b.y + b.height) - Math.max(a.y, b.y);
  return width > 0 && height > 0 ? width * height : 0;
}

/** 选出与窗口重叠最多的显示器；无重叠时取中心距离最近者。 */
export function pickPetDisplay(
  windowRect: PetRect,
  displays: readonly PetDisplayInfo[],
): PetDisplayInfo | null {
  let best: PetDisplayInfo | null = null;
  let bestArea = -1;
  for (const display of displays) {
    const area = intersectionArea(windowRect, display.workArea);
    if (area > bestArea) {
      best = display;
      bestArea = area;
    }
  }
  if (best && bestArea > 0) return best;
  const cx = windowRect.x + windowRect.width / 2;
  const cy = windowRect.y + windowRect.height / 2;
  let nearest: PetDisplayInfo | null = null;
  let nearestDistance = Infinity;
  for (const display of displays) {
    const dx = display.workArea.x + display.workArea.width / 2 - cx;
    const dy = display.workArea.y + display.workArea.height / 2 - cy;
    const distance = Math.hypot(dx, dy);
    if (distance < nearestDistance) {
      nearest = display;
      nearestDistance = distance;
    }
  }
  return nearest;
}

/**
 * 还原悬浮窗落点（建窗、改大小、显示器增删/分辨率变化共用）：
 * - 吸附态：在原显示器（已被拔除则取重叠最多者）的当前工作区重新计算吸附位置；
 * - 自由态：夹回可见工作区；
 * - 无历史落点：主显示器右下角（自由态）。
 */
export function restorePetPlacement(input: {
  placement: PetWindowPlacement | undefined;
  size: { width: number; height: number };
  displays: readonly PetDisplayInfo[];
  primaryDisplayId: number;
}): PetWindowPlacement | null {
  const { placement, size, displays, primaryDisplayId } = input;
  if (displays.length === 0) return null;

  if (!placement) {
    const primary = displays.find((display) => display.id === primaryDisplayId) ?? displays[0];
    if (!primary) return null;
    // 默认落在右下角，但不记吸附区：宠物默认可放在屏幕任意位置。
    return {
      ...computePetSnapPosition("bottom-right", primary.workArea, size),
      displayId: primary.id,
    };
  }

  const windowRect = { x: placement.x, y: placement.y, ...size };
  const stored =
    placement.displayId === undefined
      ? undefined
      : displays.find((display) => display.id === placement.displayId);
  const display = stored ?? pickPetDisplay(windowRect, displays);
  if (!display) return null;

  if (placement.snapZone) {
    return {
      ...computePetSnapPosition(placement.snapZone, display.workArea, size),
      displayId: display.id,
      snapZone: placement.snapZone,
    };
  }
  return {
    ...clampPetToWorkArea(placement, size, display.workArea),
    displayId: display.id,
  };
}
