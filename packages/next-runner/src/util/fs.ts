import { mkdir, rename, writeFile } from "node:fs/promises";
import { dirname } from "node:path";

/** Atomically replace a small JSON file (async fs only). */
export async function writeJsonAtomic(path: string, value: unknown): Promise<void> {
  await mkdir(dirname(path), { recursive: true });
  const tempPath = `${path}.${process.pid}.${Date.now()}.tmp`;
  await writeFile(tempPath, `${JSON.stringify(value, null, 2)}\n`, "utf8");
  await rename(tempPath, path);
}

/**
 * Serializes async tasks per key. The runner is the single writer of its metadata files,
 * so a promise chain is enough to keep read-modify-write cycles ordered.
 */
export class KeyedQueue {
  private readonly tails = new Map<string, Promise<unknown>>();

  run<T>(key: string, task: () => Promise<T>): Promise<T> {
    const previous = this.tails.get(key) ?? Promise.resolve();
    const next = previous.catch(() => undefined).then(task);
    this.tails.set(
      key,
      next.catch(() => undefined),
    );
    return next;
  }
}
