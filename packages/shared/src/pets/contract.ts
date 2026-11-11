/**
 * pets-shared 对外契约：跨进程/跨层共享的宠物数据形状与校验入口。
 * 所有宠物类型的唯一事实源在本目录，services / ui / desktop 只消费。
 */
export type {
  PetAnchor,
  PetListResult,
  PetLoadError,
  PetReleaseVelocity,
  PetSemanticState,
  PetSettings,
  PetSnapZone,
  PetSummary,
  PetWindowAction,
  PetWindowLabels,
  PetWindowPlacement,
  PetWindowState,
} from "./petTypes.js";
export type {
  PetFrameGrid,
  PetAnimationSpec,
  PetManifest,
  PetManifestValidation,
} from "./petManifest.js";
export type { PetVisibility } from "./petFormat.js";
export {
  PET_FRAME_HEIGHT,
  PET_FRAME_ROWS_V2,
  PET_FRAME_WIDTH,
  PET_TOGGLE_ACCELERATOR,
} from "./petFormat.js";
export { clampPetSize, mergePetPlacement, mergePetSettings } from "./petSettings.js";
export { parsePetManifest, petManifestSchema } from "./petManifest.js";
export { petWindowActionSchema } from "./petWindowActionSchema.js";
export { restorePetPlacement } from "./petPhysics.js";
export { resolvePetLookSector } from "./petLook.js";
