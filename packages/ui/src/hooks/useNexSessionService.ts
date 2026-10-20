import type { INexSessionService } from "@nex/services";
import { useOptionalWorkspaceServices } from "@/hooks/useWorkspaceServices.js";

export function useNexSessionService(
  workspacePath?: string,
  preferredRemoteSessionId?: string | null,
  workspaceIdentity?: string | null,
): INexSessionService {
  return useOptionalWorkspaceServices(workspacePath, preferredRemoteSessionId, workspaceIdentity)
    .nexSessionService;
}
