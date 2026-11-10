export const NEX_BUILTIN_PROVIDER_CONFIG_FILE_ENV = "NEX_BUILTIN_PROVIDER_CONFIG_FILE";
export const NEX_BUILTIN_PROVIDER_BUNDLED_CONFIG_FILE_ENV =
  "NEX_BUILTIN_PROVIDER_BUNDLED_CONFIG_FILE";
export const NEX_PERSONAL_PROVIDER_CONFIG_FILE_ENV = "NEX_PERSONAL_PROVIDER_CONFIG_FILE";
export const PERSONAL_PROVIDER_CONFIG_FILE_NAME = "provider_config.json";

export interface NodeProviderRuntimePaths {
  readonly nexBuiltinFilePath: string;
  readonly personalFilePath: string;
}

export function createNodeProviderRuntimePathEnv(
  paths: NodeProviderRuntimePaths,
): Record<string, string> {
  return {
    [NEX_BUILTIN_PROVIDER_CONFIG_FILE_ENV]: paths.nexBuiltinFilePath,
    [NEX_PERSONAL_PROVIDER_CONFIG_FILE_ENV]: paths.personalFilePath,
  };
}

export function resolveNodeProviderRuntimePaths(
  env: Readonly<Record<string, string | undefined>>,
): NodeProviderRuntimePaths | null {
  const nexBuiltinFilePath = env[NEX_BUILTIN_PROVIDER_CONFIG_FILE_ENV]?.trim();
  const personalFilePath = env[NEX_PERSONAL_PROVIDER_CONFIG_FILE_ENV]?.trim();
  if (!nexBuiltinFilePath && !personalFilePath) return null;
  if (!nexBuiltinFilePath || !personalFilePath) {
    throw new Error("Nex Built-in 与 Personal Provider Config 路径必须同时提供");
  }
  return Object.freeze({ nexBuiltinFilePath, personalFilePath });
}
