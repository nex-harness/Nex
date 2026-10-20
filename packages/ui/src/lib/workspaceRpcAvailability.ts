import { isClientRemoteWorkspaceIdentity } from "@/lib/workspaceIdentity.js";

interface WorkspaceRpcAvailabilityTarget {
  workspaceIdentity?: string | null;
  remoteSessionId?: string | null;
  remoteTarget?: unknown;
}

function isRemoteWorkspaceRpcTarget(target: WorkspaceRpcAvailabilityTarget): boolean {
  // `runner:` 身份不需要 remoteSessionId 就能直接走 base RPC；只有 `remote:` 身份
  // 才要等 session attachment，否则 web 上的 runner workspace 会永久禁用 RPC。
  return Boolean(
    isClientRemoteWorkspaceIdentity(target.workspaceIdentity) ||
    target.remoteSessionId?.trim() ||
    target.remoteTarget,
  );
}

export function shouldEnableWorkspaceRpc(target: WorkspaceRpcAvailabilityTarget): boolean {
  return !isRemoteWorkspaceRpcTarget(target) || Boolean(target.remoteSessionId?.trim());
}
