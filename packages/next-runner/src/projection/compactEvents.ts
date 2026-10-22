import type { EngineCompactOutcome, EngineEvent } from "../engine/types.js";
import {
  allocateRowId,
  bump,
  type CompactOperationState,
  type ProjectedRow,
  type ProjectionState,
  type TimelineMarker,
} from "./state.js";

/**
 * Compact marker lifecycle (docs/next/conversation-features.md decision 7, research §1).
 *
 * One `timelineMarker` compact row per operation, created `running` at
 * `compact_start`, upserted to its terminal status at `compact_end`:
 *
 * | terminal outcome          | status      |
 * | ------------------------- | ----------- |
 * | completed                 | `success`   |
 * | `Nothing to compact`      | `noop`      |
 * | aborted / stop            | `cancelled` |
 * | anything else             | `failed`    |
 *
 * Automatic compaction (pi `reason:"threshold"|"overflow"`) goes through the same path with
 * `origin:"auto"`, so a pi-initiated compaction is visible exactly like a manual one. The
 * marker row's `turnId` is the open turn (lane `assistantWork` folds it into "already
 * worked"); compaction that runs between turns reuses the last turn, like model changes.
 */

/** Reason codes the handlers mint for the ACK (`compactOperationLock` closes the dialog). */
export const COMPACT_OPERATION_LOCK = "compactOperationLock";
export const IDLE_CANNOT_COMPACT = "idleCannotCompact";

export function startCompactMarker(
  state: ProjectionState,
  input: {
    at: number;
    origin: "manual" | "auto";
    operationId: string;
    sourceCommandId?: string;
  },
): ProjectionState {
  if (state.compactOperations[input.operationId]) return state;
  const allocated = allocateRowId(state);
  const row: ProjectedRow = {
    rowId: allocated.id,
    turnId: state.currentTurn?.turnId ?? `turn-${state.seq + 1}`,
    kind: "timelineMarker",
    lane: "assistantWork",
    ...(input.sourceCommandId ? { sourceCommandId: input.sourceCommandId } : {}),
    marker: { type: "compact", origin: input.origin, status: "running" },
    createdAt: input.at,
    createdAtSeq: state.seq + 1,
  };
  const operation: CompactOperationState = {
    operationId: input.operationId,
    origin: input.origin,
    rowId: allocated.id,
    ...(input.sourceCommandId ? { sourceCommandId: input.sourceCommandId } : {}),
  };
  return bump({
    ...state,
    rows: [...state.rows, row],
    nextRowId: allocated.next,
    compactOperations: { ...state.compactOperations, [input.operationId]: operation },
    lastActivityAt: input.at,
  });
}

/**
 * Terminal upsert. Operations without a live marker (e.g. a manual compact whose
 * `compact_start` never arrived because pi threw before emitting) still get a row, so the
 * user always sees the outcome.
 */
export function finishCompactMarker(
  state: ProjectionState,
  input: {
    at: number;
    outcome: EngineCompactOutcome;
    operationId: string;
    sourceCommandId?: string;
  },
): ProjectionState {
  const operation = state.compactOperations[input.operationId];
  const marker: TimelineMarker = {
    type: "compact",
    origin: input.outcome.origin,
    status: input.outcome.status,
    ...(input.outcome.tokensBefore !== undefined && input.outcome.tokensBefore > 0
      ? { tokensBefore: input.outcome.tokensBefore }
      : {}),
    ...(input.outcome.tokensAfter !== undefined ? { tokensAfter: input.outcome.tokensAfter } : {}),
  };
  const rest = { ...state.compactOperations };
  delete rest[input.operationId];
  if (!operation) {
    // No live marker: create a standalone completed row in the current turn.
    const allocated = allocateRowId(state);
    const row: ProjectedRow = {
      rowId: allocated.id,
      turnId: state.currentTurn?.turnId ?? `turn-${state.seq + 1}`,
      kind: "timelineMarker",
      lane: "assistantWork",
      ...(input.sourceCommandId ? { sourceCommandId: input.sourceCommandId } : {}),
      marker,
      createdAt: input.at,
      createdAtSeq: state.seq + 1,
    };
    return bump({
      ...state,
      rows: [...state.rows, row],
      nextRowId: allocated.next,
      compactOperations: rest,
      lastActivityAt: input.at,
    });
  }
  const rows = state.rows.map(
    (row): ProjectedRow =>
      row.rowId === operation.rowId && row.kind === "timelineMarker" ? { ...row, marker } : row,
  );
  return bump({ ...state, rows, compactOperations: rest, lastActivityAt: input.at });
}

/**
 * Operation id of an automatic compaction. pi has no id for it, so the open automatic
 * operation is found by prefix; there is at most one per session (pi serializes them).
 */
export function autoCompactOperationId(at: number): string {
  return `auto:${at}`;
}

/** `compact_start` → marker row `running` for a pi-initiated automatic compaction. */
export function applyCompactStart(
  state: ProjectionState,
  event: Extract<EngineEvent, { type: "compact_start" }>,
): ProjectionState {
  if (event.reason === "manual") return state;
  if (openAutoOperationId(state)) return state;
  return startCompactMarker(state, {
    at: event.at,
    origin: "auto",
    operationId: autoCompactOperationId(event.at),
  });
}

export function applyCompactEnd(
  state: ProjectionState,
  event: Extract<EngineEvent, { type: "compact_end" }>,
): ProjectionState {
  if (event.reason === "manual") return state;
  const operationId = openAutoOperationId(state) ?? autoCompactOperationId(event.at);
  return finishCompactMarker(state, {
    at: event.at,
    operationId,
    outcome: outcomeFromEnd(state, event),
  });
}

/** The open auto operation's id, if `compact_start` opened a marker for it. */
export function openAutoOperationId(state: ProjectionState): string | undefined {
  return Object.keys(state.compactOperations).find((id) => id.startsWith("auto:"));
}

/** pi `compaction_end` → terminal outcome (shared with the engine's observed outcomes). */
export function outcomeFromEnd(
  state: ProjectionState,
  event: Extract<EngineEvent, { type: "compact_end" }>,
): EngineCompactOutcome {
  return {
    origin: "auto",
    status: autoTerminalStatus(state, event),
    ...(event.result ? { tokensBefore: event.result.tokensBefore } : {}),
    ...(event.result?.tokensAfter !== undefined ? { tokensAfter: event.result.tokensAfter } : {}),
  };
}

/**
 * A start without a matching end (pi decided not to compact) is `noop`; an aborted one is
 * `cancelled` only when a marker was actually open, so a declined attempt never shows a
 * cancelled card.
 */
function autoTerminalStatus(
  state: ProjectionState,
  event: Extract<EngineEvent, { type: "compact_end" }>,
): EngineCompactOutcome["status"] {
  if (event.result) return "success";
  const open = openAutoOperationId(state) !== undefined;
  if (event.aborted) return open ? "cancelled" : "noop";
  return open ? "failed" : "noop";
}

/** Terminal status mapping shared with the manual path (pi error strings → noop/failed). */
export function outcomeFromError(
  error: unknown,
  aborted: boolean,
  messageOf: (error: unknown) => string,
): { status: EngineCompactOutcome["status"]; message: string } {
  const message = messageOf(error);
  // pi's own error text is authoritative for "there was nothing to do"; only the session
  // state tells an abort apart from a plain failure of the summary request.
  if (/nothing to compact|already compacted/iu.test(message)) {
    return { status: "noop", message };
  }
  if (aborted) return { status: "cancelled", message };
  return { status: "failed", message };
}
