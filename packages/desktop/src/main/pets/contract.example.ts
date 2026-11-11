/**
 * pets-desktop 标准调用示例：main 在建 app ready 后注册动作通道，并随 renderer 状态同步窗口。
 */
import type { PetWindowControllerContract } from "./contract.js";
import { createPetWindowController } from "./petWindow.js";

export function syncPetOnce(
  controller: PetWindowControllerContract,
  state: Parameters<PetWindowControllerContract["syncState"]>[0],
): void {
  controller.syncState(state);
}

export const createController = createPetWindowController;
