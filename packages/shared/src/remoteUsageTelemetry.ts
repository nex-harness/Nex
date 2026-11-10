/**
 * 只提取 workspace 场景枚举；未知远端身份仍为 remote。
 *
 * Nex 不上报遥测：本函数保留给仍按 workspace 维度记录的本地事实（会话/任务归属等），
 * 不再产出任何上报 payload，也不向业务层暴露地址或路径。
 */
import { parseRemoteWorkspaceIdentity } from "./remote-workspace-identity.js";
import type { RemoteWorkspaceIdentityKind } from "./remote-workspace-identity.js";

export function resolveWorkspaceTelemetryDetail(scope: {
  workspaceIdentity?: string | null;
  remoteSessionId?: string | null;
}): { workspace_kind: "local" | "remote"; remote_kind: RemoteWorkspaceIdentityKind | "" } {
  const identity = scope.workspaceIdentity?.trim();
  return {
    workspace_kind: identity || scope.remoteSessionId?.trim() ? "remote" : "local",
    remote_kind: identity ? (parseRemoteWorkspaceIdentity(identity)?.kind ?? "") : "",
  };
}
