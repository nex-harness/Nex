import {
  chmod,
  lstat,
  mkdir,
  open,
  readFile,
  rename,
  rm,
  stat,
  unlink,
  writeFile,
} from "node:fs/promises";
import { dirname, join } from "node:path";
import { randomBytes } from "node:crypto";
import type { Logger } from "../../logger.js";

/**
 * File-level primitives shared by the checkpoint service and the apply path.
 *
 * "Atomic" here means: write a sibling temp file, `fsync` it, then `rename` over the target
 * (same directory, so the rename is atomic on every supported filesystem). The caller keeps
 * the before-image journal and compensates in reverse; this module only makes one step
 * either fully applied or not applied at all.
 */

export async function pathExists(path: string): Promise<boolean> {
  try {
    await lstat(path);
    return true;
  } catch {
    return false;
  }
}

/** Reads a file as bytes; `undefined` when it does not exist. */
export async function readFileOrUndefined(path: string): Promise<Buffer | undefined> {
  try {
    return await readFile(path);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return undefined;
    throw error;
  }
}

export interface WriteOptions {
  /** Refuse to follow a symlink at the target (rewind must not write outside the workspace). */
  replaceSymlink?: boolean;
}

/**
 * Atomic replace: temp file in the same directory → fsync → rename.
 *
 * On Windows `rename` over an existing file can fail with EPERM/EEXIST when another process
 * holds the file open; one retry after a short delay keeps a transient lock from failing the
 * whole journal (the failure is still surfaced after that).
 */
export async function atomicWriteFile(
  path: string,
  data: Buffer | string,
  options: WriteOptions = {},
): Promise<void> {
  await mkdir(dirname(path), { recursive: true });
  if (options.replaceSymlink && (await isSymlink(path))) {
    await unlink(path);
  }
  const temp = join(dirname(path), `.${randomBytes(6).toString("hex")}.nex-tmp`);
  await writeFile(temp, data);
  await syncPath(temp).catch(() => undefined);
  try {
    await rename(temp, path);
  } catch (error) {
    const code = (error as NodeJS.ErrnoException).code;
    if (
      process.platform === "win32" &&
      (code === "EPERM" || code === "EEXIST" || code === "EACCES")
    ) {
      await new Promise((resolve) => setTimeout(resolve, 120));
      try {
        await rename(temp, path);
        return;
      } catch (retryError) {
        await rm(temp, { force: true }).catch(() => undefined);
        throw retryError;
      }
    }
    await rm(temp, { force: true }).catch(() => undefined);
    throw error;
  }
}

export async function atomicDelete(path: string): Promise<void> {
  await rm(path, { force: true });
}

async function isSymlink(path: string): Promise<boolean> {
  try {
    return (await lstat(path)).isSymbolicLink();
  } catch {
    return false;
  }
}

/**
 * File mode for journal/restore parity (`undefined` on platforms without POSIX modes).
 */
export async function fileMode(path: string): Promise<number | undefined> {
  if (process.platform === "win32") return undefined;
  try {
    return (await stat(path)).mode & 0o777;
  } catch {
    return undefined;
  }
}

async function syncPath(path: string): Promise<void> {
  const handle = await open(path, "r+");
  try {
    await handle.sync();
  } finally {
    await handle.close();
  }
}

/** Restores POSIX mode bits when the platform has them (Windows has none). */
export async function applyMode(path: string, mode: number | undefined): Promise<void> {
  if (mode === undefined || process.platform === "win32") return;
  await chmod(path, mode).catch(() => undefined);
}

export interface JournalEntry {
  path: string;
  /** Before-image (undefined when the file did not exist). */
  before: Buffer | undefined;
  /** Mode bits of the before-image, when it existed. */
  beforeMode?: number;
}

/**
 * Reverse journal for a file rewind (old CLI semantics: write all, on any failure restore the
 * already-written paths in reverse order and rethrow).
 */
export class ReverseJournal {
  private readonly entries: JournalEntry[] = [];

  constructor(
    readonly root: string,
    private readonly logger: Logger,
  ) {}

  record(entry: JournalEntry): void {
    this.entries.push(entry);
  }

  get size(): number {
    return this.entries.length;
  }

  /** Compensates in reverse order; failures are logged, never thrown over the original. */
  async compensate(): Promise<string[]> {
    const failures: string[] = [];
    for (let index = this.entries.length - 1; index >= 0; index -= 1) {
      const entry = this.entries[index];
      if (!entry) continue;
      try {
        if (entry.before === undefined) await atomicDelete(entry.path);
        else {
          await atomicWriteFile(entry.path, entry.before, { replaceSymlink: true });
          await applyMode(entry.path, entry.beforeMode);
        }
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        failures.push(`${entry.path}: ${message}`);
        this.logger.warn("rewind compensation failed", { path: entry.path, message });
      }
    }
    return failures;
  }
}
