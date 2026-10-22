import { createHash } from "node:crypto";
import { CheckpointGit } from "./checkpointGit.js";

/**
 * Small helpers shared by the checkpoint service (turn snapshot / blob hashing) and the
 * rewind store: the binary probe, the NUL-separated `ls-files` count, and the whole-file
 * unified diff for `write` tool cards.
 */

/**
 * Throws `GitError(kind: "unavailable")` when the binary cannot be spawned. No repository
 * flags and an existing cwd: a nonexistent directory makes libuv report `spawn ENOENT`,
 * which would look exactly like a missing binary.
 */
export async function probeGit(executable = "git"): Promise<void> {
  const git = new CheckpointGit("/nonexistent", "/nonexistent", executable);
  await git.probeVersion();
}

export function countNulEntries(buffer: Buffer): number {
  let count = 0;
  for (const byte of buffer) if (byte === 0) count += 1;
  return count;
}

/**
 * Whole-file unified diff for a write: every removed line, then every added line.
 * The UI only needs hunks plus +/- counts; no line anchoring is attempted here.
 */
export function synthesizePatch(relative: string, before: Buffer, after: Buffer): string {
  const prior = splitLines(before);
  const next = splitLines(after);
  const lines: string[] = [];
  for (const line of prior) lines.push(`-${line}`);
  for (const line of next) lines.push(`+${line}`);
  const oldStart = prior.length === 0 ? 0 : 1;
  const newStart = next.length === 0 ? 0 : 1;
  return [
    `--- a/${relative}`,
    `+++ b/${relative}`,
    `@@ -${oldStart},${prior.length} +${newStart},${next.length} @@`,
    ...lines,
  ].join("\n");
}

function splitLines(data: Buffer): string[] {
  if (data.byteLength === 0) return [];
  const text = data.toString("utf8").replace(/\r\n/gu, "\n");
  const lines = text.split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  return lines;
}

/** Stable content hash for external-modification detection in rewind previews. */
export function contentHash(data: Buffer): string {
  return createHash("sha256").update(data).digest("hex");
}
