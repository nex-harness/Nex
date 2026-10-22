import { turnEntityId, userEntityId } from "./entityIds.js";
import { allocateRowId, bump, type ProjectedRow, type ProjectionState } from "./state.js";

/**
 * Opening a product turn for a user input (kept out of `fold.ts` for the file budget):
 * pushes/updates an interrupted open turn, then appends the turn header and the user row.
 */
export const applyUserTurnInput = applyUserInput;

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
    /** Admitted attachment refs of this prompt (spec A). */
    attachments?: import("@nex/shared/nex-protocol-v4").AttachmentRef[];
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
    ...(options.sourceCommandId ? { sourceCommandId: options.sourceCommandId } : {}),
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
    ...(options.attachments && options.attachments.length > 0
      ? { attachments: options.attachments }
      : {}),
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
