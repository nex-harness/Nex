import type { InputRouting } from "@nex/shared/nex-protocol-v4";
import type { ProjectionState } from "./state.js";

/**
 * Minimal `computeInputRouting` subset that makes `config.followupMode` meaningful
 * (old CLI `projection-state.ts:157-197`): an active run routes input to `guide` or the
 * queue; otherwise the next input starts now.
 *
 * Wired through `availability.ts` → `buildConversationSnapshot`. Batch 2a (this change)
 * reports the routing only; the runner-owned queue and its drain arrive in batch 2c, so
 * `choice` (held queue) is unreachable and busy text still goes to pi's follow-up channel.
 */
export function deriveInputRouting(state: {
  phase: ProjectionState["phase"];
  followupMode: ProjectionState["followupMode"];
}): InputRouting {
  if (state.phase === "running" || state.phase === "prewarming") {
    return { mode: state.followupMode === "guide" ? "guide" : "enqueue" };
  }
  return { mode: "startNow" };
}
