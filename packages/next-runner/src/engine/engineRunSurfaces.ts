/**
 * Small capability surfaces around the `Engine` boundary (queue run-state, model switches,
 * compaction outcomes). `types.ts` re-exports them so the boundary file stays within the
 * file budget.
 */
import type { EngineModelState } from "./types.js";

/**
 * Queue/admission surface (docs/next/conversation-features.md decision 4).
 *
 * The runner owns queued text; pi's own queues are only used for `guide`, which pi cannot
 * address by id. These three calls are what the runner's queue drain and guide admission
 * need from the engine and nothing more.
 */
export interface EngineRunState {
  /** A run (or post-run continuation/compaction) is active. */
  streaming: boolean;
  /** No active run, compaction or branch summary. */
  idle: boolean;
}

/** Payload of the v4 `switchModelConfig` command (`command.ts`). */
export interface SwitchModelConfigInput {
  provider: string;
  model: string;
  thought: string;
}

/**
 * `unchanged` is the old CLI's `noop config.unchanged`; `failed` carries the reason code
 * (`provider.notInRegistry`) and leaves the session untouched.
 */
export interface SwitchModelConfigOutcome {
  status: "changed" | "unchanged" | "failed";
  reasonCode?: string;
  message?: string;
  /** Effective model after the switch (absent on failure). */
  model?: EngineModelState;
  /** True when the model identity changed, so the caller emits a `modelChange` marker. */
  modelIdentityChanged?: boolean;
  /** Model identity before the switch, for the marker's `from` fields. */
  previous?: { provider: string; model: string };
}

export interface SubmitOutcome {
  status: "accepted" | "duplicate";
  inputId: string;
}

/** Outcome of one compaction operation (manual via `compact()`, or pi's automatic one). */
export interface EngineCompactOutcome {
  status: "success" | "noop" | "cancelled" | "failed";
  origin: "manual" | "auto";
  tokensBefore?: number;
  tokensAfter?: number;
  /** `compact.noop` / `compact.failed` / the operation lock when nothing started. */
  reasonCode?: string;
  message?: string;
}

export interface EngineCompactState {
  /** A manual compact operation started by the runner is in flight. */
  manualInFlight: boolean;
  /** A manual compact operation is still queued in the runner queue. */
  queued: boolean;
  /** pi is compacting (manual or automatic). */
  compacting: boolean;
  /** Manual + auto operations the engine observed and has not yet reported. */
  pendingOutcomes: EngineCompactOutcome[];
}
