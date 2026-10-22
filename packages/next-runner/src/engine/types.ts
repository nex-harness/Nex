import type { ModelSelection } from "@nex/shared";

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
  /** Sidebar organization state (docs/next/task-state.md). Absent = false. */
  pinned?: boolean;
  archived?: boolean;
  /** Newest unread background completion; absent means read. */
  unreadAt?: number;
}

/** Serialized result of a compare-and-clear unread write. */
export interface EngineUnreadState {
  /** Marker after the write; absent means read. */
  unreadAt?: number;
  /** True only when the expected marker matched and was cleared. */
  cleared: boolean;
}

/** Normalized transcript used for restore; mirrors the pi message shapes we consume. */
export type EngineMessage =
  | { role: "user"; at: number; text: string }
  | {
      role: "assistant";
      at: number;
      model?: string;
      usage?: EngineUsage;
      stopReason: EngineStopReason;
      errorMessage?: string;
      parts: EngineAssistantPart[];
    }
  | {
      role: "toolResult";
      at: number;
      toolCallId: string;
      toolName: string;
      text: string;
      isError: boolean;
      durationMs?: number;
    };

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
export type EngineEvent =
  | { type: "user_input"; at: number; text: string }
  | { type: "assistant_start"; at: number; model?: string }
  | { type: "text_delta"; at: number; delta: string }
  | { type: "thinking_delta"; at: number; delta: string }
  | { type: "tool_call_start"; at: number; toolCallId: string; toolName: string; input?: unknown }
  | {
      type: "tool_input_delta";
      at: number;
      toolCallId: string;
      toolName: string;
      delta: string;
    }
  | {
      type: "tool_call_update";
      at: number;
      toolCallId: string;
      toolName: string;
      text?: string;
    }
  | {
      type: "tool_call_end";
      at: number;
      toolCallId: string;
      toolName: string;
      text: string;
      isError: boolean;
      input?: unknown;
    }
  | {
      type: "assistant_end";
      at: number;
      stopReason: EngineStopReason;
      errorMessage?: string;
      model?: string;
      usage?: EngineUsage;
    }
  | { type: "settled"; at: number; aborted: boolean }
  | { type: "phase"; at: number; phase: EnginePhase }
  | { type: "mode_changed"; at: number; mode: PermissionMode; planEnabled: boolean }
  | { type: "approval_request"; at: number; approval: EngineApprovalRequest }
  | {
      type: "approval_settled";
      at: number;
      interactionId: string;
      toolCallId: string;
      outcome: EngineApprovalOutcome;
    }
  | { type: "error"; at: number; message: string };

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
}

export interface SubmitOptions {
  requestId: string;
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

export interface SubmitOutcome {
  status: "accepted" | "duplicate";
  inputId: string;
}

export interface EngineEventSubscription {
  dispose(): void;
}

export interface Engine {
  init(): Promise<void>;

  /** Creates a pi session immediately (draft prewarm relies on this). */
  createSession(
    workspacePath: string,
    options: CreateEngineSessionOptions,
  ): Promise<{ sessionId: string; inputId?: string }>;

  /** Lazy restore from disk. Idempotent for an already-open session. */
  openSession(workspacePath: string, sessionId: string): Promise<void>;

  deleteSession(workspacePath: string, sessionId: string): Promise<void>;

  /** Non-deleted sessions, newest activity first. */
  listSessions(workspacePath: string): Promise<EngineSessionState[]>;

  /** Tombstoned session ids (deleted, kept for the sidebar's negative membership join). */
  listDeletedSessionIds(workspacePath: string): Promise<string[]>;

  history(sessionId: string): Promise<EngineMessage[]>;

  subscribe(sessionId: string, listener: (event: EngineEvent) => void): EngineEventSubscription;

  submit(sessionId: string, text: string, options: SubmitOptions): Promise<SubmitOutcome>;

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

  catalog(): Promise<EngineCatalog>;

  /** Fires when session metadata (create/rename/delete/phase/title) changes. */
  onSessionsChanged(listener: (workspacePath: string) => void): () => void;

  dispose(): Promise<void>;
}
