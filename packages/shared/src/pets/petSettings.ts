import { PET_SIZE_DEFAULT_PX, PET_SIZE_MAX_PX, PET_SIZE_MIN_PX } from "./petFormat.js";
import type { PetSettings } from "./petTypes.js";

export function clampPetSize(size: number | undefined): number {
  if (size === undefined || !Number.isFinite(size)) return PET_SIZE_DEFAULT_PX;
  return Math.min(PET_SIZE_MAX_PX, Math.max(PET_SIZE_MIN_PX, Math.round(size)));
}

/**
 * 悬浮窗落点持久化专用合并：只写落点字段，绝不创建或改写 enabled / petId。
 * 当前没有宠物设置（用户从未启用，或读到的是尚未包含 pet 的旧快照）时返回 null，
 * 调用方应放弃本次写入——否则 mergePetSettings 的默认值（enabled:false、petId:null）
 * 会把用户刚选好的宠物静默关掉。
 */
export function mergePetPlacement(
  current: PetSettings | undefined,
  placement: { x: number; y: number; displayId?: number; snapZone?: PetSettings["windowSnapZone"] },
): PetSettings | null {
  if (!current) return null;
  return mergePetSettings(current, {
    windowPosition: { x: placement.x, y: placement.y },
    windowDisplayId: placement.displayId,
    windowSnapZone: placement.snapZone,
  });
}

/**
 * ISettingService.update 对顶层字段是整块覆盖；宠物设置有多个写入方
 * （设置页、main 拖拽落定），都必须经此合并，避免互相抹掉对方的字段。
 * patch 中值为 undefined 的字段表示清除。
 */
export function mergePetSettings(
  current: PetSettings | undefined,
  patch: Partial<PetSettings>,
): PetSettings {
  const merged: PetSettings = {
    enabled: current?.enabled ?? false,
    petId: current?.petId ?? null,
    ...current,
    ...patch,
  };
  for (const key of Object.keys(merged) as (keyof PetSettings)[]) {
    if (merged[key] === undefined) delete merged[key];
  }
  return merged;
}
