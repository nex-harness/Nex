/**
 * pets-ui 模块公开入口（docs/specs/desktop-pets.md）。
 * 外部只从这里引用宠物 UI；其余实现文件（精灵播放、状态机、挂件）保持模块内部。
 */
export type { PetSemanticState } from "@nex/shared";

export { PetsSettingsSection } from "../settings/PetsSettingsSection.js";
export { PetRuntime } from "./PetRuntime.js";
export { PetSprite } from "./PetSprite.js";
export {
  appendPetPointerSample,
  resolvePetPresentation,
  updatePetDragDirection,
  type PetDragDirection,
  type PetPresentation,
  type PetPresentationInput,
} from "./petInteraction.js";
