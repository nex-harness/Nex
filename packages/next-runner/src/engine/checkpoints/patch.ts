/**
 * Unified-diff helpers for checkpoint facts.
 *
 * pi's `edit` tool reports `details.patch` (a standard unified patch). The UI's tool card
 * consumes `structuredPatch` hunks (`ToolCallBlocks/fileSummaries.ts`), and the changed-files
 * panel consumes the same hunk shape plus line counts, so both are derived here — one parser,
 * one truth, for live rows and for the post-state replay.
 */

export interface DiffHunk {
  oldStart: number;
  oldLines: number;
  newStart: number;
  newLines: number;
  /** Body lines with their `+`/`-`/` ` prefix (no `\ No newline` markers). */
  lines: string[];
}

export interface ParsedPatch {
  hunks: DiffHunk[];
  additions: number;
  deletions: number;
}

const HUNK_HEADER = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/u;

/**
 * Parses a unified patch (git-style `a/`/`b/` headers or pi's `path`/`path` headers).
 * Tolerant by design: a malformed hunk header stops parsing instead of throwing, because a
 * cosmetic diff must never fail a tool result.
 */
export function parseUnifiedPatch(patch: string): ParsedPatch {
  const hunks: DiffHunk[] = [];
  let additions = 0;
  let deletions = 0;
  let current: DiffHunk | null = null;
  for (const rawLine of patch.split(/\r?\n/u)) {
    const header = HUNK_HEADER.exec(rawLine);
    if (header) {
      current = {
        oldStart: Number(header[1] ?? 0),
        oldLines: header[2] === undefined ? 1 : Number(header[2]),
        newStart: Number(header[3] ?? 0),
        newLines: header[4] === undefined ? 1 : Number(header[4]),
        lines: [],
      };
      hunks.push(current);
      continue;
    }
    if (!current) continue;
    if (rawLine.startsWith("\\")) continue;
    if (rawLine.length === 0) {
      // An empty context line (some producers strip the trailing space).
      current.lines.push(" ");
      continue;
    }
    const marker = rawLine[0];
    if (marker === "+" || marker === "-" || marker === " ") {
      current.lines.push(rawLine);
      if (marker === "+") additions += 1;
      if (marker === "-") deletions += 1;
      continue;
    }
    // Anything else ends the hunk body (e.g. a second file header).
    current = null;
  }
  return { hunks, additions, deletions };
}

/** Hunk body lines only (no headers), for line counting of a full file diff. */
export function countDiffLines(patch: string): { additions: number; deletions: number } {
  const { additions, deletions } = parseUnifiedPatch(patch);
  return { additions, deletions };
}
