/**
 * pets 模块公开契约：桌面宠物目录服务。
 * 只允许从这里 import；实现细节（fs 扫描、WebP 头解析）都在模块内部。
 * 数据类型（PetSummary/PetLoadError/...）的唯一事实源在 @nex/shared。
 */
import type { PetListResult } from "@nex/shared";
import { ServiceChannels } from "@nex/shared";

import { createServiceDescriptor } from "../descriptors.js";

export interface IPetService {
  /** 扫描 {nexDataRoot}/pets/，返回合法宠物清单与逐目录错误。目录不存在返回空。 */
  listPets(): Promise<PetListResult>;
  /** 清缓存后重新扫描。 */
  refreshPets(): Promise<PetListResult>;
  /**
   * 解析某只宠物精灵图的绝对路径（供桌面 nex-media 授权 / server 静态路由使用）。
   * 宠物不存在或精灵图缺失时返回 null。返回路径一定位于 pets 根目录内。
   */
  getPetSpritesheetPath(params: { petId: string }): Promise<{ path: string } | null>;
  /**
   * 桌面端专用：授权并返回可直接渲染的 URL（nex-media 授权协议）。
   * Web/未注入授权函数的宿主返回 null，由调用方回落到 server 静态路由。
   */
  getPetSpritesheetUrl(params: { petId: string }): Promise<{ url: string } | null>;
}

export const IPetService = createServiceDescriptor<IPetService>(ServiceChannels.Pets);
