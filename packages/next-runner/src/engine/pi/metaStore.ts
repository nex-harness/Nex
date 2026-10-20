import { readFile } from "node:fs/promises";
import { join } from "node:path";
import type { Logger } from "../../logger.js";
import { KeyedQueue, writeJsonAtomic } from "../../util/fs.js";
import type {
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
  seenRequestIds?: string[];
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
    patch: Partial<Omit<SessionMetaEntry, "sessionId">> & { seenRequestId?: string },
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
      if (index === -1) file.sessions.push(next);
      else file.sessions[index] = next;
      await this.write(workspacePath, file);
    });
  }

  markDeleted(workspacePath: string, sessionId: string): Promise<void> {
    return this.upsert(workspacePath, sessionId, {
      deleted: true,
      lastActivityAt: Date.now(),
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
