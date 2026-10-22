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
 * (`apps/nex-cli/packages/bootstrap/src/nex-protocol-v4/projection-state.ts:117-197`), with
 * every feature this runner cannot perform yet reported as denied with an existing guard
 * id instead of the previous unconditional `{allowed:true}`. Later batches flip these one
 * at a time; the table below is the single place that changes.
 *
 * | flag               | batch 2a state |
 * | ------------------ | -------------- |
 * | fork               | denied `guard.actionUnavailable` (forkAssistant lands in a later batch) |
 * | compact            | denied `guard.actionUnavailable` (batch 3) |
 * | switchModelConfig  | allowed — implemented in this batch by the title/model work |
 * | setFollowupMode    | allowed — implemented in this batch by the title/model work |
 * | queueEdit          | denied `guard.actionUnavailable` (no queue yet) |
 * | sendQueuedNow      | denied `sendQueuedNowRequiresRunning` when idle, else `guard.actionUnavailable` |
 * | pauseGoal          | denied `noGoalToPause` (this runner has no goal state) |
 * | resumeGoal         | denied `noGoalToResume` |
 */
const DENIED_ACTION: ActionAvailability = {
  allowed: false,
  reasonCode: "guard.actionUnavailable",
};

export function computeAvailability(state: ProjectionState): SessionActionAvailability {
  const running = state.phase === "running" || state.phase === "prewarming";
  const unimplemented: ActionAvailability = DENIED_ACTION;
  return {
    fork: unimplemented,
    compact: unimplemented,
    // Both are implemented (`engine.switchModelConfig` / `engine.setFollowupMode`); the
    // old CLI always allows them and nothing about phase or compaction gates them.
    ...modelConfigAvailability(state),
    queueEdit: unimplemented,
    sendQueuedNow: running
      ? unimplemented
      : { allowed: false, reasonCode: "sendQueuedNowRequiresRunning" },
    pauseGoal: { allowed: false, reasonCode: "noGoalToPause" },
    resumeGoal: { allowed: false, reasonCode: "noGoalToResume" },
  };
}

/**
 * Input routing for the composer's submit affordance. There is no runner-owned queue yet
 * (batch 2c), so `choice` (held queue) is unreachable: an idle session always starts now.
 */
export function computeInputRouting(state: ProjectionState): InputRouting {
  return deriveInputRouting({ phase: state.phase, followupMode: state.followupMode });
}
