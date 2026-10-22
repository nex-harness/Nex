import type { EngineAssistantPart, EngineMessage, EngineSessionState } from "../engine/types.js";
import {
  assistantPartEntityId,
  compactionEntityId,
  toolEntityId,
  turnEntityId,
  userEntityId,
} from "./entityIds.js";
import { mapToolInput, toolDisplayName } from "./fold.js";
import {
  jsonText,
  type ProjectedRow,
  type ProjectionState,
  type TimelineMarker,
  type ToolCallRowState,
} from "./state.js";
import { createProjection } from "./state.js";

/**
 * Rebuilds the v4 row projection from pi's raw branch (`Engine.history` →
 * `SessionManager.getBranch()`).
 *
 * Same fold rules as the live path, plus the two things only restore can produce:
 * - row identity (`entityId: pi:<entryId>`) comes from the persisted entry ids — exactly
 *   what the live projection learned from `message_entry`, so action targets survive a
 *   runner restart;
 * - a compaction entry becomes a `timelineMarker` compact row, so pre-compaction turns stay
 *   visible after a restart (research §0-b).
 *
 * Segment numbering mirrors the live fold: each text/thinking part is its own row, numbered
 * inside one assistant message, so `pi:<entry>:text:<n>` matches both paths.
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
    mode: session.mode ?? "build",
    planEnabled: session.planEnabled ?? false,
    followupMode: session.followupMode ?? "queue",
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
  let lastTurnId: string | undefined;
  let lastAt = session.createdAt;

  const allocate = (): number => {
    const id = nextRowId;
    nextRowId += 1;
    return id;
  };

  const ensureTurn = (at: number, entryId?: string): void => {
    if (headerRowId !== null) return;
    turnIndex += 1;
    turnId = `turn-${turnIndex}`;
    lastTurnId = turnId;
    const rowId = allocate();
    headerRowId = rowId;
    rows.push({
      rowId,
      turnId,
      kind: "turnHeader",
      // A user entry owns its turn's identity; a turn without one falls back to ordinal.
      entityId: turnEntityId(entryId, turnIndex),
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
    if (message.role === "compaction") {
      // The boundary sits between turns: close the current one, then place the marker in
      // that turn's assistantWork lane (the UI folds it into "already worked"), matching
      // the live model-change marker. Only a file that starts with a compaction gets a
      // standalone unit.
      closeTurn(lastAt);
      const rowId = allocate();
      rows.push({
        rowId,
        turnId: lastTurnId ?? `marker-${rowId}`,
        kind: "timelineMarker",
        entityId: compactionEntityId(message.entryId),
        lane: "assistantWork",
        marker: compactionMarker(message),
        createdAt: message.at,
        createdAtSeq: rowId,
      });
      lastAt = Math.max(lastAt, message.at);
      continue;
    }
    if (message.role === "branchSummary") {
      // 旧 CLI 不给 branch summary 生成 v4 行；跳过以保持行集合与直播路径一致。
      lastAt = Math.max(lastAt, message.at);
      continue;
    }
    if (message.role === "user") {
      closeTurn(lastAt);
      ensureTurn(message.at, message.entryId);
      const rowId = allocate();
      rows.push({
        rowId,
        turnId,
        kind: "userInput",
        ...(message.entryId ? { entityId: userEntityId(message.entryId) } : {}),
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
      let textSegments = 0;
      let thinkingSegments = 0;
      for (const part of message.parts) {
        if (part.type === "text") {
          appendPart(part, message.at, responseId, message.entryId, textSegments);
          if (part.text) textSegments += 1;
        } else if (part.type === "thinking") {
          appendPart(part, message.at, responseId, message.entryId, thinkingSegments);
          if (part.text) thinkingSegments += 1;
        } else {
          appendPart(part, message.at, responseId, message.entryId, 0);
        }
      }
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
        entityId: toolEntityId(message.toolCallId),
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
    // Restored rows carry `entityId` but no pending groups: their ids are already final.
    pendingMessageGroups: [],
    phase: state.phase,
    lastActivityAt: Math.max(state.lastActivityAt, lastAt),
  };

  function appendPart(
    part: EngineAssistantPart,
    at: number,
    responseId: string,
    entryId: string | undefined,
    segmentIndex: number,
  ): void {
    if (part.type === "text") {
      if (!part.text) return;
      const rowId = allocate();
      rows.push({
        rowId,
        turnId,
        kind: "assistantText",
        ...(entryId ? { entityId: assistantPartEntityId(entryId, "text", segmentIndex) } : {}),
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
        ...(entryId ? { entityId: assistantPartEntityId(entryId, "thinking", segmentIndex) } : {}),
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
    // A call whose result never reached the transcript was not executed (the runner
    // stopped while the approval was pending). Restoring it as `success` would invent a
    // mutation that never happened; mark it cancelled and let the toolResult upgrade it.
    const toolRow: ToolCallRowState = {
      rowId,
      turnId,
      kind: "toolCall",
      entityId: toolEntityId(part.id),
      assistantResponseId: responseId,
      toolCallId: part.id,
      toolName: toolDisplayName(part.name),
      status: "cancelled",
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

function compactionMarker(message: Extract<EngineMessage, { role: "compaction" }>): TimelineMarker {
  return {
    type: "compact",
    origin: message.origin ?? "auto",
    status: "success",
    ...(message.tokensBefore > 0 ? { tokensBefore: message.tokensBefore } : {}),
  };
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
