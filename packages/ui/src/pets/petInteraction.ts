/**
 * 宠物指针交互的纯逻辑（悬浮窗与 Web 挂件共用）：拖拽方向动效、动画优先级。
 */
import {
  PET_DRAG_DEAD_ZONE_PX,
  PET_FRAME_ROWS_V2,
  type PetPointerSample,
  type PetSemanticState,
} from "@nex/shared";

import { petStateToAnimation } from "./petStateMachine.js";

export type PetDragDirection = "running-left" | "running-right";

/**
 * 拖拽方向：相对上一次定向锚点水平位移 ≥ 4px 才切换方向，
 * 避免指针抖动让腿部动画来回闪。返回新的方向与锚点。
 */
export function updatePetDragDirection(
  current: { direction: PetDragDirection | null; anchorX: number },
  pointerX: number,
): { direction: PetDragDirection | null; anchorX: number } {
  const delta = pointerX - current.anchorX;
  if (delta >= PET_DRAG_DEAD_ZONE_PX) return { direction: "running-right", anchorX: pointerX };
  if (delta <= -PET_DRAG_DEAD_ZONE_PX) return { direction: "running-left", anchorX: pointerX };
  return current;
}

export interface PetPresentationInput {
  semantic: PetSemanticState;
  dragDirection: PetDragDirection | null;
  hovering: boolean;
  spriteRows: number;
  /** 光标相对宠物中心的扇区；仅悬停时有值。 */
  lookSector: number | null;
}

export interface PetPresentation {
  animationName: string;
  lookSector: number | null;
}

/**
 * 动画优先级：拖拽方向 > 悬停（v2 且 idle 时看向光标，否则 jumping）> 语义状态。
 */
export function resolvePetPresentation(input: PetPresentationInput): PetPresentation {
  if (input.dragDirection) return { animationName: input.dragDirection, lookSector: null };
  if (input.hovering) {
    const canLook =
      input.semantic === "idle" &&
      input.spriteRows >= PET_FRAME_ROWS_V2 &&
      input.lookSector !== null;
    if (canLook) return { animationName: "idle", lookSector: input.lookSector };
    return { animationName: "jumping", lookSector: null };
  }
  return { animationName: petStateToAnimation(input.semantic), lookSector: null };
}

/** 保留最近 160ms 内的采样，避免长拖拽无限增长。 */
export function appendPetPointerSample(
  samples: PetPointerSample[],
  sample: PetPointerSample,
  windowMs: number,
): PetPointerSample[] {
  const next = [...samples, sample];
  return next.filter((candidate) => sample.timeMs - candidate.timeMs <= windowMs);
}
