import { ServiceChannels } from "@nex/shared";
import type {
  TraceId,
  NexAgentMcpServer,
  NexDeliveryKind,
  NexMessageWithParts,
  ModelSelection,
  NexPermissionRequestParams,
  NexProviderRuntimeHeadersRequestParams,
  NexUserInputRequestParams,
  NexUserInputResponse,
  NexSessionInfo,
  NexSessionImportHistory,
  NexSessionEvent,
  NexSessionMode,
  NexSessionPersistence,
  NexSessionStateSnapshot,
  NexStateUpdatedNotification,
  NexWorkspacePresentation,
} from "@nex/shared";
import { createServiceDescriptor } from "#src/descriptors.js";
import type { ProviderRuntimeHeadersUiResponse } from "#src/nex-agent/nexAgent.js";

export interface NexSessionWorkspaceTarget {
  workspacePath: string;
  workspaceIdentity?: string;
  remoteSessionId?: string;
}

export type NexSessionReadWorkspacePresentationParams = NexSessionWorkspaceTarget;

export interface NexTaskTarget extends NexSessionWorkspaceTarget {
  sessionId: string;
}

export interface NexSessionCreateParams extends NexSessionWorkspaceTarget {
  /** 仅导入事务使用的预分配 ID；普通新会话继续由 Agent 分配。 */
  sessionId?: string;
  sessionTraceId?: TraceId;
  parentSessionId?: string;
  mode?: NexSessionMode;
  model?: ModelSelection;
  persistence?: NexSessionPersistence;
  thoughtLevel?: string;
  mcpServers?: NexAgentMcpServer[];
  importedHistory?: NexSessionImportHistory;
}

export interface NexSessionResumeParams extends NexTaskTarget {
  model?: ModelSelection;
  thoughtLevel?: string;
  mcpServers?: NexAgentMcpServer[];
  /**
   * 默认广播 resume 得到的历史快照，并让 shadow 订阅请求初始 snapshot。
   * 续聊发送前的 runtime 预恢复会关闭它，避免旧终态快照覆盖本地已开始的新输入运行态。
   */
  broadcastSnapshot?: boolean;
}

export interface NexSessionListParams extends NexSessionWorkspaceTarget {
  limit?: number;
}

export interface NexSessionReadParams extends NexTaskTarget {
  deliveryKind?: NexDeliveryKind;
  messageLimit?: number;
  afterSeq?: number;
}

export interface NexSessionMessagesParams extends NexTaskTarget {
  afterMessageId?: string;
  limit?: number;
}

export interface NexSessionEventsParams extends NexTaskTarget {
  afterSeq?: number;
  limit?: number;
}

export interface NexSessionSetModelParams extends NexTaskTarget {
  model: ModelSelection;
  expectedRevision?: number;
  persistAsWorkspaceLastUsed?: boolean;
}

export interface NexSessionSetThoughtLevelParams extends NexTaskTarget {
  thoughtLevel?: string;
  expectedRevision?: number;
  persistAsWorkspaceLastUsed?: boolean;
}

export interface NexSessionSetModeParams extends NexTaskTarget {
  mode: NexSessionMode;
  expectedRevision?: number;
}

export interface NexSessionRespondProviderRuntimeHeadersParams extends NexTaskTarget {
  requestId: string;
  response: ProviderRuntimeHeadersUiResponse;
}

export interface NexSessionSubscribeParams extends NexTaskTarget {
  deliveryKind: NexDeliveryKind;
  afterSeq?: number;
  includeSnapshot?: boolean;
  eventCoalescing?: {
    mode: "background-summary";
    intervalMs?: number;
  };
}

export type NexSessionServiceEvent =
  | { type: "session.event"; event: NexSessionEvent }
  | { type: "state.updated"; notification: NexStateUpdatedNotification }
  | { type: "permission.request"; request: NexPermissionRequestParams }
  | { type: "userInput.request"; request: NexUserInputRequestParams }
  | {
      type: "userInput.response";
      requestId: string;
      response: NexUserInputResponse;
    }
  | { type: "providerRuntimeHeaders.request"; request: NexProviderRuntimeHeadersRequestParams }
  | { type: "snapshot"; snapshot: NexSessionStateSnapshot };

export interface NexSessionInitializeResult {
  available: boolean;
  workspaceKey: string;
  protocolName?: string;
  protocolVersion?: number;
  transportKind?: "stdio" | "websocket";
  reason?: string;
  reasonCode?: "provider_not_ready";
}

export interface NexSessionWorkspaceRuntimeIdentity {
  generation: number;
  identity: string;
  processId?: number;
  workspaceKey: string;
}

export interface INexSessionService {
  initializeWorkspace(params: NexSessionWorkspaceTarget): Promise<NexSessionInitializeResult>;
  getWorkspaceRuntimeIdentity(
    params: NexSessionWorkspaceTarget,
  ): Promise<NexSessionWorkspaceRuntimeIdentity>;
  readWorkspacePresentation(
    params: NexSessionReadWorkspacePresentationParams,
  ): Promise<NexWorkspacePresentation>;
  createSession(params: NexSessionCreateParams): Promise<NexSessionStateSnapshot>;
  resumeSession(params: NexSessionResumeParams): Promise<NexSessionStateSnapshot>;
  listSessions(params: NexSessionListParams): Promise<NexSessionInfo[]>;
  readSession(params: NexSessionReadParams): Promise<NexSessionStateSnapshot>;
  readSessionMessages(params: NexSessionMessagesParams): Promise<NexMessageWithParts[]>;
  readSessionEvents(params: NexSessionEventsParams): Promise<NexSessionEvent[]>;
  promoteDeferredDraftSession(params: NexTaskTarget): Promise<void>;
  closeSession(params: NexTaskTarget): Promise<void>;
  closeDeferredDraftSession(params: NexTaskTarget): Promise<boolean>;
  setModel(params: NexSessionSetModelParams): Promise<NexSessionStateSnapshot>;
  setThoughtLevel(params: NexSessionSetThoughtLevelParams): Promise<NexSessionStateSnapshot>;
  setMode(params: NexSessionSetModeParams): Promise<NexSessionStateSnapshot>;
  respondProviderRuntimeHeaders(
    params: NexSessionRespondProviderRuntimeHeadersParams,
  ): Promise<void>;
  // M5 删波次 2：onDynamicSessionEvent（旧 session/subscribe 词表的 renderer 订阅面）
  // 已删除——v4 UI 走 agentService 的 conversation/sessions-index 帧通道，
  // 该方法在 renderer 侧已无任何调用者（grep packages/ui packages/desktop 为零）。
}

export const INexSessionService = createServiceDescriptor<INexSessionService>(
  ServiceChannels.NexSession,
);
