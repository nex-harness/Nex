/**
 * Startup rollback for a self-update that never came up
 * (docs/next/runner-update.md decision 5, "Rollback").
 *
 * The swap writes `<install>/.update-pending` before touching the install files
 * and the first successful `welcome` deletes it. If the new build crashes on
 * startup the pending file survives; this check runs at the very beginning of
 * `main` and counts the starts that still see it. After `ROLLBACK_ATTEMPTS`
 * starts — and only once the update is older than `ROLLBACK_MIN_AGE_MS`, so a
 * fast restart loop is not mistaken for a broken build — it restores `.prev/`
 * over the install files.
 *
 * The counter is written back before any restore, so a crash during rollback
 * still leaves evidence of the attempt.
 */
import { copyFile, mkdir, readFile, rm } from "node:fs/promises";
import { dirname, join } from "node:path";
import type { Logger } from "../logger.js";
import { writeJsonAtomic } from "../util/fs.js";

export const PENDING_FILE = ".update-pending";
export const PREV_DIR = ".prev";
export const UPDATE_DIR = ".update";

/** Starts that still find `.update-pending` before the new build is rolled back. */
export const ROLLBACK_ATTEMPTS = 3;
/** A younger pending file is an update in flight, not a failed one. */
export const ROLLBACK_MIN_AGE_MS = 60_000;

/** Files a swap writes; `.prev/` mirrors exactly these relative paths. */
export const UPDATE_FILES = [
  "runner.mjs",
  "pty-host.mjs",
  "lib/ptyHostAssets.mjs",
  "build.json",
  "package.json",
] as const;

export interface PendingUpdate {
  buildId: string;
  previousBuildId: string;
  /** Wall-clock ms when the swap happened. */
  at: number;
  /** Incremented at every startup that still sees this file. */
  attempts?: number;
}

/**
 * Reads `.update-pending`; `null` when absent or unreadable (a corrupt marker must
 * not block startup — the install files are then treated as authoritative).
 */
export async function readPendingUpdate(installDir: string): Promise<PendingUpdate | null> {
  try {
    const parsed = JSON.parse(
      await readFile(join(installDir, PENDING_FILE), "utf8"),
    ) as Partial<PendingUpdate>;
    if (typeof parsed.buildId !== "string" || typeof parsed.at !== "number") return null;
    return {
      buildId: parsed.buildId,
      previousBuildId:
        typeof parsed.previousBuildId === "string" ? parsed.previousBuildId : "unknown",
      at: parsed.at,
      attempts: typeof parsed.attempts === "number" ? parsed.attempts : 0,
    };
  } catch {
    return null;
  }
}

export interface RollbackOptions {
  installDir: string;
  logger: Logger;
  now?: () => number;
}

/**
 * Counts this start against `.update-pending` and restores `.prev/` once the
 * attempt budget is spent. Never throws: a failed rollback leaves the marker so
 * the next start retries, and startup continues either way.
 */
export async function rollbackPendingUpdate(options: RollbackOptions): Promise<void> {
  const { installDir, logger } = options;
  const now = options.now ?? Date.now;
  const pending = await readPendingUpdate(installDir);
  if (!pending) return;
  const attempts = (pending.attempts ?? 0) + 1;
  const next: PendingUpdate = { ...pending, attempts };
  try {
    // 先落盘计数：回滚过程中再崩溃时，下一次启动能继续计数而不是从头开始。
    await writeJsonAtomic(join(installDir, PENDING_FILE), next);
  } catch (error) {
    logger.warn("update rollback: cannot update pending counter", {
      message: error instanceof Error ? error.message : String(error),
    });
  }
  if (attempts < ROLLBACK_ATTEMPTS) return;
  if (now() - pending.at < ROLLBACK_MIN_AGE_MS) return;
  try {
    await restorePreviousFiles(installDir);
    await rm(join(installDir, PENDING_FILE), { force: true });
    await rm(join(installDir, UPDATE_DIR), { recursive: true, force: true });
    logger.warn("update rollback: restored previous build", {
      failedBuildId: pending.buildId,
      previousBuildId: pending.previousBuildId,
      attempts,
    });
  } catch (error) {
    logger.error("update rollback failed", {
      failedBuildId: pending.buildId,
      message: error instanceof Error ? error.message : String(error),
    });
  }
}

/** Copies every `.prev/<file>` over its install counterpart, skipping absent ones. */
async function restorePreviousFiles(installDir: string): Promise<void> {
  for (const file of UPDATE_FILES) {
    const source = join(installDir, PREV_DIR, file);
    try {
      await readFile(source);
    } catch {
      // 旧安装可能没有 build.json 等文件：缺什么就保留现装的那份。
      continue;
    }
    const target = join(installDir, file);
    await mkdir(dirname(target), { recursive: true });
    await copyFile(source, target);
  }
}
