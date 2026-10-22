import type { EngineEvent } from "../engine/types.js";
import { allocateRowId, bump, type ProjectedRow, type ProjectionState } from "./state.js";

/**
 * Appends a `lightBoundary` timeline marker (rows.ts `timelineMarkerRowSchema`).
 *
 * 落位语义：旧 CLI 在下一轮 turnHeader 之前落 marker
 * （`apps/nex-cli/packages/bootstrap/src/nex-protocol-v4/product-projection.ts:1946-2000`）。
 * 本 runner 的 projection 只做追加，marker 先落在当前尾部；下一轮的 turnHeader 随后
 * 追加到它后面，UI 按 lane=lightBoundary 渲染在 user 输入之前，语义一致。
 */
export function appendTimelineMarker(
  state: ProjectionState,
  event: Extract<EngineEvent, { type: "model_change_marker" }>,
): ProjectionState {
  const allocated = allocateRowId(state);
  const marker =
    event.from === undefined
      ? {
          type: "modelChange" as const,
          toProvider: event.to.provider,
          toModel: event.to.model,
          toThought: event.to.thought,
        }
      : {
          type: "modelChange" as const,
          fromProvider: event.from.provider,
          fromModel: event.from.model,
          toProvider: event.to.provider,
          toModel: event.to.model,
          toThought: event.to.thought,
        };
  const row: ProjectedRow = {
    rowId: allocated.id,
    turnId: state.currentTurn?.turnId ?? `turn-${state.seq + 1}`,
    kind: "timelineMarker",
    lane: "lightBoundary",
    ...(event.sourceCommandId ? { sourceCommandId: event.sourceCommandId } : {}),
    marker,
    createdAt: event.at,
    createdAtSeq: state.seq + 1,
  };
  return bump({
    ...state,
    rows: [...state.rows, row],
    nextRowId: allocated.next,
    lastActivityAt: event.at,
  });
}
