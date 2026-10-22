import { assistantPartEntityId, turnEntityId, userEntityId } from "./entityIds.js";
import { bump, type ProjectedRow, type ProjectionState } from "./state.js";

/**
 * Attaches a persisted pi entry id to the oldest waiting row group of the same role
 * (`EngineEvent.message_entry`).
 *
 * `message_entry` fires after the row exists, so this is a row update (not an append) and
 * must bump `revision`: an entityId is the target identity commands are sent with, so a
 * client that cached the pre-identity revision must retry.
 *
 * Identity is assigned at most once per row group; a later event for the same role (a pi
 * retry) applies to the next group, which keeps ids monotonic instead of rewriting history.
 */
export function attachEntryId(
  state: ProjectionState,
  entryId: string,
  role: "user" | "assistant",
): ProjectionState {
  const groupIndex = state.pendingMessageGroups.findIndex((group) => group.kind === role);
  if (groupIndex < 0) return state;
  const group = state.pendingMessageGroups[groupIndex];
  if (!group) return state;
  const rowIds = new Set(group.rowIds);
  const rows = state.rows.map((row): ProjectedRow => {
    if (!rowIds.has(row.rowId)) return row;
    if (row.kind === "userInput") return { ...row, entityId: userEntityId(entryId) };
    if (row.kind === "assistantText") {
      return { ...row, entityId: assistantPartEntityId(entryId, "text", row.segmentIndex ?? 0) };
    }
    if (row.kind === "reasoning") {
      return {
        ...row,
        entityId: assistantPartEntityId(entryId, "thinking", row.segmentIndex ?? 0),
      };
    }
    return row;
  });
  const withHeader =
    group.headerRowId === undefined
      ? rows
      : rows.map(
          (row): ProjectedRow =>
            row.rowId === group.headerRowId && row.kind === "turnHeader"
              ? { ...row, entityId: turnEntityId(entryId, 0) }
              : row,
        );
  const pendingMessageGroups = state.pendingMessageGroups.filter(
    (_, index) => index !== groupIndex,
  );
  return bump({ ...state, rows: withHeader, pendingMessageGroups });
}

/**
 * Turn id for a row produced outside any open turn (an extension tool call before the first
 * user input, a marker landing between turns). It reuses the last turn so such rows never
 * introduce a phantom render unit; `projectHistory` never hits this path.
 */
export function fallbackTurnId(state: ProjectionState): string {
  for (let index = state.rows.length - 1; index >= 0; index -= 1) {
    const row = state.rows[index];
    if (row && row.kind === "turnHeader") return row.turnId;
  }
  return "turn-0";
}

/**
 * Appends a row id to the newest unfinished assistant group. Tool rows join the group too,
 * so `message_entry` can settle every row identity of one assistant message in one pass.
 */
export function addRowToAssistantGroup(
  groups: ProjectionState["pendingMessageGroups"],
  rowId: number,
): ProjectionState["pendingMessageGroups"] {
  for (let index = groups.length - 1; index >= 0; index -= 1) {
    const group = groups[index];
    if (group?.kind !== "assistant") continue;
    return groups.map((item, itemIndex) =>
      itemIndex === index ? { ...item, rowIds: [...item.rowIds, rowId] } : item,
    );
  }
  return groups;
}
