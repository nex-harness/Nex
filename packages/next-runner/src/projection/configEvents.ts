import type { EngineEvent } from "../engine/types.js";
import { bump, type ProjectionState } from "./state.js";

/**
 * Session-level metadata events (title / model / follow-up mode). Kept out of `fold.ts`
 * so that file stays inside the line budget as more event families are added.
 */
export function applyTitleChanged(
  state: ProjectionState,
  event: Extract<EngineEvent, { type: "title_changed" }>,
): ProjectionState {
  // custom 永不被 generated 覆盖（旧 CLI onSessionTitleUpdated 同规则）。
  if (state.titleSource === "custom" && event.titleSource === "generated") return state;
  if (state.title === event.title && state.titleSource === event.titleSource) return state;
  return bump({
    ...state,
    title: event.title,
    titleSource: event.titleSource,
    lastActivityAt: event.at,
  });
}

export function applyModelChanged(
  state: ProjectionState,
  event: Extract<EngineEvent, { type: "model_changed" }>,
): ProjectionState {
  return bump({ ...state, model: event.model, lastActivityAt: event.at });
}

export function applyFollowupModeChanged(
  state: ProjectionState,
  event: Extract<EngineEvent, { type: "followup_mode_changed" }>,
): ProjectionState {
  return bump({ ...state, followupMode: event.mode, lastActivityAt: event.at });
}
