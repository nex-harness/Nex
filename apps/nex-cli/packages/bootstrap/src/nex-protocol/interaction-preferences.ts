import { nexWorkspaceUpdateInteractionPreferencesParamsSchema } from "@nex/shared";
import { parseParams, type NexProtocolAgentServerContext } from "./server-types.js";

/**
 * 应用 Host 全局交互偏好。共享 CLI 内各工作区收到相同设置，registry 是进程内
 * 主任务、后台任务与子 Agent 共用的权威 gate；Host 按物理连接串行同步。
 */
export async function updateInteractionPreferences(
  context: NexProtocolAgentServerContext,
  rawParams: unknown,
) {
  const params = parseParams(nexWorkspaceUpdateInteractionPreferencesParamsSchema, rawParams);
  const enabled = params.preferences.askUserQuestionAutoResolutionEnabled;
  context.appRuntimePreferences.askUserQuestionAutoResolutionEnabled = enabled;
  const snoozedInteractionCount =
    await context.v4Interactions.setAskUserQuestionAutoResolutionEnabled(enabled);

  return {
    workspace: params.workspace,
    askUserQuestionAutoResolutionEnabled: enabled,
    snoozedInteractionCount,
  };
}
