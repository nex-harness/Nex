import { materializeNexBuiltinProviderConfig } from "@nex/services/node";

declare const __NEX_BUILTIN_PROVIDER_CONFIG_JSON__: string | undefined;

export interface MaterializeBundledNexBuiltinProviderConfigOptions {
  readonly environmentConfigRoot: string;
  readonly content: string;
}

/** 返回构建时嵌入远端 Server 的 Nex Built-in Provider Config。 */
export function readBundledNexBuiltinProviderConfig(): string {
  if (typeof __NEX_BUILTIN_PROVIDER_CONFIG_JSON__ !== "string") {
    throw new Error("当前构建未嵌入 Nex Built-in Provider Config");
  }
  return __NEX_BUILTIN_PROVIDER_CONFIG_JSON__;
}

/**
 * 将 Nex Built-in Config 原子物化到所属环境的固定资源副本。
 * 升级前退出旧进程；不保留按内容 hash 增长的历史文件。
 */
export async function materializeBundledNexBuiltinProviderConfig(
  options: MaterializeBundledNexBuiltinProviderConfigOptions,
): Promise<string> {
  return materializeNexBuiltinProviderConfig(options);
}
