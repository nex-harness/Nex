import type {
  EnginePhase,
  EngineTitleSource,
  EngineUsage,
  EngineModelState,
} from "../engine/types.js";

export type ProjectedRow =
  | TurnHeaderRowState
  | UserInputRowState
  | AssistantTextRowState
  | ReasoningRowState
  | ToolCallRowState;

interface RowBase {
  rowId: number;
  turnId: string;
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
  status: "inputStreaming" | "running" | "success" | "error" | "cancelled";
  inputText: string;
  input?: unknown;
  outputText?: string;
  errorMessage?: string;
  startedAt?: number;
  endedAt?: number;
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
    textRowId?: number;
    reasoningRowId?: number;
  } | null;
  toolRows: Record<string, number>;
  lastError: ProjectionError | null;
}

export interface CreateProjectionInput {
  sessionId: string;
  logEpoch: string;
  createdAt: number;
  title?: string;
  titleSource?: EngineTitleSource;
  model?: EngineModelState | null;
  phase?: EnginePhase;
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
    rows: [],
    nextRowId: 1,
    currentTurn: null,
    currentResponse: null,
    toolRows: {},
    lastError: null,
  };
}

export function bump(state: ProjectionState): ProjectionState {
  return { ...state, seq: state.seq + 1, revision: state.revision + 1 };
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
