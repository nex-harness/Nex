/* oxlint-disable eslint(max-lines) -- Nex task 投影类型当前集中在单文件维护，新增 workspace 事件先保持就近声明。 */
// ── 旧协议兼容面（过渡期）──────────────────────────────
// 剩余 29 个导出：workspace 预热/workspace 事件、turn steer 结果、session binding/import、
// trace/input/query id 工厂函数、NexError、NexTaskCreateResult 等。
// 消费者：services 旧栈（nexAgentService/nexTaskServiceAdapter/nexTaskService）、
// UI 旧 store/投影。明星承重类型（NexTaskMeta/NexStreamEvent/NexTaskSnapshot 等）
// 已迁 nex-task-types-core.ts（幸存面）。本文件与旧 task service 组同生命周期。
import { createUuid } from "./uuid.js";
import type {
  TaskRealtimeReason,
  TraceId,
  InputId,
  QueryId,
  NexTaskGoal,
  NexProvider,
  NexConfigOption,
  NexSlashCommand,
  NexTaskMeta,
  NexTaskRuntimeCommand,
  NexTaskRuntimeCommandStatus,
  NexTaskSnapshotBodyField,
  NexTaskSnapshotToolField,
  NexTaskSnapshotFileContentField,
  NexPersistedToolCall,
} from "./nex-task-types-core.js";

/** 生成 traceId；保留旧函数名兼容已有调用，格式对齐 agent 的 UUID trace。 */
export function generateTraceId(_taskId: string): TraceId {
  return createUuid();
}

/** 创建 Nex session 级观测 traceId；后续 prompt 只继承，不重新生成。 */
export function createSessionTraceId(): TraceId {
  return createUuid();
}

export function createInputId(): InputId {
  return createUuid();
}

export function createQueryId(): QueryId {
  return createUuid();
}

/** cron 定时任务生成的 session 默认归入的系统分组固定 id。 */
export const CRON_DEFAULT_GROUP_ID = "nex-default-group-cron";

/** 闲时任务已派发会话默认归入的系统分组固定 id（与 cron 分组同构）。 */
export const OFF_PEAK_DEFAULT_GROUP_ID = "nex-default-group-off-peak";

type CronTaskIdentity = Pick<NexTaskMeta, "cronAutomationId"> & {
  automationId?: string;
};

/** 判断一个 task/session 是否由定时任务触发产生。 */
export function isCronTask(task: CronTaskIdentity): boolean {
  // 持久化 task meta 使用 cronAutomationId；但运行态派发/旧链路里字段名是 automationId。
  // UI 只看前者会让新创建或刚恢复的定时任务暂时没有 icon。
  return Boolean(task.cronAutomationId || task.automationId);
}

/** 判断一个 task/幻影行是否属于闲时任务（只看持久 meta 标记，UI 不反查 off-peak store）。 */
export function isOffPeakTask(task: Pick<NexTaskMeta, "offPeakTaskId">): boolean {
  return Boolean(task.offPeakTaskId);
}

// ---- Nex Provider ----
export type NexTaskTarget = NexTaskGoal;
// ---- Nex task 模式 ----
// ---- Nex 运行时状态 ----

/** workspace 级 Nex 初始化状态 */
export type NexWorkspaceInitStatus = "idle" | "initializing" | "ready" | "failed";

/** 预热 workspace 时返回的结果 */
export interface NexWorkspacePrepareResult {
  workspacePath: string;
  preparedSessionId: string;
  version?: string;
  provider: NexProvider;
  /** session/new 返回的初始 configOptions，预热阶段就推到 UI 让用户提前选择模型 */
  configOptions?: NexConfigOption[];
  /** 预热 session 已经广播出来的 slash commands，草稿态也可以直接拿来展示 */
  slashCommands?: NexSlashCommand[];
}

/** Codex 预热前的网络连通性检查结果 */
export interface NexCodexConnectivityCheckResult {
  /** 当前检查的目标域名 */
  target: string;
  /** host 侧是否成功连通目标站点 */
  reachable: boolean;
  /** 失败时保留原始错误，供 UI 日志和 toast 诊断 */
  error?: string;
}

// ---- Task 元数据 ----

/** createTask 的返回结果；在持久化元信息之外，补充首屏渲染需要的临时初始化数据。 */
export interface NexTaskCreateResult extends NexTaskMeta {
  /** createTask 时就能拿到的首批 slash commands，避免 UI 订阅建立前丢掉初始化命令。 */
  initialSlashCommands?: NexSlashCommand[];
}

/** “补齐已有 session”能力的结果，不负责切换当前 active provider。 */
export interface NexSyncTaskSessionBindingResult {
  provider: NexProvider;
  sessionId: string;
  lastSyncedTurnIndex: number;
  updated: boolean;
}

/** `switchAgent` 切换成功后的统一结果。 */
export interface NexSwitchAgentResult {
  meta: NexTaskMeta;
  provider: NexProvider;
  sessionId: string;
  reusedBinding: boolean;
}

/** 外部原生 session 导入的来源 provider；与 agent runtime 的 NexProvider 解耦，当前仅 Claude Code。 */
export type NexImportSessionSourceProvider = "claude";

/** 外部原生 session 的导入候选。 */
export interface NexImportableSessionCandidate {
  provider: NexImportSessionSourceProvider;
  sessionId: string;
  workspacePath: string;
  sourcePath: string;
  updatedAt: number;
  createdAt?: number;
  previewTitle?: string;
}

export interface NexImportedSessionResultItem {
  provider: NexImportSessionSourceProvider;
  sessionId: string;
  taskId: string;
  workspacePath: string;
}

export interface NexImportedSessionSkippedItem {
  provider: NexImportSessionSourceProvider;
  sessionId: string;
  reason: string;
  workspacePath?: string;
}

export interface NexImportSessionsResult {
  imported: NexImportedSessionResultItem[];
  skipped: NexImportedSessionSkippedItem[];
  failed: NexImportedSessionSkippedItem[];
}

// ---- Nex 配置与命令类型 ----
// ---- Nex 流式事件（Host → Renderer） ----
// ---- 新增流式事件类型 ----

export interface NexEnqueueTaskCommandResult {
  accepted: true;
  command: NexTaskRuntimeCommand;
}

export interface NexCancelTaskCommandResult {
  canceled: boolean;
  commandId: string;
  status?: NexTaskRuntimeCommandStatus;
  reason?: "not_found" | "already_running";
}

// ---- Workspace 级别事件（预热阶段、task 创建前的异步通知） ----

/**
 * Workspace 级别事件，用于在 task 创建前把 Nex Agent 异步推送的
 * 通知（如 slash commands、configOptions）实时传递到 UI。
 *
 * 与 NexStreamEvent 的区别：NexStreamEvent 绑定到具体 taskId，
 * NexWorkspaceEvent 绑定到 workspacePath，覆盖 task 创建前的空档期。
 */
export type NexWorkspaceEvent =
  | NexWorkspaceSlashCommandsUpdate
  | NexWorkspaceConfigOptionsUpdate
  | NexWorkspaceTaskListChanged
  | NexWorkspaceSessionMessageSendRequested;

export interface NexWorkspaceSlashCommandsUpdate {
  type: "workspace_slash_commands_update";
  workspacePath: string;
  /**
   * 远程 workspace 的 / 命令属于身份隔离状态，不能只靠 workspacePath 分发。
   * 同一路径可能来自不同 SSH/WSL/Docker session，带上 workspaceIdentity 后 service/UI 才能按 workspaceKey 收敛。
   */
  workspaceIdentity?: string;
  commands: NexSlashCommand[];
}

export interface NexWorkspaceConfigOptionsUpdate {
  type: "workspace_config_options_update";
  workspacePath: string;
  /**
   * 预热配置同样是 workspace 级身份状态，远程场景必须随事件传递 workspaceIdentity。
   */
  workspaceIdentity?: string;
  configOptions: NexConfigOption[];
}

export interface NexWorkspaceTaskListChanged {
  type: "workspace_task_list_changed";
  workspacePath: string;
  workspaceIdentity?: string;
  taskId?: string;
  reason: TaskRealtimeReason;
  taskMeta?: NexTaskMeta;
  /** Host 已确认该终态应制造后台未读；普通 status/resume/snapshot 收敛不得携带。 */
  unreadSignal?: "background_terminal";
}

export interface NexWorkspaceSessionMessageSendRequested {
  type: "workspace_session_message_send_requested";
  workspacePath: string;
  workspaceIdentity?: string;
  request: {
    content: string;
    createdAt: string;
    fromSessionId: string;
    messageId: string;
    requestId: string;
    toSessionId: string;
  };
}

// ---- 错误 ----

export interface NexError {
  code: string;
  message: string;
  traceId?: TraceId;
  taskId?: string;
}

// ---- 持久化格式 ----

export interface NexTaskSnapshotBody {
  refId: string;
  field: NexTaskSnapshotBodyField;
  content: string;
  hash: string;
  fullBytes: number;
}

export type NexTaskSnapshotRefKind = "tool_field" | "file_change_field";

export interface NexTaskSnapshotRefContent {
  kind: NexTaskSnapshotRefKind;
  refId: string;
  field: NexTaskSnapshotToolField | NexTaskSnapshotFileContentField;
  content: unknown;
  hash: string;
  fullBytes: number;
}

export interface NexTaskSnapshotToolCallsSlice {
  taskId: string;
  messageIndex: number;
  totalTools: number;
  startToolIndex: number;
  endToolIndexExclusive: number;
  tools: NexPersistedToolCall[];
}
