import type {
  V4SessionDeletionParams,
  V4SessionDeletionExecuteParams,
  V4SessionDeletionPreview,
  V4SessionDeletionResult,
} from "@nex/shared/nex-protocol-v4";
import type { PluginUiToolResultNotification } from "@nex/shared/mcp-apps";
import type {
  ContextComposition,
  ContextCompositionChangedEvent,
  ModelRequestMetricsFact,
} from "@nex/shared/nex-protocol-v4";
import type { NexAgentContextCompositionParams } from "#src/nex-agent/contextCompositionClient.js";
import type {
  NexAgentPluginMentionItemsParams,
  NexAgentPluginMentionItemsResult,
} from "#src/nex-agent/pluginHostQueriesClient.js";
import type {
  BackgroundBashOutputResult,
  SessionDebugSnapshot,
  NexSessionContentProfile,
} from "@nex/shared";
/* eslint-disable max-lines -- Nex agent service 接口集中声明 protocol/session/workspace 方法，拆分会增加 service descriptor 迁移成本。 */
import type { RemoteConversationTelemetryEvent } from "@nex/shared/conversation-telemetry";
import type { Event, IDisposable } from "@nex/rpc";
import { ServiceChannels } from "@nex/shared";
import type { AppUsageRange, AppUsageSnapshot, NexTaskTokenUsageResult } from "@nex/shared";
import type { DynamicWorkflowMode } from "@nex/shared";
import type { NexAutomation, NexAutomationRun } from "@nex/shared";
import type {
  NexStorageStartupState,
  NexDeliveryKind,
  NexAgentMcpServer,
  NexBackgroundTurnAttribution,
  TraceId,
  NexSessionCompactResult,
  NexSessionGoalAction,
  NexSessionGoalResult,
  NexMessageWithParts,
  ModelSelection,
  NexSessionImportHistory,
  NexPermissionRequestParams,
  AgentLaneResourceSample,
  NexMcpTelemetryEvent,
  NexMcpResourceSample,
  NexToolExecResource,
  NexProcessChildProcess,
  NexMcpListResult,
  WorkspaceMcpTrustDecision,
  NexWorkspaceMcpTrustResult,
  NexPluginsListResult,
  NexPluginsOverviewResult,
  NexPluginsMarketplaceMutationResult,
  NexPluginsInstallResult,
  NexPluginsReferenceCatalogResult,
  NexSkillsReferenceCatalogResult,
  NexMcpReadResourceResult,
  NexMcpUiCallToolResult,
  NexMcpUiCancelCallResult,
  NexMcpUiReadResourceResult,
  NexMcpUiListResourcesResult,
  NexMcpUiAppToolAcceptedResult,
  NexMcpUiRegisterAppToolsResult,
  NexMcpUiUnregisterAppToolsResult,
  NexMcpUiListResourceTemplatesResult,
  NexPluginsListUiSurfacesResult,
  NexWorkflowsDeleteResult,
  NexWorkflowsForRunResult,
  NexWorkflowsGetResult,
  NexWorkflowsListResult,
  NexWorkflowsMoveResult,
  NexWorkflowsRunsResult,
  NexWorkflowsSaveResult,
  NexWorkflowsUpdateMetaResult,
  NexPluginsUninstallResult,
  NexPluginsRestoreBuiltinResult,
  NexPluginsConfigureResult,
  NexPluginsDescribeResult,
  NexPluginsValidateResult,
  NexPluginsSetEnabledResult,
  NexPluginsCancelOperationResult,
  NexPluginOperationProgressNotification,
  NexProviderRuntimeHeadersRequestParams,
  RequestVerificationConfig,
  NexProviderRuntimeHeadersCancelled,
  NexProviderTestModelConnectivityParams,
  NexProviderTestModelConnectivityResult,
  NexUserInputRequestParams,
  NexUserInputResponse,
  NexSessionEvent,
  NexSessionInfo,
  NexSessionMode,
  NexSessionPersistence,
  NexSessionSendResult,
  NexSessionRequestRuntimePreferencesParams,
  NexSessionRuntimePreferencesResult,
  NexSessionStateSnapshot,
  NexSessionSubagentsResult,
  NexStateUpdatedNotification,
  NexTaskClientMode,
  NexBrowserAmbientContext,
  NexWorkspacePresentation,
  NexWorkspaceGenerateTextResult,
  NexWorkspaceGenerateTextParams,
  NexWorkspaceHookTrustGrantResult,
  NexAutomationBotDeliveryTarget,
} from "@nex/shared";
import type {
  ClientHello,
  CommandAck,
  CommandEnvelope,
  CommandKey,
  CommandsQueryResult,
  ConversationTopicWireCandidate,
  ConversationTelemetryFact,
  CuaPermissionObservation,
  ConversationRowTarget,
  HelloMessage,
  SessionsIndexTopicWireCandidate,
  SessionsIndexReconcileResult,
  V4AttachmentBeginResult,
  V4AttachmentChunkResult,
  V4AttachmentCommitResult,
  V4AttachmentPreviewSourceResult,
  V4AttachmentReadResult,
  V4ConversationAttachmentReadResult,
  V4ConversationAttachmentStatResult,
  V4ConnectionFlowState,
  V4ConversationFileChangesResult,
  V4ConversationFileRewindPreviewResult,
  V4ConversationInputPredictionResult,
  V4ConversationPlansResult,
  V4ConversationWorkflowRunEventsResult,
  V4ConversationWorkflowRunArtifactDataResult,
  V4ConversationWorkflowRunArtifactReadResult,
  V4ConversationWorkflowRunArtifactsResult,
  V4ConversationWorkflowRunNodeResultResult,
  V4ConversationWorkflowRunWorkspaceResult,
  V4ConversationWorkflowRunsResult,
  V4ConversationRowsRangeResult,
  V4ConversationResyncResult,
  V4ConversationSubscribeResult,
  V4SessionsIndexSubscribeResult,
  V4WorkspaceConfigSubscribeResult,
  WorkspaceConfigTopicWireCandidate,
} from "@nex/shared/nex-protocol-v4";
import type {
  V4SessionDataMeasureResult,
  V4SessionDataParams,
  V4SessionDataPurgeResult,
} from "@nex/shared/nex-protocol-v4";
import { createServiceDescriptor } from "../descriptors.js";

export * from "./nexAgentPluginParams.js";
export * from "./nexAgentWorkflowParams.js";
export type { NexAgentContextCompositionParams } from "#src/nex-agent/contextCompositionClient.js";
export type {
  NexAgentPluginMentionItemsParams,
  NexAgentPluginMentionItemsResult,
} from "#src/nex-agent/pluginHostQueriesClient.js";
import type {
  NexAgentAddPluginMarketplaceParams,
  NexAgentAutomationIdParams,
  NexAgentCancelPluginOperationParams,
  NexAgentConfigurePluginParams,
  NexAgentResetPluginConfigParams,
  NexAgentCreateAutomationParams,
  NexAgentDeleteAutomationRunParams,
  NexAgentDescribePluginParams,
  NexAgentInstallPluginParams,
  NexAgentListMcpServerStatusesParams,
  NexAgentPluginViewParams,
  NexAgentPluginReferenceCatalogParams,
  NexAgentSkillReferenceCatalogParams,
  NexAgentReadMcpResourceParams,
  NexAgentReadMcpResourceForUiParams,
  NexAgentListMcpResourcesForUiParams,
  NexAgentAppToolCallForUiParams,
  NexAgentAppToolInstanceForUiParams,
  NexAgentRegisterAppToolsForUiParams,
  NexAgentResolveAppToolCallForUiParams,
  NexAgentMcpResourceSubscriptionForUiParams,
  NexAgentCallMcpToolForUiParams,
  NexAgentCancelMcpToolCallForUiParams,
  NexAgentResolveSuggestedPluginReferenceParams,
  NexAgentRemovePluginMarketplaceParams,
  NexAgentRestoreBuiltinPluginParams,
  NexAgentSetPluginEnabledParams,
  NexAgentSetAutomationEnabledParams,
  NexAgentUninstallPluginParams,
  NexAgentUpdatePluginMarketplaceParams,
  NexAgentUpdatePluginParams,
  NexAgentUpdateAutomationParams,
  NexAgentValidatePluginParams,
  NexAgentWorkspaceTarget,
} from "./nexAgentPluginParams.js";
import type {
  NexAgentDeleteSavedWorkflowParams,
  NexAgentFindSavedWorkflowForRunParams,
  NexAgentGetSavedWorkflowParams,
  NexAgentListSavedWorkflowRunsParams,
  NexAgentListSavedWorkflowsParams,
  NexAgentMoveSavedWorkflowParams,
  NexAgentSaveSavedWorkflowFromRunParams,
  NexAgentUpdateSavedWorkflowMetaParams,
} from "./nexAgentWorkflowParams.js";

export interface NexAgentSessionTarget extends NexAgentWorkspaceTarget {
  sessionId: string;
}

export interface NexAgentResumeSessionParams extends NexAgentSessionTarget {
  additionalDirectories?: string[];
  model?: ModelSelection;
  thoughtLevel?: string;
  mcpServers?: NexAgentMcpServer[];
  // 冷恢复会重建 runtime，工具面隔离必须和 create 保持同一安全边界（CUA 只放行 nex-cua 工具、
  // 禁 Bash 等）。否则 resume 后模型可见工具面/执行权限会比创建时更宽。
  toolAllowlist?: string[];
  toolDenylist?: string[];
}

export interface NexAgentInitializeResult {
  available: boolean;
  workspaceKey: string;
  protocolName?: string;
  protocolVersion?: number;
  transportKind?: "stdio" | "websocket";
  reason?: string;
  reasonCode?: "provider_not_ready";
}

export interface NexAgentRunAutomationNowResult {
  status: "queued" | "duplicate";
}

export interface NexAgentWorkspaceRuntimeIdentity {
  generation: number;
  identity: string;
  processId?: number;
  workspaceKey: string;
}

export const NEX_AGENT_RUNTIME_UNAVAILABLE_CODE = "NEX_AGENT_RUNTIME_UNAVAILABLE";

export type NexAgentRuntimePolicy = "start-if-needed" | "existing-only";

export interface NexAgentRuntimeLifecycleEvent extends NexAgentWorkspaceTarget {
  workspaceKey: string;
  runtimeIdentity: NexAgentWorkspaceRuntimeIdentity;
  state: "available" | "unavailable";
}

/**
 * runtime 进程意外退出（晚于同一进程的 unavailable，退出码已知）。只用于向用户说明原因；
 * 回合、子代理的收口由 CLI 投影在重连后给出，界面不据此改写会话状态。
 */
export interface NexAgentRuntimeExitedEvent extends NexAgentWorkspaceTarget {
  workspaceKey: string;
  runtimeIdentity: NexAgentWorkspaceRuntimeIdentity;
  exitCode: number | null;
  signal: string | null;
  /** `native_out_of_memory`：Windows 0xE0000008，系统可提交内存耗尽。 */
  cause?: "native_out_of_memory";
  exitedAt: number;
}

export type NexAgentCuaPermissionObservation = CuaPermissionObservation &
  NexAgentWorkspaceTarget;

export interface NexAgentCreateSessionParams extends NexAgentWorkspaceTarget {
  projectId?: string | null;
  additionalDirectories?: string[];
  sessionId?: string;
  sessionTraceId?: TraceId;
  parentSessionId?: string;
  mode?: NexSessionMode;
  model?: ModelSelection;
  persistence?: NexSessionPersistence;
  thoughtLevel?: string;
  /** automation 执行会话关闭模型二次命名，保持首条用户 query 作为稳定标题。 */
  titleGenerationEnabled?: boolean;
  mcpServers?: NexAgentMcpServer[];
  toolAllowlist?: string[];
  toolDenylist?: string[];
  importedHistory?: NexSessionImportHistory;
}

export interface NexAgentListSessionsParams extends NexAgentWorkspaceTarget {
  sessionIds?: string[];
  runtimePolicy?: NexAgentRuntimePolicy;
  limit?: number;
}

export interface NexAgentListSessionSubagentsParams extends NexAgentSessionTarget {
  endedCursor?: string;
  endedLimit?: number;
  /** 远程 workspace 的宿主连接身份；只用于选择现有 Host，不进入 CLI wire query。 */
  remoteSessionId?: string;
}

export interface NexAgentAppUsageParams {
  range: AppUsageRange;
  timeZone?: string;
}

export interface NexAgentTaskTokenUsageParams extends NexAgentSessionTarget {}

export interface NexAgentReadSessionParams extends NexAgentSessionTarget {
  deliveryKind?: NexDeliveryKind;
  messageLimit?: number;
  afterSeq?: number;
  /** 被动索引/观察者只能读取现有 runtime，禁止为了读快照拉起 session。 */
  runtimePolicy?: NexAgentRuntimePolicy;
  /** "index" 仅供 task index：返回剥离大载荷的同结构 snapshot，禁止回写 UI 状态。 */
  contentProfile?: NexSessionContentProfile;
}

export interface NexAgentReadSessionMessagesParams extends NexAgentSessionTarget {
  afterMessageId?: string;
  limit?: number;
  direction?: "forward" | "backward";
}

export interface NexAgentReadSessionEventsParams extends NexAgentSessionTarget {
  afterSeq?: number;
  limit?: number;
  /** App 后台状态读取只观察现有 CLI，不因 runtime 消失重启。 */
  runtimePolicy?: NexAgentRuntimePolicy;
}

export type NexAgentReadWorkspacePresentationParams = NexAgentWorkspaceTarget;

export interface NexAgentGrantWorkspaceHookTrustParams extends NexAgentWorkspaceTarget {
  bundleDigest: string;
  hookDeclarationDigest: string;
}

export interface NexAgentSendPromptParamsBase extends NexAgentSessionTarget {
  modelSelection?: ModelSelection;
  modelSelectionBasis?: ModelSelection;
  modelExecution?: import("@nex/shared/nex-protocol-v4").CommandPayloadMap["sendText"]["modelExecution"];
  inputId?: string;
  queryId?: string;
  messageId?: string;
  sessionTraceId?: TraceId;
  content: string;
  attachments?: Record<string, unknown>[];
  /** provider-only 的当前 IAB 状态；UI/session persistence 仍使用 content 原文。 */
  browserAmbientContext?: NexBrowserAmbientContext;
  clientMode?: NexTaskClientMode;
  expectedRevision?: number;
  expectedProviderRevision?: string;
  runtimeProviderHeaders?: Record<string, string>;
  toolDenylist?: string[];
  /** Bot 来源 turn 的稳定回推地址；只在当前 turn 内供 CronCreate 读取。 */
  botDeliveryTarget?: NexAutomationBotDeliveryTarget;
}

export type NexAgentSendPromptParams = NexAgentSendPromptParamsBase &
  NexBackgroundTurnAttribution;

export interface NexAgentCompactParams extends NexAgentSessionTarget {
  inputId?: string;
  instructions?: string;
  expectedRevision?: number;
}

export interface NexAgentGoalParams extends NexAgentSessionTarget {
  inputId?: string;
  action: NexSessionGoalAction;
  objective?: string;
  expectedRevision?: number;
}

export interface NexAgentSetModelParams extends NexAgentSessionTarget {
  model: ModelSelection;
  expectedRevision?: number;
  persistAsWorkspaceLastUsed?: boolean;
}

export interface NexAgentSetThoughtLevelParams extends NexAgentSessionTarget {
  thoughtLevel?: string;
  expectedRevision?: number;
  persistAsWorkspaceLastUsed?: boolean;
}

export interface NexAgentSetModeParams extends NexAgentSessionTarget {
  mode: NexSessionMode;
  expectedRevision?: number;
}

export interface NexAgentGenerateWorkspaceTextParams extends NexAgentWorkspaceTarget {
  selection: NexWorkspaceGenerateTextParams["selection"];
  prompt?: string;
  messages?: NexWorkspaceGenerateTextParams["messages"];
  tools?: NexWorkspaceGenerateTextParams["tools"];
  querySource: string;
  maxOutputTokens?: number;
  signal?: AbortSignal;
  /**
   * 协议层 RPC 超时。thinking 模型的长请求会超过协议 client 默认的
   * 3 分钟；调用方必须把自身 deadline 透传到这里，否则默认超时先触发、
   * 还会被 onRequestTimeout 误判 stale 杀进程。
   */
  requestTimeoutMs?: number;
}

export interface NexAgentTestModelConnectivityParams extends NexAgentWorkspaceTarget {
  selection: NexProviderTestModelConnectivityParams["selection"];
  signal?: AbortSignal;
}

// 合并时曾混用 Agent 完整鉴权响应；UI 只能回传请求校验，账号鉴权必须由 Host 合成。
export type ProviderRuntimeHeadersUiResponse =
  | { headersApplied: true; runtimeProviderHeaders: Record<string, string>; errorMessage?: string }
  | { headersApplied: false; errorMessage?: string };

export interface NexAgentRespondProviderRuntimeHeadersParams extends NexAgentSessionTarget {
  requestId: string;
  response: ProviderRuntimeHeadersUiResponse;
}

export interface NexAgentSessionRuntimePreferencesRequest extends NexSessionRequestRuntimePreferencesParams {
  requestId: string;
}

export interface NexAgentRespondSessionRuntimePreferencesParams {
  requestId: string;
  resolution:
    | { status: "resolved"; preferences: NexSessionRuntimePreferencesResult }
    | { status: "failed"; message: string };
}

export interface NexAgentSessionSubscribeParams extends NexAgentSessionTarget {
  deliveryKind: NexDeliveryKind;
  afterSeq?: number;
  includeSnapshot?: boolean;
  eventCoalescing?: {
    mode: "background-summary";
    intervalMs?: number;
  };
}

// ── v4 conversation 通道（竖切）──
// host 只做转发：subscribe/unsubscribe/command 透传给 CLI v4 gateway，
// v4/conversation/frame 通知按 workspace fan-out 给 renderer。

export interface NexAgentConversationSubscribeParams extends NexAgentSessionTarget {
  retryInitialization?: string;
  /** 水位不变量：仅当客户端真持有该时刻一致状态才允许带。 */
  base?: { logEpoch: string; seq: number };
  visibility?: "foreground" | "background";
}

export interface NexAgentConversationUnsubscribeParams extends NexAgentWorkspaceTarget {
  subscriptionId: string;
  runtimePolicy?: NexAgentRuntimePolicy;
}

export interface NexAgentConversationResyncParams extends NexAgentWorkspaceTarget {
  subscriptionId: string;
  base: { logEpoch: string; seq: number } | null;
  forceSnapshot?: boolean;
  runtimePolicy?: NexAgentRuntimePolicy;
}

/** 行分页 query（rows/range）：按游标向上取一窗历史行。 */
export interface NexAgentConversationRowsRangeParams extends NexAgentSessionTarget {
  /** 取 rowId < beforeRowId 的行；缺省 = 从当前尾部向前。 */
  beforeRowId?: number;
  /** 1..rowsRangeMaxLimit（200）。 */
  limit: number;
  /**
   * 手机预加载：只读热会话。工作区 CLI 未运行、执行端不支持、会话不在内存都报
   * proto.sessionNotResident，不拉起进程、不冷恢复。
   */
  residency?: "resident-only";
}

/** 当前有效分支里的终态 ExitPlanMode 目录。 */
export type NexAgentConversationPlansParams = NexAgentSessionTarget;

/** 手机远控输入预测（ghost hint）；线格式见 docs/web-remote-control/mobile-input-prediction.md。 */
export interface NexAgentConversationInputPredictionParams extends NexAgentSessionTarget {
  /** 手机当前看到的会话 revision；参与 CLI 侧缓存键，回包据此丢弃过期结果。 */
  revision: number;
  /** 当前草稿；缺省/空 = 预测下一条输入，非空 = 续写草稿。 */
  draft?: string;
  /** 手机侧请求 id，原样回声，用于丢弃过期回包。 */
  requestId?: string;
}

/** workflow run 的事件日志分页（详情页审计面）；cursor = journal sequence。 */
export interface NexAgentConversationWorkflowRunEventsParams extends NexAgentSessionTarget {
  runId: string;
  afterSequence?: number;
  limit?: number;
}

/** dwf run 的枚举（重启后的发现查询）。 */
export interface NexAgentConversationWorkflowRunsParams extends NexAgentSessionTarget {
  limit?: number;
}

// ── dwf 用户面产物──
// ⚠ 术语：artifact = 脚本经 `artifact.*` 发布给**用户**看的产出（文件 / markdown / 预置看板），
// 不是 run 的顶层返回值（引擎内部对后者的同名叫法）。

/** 产物清单；UI 冷恢复与中枢详情的 durable 读法。 */
export interface NexAgentConversationWorkflowRunArtifactsParams extends NexAgentSessionTarget {
  runId: string;
}

/** 预置看板的取数面；cursor = journal sequence（严格大于）。 */
export interface NexAgentConversationWorkflowRunArtifactDataParams extends NexAgentSessionTarget {
  runId: string;
  artifactId: string;
  afterSequence?: number;
  limit?: number;
}

/** 内容产物的字节，一次一块（≤ 512 KiB，形状逐字照 attachmentRead）。 */
export interface NexAgentConversationWorkflowRunArtifactReadParams extends NexAgentSessionTarget {
  runId: string;
  artifactId: string;
  version: number;
  offset: number;
  limit: number;
}

// ── dwf 工作区 transcript──
/** 轻行清单：一个 run 的 files.* / git.* / world.run 行，不带正文。 */
export interface NexAgentConversationWorkflowRunWorkspaceParams extends NexAgentSessionTarget {
  runId: string;
}

/** 一个工作区节点的正文，按 maxBytes 保形有界化（缺省与上限在 CLI 网关侧）。 */
export interface NexAgentConversationWorkflowRunNodeResultParams extends NexAgentSessionTarget {
  runId: string;
  siteId: string;
  ordinal: number;
  maxBytes?: number;
}

export interface NexAgentBackgroundBashOutputParams extends NexAgentSessionTarget {
  workId: string;
}

export interface NexAgentConversationFileChangesParams extends NexAgentSessionTarget {
  target: ConversationRowTarget;
  baseRevision: number;
  baseLogEpoch: string;
}

export interface NexAgentConversationFileRewindPreviewParams extends NexAgentSessionTarget {
  target: ConversationRowTarget;
  baseRevision: number;
  baseLogEpoch: string;
}

/**
 * Host 进程内的受管 worktree 服务发送命令时设置的标记。只有带此标记的命令可以携带
 * createSession.cwd / worktreeCreation 与 forkAssistant.targetCwd；Symbol 键无法经 RPC 序列化伪造。
 */
export const HOST_WORKTREE_COMMAND: unique symbol = Symbol("nex.hostWorktreeCommand");

export interface NexAgentConversationCommandParams extends NexAgentWorkspaceTarget {
  /** 仅 Host 消费；显式 NULL 表示全局任务，不向 CLI 发送项目概念。 */
  projectId?: string | null;
  envelope: CommandEnvelope;
  /** 只有 Host 进程内的受管 worktree 服务设置（见 HOST_WORKTREE_COMMAND）。 */
  [HOST_WORKTREE_COMMAND]?: true;
  /** 仅 host 内部用于 Browser Use runtime 边界，不进入 v4 wire envelope。 */
  clientMode?: NexTaskClientMode;
}

export interface NexAgentCommandsQueryParams extends NexAgentWorkspaceTarget {
  clock?: true;
  commands: CommandKey[];
}

/** UI 不携带 connectionId；connection scope 以 trusted carrier 注入 wire identity。 */
export interface NexAgentAttachmentBeginParams extends NexAgentSessionTarget {
  uploadId: string;
  fileName: string;
  mime: string;
  totalBytes: number;
  totalChunks: number;
  checksum: string;
}

export interface NexAgentAttachmentChunkParams extends NexAgentSessionTarget {
  uploadId: string;
  chunkIndex: number;
  dataBase64: string;
}

export interface NexAgentAttachmentTerminalParams extends NexAgentSessionTarget {
  uploadId: string;
}

export interface NexAgentAttachmentReadParams extends NexAgentSessionTarget {
  ref: string;
  target?: ConversationRowTarget;
  attachmentIndex?: number;
  offset: number;
  limit: number;
}

export interface NexAgentConversationAttachmentReadParams extends NexAgentSessionTarget {
  ref: string;
  target: ConversationRowTarget;
  attachmentIndex: number;
  offset: number;
  limit: number;
}

export interface NexAgentConversationAttachmentStatParams extends NexAgentSessionTarget {
  ref: string;
  target: ConversationRowTarget;
  attachmentIndex: number;
}

export interface NexAgentAttachmentPreviewSourceParams extends NexAgentSessionTarget {
  ref: string;
  target?: ConversationRowTarget;
  attachmentIndex?: number;
}

/** host scope 内部 transport 控制面；connectionId 只能经 trusted carrier 注入。 */
export interface NexAgentConnectionFlowParams extends NexAgentWorkspaceTarget {
  state: V4ConnectionFlowState;
}

/** sessions-index：workspace 级列表订阅（无 sessionId 维度）。 */
export interface NexAgentSessionsIndexSubscribeParams extends NexAgentWorkspaceTarget {
  base?: { logEpoch: string; seq: number };
  visibility?: "foreground" | "background";
  /**
   * 订阅者作用域后缀：CLI 侧重订阅替换按 (connectionId, topic) 判定，
   * host 进程内多个独立消费者（renderer 侧栏 / task-index syncer）订阅同一 topic 时
   * 必须用不同 connectionId，否则互相替换对方的订阅代际。缺省共享 host 连接 id。
   */
  subscriberScope?: string;
  /**
   * task-list 等被动观察者必须使用 existing-only；runtime 不存在时返回稳定 unavailable，
   * 禁止为了建立列表订阅而启动 Agent。缺省保持显式会话入口的旧行为。
   */
  runtimePolicy?: NexAgentRuntimePolicy;
}

/** workspace-config：workspace 级配置目录订阅（config options + slash 目录）。 */
export interface NexAgentWorkspaceConfigSubscribeParams extends NexAgentWorkspaceTarget {
  base?: { logEpoch: string; seq: number };
  visibility?: "foreground" | "background";
  subscriberScope?: string;
  runtimePolicy?: NexAgentRuntimePolicy;
}

/** Host 为 App 应答方绑定的本次配置；不扩展 Agent wire 协议。 */
export type NexAgentProviderRuntimeHeadersRequest = NexProviderRuntimeHeadersRequestParams & {
  requestVerificationConfig?: RequestVerificationConfig;
};

export type NexAgentServiceEvent =
  | { type: "session.event"; event: NexSessionEvent }
  | { type: "state.updated"; notification: NexStateUpdatedNotification }
  | { type: "permission.request"; request: NexPermissionRequestParams }
  | { type: "userInput.request"; request: NexUserInputRequestParams }
  | {
      type: "userInput.response";
      requestId: string;
      response: NexUserInputResponse;
    }
  | { type: "providerRuntimeHeaders.request"; request: NexAgentProviderRuntimeHeadersRequest }
  | { type: "snapshot"; snapshot: NexSessionStateSnapshot };

export interface NexAgentAppRuntimePreferences {
  askUserQuestionAutoResolutionEnabled: boolean;
  modelIoFullRetentionEnabled?: boolean;
  pluginAutoRecommendationEnabled?: boolean;
}

export interface NexAgentLocalRuntimeChildProcesses {
  pid: number;
  provider: string;
  workspacePath: string;
  lane?: string;
  children: NexProcessChildProcess[];
}

export interface NexAgentStorageStartupSnapshot {
  generation: number;
  state: NexStorageStartupState | null;
}

export interface INexAgentService {
  /** 控制面不需要账号或模型，且不发送普通协议请求。 */
  prepareStorage(params: NexAgentWorkspaceTarget): Promise<void>;
  getStorageStartupState(
    params: NexAgentWorkspaceTarget,
  ): Promise<NexAgentStorageStartupSnapshot | null>;
  onDynamicStorageStartupState(
    params: NexAgentWorkspaceTarget,
  ): Event<NexAgentStorageStartupSnapshot>;
  initialize(params: NexAgentWorkspaceTarget): Promise<NexAgentInitializeResult>;
  /**
   * 同步 App 全局运行时偏好到所有已活动 workspace；不得为此启动空闲 Agent。
   */
  syncAppRuntimePreferences(preferences: NexAgentAppRuntimePreferences): Promise<void>;
  /**
   * 动态工作流用户选择变化的信号（docs/dynamic-workflow/launch.md「The user's choice」）：Host 重新计算
   * 生效模式并串行重发 workspace/updateDynamicWorkflowPolicy。有本机设置权威的 Host 以设置文件为准；
   * desktop-attached remote Host 以传入值为准（缺席 = 跟随服务端）。不得为此启动空闲 Agent。
   * Host 记下选择即返回，不等 CLI 应答；返回后重读的可用性快照已是新模式。
   */
  syncDynamicWorkflowUserMode(params: { mode?: DynamicWorkflowMode }): Promise<void>;
  getWorkspaceRuntimeIdentity(
    params: NexAgentWorkspaceTarget,
  ): Promise<NexAgentWorkspaceRuntimeIdentity>;
  createSession(params: NexAgentCreateSessionParams): Promise<NexSessionStateSnapshot>;
  resumeSession(params: NexAgentResumeSessionParams): Promise<NexSessionStateSnapshot>;
  listSessions(params: NexAgentListSessionsParams): Promise<NexSessionInfo[]>;
  listSessionSubagents(
    params: NexAgentListSessionSubagentsParams,
  ): Promise<NexSessionSubagentsResult>;
  getAppUsageStats(params: NexAgentAppUsageParams): Promise<AppUsageSnapshot>;
  previewSessionDeletion?(params: V4SessionDeletionParams): Promise<V4SessionDeletionPreview>;
  executeSessionDeletion?(params: V4SessionDeletionExecuteParams): Promise<V4SessionDeletionResult>;
  /** 项目数据删除前的测量：只读，用本机已有 runtime 或管理面进程，不为某个项目拉起 Agent。 */
  measureSessionData?(params: V4SessionDataParams): Promise<V4SessionDataMeasureResult>;
  /**
   * 彻底删除会话与目录记忆。workspace 已有 Agent 进程时交给它（它持有会话运行态，能判断运行中
   * 并释放已加载会话）；否则交给管理面进程。只用于本机。
   */
  purgeSessionData?(
    params: V4SessionDataParams & { workspace: NexAgentWorkspaceTarget },
  ): Promise<V4SessionDataPurgeResult>;
  /** 该 workspace 的 CLI 是否在 runtime/capabilities 中声明 worktreeCwd（只供 Host 进程内调用）。 */
  supportsWorktreeCwd?(target: NexAgentWorkspaceTarget): Promise<boolean>;
  /**
   * 该 workspace 的 CLI 是否在 runtime/capabilities 中声明 extensionInputSource（只供 Host 进程内调用）。
   * 代扩展发消息前必须确认；false 时调用方报 unavailable，不去掉来源后发送
   * （apps/nex-cli/docs/design/v2/conversation-input-source.md §7）。
   */
  supportsExtensionInputSource?(target: NexAgentWorkspaceTarget): Promise<boolean>;
  /**
   * 该 workspace 的 CLI 是否在 runtime/capabilities 中声明 conversationQuoteComment。
   * renderer 据此决定是否提供选区“批注”入口；未实现（旧 Host / 远端）按不支持处理。
   */
  supportsConversationQuoteComment?(target: NexAgentWorkspaceTarget): Promise<boolean>;
  getTaskTokenUsage(params: NexAgentTaskTokenUsageParams): Promise<NexTaskTokenUsageResult>;
  readSession(params: NexAgentReadSessionParams): Promise<NexSessionStateSnapshot>;
  readSessionMessages(
    params: NexAgentReadSessionMessagesParams,
  ): Promise<NexMessageWithParts[]>;
  readSessionDebug(params: NexAgentSessionTarget): Promise<SessionDebugSnapshot>;
  readSessionEvents(params: NexAgentReadSessionEventsParams): Promise<NexSessionEvent[]>;
  readWorkspacePresentation(
    params: NexAgentReadWorkspacePresentationParams,
  ): Promise<NexWorkspacePresentation>;
  /** 无 task/session 的 Settings 预信任；Agent 会重新发现并校验 canonical snapshot。 */
  grantWorkspaceHookTrust(
    params: NexAgentGrantWorkspaceHookTrustParams,
  ): Promise<NexWorkspaceHookTrustGrantResult>;
  setWorkspaceMcpTrust(params: WorkspaceMcpTrustDecision): Promise<NexWorkspaceMcpTrustResult>;
  listMcpServerStatuses(params: NexAgentListMcpServerStatusesParams): Promise<NexMcpListResult>;
  listPlugins(params: NexAgentPluginViewParams): Promise<NexPluginsListResult>;
  /**
   * Plugin 对话引用 catalog：session-scoped 只读投影。
   * 走 workspace 级 agent client（session 记录只存在于该进程），不走独立插件管理进程。
   */
  getPluginReferenceCatalog(
    params: NexAgentPluginReferenceCatalogParams,
  ): Promise<NexPluginsReferenceCatalogResult>;
  /** Composer Skill 引用 catalog；带 sessionId 时读取该 runtime 的冻结快照。 */
  getSkillReferenceCatalog(
    params: NexAgentSkillReferenceCatalogParams,
  ): Promise<NexSkillsReferenceCatalogResult>;
  /** 插件 UI：读取插件 MCP 的 `ui://` 资源；走 session 所在 workspace agent 进程。 */
  openMcpUiInstance(
    params: import("../plugin-ui-bridge/contract.js").PluginUiPrepareSandboxParams & {
      accountContext?: string;
    },
  ): Promise<import("@nex/shared/mcp-apps").McpAppInstance>;
  validateMcpUiInstance(
    params: import("../plugin-ui-bridge/contract.js").PluginUiPluginScope,
  ): Promise<void>;
  recycleMcpUiInstance(
    params: import("../plugin-ui-bridge/contract.js").PluginUiPluginScope,
  ): Promise<boolean>;
  closeMcpUiInstance(
    params: import("../plugin-ui-bridge/contract.js").PluginUiPluginScope,
  ): Promise<void>;
  readMcpResource(params: NexAgentReadMcpResourceParams): Promise<NexMcpReadResourceResult>;
  /** 插件 UI：UI 发起的工具调用；复用既有权限审批，不产生 transcript row。 */
  sampleMcpApp(
    params: import("../plugin-ui-bridge/samplingContract.js").PluginUiSamplingParams,
  ): Promise<import("@nex/shared/mcp-apps").McpAppsSamplingResult>;
  cancelMcpAppSampling(
    params: import("../plugin-ui-bridge/samplingContract.js").PluginUiCancelSamplingParams,
  ): Promise<{ cancelled: boolean }>;
  callMcpToolForUi(params: NexAgentCallMcpToolForUiParams): Promise<NexMcpUiCallToolResult>;
  /** 插件 UI 取消带 callId 的进行中 UI 工具调用；agent 侧 abort 到 MCP client。 */
  cancelMcpToolCallForUi(
    params: NexAgentCancelMcpToolCallForUiParams,
  ): Promise<NexMcpUiCancelCallResult>;
  /** 插件 UI 页面发起的 `resources/read`，agent 侧限同插件服务器、8 MiB、mimeType 白名单。 */
  readMcpResourceForUi(
    params: NexAgentReadMcpResourceForUiParams,
  ): Promise<NexMcpUiReadResourceResult>;
  /** 插件 UI 页面发起的 resources/list、resources/templates/list、subscribe、unsubscribe 代理。 */
  listMcpResourcesForUi(
    params: NexAgentListMcpResourcesForUiParams,
  ): Promise<NexMcpUiListResourcesResult>;
  listMcpResourceTemplatesForUi(
    params: NexAgentListMcpResourcesForUiParams,
  ): Promise<NexMcpUiListResourceTemplatesResult>;
  subscribeMcpResourceForUi(params: NexAgentMcpResourceSubscriptionForUiParams): Promise<void>;
  unsubscribeMcpResourceForUi(params: NexAgentMcpResourceSubscriptionForUiParams): Promise<void>;
  /** App-Provided Tools：页面工具登记 / 注销与模型调用的认领 / 回传（信箱投递走 v4 live 增量）。 */
  registerAppToolsForUi(
    params: NexAgentRegisterAppToolsForUiParams,
  ): Promise<NexMcpUiRegisterAppToolsResult>;
  unregisterAppToolsForUi(
    params: NexAgentAppToolInstanceForUiParams,
  ): Promise<NexMcpUiUnregisterAppToolsResult>;
  claimAppToolCallForUi(
    params: NexAgentAppToolCallForUiParams,
  ): Promise<NexMcpUiAppToolAcceptedResult>;
  resolveAppToolCallForUi(
    params: NexAgentResolveAppToolCallForUiParams,
  ): Promise<NexMcpUiAppToolAcceptedResult>;
  /** 插件 UI 工作区级面板入口（已启用插件清单 `ui.surfaces[]`）。 */
  listPluginUiSurfaces(
    params: NexAgentWorkspaceTarget,
  ): Promise<NexPluginsListUiSurfacesResult>;
  // 已保存工作流的 GUI 中枢：workspace 级、无会话，每次调用现扫 `<cwd>/.nex/workflows/`。
  // 全局档传 `scope: "global"`：带 workspace 就用它当载体，不带则由 services 层自选本机载体运行时。
  listSavedWorkflows(params: NexAgentListSavedWorkflowsParams): Promise<NexWorkflowsListResult>;
  getSavedWorkflow(params: NexAgentGetSavedWorkflowParams): Promise<NexWorkflowsGetResult>;
  updateSavedWorkflowMeta(
    params: NexAgentUpdateSavedWorkflowMetaParams,
  ): Promise<NexWorkflowsUpdateMetaResult>;
  deleteSavedWorkflow(
    params: NexAgentDeleteSavedWorkflowParams,
  ): Promise<NexWorkflowsDeleteResult>;
  listSavedWorkflowRuns(
    params: NexAgentListSavedWorkflowRunsParams,
  ): Promise<NexWorkflowsRunsResult>;
  // 在项目档 / 全局档之间移动同名文件：
  // `workspace` 是载体（移到项目传目标项目、移到全局传源项目），`to` 是落点档；不覆盖已存在的目标。
  moveSavedWorkflow(params: NexAgentMoveSavedWorkflowParams): Promise<NexWorkflowsMoveResult>;
  // 完成卡的「直接保存」（docs/dynamic-workflow/transcript-and-notifications.md）：按 runId 从
  // journal 取这次 run 实际跑过的脚本落盘，不经模型回合。两者都必带 workspace（journal 在那里）。
  saveSavedWorkflowFromRun(
    params: NexAgentSaveSavedWorkflowFromRunParams,
  ): Promise<NexWorkflowsSaveResult>;
  /** 这次 run 对应哪个已保存工作流（推导，不记忆）；顺带回 run 的实参供「再次运行」预填。 */
  findSavedWorkflowForRun(
    params: NexAgentFindSavedWorkflowForRunParams,
  ): Promise<NexWorkflowsForRunResult>;
  resolveSuggestedPluginReference(
    params: NexAgentResolveSuggestedPluginReferenceParams,
  ): Promise<import("@nex/shared").NexPluginsResolveSuggestedReferenceResult>;
  /** 推荐项 Plugin 首次本地检查缺失后的 operation-scoped 刷新进度。 */
  onDynamicPluginOperationProgress(
    operationId: string,
  ): Event<NexPluginOperationProgressNotification>;
  getPluginsOverview(params: NexAgentPluginViewParams): Promise<NexPluginsOverviewResult>;
  /**
   * 资源管理器：枚举本 Host 内全部本地 Agent 进程（含 plugin / mcp-status 泳道），
   * 并向每个存活 runtime 请求 `process/childProcesses`；单个 runtime 失败只让它的 children 为空。
   */
  collectLocalRuntimeChildProcesses(
    signal?: AbortSignal,
  ): Promise<NexAgentLocalRuntimeChildProcesses[]>;
  addPluginMarketplace(
    params: NexAgentAddPluginMarketplaceParams,
  ): Promise<NexPluginsMarketplaceMutationResult>;
  removePluginMarketplace(
    params: NexAgentRemovePluginMarketplaceParams,
  ): Promise<NexPluginsMarketplaceMutationResult>;
  updatePluginMarketplace(
    params: NexAgentUpdatePluginMarketplaceParams,
  ): Promise<NexPluginsMarketplaceMutationResult>;
  installPlugin(params: NexAgentInstallPluginParams): Promise<NexPluginsInstallResult>;
  cancelPluginOperation(
    params: NexAgentCancelPluginOperationParams,
  ): Promise<NexPluginsCancelOperationResult>;
  uninstallPlugin(params: NexAgentUninstallPluginParams): Promise<NexPluginsUninstallResult>;
  updatePlugin(params: NexAgentUpdatePluginParams): Promise<NexPluginsInstallResult>;
  restoreBuiltinPlugin(
    params: NexAgentRestoreBuiltinPluginParams,
  ): Promise<NexPluginsRestoreBuiltinResult>;
  configurePlugin(params: NexAgentConfigurePluginParams): Promise<NexPluginsConfigureResult>;
  resetPluginConfig(
    params: NexAgentResetPluginConfigParams,
  ): Promise<NexPluginsConfigureResult>;
  validatePlugin(params: NexAgentValidatePluginParams): Promise<NexPluginsValidateResult>;
  describePlugin(params: NexAgentDescribePluginParams): Promise<NexPluginsDescribeResult>;
  setPluginEnabled(params: NexAgentSetPluginEnabledParams): Promise<NexPluginsSetEnabledResult>;
  // ---- 定时任务(automation)管理 ----
  listAutomations(params: NexAgentWorkspaceTarget): Promise<NexAutomation[]>;
  listAllAutomations(): Promise<NexAutomation[]>;
  createAutomation(params: NexAgentCreateAutomationParams): Promise<NexAutomation>;
  updateAutomation(params: NexAgentUpdateAutomationParams): Promise<NexAutomation | null>;
  deleteAutomation(params: NexAgentAutomationIdParams): Promise<void>;
  setAutomationEnabled(params: NexAgentSetAutomationEnabledParams): Promise<void>;
  restartAutomation(params: NexAgentAutomationIdParams): Promise<void>;
  runAutomationNow(params: NexAgentAutomationIdParams): Promise<NexAgentRunAutomationNowResult>;
  listAutomationRuns(params: NexAgentAutomationIdParams): Promise<NexAutomationRun[]>;
  deleteAutomationRun(params: NexAgentDeleteAutomationRunParams): Promise<void>;
  generateWorkspaceText(
    params: NexAgentGenerateWorkspaceTextParams,
  ): Promise<NexWorkspaceGenerateTextResult>;
  testModelConnectivity(
    params: NexAgentTestModelConnectivityParams,
  ): Promise<NexProviderTestModelConnectivityResult>;
  /**
   * @deprecated：send 主路径已收敛 v4 sendText 命令。仅剩两个消费点——
   * adapter 带附件输入回退（待附件命令面落地后移除）与 nexSessionService
   * pass-through；新代码禁止回用。
   */
  sendPrompt(params: NexAgentSendPromptParams): Promise<NexSessionSendResult>;
  compactSession(params: NexAgentCompactParams): Promise<NexSessionCompactResult>;
  goalSession(params: NexAgentGoalParams): Promise<NexSessionGoalResult>;
  closeSession(
    params: NexAgentSessionTarget & { expectedPersistence?: "deferred" | "immediate" },
  ): Promise<boolean>;
  /**
   * 卸载 cwd 位于 directory 内的全部驻留会话（session/releaseUnderDirectory，docs/git/worktree.md §8.3）。
   * 会话选择与忙闲由 CLI 判断，任一会话忙时全部不卸载。只使用已存在的进程；没有进程时没有驻留会话。
   */
  releaseSessionsUnderDirectory?(
    params: NexAgentWorkspaceTarget & { directory: string },
  ): Promise<{ released: string[]; busy: string[] }>;
  /**
   * 按 commandId 查询 CLI 对一条命令的裁决（commands/query）；Host 内部用于确认失败的会话命令
   * 是否实际执行（docs/git/worktree.md §3.5）。CLI 没有记录时返回 "unknown"。
   */
  queryCommandResult?(
    params: NexAgentWorkspaceTarget & { key: CommandKey },
  ): Promise<CommandAck | "unknown">;
  setModel(params: NexAgentSetModelParams): Promise<NexSessionStateSnapshot>;
  setThoughtLevel(params: NexAgentSetThoughtLevelParams): Promise<NexSessionStateSnapshot>;
  setMode(params: NexAgentSetModeParams): Promise<NexSessionStateSnapshot>;
  respondProviderRuntimeHeaders(
    params: NexAgentRespondProviderRuntimeHeadersParams,
  ): Promise<void>;
  respondSessionRuntimePreferences(
    params: NexAgentRespondSessionRuntimePreferencesParams,
  ): Promise<void>;
  /**
   * v4 UI 的 Start Plan runtime-auth bridge 只需要本地 pending request 事件，
   * 不能为此额外打开旧 `session/subscribe`，否则会把旧 replay/continuous 读路径
   * 重新带回 v4 pane。
   */
  onDynamicProviderRuntimeHeadersRequest(
    params: NexAgentSessionTarget,
  ): Event<NexAgentProviderRuntimeHeadersRequest>;
  /**
   * Start Plan runtime headers 的 workspace 级订阅入口（生产应答方使用）。
   * Bug 原因（2026-08-16 web 远控卡死）：session 级入口只随桌面 pane 挂载订阅，
   * web 远控新建会话在桌面没有 pane、web 端无应答方，请求校验请求滞留 pending，
   * 任务永久「工作中」。桌面窗口用本入口为每个打开的 workspace tab 维持常驻应答方；
   * 订阅时先注册 live listener 再按 workspaceKey 前缀补投 pending，与 session 级同语义。
   */
  onDynamicWorkspaceProviderRuntimeHeadersRequest(
    params: NexAgentWorkspaceTarget,
  ): Event<NexAgentProviderRuntimeHeadersRequest>;
  /** 只投递本 workspace 的校验取消，不属于 conversation stream。 */
  onDynamicWorkspaceProviderRuntimeHeadersCancelled(
    params: NexAgentWorkspaceTarget,
  ): Event<NexProviderRuntimeHeadersCancelled>;
  onDynamicSessionRuntimePreferencesRequest(): Event<NexAgentSessionRuntimePreferencesRequest>;
  /**
   * CLI 进程级资源样本，带 services 打的 lane 标签（CLI 自己不知道 lane）。
   * 使用 dynamic event 避免 RPC 服务在无人订阅时缓冲周期事件；
   * 该事件不属于 session/conversation continuous 或 replayable 状态。
   */
  onDynamicProcessResourceSample(): Event<AgentLaneResourceSample>;
  /** 插件启动超时的工作区提示；订阅时补投当前进程的降级事实。 */
  onDynamicPluginStartupDegraded(params: NexAgentWorkspaceTarget): Event<void>;
  /** MCP 进程生命周期与低频内存事件，仅供可信 Host relay 上报 ARMS。 */
  /** 仅 trusted Host relay 订阅；手机统计不依赖 renderer 生命周期。 */
  onDynamicRemoteConversationTelemetry(): Event<RemoteConversationTelemetryEvent>;

  onDynamicMcpTelemetry(): Event<NexMcpTelemetryEvent>;
  /** MCP 进程树资源事实，只供可信 Host 汇总上报。 */
  onDynamicMcpResourceSamples(): Event<NexMcpResourceSample[]>;
  /** Bash 完成事实，仅可信 Host 资源旁路订阅。 */
  onDynamicToolExecResource(): Event<NexToolExecResource>;
  /**
   * @deprecated 旧协议订阅面（session/subscribe + session/event + state.updated）。
   * task-index syncer 已迁 v4 sessions-index/workspace-config 帧；
   * 仅剩 nexTaskServiceAdapter.onDynamicTaskEvent（replayable 读路径）消费。
   * 写路径已收敛 v4 命令面；本订阅是读路径投影源。
   */
  onDynamicSessionEvent(params: NexAgentSessionSubscribeParams): Event<NexAgentServiceEvent>;
  // ── v4 conversation 通道（竖切）──
  /** RPC attachment 建立后先读取 host 可信 hello。 */
  helloConversationV4(): Promise<HelloMessage>;
  /** hello 校验后回送 clientHello；metadata 不能覆盖 connection mode/profile。 */
  initializeConversationV4(clientHello: ClientHello): Promise<void>;
  /** 仅供 trusted host relay/facade；terminal RPC caller 必须被 connection scope 拒绝。 */
  setConnectionFlowStateV4(params: NexAgentConnectionFlowParams): Promise<void>;
  subscribeConversationV4(
    params: NexAgentConversationSubscribeParams,
  ): Promise<V4ConversationSubscribeResult>;
  resyncConversationV4(
    params: NexAgentConversationResyncParams,
  ): Promise<V4ConversationResyncResult>;
  unsubscribeConversationV4(params: NexAgentConversationUnsubscribeParams): Promise<void>;
  /** rows/range 行分页 query（loadOlder 游标向上补历史）。 */
  conversationRowsRangeV4(
    params: NexAgentConversationRowsRangeParams,
  ): Promise<V4ConversationRowsRangeResult>;
  conversationPlansV4(
    params: NexAgentConversationPlansParams,
  ): Promise<V4ConversationPlansResult>;
  /** 输入预测：只读、无状态；Host 开关关闭 / 无运行时 / 失败一律回空结果，不抛错。 */
  conversationInputPredictionV4(
    params: NexAgentConversationInputPredictionParams,
  ): Promise<V4ConversationInputPredictionResult>;
  /** workflow run 事件日志分页；与 plans 同族（只读、无状态、超时重发安全）。 */
  conversationWorkflowRunEventsV4(
    params: NexAgentConversationWorkflowRunEventsParams,
  ): Promise<V4ConversationWorkflowRunEventsResult>;
  /** workflow run 枚举；journal-backed 的重启后发现面。 */
  conversationWorkflowRunsV4(
    params: NexAgentConversationWorkflowRunsParams,
  ): Promise<V4ConversationWorkflowRunsResult>;
  /** workflow run 的用户面产物清单；与 plans 同族（只读、无状态、超时重发安全）。 */
  conversationWorkflowRunArtifactsV4(
    params: NexAgentConversationWorkflowRunArtifactsParams,
  ): Promise<V4ConversationWorkflowRunArtifactsResult>;
  /** 预置看板的条目分页；hook 以 itemCount 变化为信号增量拉取。 */
  conversationWorkflowRunArtifactDataV4(
    params: NexAgentConversationWorkflowRunArtifactDataParams,
  ): Promise<V4ConversationWorkflowRunArtifactDataResult>;
  /** 内容产物的字节，一次一块；授权在 CLI 侧（journal 行才是取字节的依据）。 */
  conversationWorkflowRunArtifactReadV4(
    params: NexAgentConversationWorkflowRunArtifactReadParams,
  ): Promise<V4ConversationWorkflowRunArtifactReadResult>;
  /** dwf 工作区 transcript 的清单。 */
  conversationWorkflowRunWorkspaceV4(
    params: NexAgentConversationWorkflowRunWorkspaceParams,
  ): Promise<V4ConversationWorkflowRunWorkspaceResult>;
  /** 一个工作区节点的有界正文。 */
  conversationWorkflowRunNodeResultV4(
    params: NexAgentConversationWorkflowRunNodeResultParams,
  ): Promise<V4ConversationWorkflowRunNodeResultResult>;
  backgroundBashOutputV4(
    params: NexAgentBackgroundBashOutputParams,
  ): Promise<BackgroundBashOutputResult>;
  conversationFileChangesV4(
    params: NexAgentConversationFileChangesParams,
  ): Promise<V4ConversationFileChangesResult>;
  conversationFileRewindPreviewV4(
    params: NexAgentConversationFileRewindPreviewParams,
  ): Promise<V4ConversationFileRewindPreviewResult>;
  sendConversationCommandV4(params: NexAgentConversationCommandParams): Promise<CommandAck>;
  queryConversationCommandsV4(params: NexAgentCommandsQueryParams): Promise<CommandsQueryResult>;
  attachmentBeginV4(params: NexAgentAttachmentBeginParams): Promise<V4AttachmentBeginResult>;
  attachmentChunkV4(params: NexAgentAttachmentChunkParams): Promise<V4AttachmentChunkResult>;
  attachmentCommitV4(params: NexAgentAttachmentTerminalParams): Promise<V4AttachmentCommitResult>;
  attachmentAbortV4(params: NexAgentAttachmentTerminalParams): Promise<void>;
  /** Desktop local 已发送视频 source query；远端与 Web 返回 chunked。 */
  attachmentPreviewSourceV4(
    params: NexAgentAttachmentPreviewSourceParams,
  ): Promise<V4AttachmentPreviewSourceResult>;
  /** 已发送 image/video 只读分块查询；connection scope 注入可信 workspace 连接。 */
  attachmentReadV4(params: NexAgentAttachmentReadParams): Promise<V4AttachmentReadResult>;
  /** Share 读取 userInput 附件，允许 text/plain 等非媒体类型。 */
  conversationAttachmentReadV4(
    params: NexAgentConversationAttachmentReadParams,
  ): Promise<V4ConversationAttachmentReadResult>;
  /** Share 选择阶段只读 userInput 附件元数据，不读取完整内容。 */
  conversationAttachmentStatV4(
    params: NexAgentConversationAttachmentStatParams,
  ): Promise<V4ConversationAttachmentStatResult>;
  getSessionLifecycle(
    params: NexAgentSessionTarget,
  ): Promise<import("@nex/shared").SessionLifecycleSnapshot | null>;
  ensureSessionReady(params: NexAgentSessionTarget): Promise<void>;
  stopSessionRuntime(params: NexAgentSessionTarget): Promise<void>;
  restartSessionRuntime(params: NexAgentSessionTarget): Promise<void>;
  /** 独立于可被释放的 conversation publisher 的生命周期事实。 */
  onDynamicSessionLifecycle(
    params: NexAgentWorkspaceTarget,
  ): Event<import("@nex/shared").SessionLifecycleSnapshot>;
  /** workspace 级下行帧流（v4/conversation/frame），renderer 侧按 topic 自行路由。 */
  onDynamicConversationFrame(
    params: NexAgentWorkspaceTarget,
  ): Event<ConversationTopicWireCandidate>;
  /** workspace 级 live telemetry 事实；connection facade 仅向可信 desktop-continuous 下游暴露。 */
  onDynamicLocalTtftFacts(
    params: NexAgentWorkspaceTarget,
  ): Event<import("@nex/shared").LocalTtftFacts>;
  onDynamicConversationTelemetryFact(
    params: NexAgentWorkspaceTarget,
  ): Event<ConversationTelemetryFact>;
  /**
   * context 组成（apps/nex-cli/docs/design/v2/model/context-composition.md §3.6）。
   * existing-only：工作区没有运行中的 CLI、会话不在内存、旧 CLI（method-not-found）都返回 null，不启动 CLI。
   */
  getContextComposition?(
    params: NexAgentContextCompositionParams,
  ): Promise<ContextComposition | null>;
  // 注意：读本插件 user 层设置（含敏感值）的 readPluginUserOptions 故意不在本接口上。本服务整个
  // NexAgent 频道经 RPC 暴露（桌面 Renderer；手机远控 allowlist 也含它），挂在这里就会被任意客户端调到。
  // Window Host 经 node.ts 的 getPluginUserOptionsReader(services) 在进程内取得（见 pluginHostQueriesClient.ts）。
  /**
   * 输入框 `@` 面板 `tool` target 的候选项（docs/plugins/extension-host.md §16.6）。existing-only：
   * 没有运行中的 CLI 抛 runtime-unavailable；会话不在 CLI 内存抛 -32004；其他失败的 `data.code` 是
   * `plugin_mention_*`。工具调用走会话的 MCP App 授权链，首次可能在会话里等待用户审批。
   * 旧 CLI（method-not-found）→ `{ supported: false }`。不设计取消，调用方只采用最新一次结果。
   */
  getPluginMentionItems?(
    params: NexAgentPluginMentionItemsParams,
  ): Promise<NexAgentPluginMentionItemsResult>;
  /** context 组成变更：只带修订号，live-only；收到后按需调 getContextComposition。 */
  onDynamicContextCompositionChanged?(
    target: NexAgentWorkspaceTarget,
  ): Event<ContextCompositionChangedEvent>;
  /**
   * 模型请求指标（docs/monitoring/conversation-telemetry-v4.md「模型请求指标」）：每个完成的请求一条，
   * 带 usage 与首内容时间；按 querySource 区分主轮与旁路。旧 CLI 不发。
   */
  onDynamicModelRequestMetrics?(target: NexAgentWorkspaceTarget): Event<ModelRequestMetricsFact>;
  /** Host 消费驻留 CLI 的闲时生命周期；不参与聊天快照恢复。 */
  onDynamicOffPeakLifecycle(
    params: NexAgentWorkspaceTarget,
  ): Event<import("@nex/shared").OffPeakLifecycleNotification>;
  controlOffPeakExecutionV4(
    params: NexAgentWorkspaceTarget & { control: import("@nex/shared").OffPeakControlParams },
  ): Promise<import("@nex/shared").OffPeakControlResult>;
  /** 当前窗口全部本地 live task 的 CUA 权限观察；历史、远程与 replayable 不在此事件面。 */
  onDynamicCuaPermissionObservation(): Event<NexAgentCuaPermissionObservation>;
  /** 本地桌面窗口创建插件页面，不依赖 conversation 订阅。 */
  onDynamicPluginUiToolResult(): Event<
    PluginUiToolResultNotification & { workspacePath: string; workspaceIdentity?: string }
  >;
  /** 只读 CLI 持久事实；不恢复会话运行实例。旧远端可以不提供此能力。 */
  reconcileSessionsIndexV4?(
    params: NexAgentWorkspaceTarget & { sessionIds: string[] },
  ): Promise<SessionsIndexReconcileResult>;
  // ── M5 sessions-index 通道（列表活性）──
  subscribeSessionsIndexV4(
    params: NexAgentSessionsIndexSubscribeParams,
  ): Promise<V4SessionsIndexSubscribeResult>;
  resyncSessionsIndexV4(
    params: NexAgentConversationResyncParams,
  ): Promise<V4ConversationResyncResult>;
  unsubscribeSessionsIndexV4(params: NexAgentConversationUnsubscribeParams): Promise<void>;
  /** workspace 级 sessions-index 下行帧流（与 conversation 同一通知，按 topic 前缀分流）。 */
  onDynamicSessionsIndexFrame(
    params: NexAgentWorkspaceTarget,
  ): Event<SessionsIndexTopicWireCandidate>;
  // ── workspace-config 通道（配置目录活性；task-index syncer 消费）──
  subscribeWorkspaceConfigV4(
    params: NexAgentWorkspaceConfigSubscribeParams,
  ): Promise<V4WorkspaceConfigSubscribeResult>;
  resyncWorkspaceConfigV4(
    params: NexAgentConversationResyncParams,
  ): Promise<V4ConversationResyncResult>;
  unsubscribeWorkspaceConfigV4(params: NexAgentConversationUnsubscribeParams): Promise<void>;
  /** workspace 级 workspace-config 下行帧流（与 conversation 同一通知，按 topic 前缀分流）。 */
  onDynamicWorkspaceConfigFrame(
    params: NexAgentWorkspaceTarget,
  ): Event<WorkspaceConfigTopicWireCandidate>;
  /**
   * （CLI 重连重订）：agent 进程换代通知（超时回收/崩溃后重新拉起）。
   * v4 订阅活在 CLI 进程内存，进程换代即失效；订阅方（task-index syncer 等）
   * 收到后必须对该 workspaceKey 重发 subscribe，否则帧流静默中断。
   */
  onAgentRuntimeRestarted(listener: (event: { workspaceKey: string }) => void): IDisposable;
  /**
   * Agent client 在 service 内完成登记后发布 available，当前 client 关闭后发布 unavailable。
   * 这是被动 observer attach/detach 的唯一生命周期信号，不表达用户使用租约。
   */
  onAgentRuntimeLifecycle?: (
    listener: (event: NexAgentRuntimeLifecycleEvent) => void,
  ) => IDisposable;
  /** 当前 runtime 进程意外退出；界面据此提示原因并提供重新连接。 */
  onAgentRuntimeExited?: (listener: (event: NexAgentRuntimeExitedEvent) => void) => IDisposable;
  /** 当前 desktop-local CUA turn 是否仍在执行，用于 Helper recovery 避免中途回收 Agent。 */
  hasActiveCuaOperationTurn(): boolean;
  disposeWorkspace(
    params: NexAgentWorkspaceTarget & { releasePreparation?: boolean },
  ): Promise<void>;
  disposeAll(): void;
}

export const INexAgentService = createServiceDescriptor<INexAgentService>(
  ServiceChannels.NexAgent,
);
