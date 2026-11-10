import type {
  ModelSelection,
  NexAgentMcpServer,
  NexAutomationScheduleRule,
  NexMcpListMode,
} from "@nex/shared";
import type { McpAppsAppToolCallResult, McpAppsAppToolDescriptor } from "@nex/shared/mcp-apps";

export interface NexAgentWorkspaceTarget {
  workspacePath: string;
  workspaceIdentity?: string;
  /** 远程 workspace 的运行时会话身份；只用于隔离/路由，不能替代 workspacePath。 */
  remoteSessionId?: string;
}

export interface NexAgentPluginMutationTarget extends NexAgentWorkspaceTarget {
  /** 仅 Host 观测使用；不会传给 CLI。 */
  telemetry?: {
    entry?:
      | "store_list"
      | "store_detail"
      | "side_pane_market"
      | "recommended_prompt"
      | "recommended_ui"
      | "component_settings";
    client_kind?: "desktop" | "mobile";
    trigger?: "user" | "automatic" | "dependency" | "restore" | "sync";
  };
}

export interface NexAgentPluginViewParams extends NexAgentWorkspaceTarget {
  configScope?: "user" | "workspace";
}

export interface NexAgentListMcpServerStatusesParams extends NexAgentWorkspaceTarget {
  mcpServers?: NexAgentMcpServer[];
  mode?: NexMcpListMode;
}

export interface NexAgentAddPluginMarketplaceParams extends NexAgentPluginMutationTarget {
  dryRun?: boolean;
  operationId?: string;
  source: string;
}

export interface NexAgentRemovePluginMarketplaceParams extends NexAgentPluginMutationTarget {
  marketplace: string;
}

export interface NexAgentUpdatePluginMarketplaceParams extends NexAgentPluginMutationTarget {
  marketplace?: string;
  operationId?: string;
}

export interface NexAgentInstallPluginParams extends NexAgentPluginMutationTarget {
  dryRun?: boolean;
  marketplace: string;
  operationId?: string;
  pluginName: string;
  scope?: "user" | "workspace";
}

export interface NexAgentCancelPluginOperationParams {
  operationId: string;
}

export interface NexAgentUninstallPluginParams extends NexAgentPluginMutationTarget {
  marketplace?: string;
  pluginId?: string;
  pluginName?: string;
  removeCache?: boolean;
}

export interface NexAgentUpdatePluginParams extends NexAgentPluginMutationTarget {
  pluginId?: string;
  marketplace?: string;
}

export interface NexAgentRestoreBuiltinPluginParams extends NexAgentPluginMutationTarget {
  pluginId: string;
}

export interface NexAgentConfigurePluginParams extends NexAgentPluginMutationTarget {
  clearOptionKeys?: string[];
  dryRun?: boolean;
  options: Record<string, unknown>;
  pluginId: string;
  scope?: "user" | "workspace";
}

export interface NexAgentResetPluginConfigParams extends NexAgentPluginMutationTarget {
  pluginId: string;
  scope?: "user" | "workspace";
}

export interface NexAgentValidatePluginParams extends NexAgentWorkspaceTarget {
  marketplace?: string;
  pluginName?: string;
  source?: string;
}

export interface NexAgentDescribePluginParams extends NexAgentWorkspaceTarget {
  marketplace: string;
  pluginName: string;
}

export interface NexAgentSetPluginEnabledParams extends NexAgentPluginMutationTarget {
  enabled: boolean;
  operationId?: string;
  pluginId: string;
  scope?: "user" | "workspace";
}

// Plugin 对话引用 catalog：
// 带 sessionId → session-owned 冻结 catalog（必须路由到持有该 session 的 workspace client）；
// 不带 → workspace 当前 catalog（新建草稿 Picker）。
export interface NexAgentPluginReferenceCatalogParams extends NexAgentWorkspaceTarget {
  sessionId?: string;
}

// Composer Skill catalog：与 Plugin 引用相同，以 sessionId 区分 workspace 当前目录和
// resident Session runtime 快照；不参与 Settings 管理目录。
export interface NexAgentSkillReferenceCatalogParams extends NexAgentWorkspaceTarget {
  sessionId?: string;
}

/** 插件 UI：UI 发起的 `ui://` 资源读取。 */
export interface NexAgentReadMcpResourceParams extends NexAgentWorkspaceTarget {
  instance: import("@nex/shared/mcp-apps").McpAppInstance;
  sessionId: string;
  pluginId: string;
  serverName: string;
  uri: string;
}

/** 插件 UI 页面发起的 `resources/read`；参数与 readMcpResource 同形，结果受 8 MiB 与 mimeType 白名单约束。 */
export type NexAgentReadMcpResourceForUiParams = NexAgentReadMcpResourceParams;

/** 插件 UI：UI 回调本插件工具；不进审批流，归属由 agent 侧 catalog fail closed。 */
export interface NexAgentCallMcpToolForUiParams extends NexAgentWorkspaceTarget {
  instance: import("@nex/shared/mcp-apps").McpAppInstance;
  sessionId: string;
  pluginId: string;
  serverName: string;
  toolName: string;
  arguments?: Record<string, unknown>;
  /** 宿主生成的调用 id，配合 cancelMcpToolCallForUi。 */
  callId: string;
}
/** 插件 UI 页面发起的 resources/list、resources/templates/list、subscribe、unsubscribe。 */
export interface NexAgentListMcpResourcesForUiParams extends NexAgentWorkspaceTarget {
  instance: import("@nex/shared/mcp-apps").McpAppInstance;
  sessionId: string;
  pluginId: string;
  serverName: string;
  cursor?: string;
}
/** App-Provided Tools：实例身份 = 会话 + 沙箱作用域 + 代际。 */
export interface NexAgentAppToolInstanceForUiParams extends NexAgentWorkspaceTarget {
  instance: import("@nex/shared/mcp-apps").McpAppInstance;
  sessionId: string;
  pluginId: string;
  serverName: string;
  scopeId: string;
  generation: number;
}
export interface NexAgentRegisterAppToolsForUiParams extends NexAgentAppToolInstanceForUiParams {
  tools: McpAppsAppToolDescriptor[];
}
export interface NexAgentAppToolCallForUiParams extends NexAgentAppToolInstanceForUiParams {
  callId: string;
}
export interface NexAgentResolveAppToolCallForUiParams extends NexAgentAppToolCallForUiParams {
  result?: McpAppsAppToolCallResult;
  error?: { message: string };
}
export interface NexAgentMcpResourceSubscriptionForUiParams extends NexAgentWorkspaceTarget {
  instance: import("@nex/shared/mcp-apps").McpAppInstance;
  sessionId: string;
  pluginId: string;
  serverName: string;
  scopeId: string;
  generation: number;
  uri: string;
}
export interface NexAgentCancelMcpToolCallForUiParams extends NexAgentWorkspaceTarget {
  instance: import("@nex/shared/mcp-apps").McpAppInstance;
  sessionId: string;
  pluginId: string;
  serverName: string;
  callId: string;
}
export interface NexAgentResolveSuggestedPluginReferenceParams extends NexAgentWorkspaceTarget {
  stableId: string;
  operationId: string;
  clientMode: "desktop-continuous" | "web-remote-replayable";
  deliveryKind: "desktop-continuous" | "web-remote-replayable";
}

// ---- 定时任务(automation)管理参数 ----

export interface NexAgentCreateAutomationParams extends NexAgentWorkspaceTarget {
  projectId?: string | null;
  title: string;
  cronExpr: string;
  relativeDelayMinutes?: number;
  prompt: string;
  modelSelection?: ModelSelection;
  mode?: string;
  recurring?: boolean;
  maxRuns?: number;
  endAt?: number;
  scheduleRule?: NexAutomationScheduleRule;
}

export interface NexAgentUpdateAutomationParams extends NexAgentWorkspaceTarget {
  automationId: string;
  title?: string;
  cronExpr?: string;
  prompt?: string;
  modelSelection?: ModelSelection | null;
  mode?: string | null;
  recurring?: boolean;
  maxRuns?: number | null;
  endAt?: number | null;
  scheduleRule?: NexAutomationScheduleRule | null;
  scheduleEditedByUser?: boolean;
}

export interface NexAgentAutomationIdParams extends NexAgentWorkspaceTarget {
  automationId: string;
}

export interface NexAgentSetAutomationEnabledParams extends NexAgentWorkspaceTarget {
  automationId: string;
  enabled: boolean;
}

export interface NexAgentDeleteAutomationRunParams extends NexAgentWorkspaceTarget {
  runId: string;
}
