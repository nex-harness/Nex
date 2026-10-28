/**
 * Shared helpers of the runner's workspace-tool channels
 * (docs/next/workspace-tools.md batch 1): argument decoding and the path
 * containment predicate used by every channel module.
 */
import { isAbsolute, join, resolve, sep } from "node:path";
import { realpath } from "node:fs/promises";
import type { JsonValue } from "../link/frames.js";

export function isRecord(value: JsonValue): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function asString(value: unknown): string | null {
  return typeof value === "string" && value.length > 0 ? value : null;
}

/** One arg (already unwrapped from the positional envelope by the link layer). */
export function recordArg(arg: JsonValue): Record<string, unknown> {
  if (!isRecord(arg)) return {};
  return arg;
}

/** Encodes an error thrown by a service into the link's error branch shape. */
export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/**
 * Path containment (decision 2): `candidate` must resolve lexically to a path
 * inside one of the served roots. Traversal (`..`) is resolved first, so
 * `/root/../x` escapes. Relative paths are refused outright.
 */
export function resolveInsideWorkspace(
  candidate: string,
  servedRoots: readonly string[],
): string | null {
  if (!isAbsolute(candidate)) return null;
  const target = resolve(candidate);
  for (const root of servedRoots) {
    const resolvedRoot = resolve(root);
    const relative = relativeWithin(target, resolvedRoot);
    if (relative === null) continue;
    return join(resolvedRoot, relative);
  }
  return null;
}

function relativeWithin(target: string, root: string): string | null {
  const rootWithSep = root.endsWith(sep) ? root : root + sep;
  if (target === root) return "";
  if (target.startsWith(rootWithSep)) return target.slice(rootWithSep.length);
  return null;
}

/**
 * Realpath-aware containment: symlinked paths must resolve inside a root. The
 * file may not exist yet (a watcher on a directory being created); fall back to
 * lexical containment only when realpath fails for non-existence reasons.
 */
export async function resolveInsideWorkspaceReal(
  candidate: string,
  servedRoots: readonly string[],
): Promise<string | null> {
  const lexical = resolveInsideWorkspace(candidate, servedRoots);
  if (lexical === null) return null;
  let real: string;
  try {
    real = await realpath(lexical);
  } catch {
    // Not (yet) on disk: lexical containment already proved the target cannot
    // point outside; fs.watch will fail on its own for a missing directory.
    return lexical;
  }
  // The resolved path must still be inside a root (the symlink target).
  if (resolveInsideWorkspace(real, servedRoots) === null) return null;
  return real;
}

/**
 * Machine browse scope (docs/next/runner-folders.md decision 2): any absolute
 * path the runner's OS user can reach, resolved through realpath. Relative
 * paths are still refused, and the caller must be a browse-only method.
 */
export async function resolveMachineScopePath(candidate: string): Promise<string> {
  if (!isAbsolute(candidate)) {
    throw new Error(`path must be absolute: ${candidate}`);
  }
  try {
    return await realpath(resolve(candidate));
  } catch {
    throw new Error(`path does not exist: ${candidate}`);
  }
}
