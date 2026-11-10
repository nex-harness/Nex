import type { IProviderSettingsService } from "./providerFacadeServices.js";
import { lookupModelsDevModelInfo } from "./modelsDevCatalog.js";

/**
 * 给 provider-settings 服务装配"获取模型信息"（models.dev 手动查询）。
 *
 * 仅可在 Node 进程使用（依赖 node:fs 磁盘缓存）；浏览器 bundle 只拿到 RPC
 * 描述符与包装后的服务实例。resolveModelConfig 保持纯规则引擎结果，
 * models.dev 只在用户主动触发时按需查询。
 */
export function createProviderSettingsWithModelsDevLookup(
  base: IProviderSettingsService,
): IProviderSettingsService {
  return { ...base, lookupModelInfo: lookupModelsDevModelInfo };
}
