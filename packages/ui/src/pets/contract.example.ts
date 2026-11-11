/**
 * pets-ui 标准调用示例：调用方只经公开入口引入两个组件，并显式传入平台能力开关。
 */
import type { PetRuntimeContract, PetsSettingsSectionContract } from "./contract.js";

export function describePetSurfaces(isDesktop: boolean): {
  runtime: PetRuntimeContract;
  settings: PetsSettingsSectionContract;
} {
  return { runtime: { isDesktop }, settings: { isDesktop } };
}
