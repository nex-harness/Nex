import { userEntityId } from "./entityIds.js";
import { bump, type ProjectionState, type TurnHeaderRowState } from "./state.js";

/**
 * Turns checkpoint facts into product rows (spec B, "Tool cards" / "Turn headers").
 *
 * - A mutating tool's `output.display = {kind:"file_diff",…}` so the card shows its patch.
 * - The turn header collects `fileChanges {files, additions, deletions, state}` from the
 *   tool displays; `canRewindFiles` itself is derived in `rowActions.ts`.
 *
 * Live and restore share these helpers so a restarted runner renders the same cards.
 */

/** Recomputes one turn header's `fileChanges` from the displays in its turn (never reverted). */
export function refreshTurnFileChanges(state: ProjectionState, turnId: string): ProjectionState {
  const index = state.rows.findIndex((row) => row.kind === "turnHeader" && row.turnId === turnId);
  const header = index >= 0 ? state.rows[index] : undefined;
  if (!header || header.kind !== "turnHeader" || header.fileChanges?.state === "reverted") {
    return state;
  }
  const displays = state.rows.filter(
    (row) => row.turnId === turnId && row.kind === "toolCall" && row.display !== undefined,
  );
  const files = new Set<string>();
  let additions = 0;
  let deletions = 0;
  for (const row of displays) {
    if (row.kind !== "toolCall" || !row.display) continue;
    files.add(row.display.filePath);
    additions += row.display.additions;
    deletions += row.display.deletions;
  }
  const next = {
    files: files.size,
    additions,
    deletions,
    state: "active" as const,
  };
  const current = header.fileChanges;
  if (
    current &&
    current.files === next.files &&
    current.additions === next.additions &&
    current.deletions === next.deletions &&
    current.state === "active"
  ) {
    return state;
  }
  const rows = [...state.rows];
  rows[index] = { ...header, fileChanges: next } as TurnHeaderRowState;
  return bump({ ...state, rows });
}

/** `turn_checkpoint` event: refresh the finished turn's header summary. */
export function applyTurnCheckpoint(
  state: ProjectionState,
  event: Extract<import("../engine/types.js").EngineEvent, { type: "turn_checkpoint" }>,
): ProjectionState {
  return refreshTurnFileChanges(state, event.turnId);
}

/** Marks the header of `userEntryId`'s turn `reverted` (a `nex.rewind` entry exists). */
export function markTurnReverted(
  state: ProjectionState,
  userEntryId: string,
  files: number,
): ProjectionState {
  for (let index = 0; index < state.rows.length; index += 1) {
    const row = state.rows[index];
    if (!row || row.kind !== "userInput" || row.entityId !== userEntityId(userEntryId)) continue;
    for (let headerIndex = index; headerIndex >= 0; headerIndex -= 1) {
      const candidate = state.rows[headerIndex];
      if (!candidate || candidate.turnId !== row.turnId) continue;
      if (candidate.kind !== "turnHeader") continue;
      const rows = [...state.rows];
      rows[headerIndex] = {
        ...candidate,
        fileChanges: {
          files: candidate.fileChanges?.files ?? files,
          additions: candidate.fileChanges?.additions ?? 0,
          deletions: candidate.fileChanges?.deletions ?? 0,
          state: "reverted",
        },
      };
      return bump({ ...state, rows });
    }
  }
  return state;
}
