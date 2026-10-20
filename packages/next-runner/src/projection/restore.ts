import type { EngineAssistantPart, EngineMessage, EngineSessionState } from "../engine/types.js";
import { mapToolInput, toolDisplayName } from "./fold.js";
import {
  jsonText,
  type ProjectedRow,
  type ProjectionState,
  type ToolCallRowState,
} from "./state.js";
import { createProjection } from "./state.js";

/**
 * Rebuilds the v4 row projection from a pi transcript. Same fold rules as the live path:
 * one turn per user message, assistant parts keep their source order, tool results attach
 * to the toolCall rows they answer.
 */
export function projectHistory(
  session: EngineSessionState,
  messages: EngineMessage[],
  logEpoch: string,
): ProjectionState {
  const base = createProjection({
    sessionId: session.sessionId,
    logEpoch,
    createdAt: session.createdAt,
    title: session.title,
    titleSource: session.titleSource,
    model: session.model,
    phase: session.phase,
  });
  const state: ProjectionState = {
    ...base,
    usage: session.usage,
    lastActivityAt: session.lastActivityAt,
  };
  const rows: ProjectedRow[] = [];
  const toolRowByCallId = new Map<string, number>();
  let nextRowId = 1;
  let turnIndex = 0;
  let turnId = "";
  let headerRowId: number | null = null;
  let lastAt = session.createdAt;

  const allocate = (): number => {
    const id = nextRowId;
    nextRowId += 1;
    return id;
  };

  const ensureTurn = (at: number): void => {
    if (headerRowId !== null) return;
    turnIndex += 1;
    turnId = `turn-${turnIndex}`;
    const rowId = allocate();
    headerRowId = rowId;
    rows.push({
      rowId,
      turnId,
      kind: "turnHeader",
      state: "running",
      startedAt: at,
      createdAt: at,
      createdAtSeq: rowId,
    });
  };

  const closeTurn = (at: number, final = false): void => {
    if (headerRowId === null) return;
    const index = rows.findIndex((row) => row.rowId === headerRowId);
    const header = rows[index];
    if (header && header.kind === "turnHeader") {
      rows[index] = {
        ...header,
        state: final ? turnHeaderState(state.phase) : "completedSuccess",
        endedAt: Math.max(at, header.startedAt),
      };
    }
    headerRowId = null;
  };

  for (const message of messages) {
    if (message.role === "user") {
      closeTurn(lastAt);
      ensureTurn(message.at);
      const rowId = allocate();
      rows.push({
        rowId,
        turnId,
        kind: "userInput",
        text: message.text,
        sourceCommandId: "",
        createdAt: message.at,
        createdAtSeq: rowId,
      });
      lastAt = message.at;
      continue;
    }

    if (message.role === "assistant") {
      ensureTurn(message.at);
      const responseId = `resp-hist-${message.at}-${allocate()}`;
      for (const part of message.parts) appendPart(part, message.at, responseId);
      lastAt = message.at;
      continue;
    }

    const toolRowId = toolRowByCallId.get(message.toolCallId);
    if (toolRowId === undefined) {
      ensureTurn(message.at);
      const rowId = allocate();
      rows.push({
        rowId,
        turnId,
        kind: "toolCall",
        toolCallId: message.toolCallId,
        toolName: toolDisplayName(message.toolName),
        status: message.isError ? "error" : "success",
        inputText: "",
        outputText: message.text,
        createdAt: message.at,
        createdAtSeq: rowId,
        endedAt: message.at,
        ...(message.isError ? { errorMessage: message.text } : {}),
      });
    } else {
      const index = rows.findIndex((row) => row.rowId === toolRowId);
      const row = rows[index];
      if (row && row.kind === "toolCall") {
        rows[index] = {
          ...row,
          status: message.isError ? "error" : "success",
          outputText: message.text,
          endedAt: message.at,
          ...(message.isError ? { errorMessage: message.text } : {}),
        };
      }
    }
    lastAt = message.at;
  }
  closeTurn(lastAt, true);

  return {
    ...state,
    rows,
    nextRowId,
    phase: state.phase,
    lastActivityAt: Math.max(state.lastActivityAt, lastAt),
  };

  function appendPart(part: EngineAssistantPart, at: number, responseId: string): void {
    if (part.type === "text") {
      if (!part.text) return;
      const rowId = allocate();
      rows.push({
        rowId,
        turnId,
        kind: "assistantText",
        assistantResponseId: responseId,
        text: part.text,
        state: "complete",
        createdAt: at,
        createdAtSeq: rowId,
      });
      return;
    }
    if (part.type === "thinking") {
      if (!part.text) return;
      const rowId = allocate();
      rows.push({
        rowId,
        turnId,
        kind: "reasoning",
        assistantResponseId: responseId,
        text: part.text,
        state: "complete",
        createdAt: at,
        createdAtSeq: rowId,
      });
      return;
    }
    const rowId = allocate();
    const input = mapToolInput(part.name, part.arguments);
    const toolRow: ToolCallRowState = {
      rowId,
      turnId,
      kind: "toolCall",
      assistantResponseId: responseId,
      toolCallId: part.id,
      toolName: toolDisplayName(part.name),
      status: "success",
      inputText: jsonText(input),
      input,
      startedAt: at,
      createdAt: at,
      createdAtSeq: rowId,
    };
    rows.push(toolRow);
    toolRowByCallId.set(part.id, rowId);
  }
}

function turnHeaderState(
  phase: ProjectionState["phase"],
): "running" | "completedSuccess" | "completedInterrupted" | "failed" {
  switch (phase) {
    case "error":
      return "failed";
    case "completedInterrupted":
      return "completedInterrupted";
    case "running":
    case "prewarming":
    case "draft":
      return "running";
    default:
      return "completedSuccess";
  }
}
