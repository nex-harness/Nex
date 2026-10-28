import type { Engine } from "../engine/types.js";
import type { Logger } from "../logger.js";
import { buildSearchSnippets } from "./taskSearchSnippets.js";

/** 单个会话参与搜索的正文上限（字符），与引擎侧的截断保持一致。 */
const SESSION_SEARCH_TEXT_MAX_CHARS = 100_000;
/** 契约默认值：limit 默认 80，上限 200。 */
const SEARCH_DEFAULT_LIMIT = 80;
const SEARCH_MAX_LIMIT = 200;
/** 短于该长度的查询只匹配标题（命令中心的既有行为）。 */
const SEARCH_TITLE_ONLY_QUERY_LENGTH = 2;

export interface TaskSearchMatch {
  taskId: string;
  searchSnippet?: string;
  searchSnippets?: string[];
}

export interface TaskSearchResult {
  matches: TaskSearchMatch[];
  total: number;
  hasMore: boolean;
}

interface IndexEntry {
  taskId: string;
  titleLower: string;
  /** 原始大小写的正文，用于截取摘要窗口。 */
  text: string;
  textLower: string;
  /** 行更新时间（`lastActivityAt`），匹配结果按它倒序。 */
  updatedAt: number;
}

interface WorkspaceIndex {
  /** 已按 updatedAt 倒序排好，搜索路径是纯内存扫描。 */
  entries: IndexEntry[];
}

/**
 * 每个 workspace 一份的内存搜索索引（docs/next/automations-views-search.md §C）。
 *
 * - 首次搜索懒构建；同一 workspace 的并发搜索共享同一个构建 promise。
 * - `engine.onSessionsChanged(workspacePath)` 触发失效；构建过程中失效的那次结果会被
 *   丢弃（用 generation 计数判断），避免把旧数据写回索引。
 * - 预热后查询只做内存扫描：标题与正文都缓存了小写副本，命中判定不再逐条 lowercase。
 */
export class TaskSearchIndex {
  private readonly indexes = new Map<string, WorkspaceIndex>();
  private readonly builds = new Map<string, Promise<WorkspaceIndex>>();
  private readonly generations = new Map<string, number>();

  constructor(
    private readonly engine: Engine,
    private readonly logger: Logger,
  ) {}

  /** 失效一个 workspace 的索引；进行中的构建结果也会被 generation 检查丢弃。 */
  invalidate(workspacePath: string): void {
    this.generations.set(workspacePath, (this.generations.get(workspacePath) ?? 0) + 1);
    this.indexes.delete(workspacePath);
    this.builds.delete(workspacePath);
  }

  /** Channel dispose：丢掉所有缓存。先抬高每个 workspace 的 generation，进行中的构建
   *  完成后就不会再写回已清空的索引。 */
  clear(): void {
    for (const workspacePath of this.generations.keys()) {
      this.generations.set(workspacePath, (this.generations.get(workspacePath) ?? 0) + 1);
    }
    this.indexes.clear();
    this.builds.clear();
  }

  async search(params: {
    workspacePath: string;
    query: string;
    limit?: number;
  }): Promise<TaskSearchResult> {
    const trimmed = params.query.trim();
    if (trimmed.length === 0) return { matches: [], total: 0, hasMore: false };
    const needle = trimmed.toLowerCase();
    const titleOnly = needle.length < SEARCH_TITLE_ONLY_QUERY_LENGTH;
    const index = await this.ensureIndex(params.workspacePath);

    const found: TaskSearchMatch[] = [];
    for (const entry of index.entries) {
      const titleHit = entry.titleLower.includes(needle);
      if (titleOnly) {
        if (titleHit) found.push({ taskId: entry.taskId });
        continue;
      }
      const textHit = entry.textLower.includes(needle);
      if (!titleHit && !textHit) continue;
      if (!textHit) {
        // 仅标题命中（或正文无命中）不带 snippet 字段。
        found.push({ taskId: entry.taskId });
        continue;
      }
      const snippets = buildSearchSnippets(entry.text, trimmed);
      found.push(
        snippets.length > 0
          ? { taskId: entry.taskId, searchSnippet: snippets[0], searchSnippets: snippets }
          : { taskId: entry.taskId },
      );
    }

    const limit = clampLimit(params.limit);
    const matches = found.slice(0, limit);
    return { matches, total: found.length, hasMore: found.length > matches.length };
  }

  private ensureIndex(workspacePath: string): Promise<WorkspaceIndex> {
    const existing = this.indexes.get(workspacePath);
    if (existing) return Promise.resolve(existing);
    const building = this.builds.get(workspacePath);
    if (building) return building;

    const generation = this.generations.get(workspacePath) ?? 0;
    const build = this.build(workspacePath)
      .then((index) => {
        // 构建期间会话发生了变化：这份结果可能已过期，不能写回缓存（否则失效被吞掉，
        // 后续搜索会一直读到旧正文）。本次仍返回它，下一次搜索会按当前状态重建。
        if ((this.generations.get(workspacePath) ?? 0) === generation) {
          this.indexes.set(workspacePath, index);
        }
        return index;
      })
      .finally(() => {
        if (this.builds.get(workspacePath) === build) this.builds.delete(workspacePath);
      });
    this.builds.set(workspacePath, build);
    return build;
  }

  private async build(workspacePath: string): Promise<WorkspaceIndex> {
    const [sessions, texts] = await Promise.all([
      this.engine.listSessions(workspacePath),
      this.engine.listSessionSearchText(workspacePath).catch((error: unknown) => {
        // 正文不可读时仍保留标题搜索，而不是让整个查询失败。
        this.logger.warn("task search text read failed", {
          workspacePath,
          message: error instanceof Error ? error.message : String(error),
        });
        return new Map<string, string>();
      }),
    ]);
    const entries: IndexEntry[] = [];
    for (const session of sessions) {
      // 只有真实会话参与搜索：draft 尚未成为任务，deleted 已被 listSessions 排除。
      if (session.phase === "draft") continue;
      const text = (texts.get(session.sessionId) ?? "").slice(0, SESSION_SEARCH_TEXT_MAX_CHARS);
      entries.push({
        taskId: session.sessionId,
        titleLower: session.title.toLowerCase(),
        text,
        textLower: text.toLowerCase(),
        updatedAt: session.lastActivityAt,
      });
    }
    // 排序一次，之后每次查询都是纯内存扫描。
    entries.sort((a, b) => b.updatedAt - a.updatedAt);
    return { entries };
  }
}

function clampLimit(value: number | undefined): number {
  if (value === undefined || !Number.isFinite(value)) return SEARCH_DEFAULT_LIMIT;
  return Math.min(SEARCH_MAX_LIMIT, Math.max(1, Math.floor(value)));
}
