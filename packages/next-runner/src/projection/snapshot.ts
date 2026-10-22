import type {
  ActiveWorkSummary,
  ConversationSnapshot,
  ModelSelection,
  PendingInteraction,
  PermissionRequestPayload,
  RowActions,
} from "@nex/shared/nex-protocol-v4";
import type { EnginePhase } from "../engine/types.js";
import type {
  AssistantTextRowState,
  PendingApprovalState,
  ProjectedRow,
  ProjectionState,
  ReasoningRowState,
  TimelineMarkerRowState,
  ToolCallRowState,
  TurnHeaderRowState,
  UserInputRowState,
} from "./state.js";
import { computeAvailability, computeInputRouting } from "./availability.js";
import { deriveRowActions } from "./rowActions.js";
import { mapToolInput, toolDisplayName } from "./toolMapping.js";

const TAIL_WINDOW = 200;

export interface SnapshotOptions {
  /** Optional model selection echoed into config.modelSelection (unused by MVP UI). */
  selection?: ModelSelection;
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
  // Action affordances are derived per snapshot from the whole row set (never stored), so
  // the "newest row" rules stay correct as rows stream in and after a restore.
  const actions = deriveRowActions(state);
  const windowRows = state.rows
    .slice(-TAIL_WINDOW)
    .map((row) => toSnapshotRow(row, state, actions.get(row.rowId)));
  const firstRowId = windowRows.length > 0 ? (windowRows[0]?.rowId ?? null) : null;
  const phase = state.phase;
  const running = phase === "running" || phase === "prewarming";
  const compacting = Object.keys(state.compactOperations).length > 0;
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
    followupMode: state.followupMode,
    // Echo the session's permission mode so the composer seeds itself after reopen; the
    // gate stays the authority, this is projection only.
    mode: state.mode,
    planEnabled: state.planEnabled,
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
      // Stop also cancels a compaction (pi's abort does both), so an idle-but-compacting
      // session still offers it — the old CLI's `cancelManualCompact` path does the same.
      canStop: running || compacting,
      stopState: running ? "stoppable" : "idle",
      stopTargetKind: running ? "assistant" : compacting ? "compact" : "unknown",
      // A compaction runs over the session even when idle; during a turn it is extra work
      // alongside the primary turn (the old CLI pushes `compact` as its own entry).
      activeWorks: [...(running ? primaryWork(state) : []), ...compactWork(state)],
      lastError: state.lastError
        ? {
            code: state.lastError.code,
            message: state.lastError.message,
            recoverable: state.lastError.recoverable,
            at: state.lastError.at,
            source: state.lastError.source,
          }
        : null,
      apiRetry: state.apiRetry ? { ...state.apiRetry } : null,
    },
    availability: computeAvailability(state),
    inputRouting: computeInputRouting(state),
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
    queue: state.queue,
    pendingInteractions: toPendingInteractions(state),
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

/**
 * Projects live approvals into the UI contract. `config.permissionGrant` is deliberately
 * never set: the UI would flip the composer to yolo for the rest of the draft.
 */
function toPendingInteractions(state: ProjectionState): PendingInteraction[] {
  return Object.values(state.pendingApprovals).map((approval) => ({
    interactionId: approval.interactionId,
    kind: "permission" as const,
    anchorRowId: state.toolRows[approval.toolCallId] ?? null,
    createdAt: approval.createdAt,
    payload: permissionPayload(approval),
  }));
}

function permissionPayload(approval: PendingApprovalState): PermissionRequestPayload {
  return {
    kind: "permission",
    toolCallId: approval.toolCallId,
    toolName: toolDisplayName(approval.toolName),
    summary: approval.summary,
    detail: mapToolInput(approval.toolName, approval.detail),
    freeText: true,
    options: [
      {
        optionId: "allowOnce",
        label: "Allow",
        kind: "allowOnce",
        response: { decision: "allow" },
      },
      {
        optionId: "allowAlways",
        label: "Always allow in this session",
        kind: "allowAlways",
        response: { decision: "allow" },
      },
      {
        optionId: "deny",
        label: "Deny",
        kind: "deny",
        response: { decision: "deny" },
      },
    ],
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

/**
 * An idle session with compaction in flight has no run work but is not settled: the UI's
 * loading shimmer binds `activeWorks.kind === "compact"` (research §1).
 */
function compactWork(state: ProjectionState): ActiveWorkSummary[] {
  const running = Object.values(state.compactOperations);
  if (running.length === 0) return [];
  const startedAt = Math.min(
    ...running.map((operation) => {
      const row = state.rows.find((item) => item.rowId === operation.rowId);
      return row?.createdAt ?? state.lastActivityAt;
    }),
  );
  return running.map((operation) => ({
    kind: "compact" as const,
    foregroundExecutionId: operation.operationId,
    startedAt,
  }));
}

function isEnded(phase: EnginePhase): boolean {
  return phase === "completedSuccess" || phase === "completedInterrupted" || phase === "error";
}

function toSnapshotRow(
  row: ProjectedRow,
  state: ProjectionState,
  actions: RowActions | undefined,
): ConversationSnapshot["rows"]["window"][number] {
  const identity = rowIdentity(row, actions);
  switch (row.kind) {
    case "turnHeader":
      return turnHeaderRow(row, identity);
    case "userInput":
      return userInputRow(row, identity);
    case "assistantText":
      return assistantTextRow(row, identity);
    case "reasoning":
      return reasoningRow(row, identity);
    case "toolCall":
      return toolCallRow(row, state, identity);
    case "timelineMarker":
      return timelineMarkerRow(row, identity);
  }
}

/**
 * Row identity every row carries (docs/next/conversation-features.md decision 1):
 * `entityId` when known, `productTurnId = turnId`, and `visibility:"visible"`.
 * `actions` is only added when non-empty; the schema forbids `false` values.
 */
interface RowIdentity {
  entityId?: string;
  productTurnId: string;
  visibility: "visible";
  actions?: RowActions;
}

function rowIdentity(row: ProjectedRow, actions: RowActions | undefined): RowIdentity {
  return {
    ...(row.entityId ? { entityId: row.entityId } : {}),
    productTurnId: row.turnId,
    visibility: "visible",
    ...(actions ? { actions } : {}),
  };
}

function timelineMarkerRow(
  row: TimelineMarkerRowState,
  identity: RowIdentity,
): ConversationSnapshot["rows"]["window"][number] {
  return {
    rowId: row.rowId,
    turnId: row.turnId,
    ...identity,
    kind: "timelineMarker",
    ...(row.lane ? { lane: row.lane } : {}),
    ...(row.sourceCommandId ? { sourceCommandId: row.sourceCommandId } : {}),
    marker: row.marker,
    createdAt: row.createdAt,
    createdAtSeq: row.createdAtSeq,
  };
}

function turnHeaderRow(
  row: TurnHeaderRowState,
  identity: RowIdentity,
): ConversationSnapshot["rows"]["window"][number] {
  return {
    rowId: row.rowId,
    turnId: row.turnId,
    ...identity,
    kind: "turnHeader",
    origin: "userInput",
    executionKind: "agent",
    ...(row.sourceCommandId ? { sourceCommandId: row.sourceCommandId } : {}),
    state: row.state,
    startedAt: row.startedAt,
    ...(row.endedAt !== undefined ? { endedAt: row.endedAt } : {}),
    ...(row.fileChanges ? { fileChanges: row.fileChanges } : {}),
    createdAt: row.createdAt,
    createdAtSeq: row.createdAtSeq,
  };
}

function userInputRow(
  row: UserInputRowState,
  identity: RowIdentity,
): ConversationSnapshot["rows"]["window"][number] {
  return {
    rowId: row.rowId,
    turnId: row.turnId,
    ...identity,
    kind: "userInput",
    text: row.text,
    origin: "realUser",
    ...(row.attachments && row.attachments.length > 0 ? { attachments: row.attachments } : {}),
    ...(row.sourceCommandId ? { sourceCommandId: row.sourceCommandId } : {}),
    createdAt: row.createdAt,
    createdAtSeq: row.createdAtSeq,
  };
}

function assistantTextRow(
  row: AssistantTextRowState,
  identity: RowIdentity,
): ConversationSnapshot["rows"]["window"][number] {
  return {
    rowId: row.rowId,
    turnId: row.turnId,
    ...identity,
    kind: "assistantText",
    assistantResponseId: row.assistantResponseId,
    text: row.text,
    state: row.state,
    ...(row.model ? { model: row.model } : {}),
    createdAt: row.createdAt,
    createdAtSeq: row.createdAtSeq,
  };
}

function reasoningRow(
  row: ReasoningRowState,
  identity: RowIdentity,
): ConversationSnapshot["rows"]["window"][number] {
  return {
    rowId: row.rowId,
    turnId: row.turnId,
    ...identity,
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
  identity: RowIdentity,
): ConversationSnapshot["rows"]["window"][number] {
  const outputText = row.outputText ?? "";
  const base = {
    rowId: row.rowId,
    turnId: row.turnId,
    ...identity,
    kind: "toolCall" as const,
    ...(row.assistantResponseId ? { assistantResponseId: row.assistantResponseId } : {}),
    toolCallId: row.toolCallId,
    toolName: row.toolName,
    status: row.status,
    inputText: row.inputText,
    ...(row.input !== undefined ? { input: row.input } : {}),
    ...(row.outputText !== undefined || row.display !== undefined
      ? {
          output: {
            text: outputText,
            ...(row.display ? { display: row.display } : {}),
          },
        }
      : {}),
    ...(row.outputText !== undefined ? { outputPreview: buildOutputPreview(outputText) } : {}),
    ...(row.status === "error"
      ? { error: { code: "tool_error", message: row.errorMessage ?? outputText } }
      : {}),
    ...(row.status === "pendingApproval" && row.approvalInteractionId
      ? { approvalInteractionId: row.approvalInteractionId }
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
