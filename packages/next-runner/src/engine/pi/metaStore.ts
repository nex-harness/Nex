import { readFile } from "node:fs/promises";
import { join } from "node:path";
import type { Logger } from "../../logger.js";
import { KeyedQueue, writeJsonAtomic } from "../../util/fs.js";
import type {
  EngineFollowupMode,
  EngineModelState,
  EnginePhase,
  EngineTitleSource,
  EngineUsage,
  PermissionMode,
} from "../types.js";

export interface SessionMetaEntry {
  sessionId: string;
  createdAt: number;
  lastActivityAt: number;
  phase: EnginePhase;
  title: string;
  titleSource: EngineTitleSource;
  deleted: boolean;
  model?: EngineModelState;
  usage?: EngineUsage;
  /** Last submission's permission mode; seeds `snapshot.config.mode` after a restart. */
  mode?: PermissionMode;
  planEnabled?: boolean;
  /** Composer follow-up mode (`queue` | `guide`); absent = runner default. */
  followupMode?: EngineFollowupMode;
  /**
   * Newest successful manual compaction: the pi entry it wrote and the command that asked
   * for it. pi's session file records no command id, so restore reads this to keep the
   * marker's `origin:"manual"` and `sourceCommandId` across a runner restart.
   */
  lastCompact?: { entryId: string; sourceCommandId: string };
  seenRequestIds?: string[];
  /**
   * Requests that ran on a session built before the latest MCP/config generation
   * (P1). A later submission of the same id is not a duplicate: its own run never
   * reached the model, it is the retry the client sends after the config push.
   */
  staleRequestIds?: string[];
  /**
   * Sidebar organization state (docs/next/task-state.md). Files written by older runners
   * lack these keys; missing means "not pinned / not archived / read" everywhere.
   */
  pinned?: boolean;
  pinnedAt?: number;
  archived?: boolean;
  archivedAt?: number;
  /** Newest unread background completion; missing = read. */
  unreadAt?: number;
  /**
   * 派发该会话的 automation（createSession.firstInput.automationId）。随 meta 持久化，
   * 供 task rows 输出 `NexTaskMeta.cronAutomationId`，也是“无人值守”的唯一判据。
   */
  automationId?: string;
}

/** Sidebar state patch. `unreadAt: null` clears the field (undefined = leave unchanged). */
export interface TaskStatePatch {
  pinned?: boolean;
  archived?: boolean;
  unreadAt?: number | null;
  title?: string;
  titleSource?: EngineTitleSource;
}

interface WorkspaceMetaFile {
  version: 1;
  workspacePath: string;
  sessions: SessionMetaEntry[];
}

const MAX_SEEN_REQUEST_IDS = 128;

/**
 * Per-workspace metadata owned exclusively by the runner (single writer). pi session
 * files remain the transcript source of truth; this file adds title/phase/tombstones,
 * which pi does not model.
 */
export class SessionMetaStore {
  private readonly cache = new Map<string, WorkspaceMetaFile>();
  private readonly queue = new KeyedQueue();
  private readonly dir: string;

  constructor(
    dataDir: string,
    private readonly logger: Logger,
  ) {
    this.dir = join(dataDir, "workspaces");
  }

  async load(): Promise<void> {
    // Files are loaded lazily per workspace; nothing global to read here.
  }

  async list(workspacePath: string): Promise<SessionMetaEntry[]> {
    const file = await this.read(workspacePath);
    return [...file.sessions];
  }

  async get(workspacePath: string, sessionId: string): Promise<SessionMetaEntry | undefined> {
    const file = await this.read(workspacePath);
    return file.sessions.find((entry) => entry.sessionId === sessionId);
  }

  upsert(
    workspacePath: string,
    sessionId: string,
    patch: Partial<Omit<SessionMetaEntry, "sessionId">> & {
      seenRequestId?: string;
      /** Merge (never replace) ids whose run never reached the model. */
      staleRequestIds?: string[];
    },
  ): Promise<void> {
    return this.queue.run(workspacePath, async () => {
      const file = await this.read(workspacePath);
      const index = file.sessions.findIndex((entry) => entry.sessionId === sessionId);
      const current: SessionMetaEntry =
        index === -1
          ? {
              sessionId,
              createdAt: patch.createdAt ?? Date.now(),
              lastActivityAt: patch.lastActivityAt ?? Date.now(),
              phase: patch.phase ?? "draft",
              title: patch.title ?? "",
              titleSource: patch.titleSource ?? "default",
              deleted: false,
            }
          : (file.sessions[index] as SessionMetaEntry);
      const next: SessionMetaEntry = {
        ...current,
        ...stripSeenRequestId(patch),
        sessionId,
      };
      if (patch.seenRequestId) {
        const ids = [...(current.seenRequestIds ?? []), patch.seenRequestId];
        next.seenRequestIds = ids.slice(-MAX_SEEN_REQUEST_IDS);
      }
      const stale = patch.staleRequestIds;
      if (stale) {
        const known = new Set([...(current.staleRequestIds ?? []), ...stale]);
        // A request that later runs for real is no longer stale.
        for (const id of next.seenRequestIds ?? []) known.delete(id);
        next.staleRequestIds = [...known].slice(-MAX_SEEN_REQUEST_IDS);
      }
      if (index === -1) file.sessions.push(next);
      else file.sessions[index] = next;
      await this.write(workspacePath, file);
    });
  }

  /**
   * 同步查询：该会话是否由 automation 派发。会话文件按 workspace 分文件、懒加载，
   * 且 `get/upsert` 是异步的；这里只读已载入的缓存，未命中返回 false（调用方据此
   * 判定“普通会话”）。仅供构建 pi 会话时判定无人值守，不作为业务读接口。
   */
  hasAutomationId(sessionId: string): boolean {
    for (const file of this.cache.values()) {
      const entry = file.sessions.find((item) => item.sessionId === sessionId);
      if (entry) return entry.automationId !== undefined;
    }
    return false;
  }

  markDeleted(workspacePath: string, sessionId: string): Promise<void> {
    return this.upsert(workspacePath, sessionId, {
      deleted: true,
      lastActivityAt: Date.now(),
    });
  }

  /**
   * Runs one read-modify-write inside the workspace queue and returns the recipe's result.
   *
   * 修复依据：归档条件删除和未读 compare-and-clear 的“检查 + 写入”必须与其它写入
   * 串行（原 tasks-index 用同一写事务保证这一点）。分成先 get 再 upsert 会让另一端
   * 的 unarchive 插进中间，删掉刚恢复的任务。recipe 返回 `{ next: null }` 表示放弃写入。
   */
  update<T>(
    workspacePath: string,
    sessionId: string,
    recipe: (current: SessionMetaEntry | undefined) => { next: SessionMetaEntry | null; result: T },
  ): Promise<T> {
    return this.queue.run(workspacePath, async () => {
      const file = await this.read(workspacePath);
      const index = file.sessions.findIndex((entry) => entry.sessionId === sessionId);
      const outcome = recipe(index === -1 ? undefined : (file.sessions[index] as SessionMetaEntry));
      if (!outcome.next) return outcome.result;
      if (index === -1) file.sessions.push(outcome.next);
      else file.sessions[index] = outcome.next;
      await this.write(workspacePath, file);
      return outcome.result;
    });
  }

  private async read(workspacePath: string): Promise<WorkspaceMetaFile> {
    const cached = this.cache.get(workspacePath);
    if (cached) return cached;
    const path = this.filePath(workspacePath);
    try {
      const text = await readFile(path, "utf8");
      const parsed = JSON.parse(text) as WorkspaceMetaFile;
      if (parsed && Array.isArray(parsed.sessions)) {
        this.cache.set(workspacePath, parsed);
        return parsed;
      }
    } catch (error) {
      const code = (error as NodeJS.ErrnoException).code;
      if (code !== "ENOENT") {
        this.logger.warn("session metadata read failed", {
          workspacePath,
          message: error instanceof Error ? error.message : String(error),
        });
      }
    }
    const empty: WorkspaceMetaFile = { version: 1, workspacePath, sessions: [] };
    this.cache.set(workspacePath, empty);
    return empty;
  }

  private async write(workspacePath: string, file: WorkspaceMetaFile): Promise<void> {
    this.cache.set(workspacePath, file);
    await writeJsonAtomic(this.filePath(workspacePath), file);
  }

  private filePath(workspacePath: string): string {
    // Workspace paths are absolute and unique; encode to a safe file name.
    const encoded = Buffer.from(workspacePath, "utf8").toString("base64url");
    return join(this.dir, `${encoded}.json`);
  }
}

function stripSeenRequestId(
  patch: Partial<Omit<SessionMetaEntry, "sessionId">> & { seenRequestId?: string },
): Partial<Omit<SessionMetaEntry, "sessionId">> {
  const { seenRequestId: _seenRequestId, ...rest } = patch;
  return rest;
}
