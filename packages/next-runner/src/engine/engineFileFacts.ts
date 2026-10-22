/**
 * File-change/rewind result types of the `Engine` boundary (spec B); `types.ts` re-exports
 * them so callers keep importing from one place.
 */

/** Checkpoint facts of one turn as the engine sees them. */
export interface EngineFileFacts {
  /** Found at all (a turn without file changes answers found:false). */
  found: boolean;
  record?: import("./checkpoints/types.js").NexCheckpointsRecord;
  reverted: boolean;
  /** Turn id (`turn-<n>`) the record belongs to. */
  turnId?: string;
  /** False when git is missing or the turn only has per-file states. */
  rewindable: boolean;
}

export interface EngineFileChanges {
  files: number;
  additions: number;
  deletions: number;
  state: "active" | "reverted";
  items: import("./checkpoints/rewindStore.js").FileChangeItem[];
}

export interface EngineApplyFileRewindOutcome {
  applied: boolean;
  preview: import("./checkpoints/rewindStore.js").RewindPreview;
  response: string;
}
