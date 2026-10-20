import { isRemoteWorkspaceIdentity } from "@nex/shared";

/**
 * UI 侧判断一个 workspace identity 是否属于桌面 remote session 体系。
 *
 * 多 runner 拓扑里 identity 有两种：
 * - `remote:`（SSH/WSL/Docker）：只有桌面 renderer 注册过 MessagePort 才有 services，
 *   未注册时必须走断连代理，不能回落 base services。
 * - `runner:<runnerId>:<path>`：与本地 workspace 共用同一条 server 连接，由 server
 *   按 runnerId 路由；UI 必须按 base/local services 处理，否则会误判为断连远端。
 *
 * 之前各调用点用 identity 非空代表“远端”，runner 身份会被送进远端等待态并让
 * 整个 workspace 静默不可用。身份键派生（`identity?.trim() || path`）不经过本函数。
 */
export function isClientRemoteWorkspaceIdentity(identity?: string | null): boolean {
  const trimmed = identity?.trim();
  return Boolean(trimmed && isRemoteWorkspaceIdentity(trimmed));
}
