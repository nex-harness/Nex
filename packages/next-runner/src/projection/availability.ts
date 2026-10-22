import type {
  ActionAvailability,
  InputRouting,
  SessionActionAvailability,
} from "@nex/shared/nex-protocol-v4";
import { deriveInputRouting } from "./inputRouting.js";
import { modelConfigAvailability } from "./modelConfigAvailability.js";
import type { ProjectionState } from "./state.js";

/**
 * `snapshot.availability` / `snapshot.inputRouting` (research §1 end, §5).
 *
 * Port of the old CLI's `computeAvailability` / `computeInputRouting`
 * (`apps/nex-cli/packages/bootstrap/src/nex-protocol-v4/projection-state.ts:117-197`).
 * Features this runner cannot perform yet are denied with an existing guard id; later
 * batches flip them one at a time and this table is the single place that changes.
 *
 * | flag               | state |
 * | ------------------ | ----- |
 * | fork               | allowed unless a compact is running/queued (`compactOperationLock`) |
 * | compact            | allowed unless a compact is running/queued (`compactOperationLock`) |
 * | switchModelConfig  | allowed — implemented by batch 2a |
 * | setFollowupMode    | allowed — implemented by batch 2a |
 * | queueEdit          | allowed — batch 2c owns the queue |
 * | sendQueuedNow      | allowed while running; `sendQueuedNowRequiresRunning` when idle |
 * | pauseGoal          | denied `noGoalToPause` (this runner has no goal state) |
 * | resumeGoal         | denied `noGoalToResume` |
 *
 * `compactOperationLock` is also the ACK reason for a second compact and for a fork while
 * compacting. The old CLI's `idleCannotCompact` is never advertised: this runner always has
 * a pi session to compact (a session with nothing to summarize ends `noop` instead).
 */
export function computeAvailability(state: ProjectionState): SessionActionAvailability {
  const running = state.phase === "running" || state.phase === "prewarming";
  const compacting = Object.keys(state.compactOperations).length > 0;
  const compactQueued = state.queue.items.some((item) => item.kind === "compact");
  const compactLocked = compacting || compactQueued;
  const compactLock: ActionAvailability = {
    allowed: false,
    reasonCode: "compactOperationLock",
  };
  return {
    fork: compactLocked ? compactLock : { allowed: true },
    compact: compactLocked ? compactLock : { allowed: true },
    // Both are implemented (`engine.switchModelConfig` / `engine.setFollowupMode`); the
    // old CLI always allows them and nothing about phase or compaction gates them.
    ...modelConfigAvailability(state),
    // The runner owns the queue: edit (delete+composer restore) works in every phase.
    queueEdit: { allowed: true },
    // "Send now" preempts the active run; with no run there is nothing to preempt.
    sendQueuedNow: running
      ? { allowed: true }
      : { allowed: false, reasonCode: "sendQueuedNowRequiresRunning" },
    pauseGoal: { allowed: false, reasonCode: "noGoalToPause" },
    resumeGoal: { allowed: false, reasonCode: "noGoalToResume" },
  };
}

/**
 * Input routing for the composer's submit affordance, derived from the queue owner's facts
 * (`deriveInputRouting` documents the table, including the held-queue `choice`).
 */
export function computeInputRouting(state: ProjectionState): InputRouting {
  return deriveInputRouting({
    phase: state.phase,
    followupMode: state.followupMode,
    queue: state.queue,
  });
}
