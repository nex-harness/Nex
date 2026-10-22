import type { EngineEvent } from "../engine/types.js";
import { toolEntityId } from "./entityIds.js";
import { fallbackTurnId } from "./messageIdentity.js";
import { jsonText, mapToolInput, toolDisplayName } from "./toolMapping.js";
import {
  allocateRowId,
  bump,
  touch,
  type ProjectedRow,
  type ProjectionState,
  type ToolCallRowState,
} from "./state.js";

type CurrentResponse = NonNullable<ProjectionState["currentResponse"]>;

/** Local copy: the live-response helper is only needed by streaming folds. */
function ensureResponse(state: ProjectionState, at: number, model?: string): CurrentResponse {
  return (
    state.currentResponse ?? {
      responseId: `resp-${state.seq + 1}`,
      at,
      ...(model ? { model } : {}),
      textSegments: 0,
      thinkingSegments: 0,
    }
  );
}

/** Registers a new tool row with the newest unfinished assistant message group. */
function withToolRowGroup(
  groups: ProjectionState["pendingMessageGroups"],
  rowId: number,
): ProjectionState["pendingMessageGroups"] {
  for (let index = groups.length - 1; index >= 0; index -= 1) {
    if (groups[index]?.kind !== "assistant") continue;
    return groups.map((item, itemIndex) =>
      itemIndex === index ? { ...item, rowIds: [...item.rowIds, rowId] } : item,
    );
  }
  return groups;
}

export function startToolCall(
  state: ProjectionState,
  event: Extract<EngineEvent, { type: "tool_call_start" }>,
): ProjectionState {
  const response = ensureResponse(state, event.at);
  const input = mapToolInput(event.toolName, event.input);
  // 同一 toolCallId 会到两条 start：pi 先发 assistant 流里的 `toolcall_start`（partial
  // arguments 可能尚未完整），再发 `tool_execution_start`（最终解析后的 args）。
  // 之前每次 start 都新建 row，卡片因此重复；这里把第二次 start 视为对已有行的
  // 输入补全，rowId/位置不变，只更新 toolName/input。
  const existingRowId = state.toolRows[event.toolCallId];
  if (existingRowId !== undefined) {
    const rows = state.rows.map((row) =>
      row.rowId === existingRowId && row.kind === "toolCall"
        ? {
            ...row,
            toolName: toolDisplayName(event.toolName),
            input,
            inputText: jsonText(input),
            // A late duplicate start must not drop an approval that is already on screen.
            ...(row.status === "pendingApproval" ? {} : { status: "running" as const }),
          }
        : row,
    );
    return bump({ ...state, rows, currentResponse: response, lastActivityAt: event.at });
  }
  const allocated = allocateRowId(state);
  const row: ToolCallRowState = {
    rowId: allocated.id,
    turnId: state.currentTurn?.turnId ?? fallbackTurnId(state),
    kind: "toolCall",
    assistantResponseId: response.responseId,
    toolCallId: event.toolCallId,
    toolName: toolDisplayName(event.toolName),
    status: "running",
    inputText: jsonText(input),
    input,
    // toolCallId is pi-stable and unique, so the identity never depends on the message.
    entityId: toolEntityId(event.toolCallId),
    startedAt: event.at,
    createdAt: event.at,
    createdAtSeq: state.seq + 1,
  };
  return bump({
    ...state,
    rows: [...state.rows, row] as ProjectedRow[],
    nextRowId: allocated.next,
    toolRows: { ...state.toolRows, [event.toolCallId]: allocated.id },
    // A tool call ends the current text segment; later text becomes a new row
    // (rows.ts comment on assistantText: appends only continue the open streaming row).
    currentResponse: {
      responseId: response.responseId,
      at: response.at,
      ...(response.model ? { model: response.model } : {}),
      textSegments: response.textSegments,
      thinkingSegments: response.thinkingSegments,
    },
    pendingMessageGroups: withToolRowGroup(state.pendingMessageGroups, allocated.id),
    phase: "running",
    lastActivityAt: event.at,
  });
}

export function updateToolInput(
  state: ProjectionState,
  event: Extract<EngineEvent, { type: "tool_input_delta" }>,
): ProjectionState {
  const rowId = state.toolRows[event.toolCallId];
  if (rowId === undefined) return state;
  const rows = state.rows.map((row) =>
    row.rowId === rowId && row.kind === "toolCall"
      ? { ...row, inputText: row.inputText + event.delta }
      : row,
  );
  return touch({ ...state, rows, lastActivityAt: event.at });
}

export function updateToolOutput(
  state: ProjectionState,
  event: Extract<EngineEvent, { type: "tool_call_update" }>,
): ProjectionState {
  const rowId = state.toolRows[event.toolCallId];
  if (rowId === undefined) return state;
  const rows = state.rows.map((row) =>
    row.rowId === rowId && row.kind === "toolCall" && event.text !== undefined
      ? { ...row, outputText: event.text }
      : row,
  );
  return touch({ ...state, rows, lastActivityAt: event.at });
}

export function endToolCall(
  state: ProjectionState,
  event: Extract<EngineEvent, { type: "tool_call_end" }>,
): ProjectionState {
  let rowId = state.toolRows[event.toolCallId];
  let working = state;
  if (rowId === undefined) {
    working = startToolCall(working, {
      type: "tool_call_start",
      at: event.at,
      toolCallId: event.toolCallId,
      toolName: event.toolName,
      input: event.input,
    });
    rowId = working.toolRows[event.toolCallId];
  }
  const input = event.input !== undefined ? mapToolInput(event.toolName, event.input) : undefined;
  const rows = working.rows.map((row) => {
    if (row.rowId !== rowId || row.kind !== "toolCall") return row;
    // A blocked (denied/aborted) call also produces a tool result with isError=true; the
    // row already carries the authoritative `cancelled` status, so the error must not
    // overwrite it.
    if (row.status === "cancelled") {
      return { ...row, outputText: event.text || row.outputText, endedAt: event.at };
    }
    const updated: ToolCallRowState = {
      ...row,
      status: event.isError ? "error" : "success",
      outputText: event.text,
      endedAt: event.at,
      ...(input !== undefined
        ? { input, inputText: jsonText(input), toolName: toolDisplayName(event.toolName) }
        : {}),
      ...(event.isError ? { errorMessage: event.text } : {}),
    };
    return updated;
  });
  return bump({
    ...working,
    rows,
    phase: working.phase === "error" ? "error" : "running",
    lastActivityAt: event.at,
  });
}
