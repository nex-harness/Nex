import type { EngineEvent } from "../engine/types.js";
import { fallbackTurnId } from "./messageIdentity.js";
import { mapToolInput, toolDisplayName } from "./toolMapping.js";
import {
  allocateRowId,
  bump,
  jsonText,
  type ProjectedRow,
  type ProjectionState,
  type ToolCallRowState,
} from "./state.js";

type ApprovalRequestEvent = Extract<EngineEvent, { type: "approval_request" }>;
type ApprovalSettledEvent = Extract<EngineEvent, { type: "approval_settled" }>;

function withToolRow(
  state: ProjectionState,
  toolCallId: string,
  update: (row: ToolCallRowState) => ToolCallRowState,
): ProjectedRow[] {
  const rowId = state.toolRows[toolCallId];
  if (rowId === undefined) return state.rows;
  return state.rows.map((row) =>
    row.rowId === rowId && row.kind === "toolCall" ? update(row) : row,
  );
}

/**
 * The gate requested approval for one tool call. pi emits `tool_execution_start` before
 * the `tool_call` hook, so the row already exists; when the call is a nested one (or the
 * start raced the hook) a row is created here so the card always has an anchor.
 */
export function applyApprovalRequest(
  state: ProjectionState,
  event: ApprovalRequestEvent,
): ProjectionState {
  const approval = event.approval;
  let working = state;
  if (working.toolRows[approval.toolCallId] === undefined) {
    const allocated = allocateRowId(working);
    const input = mapToolInput(approval.toolName, approval.input);
    working = {
      ...working,
      rows: [
        ...working.rows,
        {
          rowId: allocated.id,
          turnId: working.currentTurn?.turnId ?? fallbackTurnId(working),
          kind: "toolCall",
          toolCallId: approval.toolCallId,
          toolName: toolDisplayName(approval.toolName),
          status: "running",
          inputText: jsonText(input),
          input,
          startedAt: approval.createdAt,
          createdAt: approval.createdAt,
          createdAtSeq: working.seq + 1,
        },
      ],
      nextRowId: allocated.next,
      toolRows: { ...working.toolRows, [approval.toolCallId]: allocated.id },
    };
  }
  const input = mapToolInput(approval.toolName, approval.input);
  const rows = withToolRow(working, approval.toolCallId, (row) => ({
    ...row,
    toolName: toolDisplayName(approval.toolName),
    input,
    inputText: jsonText(input),
    status: "pendingApproval",
    approvalInteractionId: approval.interactionId,
  }));
  return bump({
    ...working,
    rows,
    phase: "running",
    pendingApprovals: {
      ...working.pendingApprovals,
      [approval.interactionId]: {
        interactionId: approval.interactionId,
        toolCallId: approval.toolCallId,
        toolName: approval.toolName,
        summary: approval.summary,
        detail: approval.input,
        createdAt: approval.createdAt,
      },
    },
    lastActivityAt: event.at,
  });
}

/**
 * The approval was answered (allow) or closed (deny/aborted). On allow the row returns to
 * `running`; every other outcome ends the row as `cancelled` — a denied call did not run,
 * so it must never render as `error` or `success`.
 */
export function applyApprovalSettled(
  state: ProjectionState,
  event: ApprovalSettledEvent,
): ProjectionState {
  const approval = state.pendingApprovals[event.interactionId];
  const pendingApprovals = { ...state.pendingApprovals };
  delete pendingApprovals[event.interactionId];
  if (!approval) {
    // A settle for an approval this projection never saw (restore race) still needs the
    // row to leave `pendingApproval`, so fall back to the event's toolCallId.
    const rows = withToolRow(state, event.toolCallId, (row) => ({
      ...row,
      status: event.outcome === "allow" ? "running" : "cancelled",
      ...(event.outcome === "allow"
        ? { approvalInteractionId: undefined }
        : { approvalInteractionId: undefined, endedAt: event.at }),
    }));
    return bump({ ...state, rows, pendingApprovals, lastActivityAt: event.at });
  }
  const rows = withToolRow(state, approval.toolCallId, (row) =>
    event.outcome === "allow"
      ? { ...row, status: "running", approvalInteractionId: undefined }
      : {
          ...row,
          status: "cancelled",
          approvalInteractionId: undefined,
          endedAt: event.at,
          ...(event.outcome === "deny" && row.outputText === undefined ? { outputText: "" } : {}),
        },
  );
  return bump({ ...state, rows, pendingApprovals, lastActivityAt: event.at });
}

/** Drops pending approvals for a run that ended without a settle event (defensive). */
export function clearPendingApprovals(state: ProjectionState, at: number): ProjectionState {
  const entries = Object.values(state.pendingApprovals);
  if (entries.length === 0) return state;
  let working = state;
  for (const entry of entries) {
    working = applyApprovalSettled(working, {
      type: "approval_settled",
      at,
      interactionId: entry.interactionId,
      toolCallId: entry.toolCallId,
      outcome: "aborted",
    });
  }
  return working;
}
