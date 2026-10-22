import type { QueueState, RowActions } from "@nex/shared/nex-protocol-v4";
import type {
  EngineApprovalOutcome,
  EngineFollowupMode,
  EnginePhase,
  EngineTitleSource,
  EngineUsage,
  EngineModelState,
  PermissionMode,
} from "../engine/types.js";

export type ProjectedRow =
  | TurnHeaderRowState
  | UserInputRowState
  | AssistantTextRowState
  | ReasoningRowState
  | ToolCallRowState
  | TimelineMarkerRowState;

interface RowBase {
  rowId: number;
  turnId: string;
  /**
   * Stable identity across runner restarts (`pi:<entryId>`, see `entityIds.ts`). Live rows
   * get it from the `message_entry` event that follows pi's message persistence; restore
   * derives the same value from `SessionManager.getBranch()`.
   */
  entityId?: string;
  /** Internal: index of this text/thinking segment inside the assistant message. */
  segmentIndex?: number;
  /** Derived per snapshot (docs/next/conversation-features.md decision 1); never stored. */
  actions?: RowActions;
  createdAt: number;
  createdAtSeq: number;
}

export interface TurnHeaderRowState extends RowBase {
  kind: "turnHeader";
  state: "running" | "completedSuccess" | "completedInterrupted" | "failed";
  startedAt: number;
  endedAt?: number;
}

export interface UserInputRowState extends RowBase {
  kind: "userInput";
  text: string;
  sourceCommandId: string;
}

export interface AssistantTextRowState extends RowBase {
  kind: "assistantText";
  assistantResponseId: string;
  text: string;
  state: "streaming" | "complete" | "interrupted" | "failed";
  model?: string;
}

export interface ReasoningRowState extends RowBase {
  kind: "reasoning";
  assistantResponseId: string;
  text: string;
  state: "streaming" | "complete" | "interrupted";
}

export interface ToolCallRowState extends RowBase {
  kind: "toolCall";
  assistantResponseId?: string;
  toolCallId: string;
  toolName: string;
  status: "inputStreaming" | "pendingApproval" | "running" | "success" | "error" | "cancelled";
  inputText: string;
  input?: unknown;
  outputText?: string;
  errorMessage?: string;
  /** Set while the row waits for approval; cleared on the terminal transition. */
  approvalInteractionId?: string;
  startedAt?: number;
  endedAt?: number;
}

/** Marker payloads this runner can produce (subset of `timelineMarkerPayloadSchema`). */
export type TimelineMarker =
  | {
      type: "modelChange";
      fromProvider: string;
      fromModel: string;
      toProvider: string;
      toModel: string;
      toThought: string;
    }
  | {
      type: "modelChange";
      toProvider: string;
      toModel: string;
      toThought: string;
    }
  | {
      type: "compact";
      origin: "manual" | "auto";
      status: "running" | "success" | "failed" | "noop" | "cancelled";
      tokensBefore?: number;
      tokensAfter?: number;
      summaryRef?: string;
    };

/** Backwards-compatible alias for the modelChange-only payload. */
export type ModelChangeMarker = Extract<TimelineMarker, { type: "modelChange" }>;

/**
 * Timeline marker row (rows.ts `timelineMarkerRowSchema`). `modelChange` comes from the
 * live model switch; `compact` is projected on restore from pi's compaction entries.
 */
export interface TimelineMarkerRowState extends RowBase {
  kind: "timelineMarker";
  lane?: "assistantWork" | "turnTailBoundary" | "lightBoundary";
  sourceCommandId?: string;
  marker: TimelineMarker;
}

/** Mirrors one live `EngineApprovalRequest`; the gate remains its owner. */
export interface PendingApprovalState {
  interactionId: string;
  toolCallId: string;
  toolName: string;
  summary: string;
  detail: unknown;
  createdAt: number;
  outcome?: EngineApprovalOutcome;
}

export interface ProjectionError {
  code: string;
  message: string;
  at: number;
  recoverable: boolean;
  source: "provider" | "runtime" | "tool" | "network";
}

export interface ProjectionState {
  sessionId: string;
  logEpoch: string;
  seq: number;
  revision: number;
  phase: EnginePhase;
  title: string;
  titleSource: EngineTitleSource;
  createdAt: number;
  lastActivityAt: number;
  model: EngineModelState | null;
  usage: EngineUsage;
  /** Permission mode echoed in `snapshot.config`; the gate is the authority. */
  mode: PermissionMode;
  planEnabled: boolean;
  /** Composer follow-up mode (snapshot `config.followupMode` + `inputRouting`). */
  followupMode: EngineFollowupMode;
  /**
   * Queue facts (snapshot `queue`). Written **only** by `v4/queue.ts` (the session queue
   * owner) through `SessionRuntimeStore`; the fold never touches it. Restored sessions start
   * empty because the queue is deliberately not persisted (old CLI parity).
   */
  queue: QueueState;
  rows: ProjectedRow[];
  nextRowId: number;
  currentTurn: {
    turnId: string;
    headerRowId: number;
    startedAt: number;
  } | null;
  currentResponse: {
    responseId: string;
    at: number;
    model?: string;
    /** Live open text row; cleared when a tool call closes the current text segment. */
    textRowId?: number;
    reasoningRowId?: number;
    /** Number of text segments already opened for this message (entityId suffix). */
    textSegments: number;
    /** Number of thinking segments already opened for this message. */
    thinkingSegments: number;
  } | null;
  toolRows: Record<string, number>;
  /**
   * Assistant messages whose pi entry id has not arrived yet. `message_entry` attaches the
   * persisted entry id to the rows of the oldest matching group (see `entityIds.ts`).
   * Internal only — never serialized.
   */
  pendingMessageGroups: PendingMessageGroup[];
  /** Live approval cards, keyed by interactionId (insertion order = request order). */
  pendingApprovals: Record<string, PendingApprovalState>;
  lastError: ProjectionError | null;
}

/** One pi message's rows, waiting for its persisted entry id. */
export interface PendingMessageGroup {
  kind: "user" | "assistant";
  rowIds: number[];
  /** Only for `user`: the turnHeader created alongside the input row. */
  headerRowId?: number;
}

export interface CreateProjectionInput {
  sessionId: string;
  logEpoch: string;
  createdAt: number;
  title?: string;
  titleSource?: EngineTitleSource;
  model?: EngineModelState | null;
  phase?: EnginePhase;
  mode?: PermissionMode;
  planEnabled?: boolean;
  followupMode?: EngineFollowupMode;
}

export function createProjection(input: CreateProjectionInput): ProjectionState {
  const now = input.createdAt;
  return {
    sessionId: input.sessionId,
    logEpoch: input.logEpoch,
    seq: 0,
    revision: 0,
    phase: input.phase ?? "draft",
    title: input.title ?? "",
    titleSource: input.titleSource ?? "default",
    createdAt: now,
    lastActivityAt: now,
    model: input.model ?? null,
    usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    mode: input.mode ?? "build",
    planEnabled: input.planEnabled ?? false,
    followupMode: input.followupMode ?? "queue",
    queue: { items: [], autoDrain: true },
    rows: [],
    nextRowId: 1,
    currentTurn: null,
    currentResponse: null,
    toolRows: {},
    pendingMessageGroups: [],
    pendingApprovals: {},
    lastError: null,
  };
}

export function bump(state: ProjectionState): ProjectionState {
  return { ...state, seq: state.seq + 1, revision: state.revision + 1 };
}

/**
 * `seq` advances on every published change (the snapshot watermark), but `revision` is the
 * CAS token and must follow the old CLI's closed definition
 * (`projection-state.ts:196-215`): row structure and the revision-bearing A-zone fields.
 *
 * This runner has no deltas — it republishes whole snapshots — so the distinction is
 * expressed as two helpers: `touch` bumps `seq` only (streaming text growth, usage), and
 * `bump` bumps both (row add/update/remove, meta, config, queue, interactions).
 */
export function touch(state: ProjectionState): ProjectionState {
  return { ...state, seq: state.seq + 1 };
}

export function allocateRowId(state: ProjectionState): { id: number; next: number } {
  return { id: state.nextRowId, next: state.nextRowId + 1 };
}

export function addUsage(total: EngineUsage, delta: EngineUsage | undefined): EngineUsage {
  if (!delta) return total;
  return {
    input: total.input + (delta.input ?? 0),
    output: total.output + (delta.output ?? 0),
    cacheRead: total.cacheRead + (delta.cacheRead ?? 0),
    cacheWrite: total.cacheWrite + (delta.cacheWrite ?? 0),
  };
}

export function jsonText(value: unknown): string {
  if (value === undefined) return "";
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

export function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

export function asString(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

export function asNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}
