import type { InputRouting } from "@nex/shared/nex-protocol-v4";
import type { ProjectionState } from "./state.js";

/**
 * `snapshot.inputRouting` (research §5; port of the old CLI's `computeInputRouting`,
 * `projection-state.ts:157-197`).
 *
 * | state                                              | mode      |
 * | -------------------------------------------------- | --------- |
 * | running / prewarming, followupMode=guide           | guide     |
 * | running / prewarming, followupMode=queue           | enqueue   |
 * | completed + queued items + autoDrain=false         | choice    |
 * | otherwise                                          | startNow  |
 *
 * `choice` is the held queue: the composer must ask clear/keep before sending
 * (`SessionPane` renders the confirmation and sends `heldQueueDisposition`). Guide items are
 * excluded from the queue count because they are pi-owned pending rows, not queued text.
 */
export function deriveInputRouting(state: {
  phase: ProjectionState["phase"];
  followupMode: ProjectionState["followupMode"];
  queue: ProjectionState["queue"];
}): InputRouting {
  if (state.phase === "running" || state.phase === "prewarming") {
    return { mode: state.followupMode === "guide" ? "guide" : "enqueue" };
  }
  const completed = state.phase === "completedSuccess" || state.phase === "completedInterrupted";
  const queued = state.queue.items.some((item) => item.delivery.admitted !== "guide");
  if (completed && queued && !state.queue.autoDrain) {
    return { mode: "choice", reasonCode: "heldQueueInputRequiresChoice" };
  }
  return { mode: "startNow" };
}
