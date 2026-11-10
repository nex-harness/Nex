/**
 * Provider 身份与本地凭据错误的共享定义。
 *
 * Nex 无账号体系：登录期 OAuth 类型已随账号能力一并移除，这里只保留
 * model-provider-family / credentialService 仍在消费的 provider id 与凭据解密错误契约。
 */

/** 内置 BigModel provider id */
export const BIGMODEL_PROVIDER_ID = "bigmodel" as const;

/** 内置 ZAI provider id */
export const ZAI_PROVIDER_ID = "zai" as const;

/** 凭据解密失败错误前缀 */
export const CREDENTIAL_DECRYPT_ERROR_PREFIX = "凭据解密失败：" as const;

/** 凭据解密失败稳定错误码 */
export const CREDENTIAL_DECRYPT_ERROR_CODE = "NEX_CREDENTIAL_DECRYPT_FAILED" as const;

/** 判断错误是否来自本地凭据解密失败 */
export function isCredentialDecryptError(error: unknown): boolean {
  const code = readCredentialErrorCode(error);
  if (code) {
    return code === CREDENTIAL_DECRYPT_ERROR_CODE;
  }

  // 兼容历史错误和跨边界丢失 code 的旧 payload；新错误应优先携带稳定 code。
  if (readCredentialErrorMessage(error).startsWith(CREDENTIAL_DECRYPT_ERROR_PREFIX)) {
    return true;
  }

  return false;
}

function readCredentialErrorCode(error: unknown): string {
  if (typeof error === "object" && error !== null && "code" in error) {
    return String((error as { code?: unknown }).code ?? "");
  }

  return "";
}

function readCredentialErrorMessage(error: unknown): string {
  if (error instanceof Error) {
    return error.message;
  }

  if (typeof error === "object" && error !== null && "message" in error) {
    return String((error as { message?: unknown }).message ?? "");
  }

  return "";
}

/** OAuth provider 标识 */
export type OAuthProviderId =
  | typeof BIGMODEL_PROVIDER_ID
  | typeof ZAI_PROVIDER_ID
  | (string & { readonly __oauthProviderBrand?: never });

/** 登录归因参数：来自官网中转页或投放链接（Nex 无账号体系后仅保留遥测参数结构）。 */
export interface OAuthLoginAttribution {
  channel_id?: string;
  utm_source?: string;
  utm_campaign?: string;
}

/** 用户资料形状；Nex 无账号体系后仍被 UI 类型边界引用（恒为 null）。 */
export interface UserInfo {
  id: string;
  username: string;
  displayName: string;
  avatarUrl?: string;
}

/** 缓存会话恢复结果；Nex 无账号体系后恒为 signed-out。 */
export type OAuthCachedSessionRestoreResult =
  | { status: "authenticated"; userInfo: UserInfo }
  | { status: "signed-out" }
  | { status: "reauthentication-required"; reason: "jwt-expired" };
