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
}

/** Serialized result of a compare-and-clear unread write. */
export interface EngineUnreadState {
  /** Marker after the write; absent means read. */
  unreadAt?: number;
  /** True only when the expected marker matched and was cleared. */
  cleared: boolean;
}

/**
 * Normalized transcript used for restore; mirrors the pi message shapes we consume.
 *
 * `entryId` is pi's session entry id (`SessionEntryBase.id`). Restore derives the row
 * `entityId` from it (`pi:<entryId>`), so it must match the id the live projection learned
 * through `message_entry` — otherwise action targets change identity across a restart.
 */
export type EngineMessage =
  | { role: "user"; at: number; text: string; entryId?: string }
  | {
      role: "assistant";
      at: number;
      model?: string;
      usage?: EngineUsage;
      stopReason: EngineStopReason;
      errorMessage?: string;
      entryId?: string;
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
      entryId?: string;
    }
  /**
   * A pi compaction boundary. Restore projects it as a `timelineMarker` row so the
   * pre-compaction transcript stays visible (research §0-b): `session.messages` is the
   * projected model context and drops exactly those rows.
   */
  | {
      role: "compaction";
      at: number;
      entryId: string;
      summary: string;
      tokensBefore: number;
      firstKeptEntryId: string;
      /**
       * `manual` when the entry belongs to a manual operation (pi `fromHook`, or the entry
       * linked to a live manual `compaction_start`); `auto` for threshold/overflow.
       */
      origin?: "manual" | "auto";
      /** Manual operation that produced this entry (the v4 commandId); keeps lineage on restore. */
      sourceCommandId?: string;
    }
  /** A branch summary left by pi's `navigateTree`; restore skips it (old CLI parity). */
  | { role: "branchSummary"; at: number; entryId: string; summary: string }
  /** Runner-only product marker stored as a pi custom entry; restore projects it. */
  | EngineForkMarker;

/**
 * `nex.fork` custom entry: the fork notice at a child session's head
 * (docs/next/conversation-features.md decision 6).
 */
export interface EngineForkMarker {
  role: "runnerMarker";
  kind: "forkNotice";
  at: number;
  entryId: string;
  parentSessionId: string;
  parentRowId: number;
}

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
  /**
   * A message became a persisted pi session entry. pi emits `message_end` before
   * `appendMessage`, so the row already exists; this event carries the persisted entry id
   * that turns row identity into `pi:<entryId>` (docs/next/conversation-features.md
   * decision 1). Events arrive in persistence order, so the projection pairs each id with
   * the oldest row group of the same role.
   */
  | { type: "message_entry"; at: number; entryId: string; role: "user" | "assistant" }
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
  /**
   * pi's own steer/follow-up queue contents (`AgentSessionEvent.queue_update`). The runner
   * never uses pi's follow-up queue for queued text (docs/next/conversation-features.md
   * decision 4), but guide messages live in pi's steering queue until the model consumes
   * them; this event is how a pending guide item learns it was consumed.
   */
  | { type: "pi_queue_update"; at: number; steering: string[]; followUp: string[] }
  | { type: "phase"; at: number; phase: EnginePhase }
  /** Generated/renamed title; projection updates `meta` without another snapshot source. */
  | { type: "title_changed"; at: number; title: string; titleSource: EngineTitleSource }
  | { type: "model_changed"; at: number; model: EngineModelState }
  /** Mid-session switch marker; `from` is absent for the explicit ∅→X boundary. */
  | {
      type: "model_change_marker";
      at: number;
      from?: { provider: string; model: string };
      to: { provider: string; model: string; thought: string };
      sourceCommandId?: string;
    }
  | {
      type: "followup_mode_changed";
      at: number;
      mode: EngineFollowupMode;
      sourceCommandId?: string;
    }
  /**
   * pi began a compaction. `reason:"manual"` is either the runner's own `compact()` call
   * (linked by `sourceCommandId`, set while a manual operation is in flight) or another
   * manual trigger; `threshold`/`overflow` are pi's automatic pre-run compaction.
   */
  | {
      type: "compact_start";
      at: number;
      reason: "manual" | "threshold" | "overflow";
      sourceCommandId?: string;
    }
  | {
      type: "compact_end";
      at: number;
      reason: "manual" | "threshold" | "overflow";
      sourceCommandId?: string;
      /** Present when the compaction completed; absent on failure/cancel. */
      result?: { tokensBefore: number; tokensAfter?: number };
      aborted: boolean;
      errorMessage?: string;
    }
  /** Transient API retry (pi `auto_retry_*`); never a row, surfaced for the UI's retry chip. */
  | { type: "api_retry_start"; at: number; attempt: number; maxAttempts: number; delayMs: number }
  | { type: "api_retry_end"; at: number; success: boolean }
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

/**
 * Queue/admission surface (docs/next/conversation-features.md decision 4).
 *
 * The runner owns queued text; pi's own queues are only used for `guide`, which pi cannot
 * address by id. These three calls are what the runner's queue drain and guide admission
 * need from the engine and nothing more.
 */
export interface EngineRunState {
  /** A run (or post-run continuation/compaction) is active. */
  streaming: boolean;
  /** No active run, compaction or branch summary. */
  idle: boolean;
}

/** Payload of the v4 `switchModelConfig` command (`command.ts`). */
export interface SwitchModelConfigInput {
  provider: string;
  model: string;
  thought: string;
}

/**
 * `unchanged` is the old CLI's `noop config.unchanged`; `failed` carries the reason code
 * (`provider.notInRegistry`) and leaves the session untouched.
 */
export interface SwitchModelConfigOutcome {
  status: "changed" | "unchanged" | "failed";
  reasonCode?: string;
  message?: string;
  /** Effective model after the switch (absent on failure). */
  model?: EngineModelState;
  /** True when the model identity changed, so the caller emits a `modelChange` marker. */
  modelIdentityChanged?: boolean;
  /** Model identity before the switch, for the marker's `from` fields. */
  previous?: { provider: string; model: string };
}

export interface EngineEventSubscription {
  dispose(): void;
}

/** Outcome of one compaction operation (manual via `compact()`, or pi's automatic one). */
export interface EngineCompactOutcome {
  status: "success" | "noop" | "cancelled" | "failed";
  origin: "manual" | "auto";
  tokensBefore?: number;
  tokensAfter?: number;
  /** `compact.noop` / `compact.failed` / the operation lock when nothing started. */
  reasonCode?: string;
  message?: string;
}

export interface EngineCompactState {
  /** A manual compact operation started by the runner is in flight. */
  manualInFlight: boolean;
  /** A manual compact operation is still queued in the runner queue. */
  queued: boolean;
  /** pi is compacting (manual or automatic). */
  compacting: boolean;
  /** Manual + auto operations the engine observed and has not yet reported. */
  pendingOutcomes: EngineCompactOutcome[];
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

  /**
   * Route text into pi's steering queue (guide delivery). The text becomes the next
   * model-step input at the next tool-batch boundary; it is not a new turn owned by the
   * runner. Returns `false` when the session is not streaming (nothing was queued).
   */
  steer(sessionId: string, text: string): Promise<boolean>;

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

  catalog(): Promise<EngineCatalog>;

  /** Fires when session metadata (create/rename/delete/phase/title) changes. */
  onSessionsChanged(listener: (workspacePath: string) => void): () => void;

  dispose(): Promise<void>;
}
