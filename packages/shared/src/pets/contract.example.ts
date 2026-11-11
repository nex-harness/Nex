/**
 * pets-shared 标准调用示例：合并设置、校验窗口动作、还原落点。
 */
import type { PetSettings } from "./contract.js";
import { mergePetSettings, petWindowActionSchema, restorePetPlacement } from "./contract.js";

export function applyPetPreferences(
  current: PetSettings | undefined,
  enable: boolean,
  displays: Array<{
    id: number;
    workArea: { x: number; y: number; width: number; height: number };
  }>,
): { settings: PetSettings; placement: { x: number; y: number } | null; dragValid: boolean } {
  const settings = mergePetSettings(current, { enabled: enable });
  const placement = restorePetPlacement({
    placement: settings.windowPosition ? { ...settings.windowPosition } : undefined,
    size: { width: 112, height: 121 },
    displays,
    primaryDisplayId: displays[0]?.id ?? 0,
  });
  const dragValid = petWindowActionSchema.safeParse({
    kind: "drag-end",
    pointerX: 0,
    pointerY: 0,
    altKey: false,
  }).success;
  return { settings, placement, dragValid };
}
