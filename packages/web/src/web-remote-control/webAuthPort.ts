/**
 * Nex 无账号体系：Web 端登录已移除。
 *
 * 远控/分享页历史上通过 WebAuthService 读写 JWT 与当前账号。现在只保留端口形状：
 * 实现恒为未登录，登录入口为 no-op，避免把登录 UI 重新引回来。
 */
export type WebOAuthProviderId = "zai" | "bigmodel";

export interface WebAuthPort {
  getNexJwtToken(): string | null;
  getActiveAccount(): { provider: WebOAuthProviderId; displayName: string; avatarUrl?: string } | null;
  startLogin(options?: { provider?: WebOAuthProviderId; appReturnTo?: string }): void;
  logout(): Promise<void>;
}

/** 恒未登录的 inert 实现；所有登录动作都是 no-op。 */
export const INERT_WEB_AUTH_PORT: WebAuthPort = {
  getNexJwtToken: () => null,
  getActiveAccount: () => null,
  startLogin: () => {},
  logout: async () => {},
};
