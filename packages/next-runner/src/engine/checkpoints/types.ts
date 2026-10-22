/**
 * Checkpoint facts (spec: docs/next/attachments-rewind.md B).
 *
 * Everything here is derived per workspace and addressed by product ids:
 * - `NexCheckpointsRecord` is what the runner persists into the pi session (`nex.checkpoints`
 *   custom entry) and what restore rebuilds rows from;
 * - `NexRewindRecord` is the `nex.rewind` marker that flips a turn to `reverted`;
 * - `ToolFileChange` is the per-tool diff the projection turns into `output.display`.
 */

/** pi custom entry types this runner writes for file features. */
export const CHECKPOINTS_CUSTOM_TYPE = "nex.checkpoints";
export const REWIND_CUSTOM_TYPE = "nex.rewind";

/** One file a mutating tool touched, as recorded after the tool result. */
export interface ToolFileChange {
  /** Workspace-relative, forward slashes. */
  path: string;
  /** Post-state blob id, or null when the file no longer exists. */
  postBlob: string | null;
  /** Blob hash of the content when this operation started (used for write diffs). */
  preBlob?: string | null;
  /** pi tool name (`edit` | `write` | …). */
  toolName: string;
  toolCallId: string;
  /** Unified patch for this operation (edit: pi's `details.patch`; write: synthesized). */
  patch?: string;
  additions: number;
  deletions: number;
}

/** Diff display payload the projection emits as `toolCall.output.display`. */
export interface FileDiffDisplay {
  kind: "file_diff";
  filePath: string;
  additions: number;
  deletions: number;
  structuredPatch: Array<{
    oldStart: number;
    oldLines: number;
    newStart: number;
    newLines: number;
    lines: string[];
  }>;
}

/** Aggregated turn summary (`turnHeader.fileChanges`). */
export interface TurnFileSummary {
  files: number;
  additions: number;
  deletions: number;
  state: "active" | "reverted";
}

/** Persisted turn → checkpoint facts (pi custom entry `nex.checkpoints`). */
export interface NexCheckpointsRecord {
  version: 1;
  /** pi entry id of the turn's user message (the rewind anchor). */
  userEntryId: string;
  /** Product turn id (`turn-<n>`). */
  turnId: string;
  /** Turn-start tree: full tree when a snapshot exists, else per-file parent blobs. */
  files: Record<string, NexFileStartState>;
  /** Tool operations in execution order. */
  operations: NexFileOperation[];
  sequence: number;
  /**
   * Why only per-file states exist (large workspace / git failure). `true` marks the turn
   * checkpoint as partial so rewind answers `checkpoint_missing` instead of guessing.
   */
  partial?: boolean;
}

/** What one path looked like when the turn started. */
export interface NexFileStartState {
  /** Blob id, or null when the file did not exist. */
  blob: string | null;
}

export interface NexFileOperation {
  path: string;
  toolCallId: string;
  toolName: string;
  postBlob: string | null;
  additions: number;
  deletions: number;
  /**
   * Unified patch of this operation, bounded (spec B: tool cards must render after a
   * restart without replaying git). Omitted for oversized files.
   */
  patch?: string;
}

/** `nex.rewind` custom entry: the turn whose files were restored. */
export interface NexRewindRecord {
  version: 1;
  /** pi entry id of the reverted turn's user message. */
  userEntryId: string;
  turnId: string;
  revertedAt: number;
  sourceCommandId: string;
  files: number;
}
