/**
 * Runner-written pi custom entries for file features (spec B). Kept apart from `types.ts`
 * so the engine boundary declaration stays within the file budget; `EngineMessage` unions
 * them in.
 */

/**
 * `nex.checkpoints` custom entry: the turn → shadow-git checkpoint facts (spec B). Restore
 * turns it into the turn header's `fileChanges` summary and the reverting markers' state.
 */
export interface EngineCheckpointsRecord {
  role: "checkpoints";
  at: number;
  entryId: string;
  /** pi entry id of the turn's user message (rewind anchor). */
  userEntryId: string;
  turnId: string;
  data: import("./checkpoints/types.js").NexCheckpointsRecord;
}

/** `nex.rewind` custom entry: this turn's files were restored; the header shows `reverted`. */
export interface EngineRewindRecord {
  role: "rewind";
  at: number;
  entryId: string;
  userEntryId: string;
  data: import("./checkpoints/types.js").NexRewindRecord;
}
