import type * as T from "../../src/engine/types.js";
import type { RewindPreview } from "../../src/engine/checkpoints/rewindStore.js";
import type { FakeEngine } from "./fakeEngine.js";

/**
 * Engine double for the file-change/rewind surface (spec B). Tests script one entry per
 * user entry id; the command handler and projection see exactly what `PiEngine` returns.
 */
export class FakeFileOps {
  /** userEntryId → preview the next calls answer. */
  readonly previews = new Map<string, RewindPreview>();
  /** userEntryId → per-file aggregation. */
  readonly changes = new Map<string, T.EngineFileChanges>();
  /** userEntryId → applied outcome; a missing entry applies the current preview. */
  readonly applies = new Map<string, T.EngineApplyFileRewindOutcome>();
  readonly applyCalls: Array<{ sessionId: string; userEntryId: string }> = [];
  enabled = true;

  emptyPreview(): RewindPreview {
    return { canApply: false, ignoredFiles: [], safeFiles: [], unsafeFiles: [] };
  }

  scriptChange(userEntryId: string, changes: T.EngineFileChanges): void {
    this.changes.set(userEntryId, changes);
  }

  scriptPreview(userEntryId: string, preview: RewindPreview): void {
    this.previews.set(userEntryId, preview);
  }

  async facts(userEntryId?: string): Promise<T.EngineFileFacts> {
    return {
      found: false,
      reverted: userEntryId !== undefined && this.changes.get(userEntryId)?.state === "reverted",
      rewindable: this.enabled,
    };
  }

  async enabledAsync(): Promise<boolean> {
    return this.enabled;
  }

  async fileChanges(userEntryId: string): Promise<T.EngineFileChanges> {
    return (
      this.changes.get(userEntryId) ?? {
        files: 0,
        additions: 0,
        deletions: 0,
        state: "active",
        items: [],
      }
    );
  }

  async previewFor(userEntryId: string): Promise<RewindPreview> {
    return this.previews.get(userEntryId) ?? this.emptyPreview();
  }

  async apply(
    sessionId: string,
    userEntryId: string,
    options: { commitAfterApply?: () => Promise<void> },
  ): Promise<T.EngineApplyFileRewindOutcome> {
    this.applyCalls.push({ sessionId, userEntryId });
    const scripted = this.applies.get(userEntryId);
    if (scripted) {
      if (scripted.applied) await options.commitAfterApply?.();
      return scripted;
    }
    const preview = await this.previewFor(userEntryId);
    if (!preview.canApply) return { applied: false, preview, response: "cannot apply" };
    await options.commitAfterApply?.();
    return { applied: true, preview, response: "restored" };
  }
}

export type WithFileOps = Pick<
  T.Engine,
  | "fileCheckpointFacts"
  | "fileChanges"
  | "fileRewindPreview"
  | "applyFileRewind"
  | "fileCheckpointsEnabled"
>;

/** The `Engine` file-surface methods, delegated from `FakeEngine` to keep that file small. */
export class FileOpsDelegation implements WithFileOps {
  private readonly files: FakeFileOps;

  constructor(host: FakeEngine) {
    this.files = host.files;
  }

  fileCheckpointFacts(_w: string, _s: string, userEntryId: string): Promise<T.EngineFileFacts> {
    return this.files.facts(userEntryId);
  }
  fileChanges(_w: string, _s: string, userEntryId: string): Promise<T.EngineFileChanges> {
    return this.files.fileChanges(userEntryId);
  }
  fileRewindPreview(_w: string, _s: string, userEntryId: string): Promise<RewindPreview> {
    return this.files.previewFor(userEntryId);
  }
  applyFileRewind(
    _w: string,
    s: string,
    userEntryId: string,
    options: { commitAfterApply?: () => Promise<void>; sourceCommandId?: string },
  ): Promise<T.EngineApplyFileRewindOutcome> {
    return this.files.apply(s, userEntryId, options);
  }
  fileCheckpointsEnabled(): Promise<boolean> {
    return this.files.enabledAsync();
  }
}
