/**
 * Runner-owned served folder list (docs/next/runner-folders.md decision 1).
 *
 * The runner is the only owner of which folders it serves: the env roots
 * (`NEX_RUNNER_WORKSPACES`, never removable) plus the folders persisted in
 * `<dataDir>/workspaces.json`. Every consumer (channel guard, file-tools root
 * check, nex-task workspace-less lists, hello, `runner-workspaces`) reads this
 * live object instead of a startup array, so an added folder works without a
 * restart.
 *
 * The file is written atomically (temp file + rename) and read-modify-write
 * cycles are serialized, because a lost update would silently drop a folder.
 */
import { constants as fsConstants } from "node:fs";
import { access, readFile, realpath, stat } from "node:fs/promises";
import { isAbsolute, resolve } from "node:path";
import { labelForWorkspace } from "../config.js";
import type { Logger } from "../logger.js";
import { KeyedQueue, writeJsonAtomic } from "../util/fs.js";

export interface WorkspaceFolder {
  path: string;
  label: string;
  removable: boolean;
}

interface PersistedFolder {
  path: string;
  addedAt: string;
}

interface PersistedFile {
  version: 1;
  folders: PersistedFolder[];
}

const FILE_NAME = "workspaces.json";
const WRITE_KEY = "workspaces.json";

export interface WorkspaceRootsOptions {
  /** Absolute env roots from `NEX_RUNNER_WORKSPACES`; never removable from the UI. */
  envRoots: readonly string[];
  dataDir: string;
  logger: Logger;
}

export class WorkspaceRoots {
  private readonly envRoots: readonly string[];
  private readonly filePath: string;
  private readonly logger: Logger;
  private readonly writes = new KeyedQueue();
  private readonly listeners = new Set<() => void>();
  private persisted: readonly PersistedFolder[] = [];
  private realRootsCache: readonly string[] | null = null;
  private envRealRootsCache: readonly string[] | null = null;

  constructor(options: WorkspaceRootsOptions) {
    this.envRoots = dedupe(options.envRoots.map((root) => resolve(root)));
    this.filePath = resolve(options.dataDir, FILE_NAME);
    this.logger = options.logger;
  }

  /**
   * Reads the persisted folders. A missing file is the normal first start; an
   * unreadable or malformed file is treated as empty (warned), because serving
   * a guess is worse than serving the env roots alone.
   */
  async load(): Promise<void> {
    let text: string;
    try {
      text = await readFile(this.filePath, "utf8");
    } catch (error) {
      const code = (error as NodeJS.ErrnoException).code;
      if (code !== "ENOENT") {
        this.logger.warn("workspaces.json is unreadable; serving env roots only", {
          path: this.filePath,
          code: code ?? "unknown",
        });
      }
      this.persisted = [];
      this.invalidate();
      return;
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = undefined;
    }
    const folders = readPersistedFolders(parsed);
    if (folders === null) {
      this.logger.warn("workspaces.json is corrupt; serving env roots only", {
        path: this.filePath,
      });
      this.persisted = [];
      this.invalidate();
      return;
    }
    this.persisted = folders;
    this.invalidate();
  }

  /** Env roots first, then persisted folders, de-duplicated by resolved path. */
  list(): WorkspaceFolder[] {
    const result: WorkspaceFolder[] = [];
    const seen = new Set<string>();
    for (const path of this.envRoots) {
      if (seen.has(path)) continue;
      seen.add(path);
      result.push({ path, label: labelForWorkspace(path), removable: false });
    }
    for (const folder of this.persisted) {
      const path = resolve(folder.path);
      if (seen.has(path)) continue;
      seen.add(path);
      result.push({ path, label: labelForWorkspace(path), removable: true });
    }
    return result;
  }

  has(candidate: string): boolean {
    const target = resolve(candidate);
    return this.list().some((folder) => resolve(folder.path) === target);
  }

  /** Realpath + lexical forms of every served root (containment checks use both). */
  async realRoots(): Promise<readonly string[]> {
    if (this.realRootsCache === null) {
      const roots = this.list().map((folder) => folder.path);
      const real = await Promise.all(
        roots.map(async (root) => {
          try {
            return await realpath(root);
          } catch {
            return resolve(root);
          }
        }),
      );
      this.realRootsCache = dedupe([
        ...real.map((path) => resolve(path)),
        ...roots.map((path) => resolve(path)),
      ]);
    }
    return this.realRootsCache;
  }

  /**
   * Validates and persists one folder. Absolute, existing, directory, readable;
   * stored as its realpath so a later containment check matches what the OS
   * resolves. Adding an already served folder is a no-op that returns the
   * existing entry.
   */
  async add(rawPath: string): Promise<WorkspaceFolder> {
    const target = await validateFolder(rawPath);
    return this.writes.run(WRITE_KEY, async () => {
      const existing = await this.findServed(target);
      if (existing) return existing;
      const next: PersistedFolder[] = [
        ...this.persisted,
        { path: target, addedAt: new Date().toISOString() },
      ];
      // Persist first: a failed write must not leave an in-memory folder the
      // runner would lose on restart.
      await this.writePersisted(next);
      this.persisted = next;
      this.invalidate();
      this.emit();
      return { path: target, label: labelForWorkspace(target), removable: true };
    });
  }

  /** Removes a persisted folder; env roots are refused (spec decision 1). */
  async remove(rawPath: string): Promise<void> {
    const target = resolve(rawPath);
    if (await this.isEnvRoot(target)) {
      throw new Error(`env workspace cannot be removed: ${rawPath}`);
    }
    // 修复依据：持久化条目存的是 realpath，调用方可能用 symlink 形式删除；
    // 两种形式都要匹配，否则删除会静默 no-op。
    const targetReal = await realpath(target).catch(() => null);
    await this.writes.run(WRITE_KEY, async () => {
      const removed = this.persisted.filter((folder) => {
        const stored = resolve(folder.path);
        return stored !== target && (targetReal === null || stored !== targetReal);
      });
      // 幂等：不在列表里就什么都不做（UI 可能重试同一删除）。
      if (removed.length === this.persisted.length) return;
      await this.writePersisted(removed);
      this.persisted = removed;
      this.invalidate();
      this.emit();
    });
  }

  /** Notifies on add/remove; returns the unsubscribe function. */
  onChange(listener: () => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  private async findServed(target: string): Promise<WorkspaceFolder | undefined> {
    const folders = this.list();
    const lexical = folders.find((folder) => resolve(folder.path) === target);
    if (lexical) return lexical;
    // macOS 的 /var 与 /private/var 是同一目录：env 根可能以 symlink 形式配置，
    // 用户从机器浏览里选中的却是 realpath。按 realpath 再比一次，避免同一目录
    // 以两条记录出现。
    let real: string;
    try {
      real = await realpath(target);
    } catch {
      return undefined;
    }
    for (const folder of folders) {
      try {
        if ((await realpath(folder.path)) === real) return folder;
      } catch {
        continue;
      }
    }
    return undefined;
  }

  private async isEnvRoot(target: string): Promise<boolean> {
    if (this.envRoots.includes(target)) return true;
    let real: string;
    try {
      real = await realpath(target);
    } catch {
      return false;
    }
    if (this.envRealRootsCache === null) {
      this.envRealRootsCache = await Promise.all(
        this.envRoots.map(async (root) => {
          try {
            return await realpath(root);
          } catch {
            return resolve(root);
          }
        }),
      );
    }
    return this.envRealRootsCache.includes(real);
  }

  private async writePersisted(folders: readonly PersistedFolder[]): Promise<void> {
    await writeJsonAtomic(this.filePath, {
      version: 1,
      folders: [...folders],
    } satisfies PersistedFile);
  }

  private invalidate(): void {
    this.realRootsCache = null;
  }

  private emit(): void {
    for (const listener of this.listeners) {
      try {
        listener();
      } catch (error) {
        this.logger.warn("workspace roots listener failed", {
          message: error instanceof Error ? error.message : String(error),
        });
      }
    }
  }
}

async function validateFolder(rawPath: string): Promise<string> {
  if (!isAbsolute(rawPath)) {
    throw new Error(`workspace path must be absolute: ${rawPath}`);
  }
  const resolved = resolve(rawPath);
  let info: Awaited<ReturnType<typeof stat>>;
  try {
    info = await stat(resolved);
  } catch {
    throw new Error(`workspace path does not exist: ${rawPath}`);
  }
  if (!info.isDirectory()) {
    throw new Error(`workspace path is not a directory: ${rawPath}`);
  }
  try {
    await access(resolved, fsConstants.R_OK);
  } catch {
    throw new Error(`workspace path is not readable: ${rawPath}`);
  }
  try {
    return await realpath(resolved);
  } catch {
    throw new Error(`workspace path cannot be resolved: ${rawPath}`);
  }
}

function readPersistedFolders(value: unknown): PersistedFolder[] | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  if (record["version"] !== 1) return null;
  const folders = record["folders"];
  if (!Array.isArray(folders)) return null;
  const result: PersistedFolder[] = [];
  for (const folder of folders) {
    if (typeof folder !== "object" || folder === null || Array.isArray(folder)) return null;
    const entry = folder as Record<string, unknown>;
    const path = entry["path"];
    const addedAt = entry["addedAt"];
    if (typeof path !== "string" || !isAbsolute(path)) return null;
    if (typeof addedAt !== "string") return null;
    result.push({ path: resolve(path), addedAt });
  }
  return result;
}

function dedupe(values: readonly string[]): string[] {
  return [...new Set(values)];
}
