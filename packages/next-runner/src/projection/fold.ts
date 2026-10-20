import type { EngineEvent, EngineStopReason } from "../engine/types.js";
import {
  applyApprovalRequest,
  applyApprovalSettled,
  clearPendingApprovals,
} from "./approvalEvents.js";
import {
  endToolCall as endToolCallEvent,
  startToolCall as startToolCallEvent,
  updateToolInput as updateToolInputEvent,
  updateToolOutput as updateToolOutputEvent,
} from "./toolEvents.js";
export { mapToolInput, mapToolName, toolDisplayName } from "./toolMapping.js";
import {
  addUsage,
  allocateRowId,
  bump,
  type ProjectedRow,
  type ProjectionState,
  type ReasoningRowState,
} from "./state.js";

export function applyUserInput(
  state: ProjectionState,
  text: string,
  options: { at: number; sourceCommandId: string },
): ProjectionState {
  const turnId = `turn-${state.seq + 1}`;
  const turnIdHeader = allocateRowId(state);
  const header: ProjectedRow = {
    rowId: turnIdHeader.id,
    turnId,
    kind: "turnHeader",
    state: "running",
    startedAt: options.at,
    createdAt: options.at,
    createdAtSeq: state.seq + 1,
  };
  const inputRowId = allocateRowId({ ...state, nextRowId: turnIdHeader.next });
  const inputRow: ProjectedRow = {
    rowId: inputRowId.id,
    turnId,
    kind: "userInput",
    text,
    sourceCommandId: options.sourceCommandId,
    createdAt: options.at,
    createdAtSeq: state.seq + 1,
  };
  return bump({
    ...state,
    rows: [...state.rows, header, inputRow],
    nextRowId: inputRowId.next,
    currentTurn: { turnId, headerRowId: turnIdHeader.id, startedAt: options.at },
    currentResponse: null,
    lastActivityAt: options.at,
    phase: "running",
    lastError: null,
  });
}

export function applyEngineEvent(state: ProjectionState, event: EngineEvent): ProjectionState {
  switch (event.type) {
    case "user_input":
      return applyUserInput(state, event.text, {
        at: event.at,
        sourceCommandId: "",
      });
    case "assistant_start":
      return startResponse(state, event.at, event.model);
    case "text_delta":
      return appendText(state, event.delta, event.at);
    case "thinking_delta":
      return appendThinking(state, event.delta, event.at);
    case "tool_call_start":
      return startToolCallEvent(state, event);
    case "tool_input_delta":
      return updateToolInputEvent(state, event);
    case "tool_call_update":
      return updateToolOutputEvent(state, event);
    case "tool_call_end":
      return endToolCallEvent(state, event);
    case "assistant_end":
      return endResponse(state, event);
    case "settled":
      return settle(state, event.at, event.aborted);
    case "phase":
      return bump({ ...state, phase: event.phase, lastActivityAt: event.at });
    case "mode_changed":
      return bump({
        ...state,
        mode: event.mode,
        planEnabled: event.planEnabled,
        lastActivityAt: event.at,
      });
    case "approval_request":
      return applyApprovalRequest(state, event);
    case "approval_settled":
      return applyApprovalSettled(state, event);
    case "error":
      return bump({
        ...state,
        phase: "error",
        lastActivityAt: event.at,
        lastError: {
          code: "runtime_error",
          message: event.message,
          at: event.at,
          recoverable: false,
          source: "runtime",
        },
      });
  }
}

function startResponse(state: ProjectionState, at: number, model?: string): ProjectionState {
  return bump({
    ...state,
    phase: "running",
    lastActivityAt: at,
    currentResponse: {
      responseId: `resp-${state.seq + 1}`,
      at,
      model,
    },
  });
}

type CurrentResponse = NonNullable<ProjectionState["currentResponse"]>;

function ensureResponse(state: ProjectionState, at: number, model?: string): CurrentResponse {
  return (
    state.currentResponse ?? {
      responseId: `resp-${state.seq + 1}`,
      at,
      ...(model ? { model } : {}),
    }
  );
}

function appendText(state: ProjectionState, delta: string, at: number): ProjectionState {
  if (!delta) return state;
  const response = ensureResponse(state, at);
  const existing = response.textRowId
    ? state.rows.find((row) => row.rowId === response.textRowId)
    : undefined;
  if (existing && existing.kind === "assistantText") {
    const rows = state.rows.map((row) =>
      row.rowId === existing.rowId && row.kind === "assistantText"
        ? { ...row, text: row.text + delta }
        : row,
    );
    return bump({ ...state, rows, currentResponse: response, lastActivityAt: at });
  }
  const allocated = allocateRowId(state);
  const row: ProjectedRow = {
    rowId: allocated.id,
    turnId: state.currentTurn?.turnId ?? "turn-1",
    kind: "assistantText",
    assistantResponseId: response.responseId,
    text: delta,
    state: "streaming",
    ...(response.model ? { model: response.model } : {}),
    createdAt: at,
    createdAtSeq: state.seq + 1,
  };
  return bump({
    ...state,
    rows: [...state.rows, row],
    nextRowId: allocated.next,
    currentResponse: { ...response, textRowId: allocated.id },
    phase: "running",
    lastActivityAt: at,
  });
}

function appendThinking(state: ProjectionState, delta: string, at: number): ProjectionState {
  if (!delta) return state;
  const response = ensureResponse(state, at);
  const existing = response.reasoningRowId
    ? state.rows.find((row) => row.rowId === response.reasoningRowId)
    : undefined;
  if (existing && existing.kind === "reasoning") {
    const rows = state.rows.map((row) =>
      row.rowId === existing.rowId && row.kind === "reasoning"
        ? { ...row, text: row.text + delta }
        : row,
    );
    return bump({ ...state, rows, currentResponse: response, lastActivityAt: at });
  }
  const allocated = allocateRowId(state);
  const row: ProjectedRow = {
    rowId: allocated.id,
    turnId: state.currentTurn?.turnId ?? "turn-1",
    kind: "reasoning",
    assistantResponseId: response.responseId,
    text: delta,
    state: "streaming",
    createdAt: at,
    createdAtSeq: state.seq + 1,
  };
  return bump({
    ...state,
    rows: [...state.rows, row],
    nextRowId: allocated.next,
    currentResponse: { ...response, reasoningRowId: allocated.id },
    phase: "running",
    lastActivityAt: at,
  });
}

function endResponse(
  state: ProjectionState,
  event: Extract<EngineEvent, { type: "assistant_end" }>,
): ProjectionState {
  const response = state.currentResponse;
  const rowState = assistantRowState(event.stopReason);
  const rows = state.rows.map((row) => {
    if (!response) return row;
    if (row.kind === "assistantText" && row.assistantResponseId === response.responseId) {
      return { ...row, state: rowState };
    }
    if (row.kind === "reasoning" && row.assistantResponseId === response.responseId) {
      const nextState: ReasoningRowState["state"] =
        event.stopReason === "aborted" ? "interrupted" : "complete";
      return { ...row, state: nextState };
    }
    return row;
  });
  const next: ProjectionState = {
    ...state,
    rows,
    currentResponse: null,
    usage: addUsage(state.usage, event.usage),
    lastActivityAt: event.at,
  };
  if (event.stopReason === "error") {
    return bump({
      ...next,
      phase: "error",
      lastError: {
        code: "provider_error",
        message: event.errorMessage ?? "Model request failed",
        at: event.at,
        recoverable: true,
        source: "provider",
      },
    });
  }
  if (event.stopReason === "aborted") {
    return bump({ ...next, phase: "completedInterrupted" });
  }
  return bump(next);
}

function assistantRowState(
  stopReason: EngineStopReason,
): "streaming" | "complete" | "interrupted" | "failed" {
  if (stopReason === "error") return "failed";
  if (stopReason === "aborted") return "interrupted";
  return "complete";
}

function settle(state: ProjectionState, at: number, aborted: boolean): ProjectionState {
  // Defensive: a run cannot end while a tool_call hook still awaits approval. If a settle
  // event was lost, close the cards here so the UI is never stuck on a dead interaction.
  const working = clearPendingApprovals(state, at);
  const phase = aborted
    ? "completedInterrupted"
    : working.phase === "error"
      ? "error"
      : "completedSuccess";
  const turn = working.currentTurn;
  const rows =
    turn === null
      ? working.rows
      : working.rows.map((row) => {
          if (row.rowId !== turn.headerRowId || row.kind !== "turnHeader") return row;
          return {
            ...row,
            state:
              phase === "error" ? "failed" : aborted ? "completedInterrupted" : "completedSuccess",
            endedAt: at,
          } as ProjectedRow;
        });
  const closedResponse = working.currentResponse === null ? null : working.currentResponse;
  const rowsWithClosedText =
    closedResponse === null
      ? rows
      : rows.map((row): ProjectedRow => {
          if (
            row.kind === "assistantText" &&
            row.assistantResponseId === closedResponse.responseId
          ) {
            return aborted ? { ...row, state: "interrupted" } : row;
          }
          if (row.kind === "reasoning" && row.assistantResponseId === closedResponse.responseId) {
            const nextState: ReasoningRowState["state"] = aborted ? "interrupted" : "complete";
            return { ...row, state: nextState };
          }
          return row;
        });
  return bump({
    ...working,
    rows: rowsWithClosedText,
    currentTurn: null,
    currentResponse: null,
    phase,
    lastActivityAt: at,
  });
}
