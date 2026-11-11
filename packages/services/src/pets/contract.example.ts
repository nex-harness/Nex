/**
 * contract 使用示例（架构检查要求的可编译样例）。
 * 展示消费方如何通过公开契约使用 pets 服务。
 */
import type { IPetService } from "./contract.js";

export async function listPetNames(petService: IPetService): Promise<string[]> {
  const { pets } = await petService.listPets();
  return pets.map((pet) => pet.displayName);
}
