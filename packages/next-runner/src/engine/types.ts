import type { ModelSelection } from "@nex/shared";
import type {
  EngineApplyFileRewindOutcome,
  EngineFileChanges,
  EngineFileFacts,
} from "./engineFileFacts.js";
import type { RewindPreview } from "./checkpoints/rewindStore.js";
import type {
  EngineCompactOutcome,
  EngineCompactState,
  EngineRunState,
  SubmitOutcome,
  SwitchModelConfigInput,
  SwitchModelConfigOutcome,
} from "./engineRunSurfaces.js";

/**
 * Engine boundary. The rest of the runner (projection, publishers, channels) only sees
 * these types; `engine/pi/` is the single implementation that touches the pi SDK.
 * Keeping this narrow is what makes a later pi-durable swap possible.
 */

export type EnginePhase =
  | "draft"
  | "prewarming"
  | "running"
  | "completedSuccess"
  | "completedInterrupted"
  | "error";

export type EngineTitleSource = "default" | "generated" | "custom";

/** Composer follow-up mode for input sent while a run is active (docs/next/conversation-features.md §9). */
export type EngineFollowupMode = "queue" | "guide";

export interface EngineUsage {
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
}

export interface EngineModelState {
  providerId: string;
  modelId: string;
  displayName?: string;
  thought: string;
  thoughtLevels: string[];
}

export interface EngineSessionState {
  sessionId: string;
  workspacePath: string;
  title: string;
  titleSource: EngineTitleSource;
  createdAt: number;
  lastActivityAt: number;
  phase: EnginePhase;
  model: EngineModelState | null;
  usage: EngineUsage;
  isRunning: boolean;
  /** Last submission's permission mode; seeds the restored projection. */
  mode?: PermissionMode;
  planEnabled?: boolean;
  /** Persisted composer follow-up mode; absent means the runner default (`queue`). */
  followupMode?: EngineFollowupMode;
  /** Sidebar organization state (docs/next/task-state.md). Absent = false. */
  pinned?: boolean;
  archived?: boolean;
  /** Newest unread background completion; absent means read. */
  unreadAt?: number;
  /**
   * 该会话由哪条 automation 派发创建（createSession.firstInput.automationId）。侧栏
   * cron 徽章经 `NexTaskMeta.cronAutomationId` 消费；也是“无人值守”的唯一判据。
   */
  automationId?: string;
}

/** Serialized result of a compare-and-clear unread write. */
export interface EngineUnreadState {
  /** Marker after the write; absent means read. */
  unreadAt?: number;
  /** True only when the expected marker matched and was cleared. */
  cleared: boolean;
}

export type { EngineMessage } from "./engineMessages.js";

export type EngineAssistantPart =
  | { type: "text"; text: string }
  | { type: "thinking"; text: string }
  | { type: "toolCall"; id: string; name: string; arguments: unknown };

export type EngineStopReason =
  | "pending"
  | "stop"
  | "length"
  | "toolUse"
  | "error"
  | "aborted"
  | "deferred";

/** Submission permission mode (spec: docs/next/permissions.md). */
export type PermissionMode = "build" | "edit" | "yolo";

export type EngineApprovalOutcome = "allow" | "deny" | "aborted";

/** One tool call waiting for the user's approval. Owned by the engine's PermissionGate. */
export interface EngineApprovalRequest {
  interactionId: string;
  toolCallId: string;
  toolName: string;
  input: unknown;
  summary: string;
  createdAt: number;
}

/**
 * Live engine events. Deliberately close to pi's AgentSessionEvent but stripped of
 * cumulative `partial` payloads, so projection stays pure and cheap.
 */
import type { EngineEvent } from "./engineEvents.js";
import type { EngineMessage } from "./engineMessages.js";
import type { EngineSessions } from "./engineSessions.js";

export type { EngineEvent };

export interface EngineModelInfo {
  providerId: string;
  providerName: string;
  modelId: string;
  modelName: string;
  apiType: "anthropic-messages" | "openai-chat-completions" | "openai-responses";
  baseUrl: string;
  reasoningLevels: string[];
  maxOutputTokens: number;
  contextWindow: number;
  supportsImage: boolean;
  supportsVideo: boolean;
  supportsAudio: boolean;
  supportsPdf: boolean;
}

export interface EngineCatalog {
  models: EngineModelInfo[];
  defaultSelection: ModelSelection | null;
}

export interface CreateEngineSessionOptions {
  modelSelection?: ModelSelection;
  /**
   * automation 派发身份（server 侧 automations 引擎传 `firstInput.automationId`）。
   * 写入会话元数据并标记该会话无人值守：审批请求一律以 deny 结束。
   */
  automationId?: string;
}

export interface SubmitOptions {
  requestId: string;
  /**
   * Admitted attachment refs resolved by the runner (blob refs or absolute paths). The
   * engine maps images to pi prompt images and every other type to an attachment block.
   */
  attachments?: import("@nex/shared/nex-protocol-v4").AttachmentRef[];
  /**
   * Prepared pi prompt (attachment block + images). Absent for a plain submission; the
   * engine maps `attachments` itself in that case.
   */
  prompt?: import("./pi/piSessionOps.js").PromptAttachmentPayload;
  modelSelection?: ModelSelection;
  /** Permission mode of this submission (spec: docs/next/permissions.md). */
  mode?: string;
  planEnabled?: boolean;
}

/** Result of resolving a permission interaction. */
export interface ResolveApprovalResult {
  /** false for unknown/already-resolved ids (idempotent no-op). */
  resolved: boolean;
  outcome?: EngineApprovalOutcome;
}

export interface EngineEventSubscription {
  dispose(): void;
}

/** Resolves a row entityId one way or the other; the caller only sees pi entry ids. */
export interface EngineRewindTarget {
  /** Persisted pi entry id of the user message whose turn is being rewritten. */
  userEntryId: string;
  /** Text of that user message (the retry input). */
  text: string;
}

export interface EngineRewindRequest {
  /** Target user entry; the branch is cut to its parent. */
  target: EngineRewindTarget;
  newText: string;
  /** Attachments of the rewritten prompt (omitted by the caller only when there are none). */
  attachments?: import("@nex/shared/nex-protocol-v4").AttachmentRef[];
}

export interface EngineRewindOutcome {
  /** The prompt started (false when `newText` was blank). */
  started: boolean;
}

export interface EngineForkRequest {
  /** Persisted pi entry id of the target assistant message (must be on the active branch). */
  targetEntryId: string;
  /** Attachment marker recorded in the child's `nex.fork` entry. */
  parentSessionId: string;
  parentRowId: number;
  title: string;
}

export interface EngineForkOutcome {
  sessionId: string;
  title: string;
  titleSource: EngineTitleSource;
}

export interface Engine extends EngineSessions {
  init(): Promise<void>;

  /**
   * Route text into pi's steering queue (guide delivery). The text becomes the next
   * model-step input at the next tool-batch boundary; it is not a new turn owned by the
   * runner. Returns `false` when the session is not streaming (nothing was queued).
   */
  steer(
    sessionId: string,
    text: string,
    attachments?: import("@nex/shared/nex-protocol-v4").AttachmentRef[],
  ): Promise<boolean>;

  /** True while a run is active; drives admission and the queue drain gate. */
  runState(sessionId: string): EngineRunState;

  /** Resolves once no run/compaction is active (bounded by the caller's timeout). */
  waitForIdle(sessionId: string): Promise<void>;

  /**
   * Drops anything still sitting in pi's steering/follow-up queues. The runner never leaves
   * follow-up items there, so this exists to stop an undelivered guide from leaking into the
   * next run invisibly (the runner re-queues it as a visible item instead).
   */
  clearPiQueue(sessionId: string): void;

  /** Resolves a pending permission interaction; unknown ids are an idempotent no-op. */
  resolveApproval(
    sessionId: string,
    interactionId: string,
    answer: { optionId?: string; freeText?: string },
  ): Promise<ResolveApprovalResult>;

  /** The session's current permission mode (echoed into `snapshot.config.mode`). */
  permissionMode(sessionId: string): { mode: PermissionMode; planEnabled: boolean } | null;

  abort(sessionId: string): Promise<void>;

  rename(sessionId: string, title: string): Promise<void>;

  // ── sidebar task state (docs/next/task-state.md); metadata store is the single writer ──

  /** Sets pinned/archived flags. Returns the persisted row for the caller's event payload. */
  setTaskState(
    workspacePath: string,
    sessionId: string,
    patch: { pinned?: boolean; archived?: boolean },
  ): Promise<void>;

  /** Writes the unread marker (a number) or clears it (null). */
  setTaskUnread(
    workspacePath: string,
    sessionId: string,
    unreadAt: number | null,
    expectedUnreadAt?: number,
  ): Promise<EngineUnreadState>;

  /** Conditional tombstone: only while the row is still archived and not deleted. */
  deleteTaskIfArchived(workspacePath: string, sessionId: string): Promise<boolean>;

  setModel(sessionId: string, selection: ModelSelection | undefined): Promise<void>;

  /**
   * Mid-session model/thought switch (`switchModelConfig`). Applies pi's `setModel` +
   * clamped `setThinkingLevel`, persists the selection in metadata, and reports whether the
   * model identity changed so the caller can append the `modelChange` marker row.
   */
  switchModelConfig(
    workspacePath: string,
    sessionId: string,
    input: SwitchModelConfigInput,
  ): Promise<SwitchModelConfigOutcome>;

  /** Persists the follow-up mode (`queue` | `guide`); the projection derives inputRouting. */
  setFollowupMode(
    workspacePath: string,
    sessionId: string,
    mode: EngineFollowupMode,
  ): Promise<{ previous: EngineFollowupMode; mode: EngineFollowupMode }>;

  // ── conversation rewind / compact / fork (docs/next/conversation-features.md 5-7) ──

  /** Current compact facts (manual in flight, queued, pi compacting) for both owners. */
  compactState(sessionId: string): EngineCompactState;

  /**
   * Manual compaction (`session.compact()`). The caller emits the queued/running marker;
   * the engine reports the terminal outcome here and through `compact_end` events (so an
   * automatic compaction reaches the same marker path). Returns once pi settles it.
   */
  compact(sessionId: string, sourceCommandId: string): Promise<EngineCompactOutcome>;

  /** Engine-observed, not-yet-reported compact outcomes (drained by the projection owner). */
  takeCompactOutcomes(sessionId: string): EngineCompactOutcome[];

  /**
   * Rewind primitive (decision 5): abort + wait idle if running, branch to the parent of
   * the target user entry, refresh pi's context, then optionally prompt `newText` again in
   * the same session. The live projection is truncated by the caller *before* this is
   * called; the outcome reports what it removed.
   */
  rewind(sessionId: string, request: EngineRewindRequest): Promise<EngineRewindOutcome>;

  /**
   * Resolves the active branch's user message that owns the assistant `targetEntryId`
   * (the rewind anchor for retry). Undefined when the target is not a stable message.
   */
  userMessageBefore(sessionId: string, targetEntryId: string): Promise<EngineRewindTarget | null>;

  /**
   * Fork: copy the conversation up to `targetEntryId` into a new pi session file (second
   * `SessionManager`, `createBranchedSession`), record the `nex.fork` custom entry, and
   * register the child. The parent is never touched.
   */
  forkAssistant(
    workspacePath: string,
    sessionId: string,
    request: EngineForkRequest,
  ): Promise<EngineForkOutcome>;

  // ── file changes and rewind (batch 3, docs/next/attachments-rewind.md B) ──

  /**
   * Checkpoint facts for one turn's user entry: its `nex.checkpoints` record (with the
   * `nex.rewind` state folded in) and the latest log epoch the facts belong to.
   */
  fileCheckpointFacts(
    workspacePath: string,
    sessionId: string,
    userEntryId: string,
  ): Promise<EngineFileFacts>;

  /** Per-file diff aggregation for `conversationFileChangesV4`. */
  fileChanges(
    workspacePath: string,
    sessionId: string,
    userEntryId: string,
  ): Promise<EngineFileChanges>;

  /** Rewind preview (safe / unsafe / ignored) for a turn. */
  fileRewindPreview(
    workspacePath: string,
    sessionId: string,
    userEntryId: string,
  ): Promise<RewindPreview>;

  /**
   * Restores the turn-start files (deleting files the turn created) with a reverse journal.
   * `commitAfterApply` runs after every write; a throw compensates the writes and fails the
   * apply. On success the engine writes the `nex.rewind` entry (turn shows `reverted`).
   */
  applyFileRewind(
    workspacePath: string,
    sessionId: string,
    userEntryId: string,
    options: { commitAfterApply?: () => Promise<void>; sourceCommandId?: string },
  ): Promise<EngineApplyFileRewindOutcome>;

  /** True when file checkpoints are usable on this runner (git present). */
  fileCheckpointsEnabled(workspacePath: string): Promise<boolean>;

  catalog(): Promise<EngineCatalog>;

  /** MCP statuses of the newest session, freshly sampled (P1 `mcp-sync`). */
  refreshMcpStatus(): import("./pi/mcpStatusOps.js").McpStatusSnapshot;
  /** Async live statuses (hidden status session; waits briefly for connecting servers). */
  requestMcpStatus(settleMs?: number): Promise<import("./pi/mcpStatusOps.js").McpStatusSnapshot>;

  /** pi's skills and prompt templates for a workspace (P2 catalog / `/` menu). */
  piResources(workspacePath: string): Promise<import("./pi/piResources.js").PiResourceCatalog>;

  /** Applied generation switches relevant to the `/` menu (P2). */
  appliedConfig(): { skillsEnabled: boolean };

  /** Fires when session metadata (create/rename/delete/phase/title) changes. */
  onSessionsChanged(listener: (workspacePath: string) => void): () => void;

  dispose(): Promise<void>;
}

export type { EngineApplyFileRewindOutcome, EngineFileChanges, EngineFileFacts };
export type { EngineCheckpointsRecord, EngineRewindRecord } from "./checkpointMessages.js";

export type {
  EngineCompactOutcome,
  EngineCompactState,
  EngineRunState,
  SubmitOutcome,
  SwitchModelConfigInput,
  SwitchModelConfigOutcome,
} from "./engineRunSurfaces.js";
