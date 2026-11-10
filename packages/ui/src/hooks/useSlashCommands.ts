/**
 * Nex Agent Slash Commands 便捷 hook
 *
 * 返回当前 workspace 下 Agent 广播的可用 slash commands 列表。
 */
import { useNexSessionStore, selectWorkspaceNexState } from "../store/nexSessionStore.js";

export function useSlashCommands(workspacePath: string, workspaceIdentity?: string) {
  return useNexSessionStore(
    (state) => selectWorkspaceNexState(state, workspacePath, workspaceIdentity).slashCommands,
  );
}
