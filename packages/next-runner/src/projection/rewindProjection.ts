import { bump, type ProjectedRow, type ProjectionState } from "./state.js";

/**
 * Rewind projection (docs/next/conversation-features.md decision 5).
 *
 * The rewind primitive cuts pi's branch back to the parent of a user entry and re-prompts
 * in the same session. This runner republishes whole snapshots instead of emitting
 * `row.removed` deltas, so "rows after the cut disappear" is expressed by dropping them
 * from the row set before the new prompt's events arrive.
 *
 * Invariants:
 * - rowIds stay monotonic: `nextRowId` is never lowered, so a removed id is never reused;
 * - only rows at or after the turn header are dropped (the header and its user row are the
 *   turn being rewritten; the new prompt creates a fresh turn);
 * - pending identity/anchor state that referred to dropped rows is cleared.
 */
export interface RewindCut {
  /** First row id to drop (the rewritten turn's header). */
  fromRowId: number;
  /** Where the cut happened, for the caller's diagnostics. */
  turnId: string;
}

export function cutRowsFrom(state: ProjectionState, cut: RewindCut): ProjectionState {
  const kept: ProjectedRow[] = [];
  const dropped = new Set<number>();
  for (const row of state.rows) {
    if (row.rowId >= cut.fromRowId) dropped.add(row.rowId);
    else kept.push(row);
  }
  if (dropped.size === 0) return state;
  // A turn header before the cut cannot still be open (its rows are gone), and a cutoff
  // in the middle of a streaming response must not leave the fold's cursors dangling.
  const currentTurn =
    state.currentTurn && dropped.has(state.currentTurn.headerRowId) ? null : state.currentTurn;
  const currentResponse =
    state.currentResponse && responseTouched(state, dropped) ? null : state.currentResponse;
  const toolRows = Object.fromEntries(
    Object.entries(state.toolRows).filter(([, rowId]) => !dropped.has(rowId)),
  );
  // A card anchored to a dropped tool row can no longer be rendered or answered; the gate
  // already settled it (rewind aborts the run first) — this only keeps the projection honest.
  const pendingApprovals = Object.fromEntries(
    Object.entries(state.pendingApprovals).filter(
      ([, approval]) => !dropped.has(anchoredToolRow(state, approval.toolCallId, dropped) ?? -1),
    ),
  );
  const pendingMessageGroups = state.pendingMessageGroups
    .map((group) => ({
      ...group,
      rowIds: group.rowIds.filter((rowId) => !dropped.has(rowId)),
      ...(group.headerRowId !== undefined && dropped.has(group.headerRowId)
        ? { headerRowId: undefined }
        : {}),
    }))
    .filter((group) => group.rowIds.length > 0 || group.headerRowId !== undefined);
  const compactOperations = Object.fromEntries(
    Object.entries(state.compactOperations).filter(
      ([, operation]) => !dropped.has(operation.rowId),
    ),
  );
  return bump({
    ...state,
    rows: kept,
    currentTurn,
    currentResponse,
    toolRows,
    pendingApprovals,
    pendingMessageGroups,
    compactOperations,
    // A half-streamed reply that was just cut is not an error; the new prompt owns the phase.
    lastError: null,
  });
}

function anchoredToolRow(
  state: ProjectionState,
  toolCallId: string,
  dropped: Set<number>,
): number | undefined {
  const rowId = state.toolRows[toolCallId];
  return rowId !== undefined && dropped.has(rowId) ? rowId : undefined;
}

function responseTouched(state: ProjectionState, dropped: Set<number>): boolean {
  const response = state.currentResponse;
  if (!response) return false;
  for (const rowId of [response.textRowId, response.reasoningRowId]) {
    if (rowId !== undefined && dropped.has(rowId)) return true;
  }
  const touchedTool = Object.values(state.toolRows).some((rowId) => dropped.has(rowId));
  return touchedTool;
}

/**
 * Row id of the turn header that owns a user row (`pi:<entryId>`). Live and restored
 * projections both number the header immediately before its user row, so the header is the
 * nearest preceding `turnHeader` of the same turn.
 */
export function turnHeaderRowForUserRow(
  state: ProjectionState,
  userRowId: number,
): { rowId: number; turnId: string } | null {
  const userRow = state.rows.find((row) => row.rowId === userRowId);
  if (!userRow) return null;
  for (let index = state.rows.length - 1; index >= 0; index -= 1) {
    const row = state.rows[index];
    if (!row || row.turnId !== userRow.turnId) continue;
    if (row.kind === "turnHeader") return { rowId: row.rowId, turnId: row.turnId };
  }
  return null;
}
