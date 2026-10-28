import type { NexTaskMeta, NexWorkspaceEvent } from "@nex/shared";
import { nexTaskMetaSchema } from "@nex/shared";
import type { Engine, EngineSessionState } from "../engine/types.js";
import type { Logger } from "../logger.js";
import { diffTaskSnapshots, isProjectRow, isTerminalPhase, toNexTaskMeta } from "./nexTaskRows.js";
import { TaskSearchIndex, type TaskSearchResult } from "./taskSearchIndex.js";

interface WorkspaceSnapshot {
  deleted: string[];
  /** Every non-draft session (archived/pinned included) so transitions stay diffable. */
  byId: Map<string, EngineSessionState>;
  /** Identity the UI used for this scope; rows must echo it for workspaceKey grouping. */
  workspaceIdentity?: string;
}

export interface NexTaskChannelOptions {
  /** Every workspace this runner serves; workspace-less list calls span them. */
  workspacePaths?: readonly string[];
  /**
   * True while a browser is subscribed to the session's conversation. A terminal run in
   * the foreground must not raise an unread badge (docs/next/task-state.md).
   */
  isSessionWatched?: (sessionId: string) => boolean;
}

interface TaskTarget {
  taskId: string;
  workspacePath: string;
  workspaceIdentity?: string;
}

/**
 * `nex-task` is the sidebar's row source and the runner-side owner of sidebar task state
 * (docs/next/task-state.md). Rows derive from the engine's metadata store; every mutation
 * goes through the engine, and its `onSessionsChanged` diff emits the
 * `workspace_task_list_changed` reasons the UI's refresh policy expects:
 *
 * - `task_created` / `task_deleted` — membership add/remove
 * - `task_archived` / `task_unarchived` / `task_pinned` / `task_unpinned` — membership move
 * - `task_title_changed` — detail only (title)
 * - `task_meta_changed` — unread/activity (membership refetch, low frequency)
 * - `task_status_changed` — phase; `unreadSignal: "background_terminal"` for a completion
 *   the user was not watching
 *
 * The calls carry a `workspaceIdentity` (`runner:<id>:<path>`) from the server's routing
 * table. Rows and events must echo it: the UI groups sidebar rows by
 * `workspaceIdentity?.trim() || workspacePath`, so a scope opened with an identity would
 * otherwise never match its rows.
 */
export class NexTaskChannel {
  private readonly snapshots = new Map<string, WorkspaceSnapshot>();
  private readonly watchers = new Map<string, Set<(event: NexWorkspaceEvent) => void>>();
  private readonly identities = new Map<string, string | undefined>();
  /** Serializes refreshes per workspace: a mutation emits re-entrantly via the engine. */
  private readonly refreshTails = new Map<string, Promise<void>>();
  private readonly engineUnsubscribe: () => void;
  private readonly workspacePaths: readonly string[];
  private readonly isSessionWatched: (sessionId: string) => boolean;
  /** 全文搜索索引（docs/next/automations-views-search.md §C）：每 workspace 一份、懒构建。 */
  private readonly searchIndex: TaskSearchIndex;

  constructor(
    private readonly engine: Engine,
    private readonly logger: Logger,
    options: NexTaskChannelOptions = {},
  ) {
    this.workspacePaths = options.workspacePaths ?? [];
    this.isSessionWatched = options.isSessionWatched ?? (() => false);
    // 索引必须在订阅之前就绪：`onSessionsChanged` 的第一次回调会调用 `invalidate`。
    this.searchIndex = new TaskSearchIndex(engine, logger);
    this.engineUnsubscribe = engine.onSessionsChanged((workspacePath) => {
      // 搜索索引与会话内容同源：任何元数据变更（新建/重命名/删除/新消息）都要失效，
      // 否则命令中心会拿到旧标题/旧正文。放在 refresh 之前，避免并发搜索读到过期索引。
      this.searchIndex.invalidate(workspacePath);
      this.scheduleRefresh(workspacePath, this.identities.get(workspacePath));
    });
  }

  // ── reads ──

  /** Project list: real (non-draft), live rows that are neither pinned nor archived. */
  async listTasks(params: {
    workspacePath: string;
    workspaceIdentity?: string;
  }): Promise<NexTaskMeta[]> {
    this.rememberIdentity(params.workspacePath, params.workspaceIdentity);
    const sessions = await this.engine.listSessions(params.workspacePath);
    return sessions
      .filter(isProjectRow)
      .map((session) => toNexTaskMeta(params.workspacePath, session, params.workspaceIdentity));
  }

  async listPinnedTasks(params?: {
    workspacePath?: string;
    workspaceIdentity?: string;
  }): Promise<NexTaskMeta[]> {
    this.rememberIdentity(params?.workspacePath, params?.workspaceIdentity);
    return this.collectTasks(params, (session) => session.pinned === true && !session.archived);
  }

  async listArchivedTasks(params?: {
    workspacePath?: string;
    workspaceIdentity?: string;
  }): Promise<NexTaskMeta[]> {
    this.rememberIdentity(params?.workspacePath, params?.workspaceIdentity);
    return this.collectTasks(params, (session) => session.archived === true);
  }

  /** Pinned ids across every served workspace (supplemental hint; see the spec). */
  async listPinnedTaskIds(): Promise<string[]> {
    const tasks = await this.collectTasks(
      undefined,
      (session) => session.pinned === true && !session.archived,
    );
    return tasks.map((task) => task.taskId);
  }

  async listDeletedTaskIds(params: { workspacePath: string }): Promise<string[]> {
    return this.engine.listDeletedSessionIds(params.workspacePath);
  }

  /**
   * 全文搜索（docs/next/automations-views-search.md §C）：标题 + 可见消息正文。
   *
   * 返回的只有会话 id 和摘要片段 —— 行数据（标题/状态/pinned/archived/排序键）仍由
   * server 的行缓存拥有，这里不做投影。pinned/archived 也参与搜索（与侧栏一致），
   * draft/deleted 不参与（`listSessions` 已排除 deleted，索引构建再排除 draft）。
   */
  async searchTasks(params: {
    workspacePath: string;
    workspaceIdentity?: string;
    query: string;
    limit?: number;
  }): Promise<TaskSearchResult> {
    this.rememberIdentity(params.workspacePath, params.workspaceIdentity);
    return this.searchIndex.search({
      workspacePath: params.workspacePath,
      query: params.query,
      ...(params.limit !== undefined ? { limit: params.limit } : {}),
    });
  }

  /**
   * 任务右键菜单会无条件读取这两个路径（useTaskSessionFilePath /
   * useTaskNativeSessionLogFile）。pi 的 session 文件按 workspace 摘要目录存放，
   * 没有 UI 约定的可展示路径，返回空路径让菜单隐藏入口即可；不要抛
   * "Unknown method"，那会在每次打开会话时刷出 warn 日志。
   */
  async getTaskSessionFilePath(): Promise<{ path: string; exists: boolean }> {
    return { path: "", exists: false };
  }

  async getTaskNativeSessionLogFile(): Promise<{
    provider: null;
    path: null;
    exists: boolean;
  }> {
    return { provider: null, path: null, exists: false };
  }

  // ── mutations (docs/next/task-state.md) ──

  async archiveTask(params: TaskTarget): Promise<NexTaskMeta> {
    return this.writeState(params, { archived: true });
  }

  async unarchiveTask(params: TaskTarget): Promise<NexTaskMeta> {
    return this.writeState(params, { archived: false });
  }

  async setTaskPinned(params: TaskTarget & { pinned: boolean }): Promise<NexTaskMeta> {
    return this.writeState(params, { pinned: params.pinned });
  }

  async setTaskUnread(
    params: TaskTarget & { unread: boolean; expectedUnreadAt?: number },
  ): Promise<NexTaskMeta> {
    this.rememberIdentity(params.workspacePath, params.workspaceIdentity);
    await this.engine.setTaskUnread(
      params.workspacePath,
      params.taskId,
      params.unread ? Date.now() : null,
      params.unread ? undefined : params.expectedUnreadAt,
    );
    return this.readTaskMeta(params);
  }

  /**
   * Same write path as v4 `renameSession`: engine.rename writes the metadata row and the pi
   * session name, then fires one `onSessionsChanged` that republishes the sessions index,
   * patches an open conversation snapshot and drives `task_title_changed` here.
   */
  async renameTask(params: TaskTarget & { title: string }): Promise<NexTaskMeta> {
    this.rememberIdentity(params.workspacePath, params.workspaceIdentity);
    // A restored-but-unopened session must be open before pi can hold the new name.
    await this.engine.openSession(params.workspacePath, params.taskId);
    await this.engine.rename(params.taskId, params.title);
    return this.readTaskMeta(params);
  }

  /** Soft delete = the tombstone `deleteSession` writes; the transcript is removed too. */
  async deleteTask(params: TaskTarget): Promise<void> {
    this.rememberIdentity(params.workspacePath, params.workspaceIdentity);
    await this.engine.deleteSession(params.workspacePath, params.taskId);
  }

  async deleteArchivedTask(params: TaskTarget): Promise<boolean> {
    this.rememberIdentity(params.workspacePath, params.workspaceIdentity);
    return this.engine.deleteTaskIfArchived(params.workspacePath, params.taskId);
  }

  async deleteArchivedTasks(params: {
    workspacePath: string;
    workspaceIdentity?: string;
    taskIds: string[];
  }): Promise<{ deletedTaskIds: string[]; skippedTaskIds: string[]; failedTaskIds: string[] }> {
    this.rememberIdentity(params.workspacePath, params.workspaceIdentity);
    const result: {
      deletedTaskIds: string[];
      skippedTaskIds: string[];
      failedTaskIds: string[];
    } = { deletedTaskIds: [], skippedTaskIds: [], failedTaskIds: [] };
    for (const taskId of new Set(params.taskIds)) {
      try {
        // 逐项事务：一项失败不能回滚其它成功项，也不能把失败项提前从列表隐藏。
        if (await this.engine.deleteTaskIfArchived(params.workspacePath, taskId)) {
          result.deletedTaskIds.push(taskId);
        } else {
          result.skippedTaskIds.push(taskId);
        }
      } catch (error) {
        result.failedTaskIds.push(taskId);
        this.logger.warn("archived task delete failed", {
          workspacePath: params.workspacePath,
          taskId,
          message: error instanceof Error ? error.message : String(error),
        });
      }
    }
    return result;
  }

  // ── events ──

  onDynamicWorkspaceEvent(
    workspacePath: string,
    emit: (event: NexWorkspaceEvent) => void,
    workspaceIdentity?: string,
  ): { dispose(): void } {
    this.rememberIdentity(workspacePath, workspaceIdentity);
    const listeners = this.watchers.get(workspacePath) ?? new Set();
    listeners.add(emit);
    this.watchers.set(workspacePath, listeners);
    this.scheduleRefresh(workspacePath, workspaceIdentity);
    return {
      dispose: () => {
        listeners.delete(emit);
        if (listeners.size === 0) {
          this.watchers.delete(workspacePath);
          // 无观察者时丢掉基线：下次订阅从当前状态开始，不回放无人观察期间的变更。
          this.snapshots.delete(workspacePath);
        }
      },
    };
  }

  dispose(): void {
    this.engineUnsubscribe();
    this.watchers.clear();
    this.snapshots.clear();
    this.searchIndex.clear();
  }

  // ── internals ──

  private async writeState(
    params: TaskTarget,
    patch: { pinned?: boolean; archived?: boolean },
  ): Promise<NexTaskMeta> {
    this.rememberIdentity(params.workspacePath, params.workspaceIdentity);
    await this.engine.setTaskState(params.workspacePath, params.taskId, patch);
    return this.readTaskMeta(params);
  }

  /** Reads one row back after a write; the row must satisfy `nexTaskMetaSchema`. */
  private async readTaskMeta(params: TaskTarget): Promise<NexTaskMeta> {
    const sessions = await this.engine.listSessions(params.workspacePath);
    const session = sessions.find((item) => item.sessionId === params.taskId);
    if (!session) throw new Error(`Task ${params.taskId} not found`);
    return nexTaskMetaSchema.parse(
      toNexTaskMeta(params.workspacePath, session, params.workspaceIdentity),
    );
  }

  private async collectTasks(
    params: { workspacePath?: string; workspaceIdentity?: string } | undefined,
    predicate: (session: EngineSessionState) => boolean,
  ): Promise<NexTaskMeta[]> {
    const targets = params?.workspacePath
      ? [
          {
            workspacePath: params.workspacePath,
            workspaceIdentity:
              params.workspaceIdentity ?? this.identities.get(params.workspacePath),
          },
        ]
      : this.workspacePaths.map((workspacePath) => ({
          workspacePath,
          workspaceIdentity: this.identities.get(workspacePath),
        }));
    const results: NexTaskMeta[] = [];
    for (const target of targets) {
      try {
        const sessions = await this.engine.listSessions(target.workspacePath);
        for (const session of sessions) {
          if (session.phase === "draft" || !predicate(session)) continue;
          results.push(
            nexTaskMetaSchema.parse(
              toNexTaskMeta(target.workspacePath, session, target.workspaceIdentity),
            ),
          );
        }
      } catch (error) {
        // One unreadable workspace must not fail the whole (workspace-less) query.
        this.logger.warn("nex-task list failed", {
          workspacePath: target.workspacePath,
          message: error instanceof Error ? error.message : String(error),
        });
      }
    }
    return results;
  }

  private rememberIdentity(workspacePath: string | undefined, identity: string | undefined): void {
    if (!workspacePath || !identity?.trim()) return;
    this.identities.set(workspacePath, identity.trim());
  }

  private scheduleRefresh(workspacePath: string, workspaceIdentity?: string): void {
    const previous = this.refreshTails.get(workspacePath) ?? Promise.resolve();
    const next = previous.then(() => this.refresh(workspacePath, workspaceIdentity));
    this.refreshTails.set(
      workspacePath,
      next.catch(() => undefined),
    );
  }

  private async refresh(workspacePath: string, workspaceIdentity?: string): Promise<void> {
    const listeners = this.watchers.get(workspacePath);
    if (!listeners || listeners.size === 0) return;
    let sessions: EngineSessionState[];
    let deleted: string[];
    try {
      sessions = await this.listSnapshotSessions(workspacePath);
      deleted = await this.engine.listDeletedSessionIds(workspacePath);
    } catch (error) {
      this.logger.warn("nex-task refresh failed", {
        workspacePath,
        message: error instanceof Error ? error.message : String(error),
      });
      return;
    }
    const identity = workspaceIdentity ?? this.identities.get(workspacePath);
    const previous = this.snapshots.get(workspacePath);

    // Background completion → unread, decided before the diff and re-read afterwards so
    // the emitted meta already carries the marker the UI copies into its query cache.
    const backgroundCompletions = previous
      ? sessions.filter((session) => {
          const before = previous.byId.get(session.sessionId);
          return (
            before !== undefined &&
            (before.phase === "running" || before.phase === "prewarming") &&
            isTerminalPhase(session.phase) &&
            typeof session.unreadAt !== "number" &&
            !this.isSessionWatched(session.sessionId)
          );
        })
      : [];
    const unreadSignalIds = new Set<string>();
    if (backgroundCompletions.length > 0) {
      try {
        for (const session of backgroundCompletions) {
          const state = await this.engine.setTaskUnread(
            workspacePath,
            session.sessionId,
            Date.now(),
          );
          if (typeof state.unreadAt === "number") unreadSignalIds.add(session.sessionId);
        }
        sessions = await this.listSnapshotSessions(workspacePath);
      } catch (error) {
        // 未读写入失败不能挡住状态/列表事件；下一轮终态仍会重试（期间 unreadAt 仍缺省）。
        this.logger.warn("nex-task unread write failed", {
          workspacePath,
          message: error instanceof Error ? error.message : String(error),
        });
      }
    }

    const next: WorkspaceSnapshot = {
      deleted,
      byId: new Map(sessions.map((session) => [session.sessionId, session])),
      ...(identity ? { workspaceIdentity: identity } : {}),
    };
    this.snapshots.set(workspacePath, next);
    if (!previous) return;

    const events = diffTaskSnapshots({
      workspacePath,
      ...(identity ? { workspaceIdentity: identity } : {}),
      previous,
      next,
      unreadSignalIds,
    });
    for (const event of events) {
      for (const listener of listeners) {
        try {
          listener(event);
        } catch (error) {
          this.logger.warn("nex-task event listener failed", {
            message: error instanceof Error ? error.message : String(error),
          });
        }
      }
    }
  }

  private async listSnapshotSessions(workspacePath: string): Promise<EngineSessionState[]> {
    const sessions = await this.engine.listSessions(workspacePath);
    return sessions.filter((session) => session.phase !== "draft");
  }
}
