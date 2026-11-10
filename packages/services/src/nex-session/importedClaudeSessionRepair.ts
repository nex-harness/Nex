import type { NexSessionStateSnapshot } from "@nex/shared";
import { createServiceLogger } from "#src/logger/serviceLogger.js";
import { repairImportedClaudeSessionSnapshot } from "#src/session/claude-native/importedClaudeHistoryRepair.js";
import type { INexAgentService } from "#src/nex-agent/nexAgent.js";
import type {
  NexSessionReadParams,
  NexSessionResumeParams,
} from "#src/nex-session/nexSession.js";

const logger = createServiceLogger("nex-session-service");

export async function repairEmptyImportedClaudeSessionSnapshot(params: {
  agentService: INexAgentService;
  snapshot: NexSessionStateSnapshot;
  target: NexSessionResumeParams | NexSessionReadParams;
}): Promise<NexSessionStateSnapshot> {
  const repaired = await repairImportedClaudeSessionSnapshot({
    snapshot: params.snapshot,
    target: {
      workspacePath: params.target.workspacePath,
      workspaceIdentity: params.target.workspaceIdentity,
      taskId: params.target.sessionId,
      ...("mcpServers" in params.target && params.target.mcpServers
        ? { mcpServers: params.target.mcpServers }
        : {}),
    },
    createSession: (input) => params.agentService.createSession(input),
    onRepair: (history) => {
      logger.warn(
        undefined,
        `[nex-session-service] Claude 导入 session 历史异常，按 ${history.source} 回填 taskId=${params.target.sessionId}`,
      );
    },
  });
  return repaired ?? params.snapshot;
}
