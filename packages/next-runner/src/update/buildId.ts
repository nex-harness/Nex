/**
 * Runner build identity (docs/next/runner-update.md decision 1).
 *
 * buildId = sha256 over the bytes of `runner.mjs`, `pty-host.mjs` and
 * `lib/ptyHostAssets.mjs` in that fixed order, first 16 hex characters.
 *
 * The same algorithm lives in `scripts/buildId.mjs` because `scripts/bundle.mjs`
 * is a plain Node script that cannot import TypeScript. `test/self-update.test.ts`
 * asserts both implementations agree, so the two copies cannot drift.
 */
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

/** Files that make up a build, in hash order (bundle-relative paths). */
export const BUILD_ID_FILES = ["runner.mjs", "pty-host.mjs", "lib/ptyHostAssets.mjs"] as const;

/** BuildId reported when the runner runs from source (tsx) and no bundle exists. */
export const DEV_BUILD_ID = "dev";

/** Pure hash: the single source of truth for the id of a set of build files. */
export function computeBuildId(files: ReadonlyArray<Buffer | Uint8Array>): string {
  const hash = createHash("sha256");
  for (const file of files) hash.update(file);
  return hash.digest("hex").slice(0, 16);
}

/**
 * The directory holding the running bundle's files. In a bundled install this is
 * the directory of `runner.mjs`; under `tsx src/main.ts` it is `src/`, which has
 * no bundle files (callers fall back to `DEV_BUILD_ID`).
 */
export function bundleDir(): string {
  const entry = process.argv[1];
  // resolve()：argv[1] 可能是相对路径（`node dist/runner.mjs`），而后续读写与
  // 进程 cwd 无关，必须用绝对目录。
  if (entry && entry.length > 0) return dirname(resolve(entry));
  // `--import tsx` still sets argv[1]; import.meta.url is the fallback for the
  // unlikely case a host runs main.ts without an argv entry.
  return dirname(fileURLToPath(import.meta.url));
}

/**
 * Reads the three build files from `dir` and hashes them. Returns `null` when any
 * file is missing (a dev checkout, or a partially installed directory) — the
 * caller reports `DEV_BUILD_ID` and refuses self-update.
 */
export async function readBuildId(dir: string): Promise<string | null> {
  try {
    const files = await Promise.all(BUILD_ID_FILES.map((file) => readFile(join(dir, file))));
    return computeBuildId(files);
  } catch {
    return null;
  }
}

/** The running runner's own buildId: a hash for an install, `"dev"` otherwise. */
export async function currentBuildId(dir: string = bundleDir()): Promise<string> {
  return (await readBuildId(dir)) ?? DEV_BUILD_ID;
}
