import type { NexTaskMeta, NexWorkspaceEvent } from "@nex/shared";
import type { Engine, EngineSessionState } from "../engine/types.js";
import type { Logger } from "../logger.js";

interface WorkspaceSnapshot {
  deleted: string[];
  byId: Map<string, EngineSessionState>;
}

/**
 * `nex-task` is the sidebar's row source. The runner derives one NexTaskMeta per live
 * session and keeps deleted ids in the negative-membership list. Events use reasons the
 * UI's refresh policy treats as membership changes (`task_created`, `task_deleted`,
 * `task_meta_changed`).
 */
export class NexTaskChannel {
  private readonly snapshots = new Map<string, WorkspaceSnapshot>();
  private readonly watchers = new Map<string, Set<(event: NexWorkspaceEvent) => void>>();
  private readonly engineUnsubscribe: () => void;

  constructor(
    private readonly engine: Engine,
    private readonly logger: Logger,
  ) {
    this.engineUnsubscribe = engine.onSessionsChanged((workspacePath) => {
      void this.refresh(workspacePath);
    });
  }

  async listTasks(params: { workspacePath: string }): Promise<NexTaskMeta[]> {
    const sessions = await this.listVisibleSessions(params.workspacePath);
    return sessions.map((session) => toNexTaskMeta(params.workspacePath, session));
  }

  async listPinnedTasks(): Promise<NexTaskMeta[]> {
    return [];
  }

  async listArchivedTasks(): Promise<NexTaskMeta[]> {
    return [];
  }

  async listPinnedTaskIds(): Promise<string[]> {
    return [];
  }

  async listDeletedTaskIds(params: { workspacePath: string }): Promise<string[]> {
    return this.engine.listDeletedSessionIds(params.workspacePath);
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

  onDynamicWorkspaceEvent(
    workspacePath: string,
    emit: (event: NexWorkspaceEvent) => void,
  ): { dispose(): void } {
    const listeners = this.watchers.get(workspacePath) ?? new Set();
    listeners.add(emit);
    this.watchers.set(workspacePath, listeners);
    void this.refresh(workspacePath);
    return {
      dispose: () => {
        listeners.delete(emit);
        if (listeners.size === 0) this.watchers.delete(workspacePath);
      },
    };
  }

  dispose(): void {
    this.engineUnsubscribe();
    this.watchers.clear();
  }

  /**
   * 侧栏只能看到真实任务：`draft` 是 createSession 预热载体（无输入、无标题），
   * 一旦发给 sidebar 就会以 “New task” 行出现，且每次预热/丢弃都会新增一行。
   * 修复依据：UI 的 tasks-index syncer 会显式跳过 `phase === "draft"` 的 summary
   * （packages/services/src/nex-agent/nexTaskIndexSyncer.ts），runner 作为同一
   * 事实源必须保持一致；sendText 后 phase 变 running，行自然出现。
   */
  private async listVisibleSessions(workspacePath: string): Promise<EngineSessionState[]> {
    const sessions = await this.engine.listSessions(workspacePath);
    return sessions.filter((session) => session.phase !== "draft");
  }

  private async refresh(workspacePath: string): Promise<void> {
    const listeners = this.watchers.get(workspacePath);
    if (!listeners || listeners.size === 0) return;
    let sessions: EngineSessionState[];
    let deleted: string[];
    try {
      sessions = await this.listVisibleSessions(workspacePath);
      deleted = await this.engine.listDeletedSessionIds(workspacePath);
    } catch (error) {
      this.logger.warn("nex-task refresh failed", {
        workspacePath,
        message: error instanceof Error ? error.message : String(error),
      });
      return;
    }
    const next: WorkspaceSnapshot = {
      deleted,
      byId: new Map(sessions.map((session) => [session.sessionId, session])),
    };
    const previous = this.snapshots.get(workspacePath);
    this.snapshots.set(workspacePath, next);
    if (!previous) return;

    const events: NexWorkspaceEvent[] = [];
    for (const session of sessions) {
      const before = previous.byId.get(session.sessionId);
      if (!before) {
        events.push({
          type: "workspace_task_list_changed",
          workspacePath,
          taskId: session.sessionId,
          reason: "task_created",
          taskMeta: toNexTaskMeta(workspacePath, session),
        });
        continue;
      }
      if (
        before.title !== session.title ||
        before.phase !== session.phase ||
        before.lastActivityAt !== session.lastActivityAt
      ) {
        events.push({
          type: "workspace_task_list_changed",
          workspacePath,
          taskId: session.sessionId,
          reason: "task_meta_changed",
          taskMeta: toNexTaskMeta(workspacePath, session),
        });
      }
    }
    for (const deletedId of deleted) {
      if (previous.deleted.includes(deletedId)) continue;
      events.push({
        type: "workspace_task_list_changed",
        workspacePath,
        taskId: deletedId,
        reason: "task_deleted",
      });
    }
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
}

export function toNexTaskMeta(workspacePath: string, session: EngineSessionState): NexTaskMeta {
  return {
    taskId: session.sessionId,
    traceId: session.sessionId,
    title: session.title,
    ...(session.titleSource === "custom" ? { titleOverridden: true } : {}),
    workspacePath,
    createdAt: session.createdAt,
    updatedAt: session.lastActivityAt,
    mode: "build",
    ...(session.model ? { model: session.model.modelId } : {}),
    ...(session.model ? { thoughtLevel: session.model.thought } : {}),
    status:
      session.phase === "error"
        ? "error"
        : session.phase === "running" || session.phase === "prewarming"
          ? "running"
          : "completed",
  };
}
