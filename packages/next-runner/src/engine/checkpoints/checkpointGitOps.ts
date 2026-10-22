import { readFile, rm } from "node:fs/promises";
import { join } from "node:path";
import { toGitPath, type CheckpointGit } from "./checkpointGit.js";
import { countNulEntries, synthesizePatch } from "./checkpointSnapshot.js";
import type { NexCheckpointsRecord } from "./types.js";
import type { TurnState } from "./checkpointService.js";

/**
 * Git operations of the checkpoint service, split out for the file budget. The service owns
 * state (git handle, live turn); these functions do one git interaction each and never keep
 * any.
 */

const EMPTY_TREE = "4b825dc642cb6eb9a060e54bf8d69288fbee4904";

/** Tracked + untracked (not ignored) entries; above the threshold a turn is partial. */
export async function countTrackedFiles(git: CheckpointGit): Promise<number | null> {
  try {
    const result = await git.run(["ls-files", "-z", "--cached", "--others", "--exclude-standard"], {
      timeoutMs: 120_000,
    });
    return countNulEntries(result.stdoutBuffer);
  } catch {
    return null;
  }
}

/**
 * `add -A` in a side index seeded from the previous turn's tree, so modified, untracked and
 * deleted files all land in one tree while the shadow repo's own index stays untouched.
 * Advances `refs/nex/turns/<sequence>` (history) — best effort, the tree is the fact.
 */
export async function snapshotTree(
  git: CheckpointGit,
  /** Tree id of the previous turn (the new commit's parent commit is derived from it). */
  parentTree: string | undefined,
  turn: TurnState & { parentCommit?: string },
): Promise<{ tree: string; commit: string } | undefined> {
  const indexFile = join(
    git.repositoryDir,
    `nex-index-${process.pid}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}.tmp`,
  );
  const env = { GIT_INDEX_FILE: toGitPath(indexFile) };
  try {
    const seeded =
      parentTree !== undefined &&
      (await git.run(["read-tree", parentTree], { env, timeoutMs: 30_000 }).then(
        () => true,
        () => false,
      ));
    if (!seeded) await git.run(["read-tree", EMPTY_TREE], { env, timeoutMs: 30_000 });
    // `--work-tree` is supplied by the runner on every call; `.` = the workspace tree.
    await git.run(["add", "-A", "--", "."], { env, timeoutMs: 120_000 });
    const tree = (await git.run(["write-tree"], { env, timeoutMs: 60_000 })).stdout.trim();
    if (!tree) return undefined;
    // `commit-tree` needs a PARENT COMMIT, not a tree; the service records the commit id it
    // produced for each turn tree.
    const parentCommit =
      parentTree === undefined
        ? undefined
        : await commitForTree(git, parentTree, turn.parentCommit);
    const args = ["commit-tree", tree, "-m", `nex ${turn.turnId}`];
    if (parentCommit !== undefined) args.push("-p", parentCommit);
    const commit = (await git.run(args, { timeoutMs: 60_000 })).stdout.trim();
    await git
      .run(["update-ref", `refs/nex/turns/${turn.sequence}`, commit], { timeoutMs: 30_000 })
      .catch(() => undefined);
    return { tree, commit };
  } finally {
    await rm(indexFile, { force: true }).catch(() => undefined);
  }
}

/**
 * Parent commit of the new turn commit. The service passes the commit id it recorded for the
 * previous turn's tree; a missing record falls back to wrapping the tree in its own commit.
 */
async function commitForTree(git: CheckpointGit, tree: string, commit?: string): Promise<string> {
  if (commit) return commit;
  return (
    await git.run(["commit-tree", tree, "-m", "nex turn"], { timeoutMs: 60_000 })
  ).stdout.trim();
}

/** `hash-object -w` of the file's CURRENT bytes (null when absent/unreadable). */
export async function hashFile(
  git: CheckpointGit,
  absolute: string,
  relative: string,
): Promise<string | null> {
  try {
    await readFile(absolute);
  } catch {
    return null;
  }
  try {
    // `--no-filters` keeps CRLF/clean filters out of the stored bytes on every platform.
    const result = await git.run(["hash-object", "-w", "--no-filters", "--", relative], {
      timeoutMs: 60_000,
    });
    return result.stdout.trim() || null;
  } catch {
    return null;
  }
}

/** Blob id of `relative` in a tree (`undefined` = the tree could not be read). */
export async function blobInTree(
  git: CheckpointGit,
  tree: string,
  relative: string,
): Promise<string | null | undefined> {
  try {
    const result = await git.run(["ls-tree", tree, "--", relative], { timeoutMs: 30_000 });
    const line = result.stdout.split("\n")[0] ?? "";
    // `<mode> <type> <object>\t<path>`
    const match = /^\d+ blob ([0-9a-f]{40,64})\t/u.exec(line);
    return match?.[1] ?? null;
  } catch {
    return undefined;
  }
}

export async function readBlob(
  git: CheckpointGit,
  blob: string | null,
): Promise<Buffer | undefined> {
  if (!blob) return undefined;
  try {
    return (await git.run(["cat-file", "blob", blob], { timeoutMs: 60_000 })).stdoutBuffer;
  } catch {
    return undefined;
  }
}

/** Whole-file patch for a write: pre-state blob vs post-state blob. */
export async function writePatch(
  git: CheckpointGit,
  relative: string,
  preBlob: string | null,
  postBlob: string | null,
): Promise<string | undefined> {
  if (!postBlob) return undefined;
  const before = (await readBlob(git, preBlob)) ?? Buffer.alloc(0);
  const after = (await readBlob(git, postBlob)) ?? Buffer.alloc(0);
  return synthesizePatch(relative, before, after);
}

/** The persisted `nex.checkpoints` payload of a finished turn (version added by the caller). */
export function recordOf(turn: TurnState): Omit<NexCheckpointsRecord, "version"> | null {
  if (turn.operations.length === 0) return null;
  return {
    turnId: turn.turnId,
    userEntryId: turn.userEntryId,
    sequence: turn.sequence,
    files: Object.fromEntries(turn.startFiles),
    operations: turn.operations.map((operation) => ({ ...operation })),
    ...(turn.partial ? { partial: true } : {}),
  };
}
