import type {
  ActiveWorkSummary,
  ConversationSnapshot,
  ModelSelection,
} from "@nex/shared/nex-protocol-v4";
import type { EnginePhase } from "../engine/types.js";
import type {
  AssistantTextRowState,
  ProjectedRow,
  ProjectionState,
  ReasoningRowState,
  ToolCallRowState,
  TurnHeaderRowState,
  UserInputRowState,
} from "./state.js";

const TAIL_WINDOW = 200;

export interface SnapshotOptions {
  /** Optional model selection echoed into config.modelSelection (unused by MVP UI). */
  selection?: ModelSelection;
  mode?: string;
}

/**
 * Full v4 ConversationSnapshot. Every change is published as a full snapshot in MVP, so
 * this is the only shape the publisher needs. Rows are the whole log (capped at a tail
 * window; MVP sessions stay small) and `firstRowId` equals the window head, so the UI
 * never attempts an older-page fetch it cannot satisfy.
 */
export function buildConversationSnapshot(
  state: ProjectionState,
  options: SnapshotOptions = {},
): ConversationSnapshot {
  const windowRows = state.rows.slice(-TAIL_WINDOW).map((row) => toSnapshotRow(row, state));
  const firstRowId = windowRows.length > 0 ? (windowRows[0]?.rowId ?? null) : null;
  const phase = state.phase;
  const running = phase === "running" || phase === "prewarming";
  const thoughtLevels =
    state.model && state.model.thoughtLevels.length > 0 ? state.model.thoughtLevels : ["off"];
  const thought = state.model?.thought ?? "off";
  const normalizedThought = thoughtLevels.includes(thought) ? thought : (thoughtLevels[0] ?? "off");
  // 修复依据：打开已有会话时 UI 用 `snapshot.config.modelSelection` 初始化 Composer
  // （useDraftConfigControl 的 sessionConfig 分支）。只给 provider/model 平铺字段时
  // Composer 没有结构化选择，会显示 “Choose model” 并禁止发送。
  const modelSelection: ModelSelection | undefined =
    options.selection ??
    (state.model
      ? {
          providerId: state.model.providerId,
          modelId: state.model.modelId,
          options: { reasoningLevel: normalizedThought },
        }
      : undefined);
  const config = {
    provider: state.model?.providerId ?? "",
    model: state.model?.modelId ?? "",
    thought: normalizedThought,
    thoughtLevels,
    followupMode: "queue" as const,
    mode: options.mode ?? "build",
    ...(modelSelection ? { modelSelection } : {}),
  };
  return {
    protocolVersion: 1,
    sessionId: state.sessionId,
    logEpoch: state.logEpoch,
    seq: state.seq,
    revision: state.revision,
    control: {
      phase,
      sessionEnded: isEnded(phase),
      canStop: running,
      stopState: running ? "stoppable" : "idle",
      stopTargetKind: running ? "assistant" : "unknown",
      activeWorks: running ? primaryWork(state) : [],
      lastError: state.lastError
        ? {
            code: state.lastError.code,
            message: state.lastError.message,
            recoverable: state.lastError.recoverable,
            at: state.lastError.at,
            source: state.lastError.source,
          }
        : null,
      apiRetry: null,
    },
    availability: {
      fork: { allowed: true },
      compact: { allowed: true },
      switchModelConfig: { allowed: true },
      setFollowupMode: { allowed: true },
      queueEdit: { allowed: true },
      sendQueuedNow: { allowed: true },
      pauseGoal: { allowed: true },
      resumeGoal: { allowed: true },
    },
    inputRouting: { mode: "startNow" },
    meta: { title: state.title, titleSource: state.titleSource },
    config,
    modelTransition: null,
    usage: {
      contextWindow: null,
      cumulative: {
        inputTokens: state.usage.input,
        outputTokens: state.usage.output,
        cacheReadTokens: state.usage.cacheRead,
        cacheWriteTokens: state.usage.cacheWrite,
      },
    },
    queue: { items: [], autoDrain: true },
    pendingInteractions: [],
    pendingCommands: [],
    backgroundWorks: [],
    goal: null,
    plan: null,
    workspaceHookAdmission: null,
    rows: {
      window: windowRows,
      totalCount: state.rows.length,
      firstRowId,
    },
  };
}

function primaryWork(state: ProjectionState): ActiveWorkSummary[] {
  const summary: ActiveWorkSummary = {
    kind: "primaryTurn",
    startedAt: state.currentTurn?.startedAt ?? state.lastActivityAt,
    ...(state.currentTurn ? { foregroundExecutionId: state.currentTurn.turnId } : {}),
  };
  return [summary];
}

function isEnded(phase: EnginePhase): boolean {
  return phase === "completedSuccess" || phase === "completedInterrupted" || phase === "error";
}

function toSnapshotRow(
  row: ProjectedRow,
  state: ProjectionState,
): ConversationSnapshot["rows"]["window"][number] {
  switch (row.kind) {
    case "turnHeader":
      return turnHeaderRow(row);
    case "userInput":
      return userInputRow(row);
    case "assistantText":
      return assistantTextRow(row);
    case "reasoning":
      return reasoningRow(row);
    case "toolCall":
      return toolCallRow(row, state);
  }
}

function turnHeaderRow(row: TurnHeaderRowState): ConversationSnapshot["rows"]["window"][number] {
  return {
    rowId: row.rowId,
    turnId: row.turnId,
    kind: "turnHeader",
    origin: "userInput",
    executionKind: "agent",
    state: row.state,
    startedAt: row.startedAt,
    ...(row.endedAt !== undefined ? { endedAt: row.endedAt } : {}),
    createdAt: row.createdAt,
    createdAtSeq: row.createdAtSeq,
  };
}

function userInputRow(row: UserInputRowState): ConversationSnapshot["rows"]["window"][number] {
  return {
    rowId: row.rowId,
    turnId: row.turnId,
    kind: "userInput",
    text: row.text,
    origin: "realUser",
    ...(row.sourceCommandId ? { sourceCommandId: row.sourceCommandId } : {}),
    createdAt: row.createdAt,
    createdAtSeq: row.createdAtSeq,
  };
}

function assistantTextRow(
  row: AssistantTextRowState,
): ConversationSnapshot["rows"]["window"][number] {
  return {
    rowId: row.rowId,
    turnId: row.turnId,
    kind: "assistantText",
    assistantResponseId: row.assistantResponseId,
    text: row.text,
    state: row.state,
    ...(row.model ? { model: row.model } : {}),
    createdAt: row.createdAt,
    createdAtSeq: row.createdAtSeq,
  };
}

function reasoningRow(row: ReasoningRowState): ConversationSnapshot["rows"]["window"][number] {
  return {
    rowId: row.rowId,
    turnId: row.turnId,
    kind: "reasoning",
    assistantResponseId: row.assistantResponseId,
    text: row.text,
    state: row.state,
    createdAt: row.createdAt,
    createdAtSeq: row.createdAtSeq,
  };
}

function toolCallRow(
  row: ToolCallRowState,
  _state: ProjectionState,
): ConversationSnapshot["rows"]["window"][number] {
  const outputText = row.outputText ?? "";
  const base = {
    rowId: row.rowId,
    turnId: row.turnId,
    kind: "toolCall" as const,
    ...(row.assistantResponseId ? { assistantResponseId: row.assistantResponseId } : {}),
    toolCallId: row.toolCallId,
    toolName: row.toolName,
    status: row.status,
    inputText: row.inputText,
    ...(row.input !== undefined ? { input: row.input } : {}),
    ...(row.outputText !== undefined ? { output: { text: outputText } } : {}),
    ...(row.outputText !== undefined ? { outputPreview: buildOutputPreview(outputText) } : {}),
    ...(row.status === "error"
      ? { error: { code: "tool_error", message: row.errorMessage ?? outputText } }
      : {}),
    ...(row.startedAt !== undefined ? { startedAt: row.startedAt } : {}),
    ...(row.endedAt !== undefined ? { endedAt: row.endedAt } : {}),
    createdAt: row.createdAt,
    createdAtSeq: row.createdAtSeq,
  };
  return base;
}

function buildOutputPreview(text: string) {
  const normalized = text.replace(/\n$/u, "");
  const totalBytes = Buffer.byteLength(text, "utf8");
  const totalLines = normalized.length === 0 ? 0 : normalized.split("\n").length;
  const truncated = normalized.length > 4096;
  const clipped = truncated ? normalized.slice(0, 4096) : normalized;
  return {
    text: clipped,
    fullText: clipped,
    totalLines,
    totalBytes,
    linesEstimated: false,
  };
}
