import type { EngineEvent, EngineStopReason } from "../engine/types.js";
import {
  applyApprovalRequest,
  applyApprovalSettled,
  clearPendingApprovals,
} from "./approvalEvents.js";
import { turnEntityId, userEntityId } from "./entityIds.js";
import { addRowToAssistantGroup, attachEntryId, fallbackTurnId } from "./messageIdentity.js";
import {
  endToolCall as endToolCallEvent,
  startToolCall as startToolCallEvent,
  updateToolInput as updateToolInputEvent,
  updateToolOutput as updateToolOutputEvent,
} from "./toolEvents.js";
export { mapToolInput, mapToolName, toolDisplayName } from "./toolMapping.js";
import { appendTimelineMarker } from "./markerEvents.js";
import { applyFollowupModeChanged, applyModelChanged, applyTitleChanged } from "./configEvents.js";
import {
  addUsage,
  allocateRowId,
  bump,
  touch,
  type ProjectedRow,
  type ProjectionState,
  type ReasoningRowState,
} from "./state.js";

function countTurns(state: ProjectionState): number {
  let count = 0;
  for (const row of state.rows) if (row.kind === "turnHeader") count += 1;
  return count;
}

export function applyUserInput(
  state: ProjectionState,
  text: string,
  options: {
    at: number;
    sourceCommandId: string;
    /** pi session entry id when already known (`message_entry` arrived first). */
    entryId?: string;
  },
): ProjectionState {
  // Turn numbering must match `projectHistory`'s (`turn-<n>` by turnHeader ordinal), not
  // `seq`: after a restore the two paths would otherwise disagree and a session would carry
  // two numbering schemes, which the UI uses as its render-unit key (productTurnId).
  const turnId = `turn-${countTurns(state) + 1}`;
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
  const withEntryId = options.entryId
    ? {
        header: { ...header, entityId: turnEntityId(options.entryId, 0) },
        input: { ...inputRow, entityId: userEntityId(options.entryId) },
      }
    : { header, input: inputRow };
  // A user message's persisted entry id arrives after `message_end` (see
  // `EngineEvent.message_entry`); remember the rows that identity belongs to.
  const groups = options.entryId
    ? state.pendingMessageGroups
    : [
        ...state.pendingMessageGroups,
        {
          kind: "user" as const,
          rowIds: [inputRow.rowId],
          headerRowId: header.rowId,
        },
      ];
  return bump({
    ...state,
    rows: [...state.rows, withEntryId.header, withEntryId.input],
    nextRowId: inputRowId.next,
    currentTurn: { turnId, headerRowId: turnIdHeader.id, startedAt: options.at },
    currentResponse: null,
    pendingMessageGroups: groups,
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
    case "message_entry":
      return attachEntryId(state, event.entryId, event.role);
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
    case "title_changed":
      return applyTitleChanged(state, event);
    case "model_changed":
      return applyModelChanged(state, event);
    case "model_change_marker":
      return appendTimelineMarker(state, event);
    case "followup_mode_changed":
      return applyFollowupModeChanged(state, event);
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
  const responseId = `resp-${state.seq + 1}`;
  return bump({
    ...state,
    phase: "running",
    lastActivityAt: at,
    currentResponse: {
      responseId,
      at,
      ...(model ? { model } : {}),
      textSegments: 0,
      thinkingSegments: 0,
    },
    pendingMessageGroups: [...state.pendingMessageGroups, { kind: "assistant", rowIds: [] }],
  });
}

type CurrentResponse = NonNullable<ProjectionState["currentResponse"]>;

function ensureResponse(state: ProjectionState, at: number, model?: string): CurrentResponse {
  const response =
    state.currentResponse ??
    ({
      responseId: `resp-${state.seq + 1}`,
      at,
      ...(model ? { model } : {}),
      textSegments: 0,
      thinkingSegments: 0,
    } satisfies CurrentResponse);
  return response;
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
    // Append-only text growth is `row.delta` in the old CLI (`delta.ts:100`), which is
    // deliberately NOT revision-bearing (`deltaBumpsRevision`): bumping here would make
    // every CAS command stale for as long as the model streams.
    return touch({ ...state, rows, currentResponse: response, lastActivityAt: at });
  }
  const allocated = allocateRowId(state);
  const segmentIndex = response.textSegments;
  const row: ProjectedRow = {
    rowId: allocated.id,
    turnId: state.currentTurn?.turnId ?? fallbackTurnId(state),
    kind: "assistantText",
    assistantResponseId: response.responseId,
    text: delta,
    state: "streaming",
    ...(response.model ? { model: response.model } : {}),
    segmentIndex,
    createdAt: at,
    createdAtSeq: state.seq + 1,
  };
  return bump({
    ...state,
    rows: [...state.rows, row],
    nextRowId: allocated.next,
    currentResponse: {
      ...response,
      textRowId: allocated.id,
      textSegments: segmentIndex + 1,
    },
    pendingMessageGroups: addRowToAssistantGroup(state.pendingMessageGroups, allocated.id),
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
    return touch({ ...state, rows, currentResponse: response, lastActivityAt: at });
  }
  const allocated = allocateRowId(state);
  const segmentIndex = response.thinkingSegments;
  const row: ProjectedRow = {
    rowId: allocated.id,
    turnId: state.currentTurn?.turnId ?? fallbackTurnId(state),
    kind: "reasoning",
    assistantResponseId: response.responseId,
    text: delta,
    state: "streaming",
    segmentIndex,
    createdAt: at,
    createdAtSeq: state.seq + 1,
  };
  return bump({
    ...state,
    rows: [...state.rows, row],
    nextRowId: allocated.next,
    currentResponse: {
      ...response,
      reasoningRowId: allocated.id,
      thinkingSegments: segmentIndex + 1,
    },
    pendingMessageGroups: addRowToAssistantGroup(state.pendingMessageGroups, allocated.id),
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
  // The assistant message is now complete; its entry id arrives with the next
  // `message_entry`. Keep the row group queued and reset the segment cursor.
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
