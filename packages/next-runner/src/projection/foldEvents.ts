/**
 * Small event folds re-exported through one import in `fold.ts` (file budget): timeline
 * markers, config markers and compaction markers.
 */
export { appendTimelineMarker } from "./markerEvents.js";
export { applyFollowupModeChanged, applyModelChanged, applyTitleChanged } from "./configEvents.js";
export { applyCompactEnd, applyCompactStart } from "./compactEvents.js";
