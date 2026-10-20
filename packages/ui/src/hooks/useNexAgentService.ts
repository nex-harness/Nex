import type { INexAgentService } from "@nex/services";
import { useOptionalWorkspaceServices } from "@/hooks/useWorkspaceServices.js";

export function useNexAgentService(
  workspacePath?: string,
  preferredRemoteSessionId?: string | null,
  workspaceIdentity?: string | null,
): INexAgentService {
  return useOptionalWorkspaceServices(workspacePath, preferredRemoteSessionId, workspaceIdentity)
    .nexAgentService;
}
