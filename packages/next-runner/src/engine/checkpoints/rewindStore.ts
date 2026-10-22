import {
  applyMode,
  atomicDelete,
  atomicWriteFile,
  fileMode,
  readFileOrUndefined,
  ReverseJournal,
} from "./checkpointFiles.js";
import type { CheckpointService } from "./checkpointService.js";
import { contentHash } from "./checkpointSnapshot.js";
import { parseUnifiedPatch } from "./patch.js";
import type { NexCheckpointsRecord } from "./types.js";
import type { GitResult } from "./checkpointGit.js";
import type { Logger } from "../../logger.js";

/**
 * Read/plan/apply side of the checkpoints (spec B): `conversationFileChangesV4`, the rewind
 * preview and `applyFileRewind` all derive from the persisted `nex.checkpoints` records of a
 * turn (and the `nex.rewind` markers that reverted it).
 *
 * Semantics (old CLI parity):
 * - a path is `unsafe (external_modified)` when its current sha-256 differs from the blob its
 *   LAST recorded operation produced — another process, or `bash`, changed it afterwards;
 * - a path touched only by shell tools (`bash`) is `ignored (bash_ignored)`;
 * - a turn whose checkpoint was only per-file (partial) or is missing is
 *   `unsafe (checkpoint_missing)` rather than a guessed restore;
 * - `canApply` = at least one safe file and no unsafe file;
 * - apply restores the turn-start content (deleting files the turn created) through a reverse
 *   journal: every already-written path is compensated in reverse order on any failure.
 */

export interface FileChangeItem {
  path: string;
  additions: number;
  deletions: number;
  writeCount: number;
  toolNames: string[];
  patches: Array<{
    oldStart: number;
    oldLines: number;
    newStart: number;
    newLines: number;
    lines: string[];
  }>;
}

export interface FileChangesResult {
  files: number;
  additions: number;
  deletions: number;
  state: "active" | "reverted";
  items: FileChangeItem[];
}

export type RewindAction = "restore" | "delete";

export interface SafeFile {
  action: RewindAction;
  operationCount: number;
  path: string;
  toolNames: string[];
}

export interface UnsafeFile {
  path: string;
  reason:
    | "checkpoint_missing"
    | "checkpoint_unreadable"
    | "external_modified"
    | "file_read_failed"
    | "unsupported_checkpoint";
  operationCount: number;
  toolNames: string[];
  currentHash?: string;
  expectedHash?: string;
  message?: string;
}

export interface IgnoredFile {
  path: string;
  reason: "bash_ignored";
  operationCount: number;
  toolNames: string[];
}

export interface RewindPreview {
  canApply: boolean;
  ignoredFiles: IgnoredFile[];
  safeFiles: SafeFile[];
  unsafeFiles: UnsafeFile[];
}

/** The wire preview plus the internal plan (the turn-start blob never leaves the runner). */
export interface PlanResult {
  preview: RewindPreview;
  plan: Array<SafeFile & { absolutePath: string; startBlob: string | null }>;
}

interface GitReader {
  run(args: string[], options?: { timeoutMs?: number }): Promise<GitResult>;
}

export class RewindStore {
  constructor(
    private readonly checkpoints: CheckpointService,
    private readonly logger: Logger,
  ) {}

  /** Repository read access; null when git is unavailable. */
  private async reader(): Promise<GitReader | null> {
    return (await this.checkpoints.gitReader()) as GitReader | null;
  }

  /**
   * `conversationFileChangesV4` for one turn: per path, the LAST operation's patch (the same
   * diff the tool card shows), so a write→edit pair never reports the write's whole-file
   * patch as the turn's change. Counts fall back to the op totals without git/patch.
   */
  async fileChanges(
    record: NexCheckpointsRecord | undefined,
    reverted: boolean,
  ): Promise<FileChangesResult> {
    if (!record || record.operations.length === 0) {
      return { files: 0, additions: 0, deletions: 0, state: "active", items: [] };
    }
    const items: FileChangeItem[] = [];
    for (const [path, operations] of groupOperations(record)) {
      const last = operations[operations.length - 1];
      const parsed = last?.patch
        ? parseUnifiedPatch(last.patch)
        : { hunks: [], additions: 0, deletions: 0 };
      items.push({
        path,
        additions: last?.additions ?? parsed.additions,
        deletions: last?.deletions ?? parsed.deletions,
        writeCount: operations.length,
        toolNames: unique(operations.map((operation) => operation.toolName)),
        patches: parsed.hunks,
      });
    }
    return {
      files: items.length,
      additions: items.reduce((total, item) => total + item.additions, 0),
      deletions: items.reduce((total, item) => total + item.deletions, 0),
      state: reverted ? "reverted" : "active",
      items,
    };
  }

  /** Rewind preview: classify every path the turn touched as safe / unsafe / ignored. */
  async preview(
    record: NexCheckpointsRecord | undefined,
    shellTouched: string[],
  ): Promise<PlanResult> {
    const plan: PlanResult["plan"] = [];
    const preview: RewindPreview = {
      canApply: false,
      ignoredFiles: [],
      safeFiles: [],
      unsafeFiles: [],
    };
    const ignoreSet = new Set(shellTouched);
    if (!record) {
      for (const path of shellTouched) {
        preview.ignoredFiles.push({
          path,
          reason: "bash_ignored",
          operationCount: 1,
          toolNames: ["Bash"],
        });
      }
      return { preview, plan };
    }
    const git = await this.reader();
    const grouped = groupOperations(record);
    for (const [path, operations] of grouped) {
      const toolNames = unique(operations.map((operation) => operation.toolName));
      if (!git) {
        preview.unsafeFiles.push({
          path,
          reason: "checkpoint_unreadable",
          operationCount: operations.length,
          toolNames,
          message: "git is unavailable on the runner host",
        });
        continue;
      }
      if (
        ignoreSet.has(path) ||
        operations.some((operation) => this.checkpoints.isShellTool(operation.toolName))
      ) {
        preview.ignoredFiles.push({
          path,
          reason: "bash_ignored",
          operationCount: operations.length,
          toolNames,
        });
        continue;
      }
      const start = record.files[path];
      if (record.partial || start === undefined) {
        preview.unsafeFiles.push({
          path,
          reason: "checkpoint_missing",
          operationCount: operations.length,
          toolNames,
        });
        continue;
      }
      const last = operations[operations.length - 1];
      const expected = last?.postBlob ?? null;
      let current: Buffer | undefined;
      try {
        current = await readFileOrUndefined(this.checkpoints.absolute(path));
      } catch {
        preview.unsafeFiles.push({
          path,
          reason: "file_read_failed",
          operationCount: operations.length,
          toolNames,
        });
        continue;
      }
      const currentHash = current === undefined ? null : contentHash(current);
      if (currentHash === null && expected !== null) {
        preview.unsafeFiles.push({
          path,
          reason: "external_modified",
          operationCount: operations.length,
          toolNames,
          expectedHash: await blobHashOf(git, expected),
          message: "the file was deleted outside the runner",
        });
        continue;
      }
      if (currentHash !== null) {
        const expectedHash = await blobHashOf(git, expected);
        if (expectedHash === undefined) {
          preview.unsafeFiles.push({
            path,
            reason: "checkpoint_unreadable",
            operationCount: operations.length,
            toolNames,
          });
          continue;
        }
        if (expectedHash !== currentHash) {
          preview.unsafeFiles.push({
            path,
            reason: "external_modified",
            operationCount: operations.length,
            toolNames,
            currentHash,
            expectedHash,
            message: "the file changed outside the runner",
          });
          continue;
        }
      }
      const action: RewindAction = start.blob === null ? "delete" : "restore";
      const safe: SafeFile = { action, operationCount: operations.length, path, toolNames };
      preview.safeFiles.push(safe);
      plan.push({ ...safe, absolutePath: this.checkpoints.absolute(path), startBlob: start.blob });
    }
    // A shell-touched path with no per-file record still belongs in the ignored list.
    for (const path of ignoreSet) {
      if (grouped.has(path)) continue;
      preview.ignoredFiles.push({
        path,
        reason: "bash_ignored",
        operationCount: 1,
        toolNames: ["Bash"],
      });
    }
    preview.canApply = preview.safeFiles.length > 0 && preview.unsafeFiles.length === 0;
    return { preview, plan };
  }

  /**
   * Applies a plan atomically with a reverse journal. `commitAfterApply` runs after every
   * file was written and throws to trigger compensation (e.g. a failed branch cut).
   */
  async apply(
    plan: PlanResult["plan"],
    options: { commitAfterApply?: () => Promise<void> } = {},
  ): Promise<{ applied: boolean; failures: string[] }> {
    const git = await this.reader();
    if (!git) return { applied: false, failures: ["git is unavailable"] };
    const journal = new ReverseJournal(this.checkpoints.root, this.logger);
    try {
      for (const file of plan) {
        const before = await readFileOrUndefined(file.absolutePath);
        const beforeMode = before === undefined ? undefined : await fileMode(file.absolutePath);
        journal.record({
          path: file.absolutePath,
          before,
          ...(beforeMode !== undefined ? { beforeMode } : {}),
        });
        if (file.action === "delete") {
          if (before !== undefined) await atomicDelete(file.absolutePath);
          continue;
        }
        const content = await readBlob(git, file.startBlob);
        if (content === undefined)
          throw new Error(`checkpoint blob for ${file.path} is unreadable`);
        await atomicWriteFile(file.absolutePath, content, { replaceSymlink: true });
        await applyMode(file.absolutePath, beforeMode);
      }
      if (options.commitAfterApply) await options.commitAfterApply();
      return { applied: true, failures: [] };
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      const failures = await journal.compensate();
      this.logger.warn("file rewind failed; compensated", { message, failures });
      return { applied: false, failures: [message, ...failures] };
    }
  }
}

function groupOperations(
  record: NexCheckpointsRecord,
): Map<string, NexCheckpointsRecord["operations"]> {
  const grouped = new Map<string, NexCheckpointsRecord["operations"]>();
  for (const operation of record.operations) {
    const list = grouped.get(operation.path) ?? [];
    list.push(operation);
    grouped.set(operation.path, list);
  }
  return grouped;
}

async function blobHashOf(git: GitReader, blob: string | null): Promise<string | undefined> {
  const content = await readBlob(git, blob);
  return content === undefined ? undefined : contentHash(content);
}

async function readBlob(git: GitReader, blob: string | null): Promise<Buffer | undefined> {
  if (blob === null) return undefined;
  try {
    return (await git.run(["cat-file", "blob", blob], { timeoutMs: 60_000 })).stdoutBuffer;
  } catch {
    return undefined;
  }
}

function unique(values: string[]): string[] {
  return [...new Set(values)];
}

/** Display name for the tool names recorded in checkpoints (protocol vocabulary). */
export function displayToolName(toolName: string): string {
  switch (toolName) {
    case "edit":
      return "Edit";
    case "write":
      return "Write";
    case "bash":
    case "powershell":
      return "Bash";
    default:
      return toolName;
  }
}
