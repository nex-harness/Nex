import type { BuiltinModelProviderId, IPlatformService } from "@nex/shared";
import type { IOAuthService } from "@nex/services";
import { resolveOAuthProviderIdByPreset } from "./utils.js";
import { logger } from "@/logger.js";

export async function startPresetSubscriptionLogin(params: {
  presetId: BuiltinModelProviderId;
  oauthService: IOAuthService;
  platform: IPlatformService;
  clearPending: () => void;
}) {
  const oauthProviderId = resolveOAuthProviderIdByPreset(params.presetId);
  if (!oauthProviderId) {
    return;
  }

  try {
    const { authorizeUrl, state, provider } = await params.oauthService.startOAuth(oauthProviderId);
    params.platform.registerOAuthState({ state, provider });
    params.platform.openExternal(authorizeUrl);
    logger.info("[ModelProviderSection] 已发起模型供应商 App OAuth 登录", {
      presetId: params.presetId,
      provider,
    });
  } catch (error) {
    logger.error("[ModelProviderSection] 发起模型供应商 App OAuth 登录失败", {
      presetId: params.presetId,
      error,
    });
    params.clearPending();
  }
}

