import { randomBytes } from "node:crypto";
import { mkdir, rename, writeFile } from "node:fs/promises";
import { dirname } from "node:path";

/**
 * A temp name next to `path` that no concurrent writer shares.
 *
 * 修复依据：旧名字是 `<path>.<pid>.<ms>.tmp`。同一进程里两个写者（例如恢复时新建的第二个
 * 引擎实例与旧实例各有自己的串行队列）在同一毫秒写同一文件会拿到同名 temp：一个 rename
 * 之后另一个 rename 报 ENOENT/EINVAL。加随机后缀让每次写都有独立 temp。
 */
export function uniqueTempPath(path: string): string {
  return `${path}.${process.pid}.${Date.now()}.${randomBytes(4).toString("hex")}.tmp`;
}

/** Atomically replace a small JSON file (async fs only). */
export async function writeJsonAtomic(path: string, value: unknown): Promise<void> {
  await mkdir(dirname(path), { recursive: true });
  const tempPath = uniqueTempPath(path);
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
