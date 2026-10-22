import { stat } from "node:fs/promises";
import { join } from "node:path";
import type { Logger } from "../../logger.js";
import {
  CheckpointGit,
  GitError,
  canonicalRoot,
  ensureShadowRepo,
  relativeInside,
} from "./checkpointGit.js";
import { parseUnifiedPatch } from "./patch.js";
import { probeGit } from "./checkpointSnapshot.js";
import {
  blobInTree,
  countTrackedFiles,
  hashFile,
  recordOf,
  snapshotTree,
  writePatch,
} from "./checkpointGitOps.js";
import type { NexCheckpointsRecord, NexFileStartState } from "./types.js";

/**
 * Shadow-git checkpoint service — one instance per workspace
 * (spec: docs/next/attachments-rewind.md B).
 *
 * ```
 * tool_call(edit/write) ──► prepareTurn(userEntryId, path)
 *      first mutating call of the turn: remember the path's start blob, then
 *      add -A (honours .gitignore) → write-tree → commit-tree -p <previous turn>
 *      → refs/nex/turns/<turnSequence>
 * tool_result ──► recordOperation(path) == git hash-object -w of the post-state
 * ```
 *
 * Blobs live in the shadow object database (content addressed): a per-file post-state is one
 * cheap `hash-object -w`, the turn-start state is either the turn tree or the blobs captured
 * before each path's first write, and the diff payload is computed from those blobs.
 *
 * Failure policy: a missing `git` disables checkpoints for the whole runner (warned once);
 * any other git failure marks the turn partial, and rewind then answers `checkpoint_missing`
 * instead of restoring a guessed state.
 */

export interface CheckpointServiceOptions {
  dataDir: string;
  logger: Logger;
  gitExecutable?: string;
  /** Above this many tracked files the full-tree snapshot is skipped (per-file only). */
  maxSnapshotFiles?: number;
}

type GitState =
  | { kind: "unknown" }
  | { kind: "ready"; git: CheckpointGit }
  | { kind: "disabled"; reason: string };

export interface RecordedOp {
  path: string;
  toolCallId: string;
  toolName: string;
  postBlob: string | null;
  additions: number;
  deletions: number;
  patch?: string;
}

/** Live bookkeeping of the run's current turn (the persisted record is built from it). */
export interface TurnState {
  turnId: string;
  userEntryId: string;
  sequence: number;
  /** Turn-start tree when the full snapshot succeeded. */
  tree?: string;
  /** Per-path start blob, in first-write order (before any write of that path). */
  startFiles: Map<string, NexFileStartState>;
  operations: RecordedOp[];
  partial: boolean;
}

interface SessionState {
  current: TurnState | null;
  /** Tree of the previous prepared turn (read-tree base of the next one). */
  previousTree?: string;
  /** Commit of the previous prepared turn (parent of the next one). */
  previousCommit?: string;
}

export interface TurnSnapshot {
  turnId: string;
  userEntryId: string;
  sequence: number;
  ok: boolean;
  partial: boolean;
}

export interface RecordedOperation {
  /** Workspace-relative path (undefined when the path is outside the workspace). */
  path?: string;
  /** Post-state blob; null when the file no longer exists. */
  postBlob: string | null;
  /** Blob of the content this operation started from. */
  preBlob: string | null;
  /** Unified patch for the tool card (edit: pi's patch; write: synthesized). */
  patch?: string;
  additions: number;
  deletions: number;
}

const MUTATING_TOOLS = new Set(["edit", "write"]);
/** Tools that change files with no per-file checkpoint: their files show as ignored. */
const SHELL_TOOLS = new Set(["bash", "powershell", "shell", "terminal"]);
/** Files above this size keep their hashes but omit the diff payload. */
const MAX_DIFF_BYTES = 4 * 1024 * 1024;

export class CheckpointService {
  private canonicalRoot: string | null = null;
  private gitState: GitState = { kind: "unknown" };
  /** Live turn bookkeeping of this workspace's one running session at a time. */
  private session: SessionState = { current: null };
  private trackedFileCount: number | null = null;
  private gitWarningLogged = false;

  constructor(
    readonly workspacePath: string,
    private readonly options: CheckpointServiceOptions,
  ) {}

  /** True while git is missing; the caller hides Undo and counts this as disabled. */
  get disabled(): boolean {
    return this.gitState.kind === "disabled";
  }

  /** The workspace root as recorded (callers canonicalize once before relative paths). */
  get root(): string {
    return this.workspacePath;
  }

  /** Absolute path inside the workspace for a recorded relative path. */
  absolute(relative: string): string {
    return join(this.workspacePath, relative);
  }

  /** Resolves whether git works (creates the shadow repo on first use). */
  async available(): Promise<boolean> {
    return (await this.git()) !== null;
  }

  /** Shadow-repo handle for read paths (preview/history); null when git is unavailable. */
  async gitReader(): Promise<CheckpointGit | null> {
    return this.git();
  }

  /** Drops live turn bookkeeping after a branch cut / restart (records live in pi). */
  resetSession(): void {
    this.session = { current: null };
  }

  isMutatingTool(toolName: string): boolean {
    return MUTATING_TOOLS.has(toolName);
  }

  isShellTool(toolName: string): boolean {
    return SHELL_TOOLS.has(toolName);
  }

  /**
   * Begins a turn; the next mutating call with the same turn id reuses it. One workspace has
   * one pi session writing files at a time (the runner serializes runs per session), so the
   * service keeps only the live turn rather than a per-session table.
   */
  beginTurn(turnId: string, userEntryId: string): void {
    const state = this.session;
    if (state.current?.turnId === turnId) return;
    state.current = {
      turnId,
      userEntryId,
      sequence: (state.current?.sequence ?? 0) + 1,
      startFiles: new Map(),
      operations: [],
      partial: false,
    };
  }

  /**
   * First mutating tool call of the turn: snapshot the workspace (`add -A` + `write-tree` →
   * `commit-tree`). Idempotent per turn; every later mutating call only records its path.
   */
  async prepareTurn(
    turnId: string,
    userEntryId: string,
    path: string | undefined,
  ): Promise<TurnSnapshot> {
    if (this.session.current?.turnId !== turnId) this.beginTurn(turnId, userEntryId);
    const turn = this.session.current as TurnState;
    const git = await this.git();
    const relative = path ? await this.relative(path) : undefined;
    if (!git) {
      turn.partial = true;
      return snapshotOf(turn, false);
    }
    if (relative) await this.captureStart(git, turn, relative);
    if (turn.tree !== undefined) return snapshotOf(turn, true);
    const tracked = await this.countTracked();
    if (tracked !== null && tracked > (this.options.maxSnapshotFiles ?? 100_000)) {
      turn.partial = true;
      return snapshotOf(turn, false);
    }
    try {
      const snapshot = await snapshotTree(git, this.session.previousTree, {
        ...turn,
        ...(this.session.previousCommit ? { parentCommit: this.session.previousCommit } : {}),
      });
      if (snapshot === undefined) {
        turn.partial = true;
        return snapshotOf(turn, false);
      }
      turn.tree = snapshot.tree;
      this.session.previousTree = snapshot.tree;
      this.session.previousCommit = snapshot.commit;
      return snapshotOf(turn, true);
    } catch (error) {
      turn.partial = true;
      this.warn("turn checkpoint failed", error, { turnId: turn.turnId });
      return snapshotOf(turn, false);
    }
  }

  /**
   * After a mutating tool result: hashes the post-state of one path, keeps the operation in
   * turn order, and returns the diff facts for the tool card.
   */
  async recordOperation(
    turnId: string,
    input: {
      toolCallId: string;
      toolName: string;
      path: string | undefined;
      patch?: string;
      isError: boolean;
    },
  ): Promise<RecordedOperation | null> {
    if (input.isError) return null;
    const turn = this.session.current;
    if (!turn || turn.turnId !== turnId) return null;
    const git = await this.git();
    if (!git) return null;
    const relative = input.path ? await this.relative(input.path) : undefined;
    if (!relative) return null;
    const postBlob = await hashFile(git, join(this.workspacePath, relative), relative);
    const preBlob = await this.startBlob(git, turn, relative);
    let patch = input.patch;
    if (patch === undefined && input.toolName === "write") {
      patch = await writePatch(git, relative, preBlob, postBlob);
    }
    const parsed = patch ? parseUnifiedPatch(patch) : { additions: 0, deletions: 0 };
    const size = await this.fileSize(relative);
    const keepPatch = patch !== undefined && size <= MAX_DIFF_BYTES;
    const operation: RecordedOp = {
      path: relative,
      toolCallId: input.toolCallId,
      toolName: input.toolName,
      postBlob,
      additions: parsed.additions,
      deletions: parsed.deletions,
      ...(keepPatch ? { patch } : {}),
    };
    turn.operations.push(operation);
    return {
      path: relative,
      postBlob,
      preBlob,
      ...(keepPatch ? { patch } : {}),
      additions: operation.additions,
      deletions: operation.deletions,
    };
  }

  /**
   * Persistable `nex.checkpoints` payload for a finished turn; null when the turn recorded
   * nothing (no mutating call, no git, everything outside the workspace).
   */
  record(turnId: string): Omit<NexCheckpointsRecord, "version"> | null {
    const turn = this.session.current;
    if (!turn || turn.turnId !== turnId) return null;
    return recordOf(turn);
  }

  // ── internals ──

  /** Workspace-relative path for a tool path; undefined outside the workspace. */
  async relative(path: string): Promise<string | undefined> {
    if (!this.canonicalRoot) await this.git();
    return relativeInside(this.workspacePath, path, this.canonicalRoot ?? this.workspacePath);
  }

  /** Resolves the workspace's shadow repo once; a missing git disables it for the process. */
  private async git(): Promise<CheckpointGit | null> {
    if (this.gitState.kind === "ready") return this.gitState.git;
    if (this.gitState.kind === "disabled") return null;
    try {
      this.canonicalRoot = await canonicalRoot(this.workspacePath);
      // A missing binary must not create a directory tree in the caller's data dir.
      await probeGit(this.options.gitExecutable);
      const git = await ensureShadowRepo({
        workspaceRealPath: this.canonicalRoot,
        dataDir: this.options.dataDir,
        ...(this.options.gitExecutable ? { gitExecutable: this.options.gitExecutable } : {}),
      });
      this.gitState = { kind: "ready", git };
      return git;
    } catch (error) {
      const reason = error instanceof GitError ? error.message : String(error);
      this.gitState = { kind: "disabled", reason };
      if (!this.gitWarningLogged) {
        this.gitWarningLogged = true;
        this.options.logger.warn("file checkpoints disabled: git is unavailable", {
          workspace: this.workspacePath,
          reason,
        });
      }
      return null;
    }
  }

  private async countTracked(): Promise<number | null> {
    if (this.trackedFileCount !== null) return this.trackedFileCount;
    const git = this.gitState.kind === "ready" ? this.gitState.git : null;
    if (!git) return null;
    this.trackedFileCount = await countTrackedFiles(git);
    return this.trackedFileCount;
  }

  private async captureStart(git: CheckpointGit, turn: TurnState, relative: string): Promise<void> {
    if (turn.startFiles.has(relative)) return;
    turn.startFiles.set(relative, {
      blob: await hashFile(git, join(this.workspacePath, relative), relative),
    });
  }

  /** Turn-start blob of `relative`: the turn tree first, then the captured first-write blob. */
  private async startBlob(
    git: CheckpointGit,
    turn: TurnState,
    relative: string,
  ): Promise<string | null> {
    if (turn.tree !== undefined) {
      const fromTree = await blobInTree(git, turn.tree, relative);
      if (fromTree !== undefined) return fromTree;
    }
    return turn.startFiles.get(relative)?.blob ?? null;
  }

  private async fileSize(relative: string): Promise<number> {
    try {
      return (await stat(join(this.workspacePath, relative))).size;
    } catch {
      return 0;
    }
  }

  private warn(message: string, error: unknown, extra: Record<string, unknown>): void {
    warnCheckpoint(this.options.logger, this.workspacePath, message, error, extra);
  }
}

/** One warn line for a failed checkpoint step (never a stack trace in the runner logs). */
export function warnCheckpoint(
  logger: Logger,
  workspacePath: string,
  message: string,
  error: unknown,
  extra: Record<string, unknown> = {},
): void {
  logger.warn(message, {
    workspace: workspacePath,
    message: error instanceof Error ? error.message : String(error),
    ...extra,
  });
}

function snapshotOf(turn: TurnState, ok: boolean): TurnSnapshot {
  return {
    turnId: turn.turnId,
    userEntryId: turn.userEntryId,
    sequence: turn.sequence,
    ok,
    partial: turn.partial,
  };
}
