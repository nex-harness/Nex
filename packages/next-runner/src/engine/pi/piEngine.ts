import {
  ModelRuntime,
  SettingsManager,
  type AgentSessionEvent,
} from "@earendil-works/pi-coding-agent";
import type { ModelSelection } from "@nex/shared";
import type { Logger } from "../../logger.js";
import type {
  CreateEngineSessionOptions,
  Engine,
  EngineCatalog,
  EngineEvent,
  EngineEventSubscription,
  EngineFollowupMode,
  EngineMessage,
  EngineSessionState,
  EngineRunState,
  EngineUnreadState,
  PermissionMode,
  ResolveApprovalResult,
  SubmitOptions,
  SubmitOutcome,
  SwitchModelConfigInput,
  SwitchModelConfigOutcome,
} from "../types.js";
import { SessionMetaStore } from "./metaStore.js";
import { toEngineSessionStates, teardownSession } from "./sessionViews.js";
import { createTaskStateApi, deleteTaskImpl } from "./taskStateOps.js";
import { EMPTY_USAGE, defaultTitle, selectionFor, usageFromSession } from "./sessionHelpers.js";
import { buildCatalog } from "./modelCatalog.js";
import { toEngineBranchHistory } from "./translate.js";
import { reportMessageEntry } from "./messageEntries.js";
import { normalizeMode } from "../permissions/policy.js";
import { openPiSession, type OpenSession } from "./sessionLifecycle.js";
import { handlePiSessionEvent } from "./piSessionOps.js";
import { applyEngineSelection, startEnginePrompt, type PromptOpsDeps } from "./promptOps.js";
import type { SessionConfigOps } from "./sessionConfigOps.js";
import { createEngineOps } from "./engineOps.js";
import { createEngineSession, restoreEngineSession } from "./sessionCreateOps.js";
import { createHookFactories, sessionDirFactory } from "./engineHooks.js";
import type { TitleCompletionRequest, TitleGenerationSettings } from "./titleGenerator.js";
import type { TitleCoordinator } from "./titleRunner.js";

export interface PiEngineOptions {
  dataDir: string;
  logger: Logger;
  /** Test seam: a faux-backed ModelRuntime instead of `~/.pi/agent` credentials. */
  createModelRuntime?: () => Promise<ModelRuntime>;
  /** Test seam: in-memory settings instead of the user's settings.json. */
  createSettingsManager?: (workspacePath: string) => SettingsManager;
  /** Test seam: title generation settings instead of `NEX_RUNNER_TITLES` / `NEX_RUNNER_TITLE_MODEL`. */
  titleSettings?: TitleGenerationSettings;
  /** Test seam: runs one title completion; the engine resolves the model and context. */
  completeTitle?: (request: TitleCompletionRequest) => Promise<string>;
}

/**
 * pi-backed Engine. Owns all mutable session facts:
 * - pi session files under `<dataDir>/sessions/<sha1(workspacePath)>/`
 * - one runner metadata file per workspace (title/phase/tombstones)
 * - the in-process AgentSession map
 */
export class PiEngine implements Engine {
  private readonly sessions = new Map<string, OpenSession>();
  private readonly listeners = new Set<(workspacePath: string) => void>();
  private readonly metadata: SessionMetaStore;
  private readonly configOps: SessionConfigOps;
  private readonly titleSettings: TitleGenerationSettings;
  private readonly titles: TitleCoordinator;
  private readonly taskApi: ReturnType<typeof createTaskStateApi>;
  private readonly hooks: ReturnType<typeof createHookFactories>;
  private modelRuntime: ModelRuntime | null = null;
  private settingsManager: SettingsManager | null = null;
  private catalogCache: EngineCatalog | null = null;
  private initialized = false;

  constructor(private readonly options: PiEngineOptions) {
    this.metadata = new SessionMetaStore(options.dataDir, options.logger);
    this.hooks = createHookFactories({
      dataDir: options.dataDir,
      metadata: this.metadata,
      sessions: this.sessions,
      sessionDir: sessionDirFactory(options.dataDir),
      attach: (open) => this.attach(open),
      teardown: teardownSession,
      sessionsChanged: (workspacePath) => this.emitSessionsChanged(workspacePath),
      factoryDeps: () => this.sessionFactoryDeps(),
      runtime: () => this.requireRuntime(),
    });
    this.taskApi = createTaskStateApi(this.hooks.taskStateHooks());
    const ops = createEngineOps({
      logger: options.logger,
      metadata: this.metadata,
      ...(options.titleSettings ? { titleSettings: options.titleSettings } : {}),
      ...(options.completeTitle ? { completeTitle: options.completeTitle } : {}),
      runtime: () => this.requireRuntime(),
      sessionFor: (sessionId) => this.sessions.get(sessionId)?.session,
      emit: (sessionId, event) => this.emitEngineEvent(sessionId, event),
      sessionsChanged: (workspacePath) => this.emitSessionsChanged(workspacePath),
    });
    this.configOps = ops.configOps;
    this.titles = ops.titles;
    this.titleSettings = ops.titleSettings;
  }

  async init(): Promise<void> {
    if (this.initialized) return;
    // Defaults read ~/.pi/agent/auth.json + models.json, so the user's existing pi
    // credentials are used without any server push.
    this.modelRuntime = this.options.createModelRuntime
      ? await this.options.createModelRuntime()
      : await ModelRuntime.create();
    this.settingsManager = SettingsManager.create(process.cwd());
    await this.metadata.load();
    this.initialized = true;
  }

  async createSession(
    workspacePath: string,
    options: CreateEngineSessionOptions,
  ): Promise<{ sessionId: string; inputId?: string }> {
    await this.init();
    return createEngineSession(this.hooks.sessionCreateDeps(), workspacePath, options);
  }

  async openSession(workspacePath: string, sessionId: string): Promise<void> {
    await this.init();
    if (this.sessions.has(sessionId)) return;
    await restoreEngineSession(this.hooks.sessionCreateDeps(), workspacePath, sessionId);
  }

  async deleteSession(workspacePath: string, sessionId: string): Promise<void> {
    await this.init();
    await deleteTaskImpl(this.hooks.taskStateHooks(), workspacePath, sessionId);
  }

  async listSessions(workspacePath: string): Promise<EngineSessionState[]> {
    await this.init();
    const entries = await this.metadata.list(workspacePath);
    return toEngineSessionStates(
      workspacePath,
      entries,
      (sessionId) => {
        const open = this.sessions.get(sessionId);
        if (!open) return undefined;
        return {
          session: open.session,
          streaming: open.session.isStreaming,
          mode: open.gate.getMode().mode,
          planEnabled: open.gate.getMode().planEnabled,
        };
      },
      {
        selectionFor,
        usageFromSession,
        emptyUsage: EMPTY_USAGE,
      },
    );
  }

  async listDeletedSessionIds(workspacePath: string): Promise<string[]> {
    await this.init();
    const entries = await this.metadata.list(workspacePath);
    return entries.filter((entry) => entry.deleted).map((entry) => entry.sessionId);
  }

  async history(sessionId: string): Promise<EngineMessage[]> {
    const open = this.requireOpen(sessionId);
    // 修复依据（research §0-b）：`session.messages` 是喂给模型的投影上下文——压缩后旧消息
    // 被 summary 取代、`branch()` 后废弃分支消失。restore 必须读 pi 的原始分支，才能把压缩
    // 边界投影成 marker、并保留压缩前的行；每条 message 还带稳定 entryId 供 entityId 派生。
    return toEngineBranchHistory(open.session.sessionManager.getBranch());
  }

  subscribe(sessionId: string, listener: (event: EngineEvent) => void): EngineEventSubscription {
    const open = this.requireOpen(sessionId);
    open.listeners.add(listener);
    return { dispose: () => open.listeners.delete(listener) };
  }

  async submit(sessionId: string, text: string, options: SubmitOptions): Promise<SubmitOutcome> {
    const open = this.requireOpen(sessionId);
    const workspacePath = open.workspacePath;
    const meta = await this.metadata.get(workspacePath, sessionId);
    if (meta?.seenRequestIds?.includes(options.requestId)) {
      return { status: "duplicate", inputId: options.requestId };
    }
    if (options.modelSelection) await this.applySelection(sessionId, options.modelSelection);
    // The mode arrives per submission; the gate applies it before the run starts so the
    // very first tool call of this turn sees it.
    const mode = normalizeMode(options.mode ?? meta?.mode);
    const planEnabled =
      options.planEnabled ?? (options.mode ? false : (meta?.planEnabled ?? false));
    open.gate.setMode(mode, planEnabled);
    const now = Date.now();
    const title = meta?.title || defaultTitle(text);
    await this.metadata.upsert(workspacePath, sessionId, {
      lastActivityAt: now,
      phase: text.trim() ? "running" : (meta?.phase ?? "draft"),
      title,
      titleSource: meta?.titleSource ?? "default",
      mode,
      planEnabled,
      seenRequestId: options.requestId,
      ...(selectionFor(open.session) ? { model: selectionFor(open.session) ?? undefined } : {}),
    });
    if (text.trim().length > 0) {
      const inputId = await this.startPrompt(workspacePath, sessionId, text, options.requestId);
      // 标题生成在首条 prompt 启动后以 fire-and-forget 进行（docs/next/
      // conversation-features.md 决策 8，旧 CLI 同样是首条 query 落库后并发启动）：
      // 永不 await、永不改变本次 turn 的结果，失败只记 warn。
      this.titles.maybeStart(workspacePath, sessionId, text, meta);
      return { status: "accepted", inputId };
    }
    this.emitSessionsChanged(workspacePath);
    return { status: "accepted", inputId: options.requestId };
  }

  async resolveApproval(
    sessionId: string,
    interactionId: string,
    answer: { optionId?: string; freeText?: string },
  ): Promise<ResolveApprovalResult> {
    const open = this.sessions.get(sessionId);
    if (!open) return { resolved: false };
    const outcome = open.gate.resolve(interactionId, answer);
    if (outcome.resolved) this.emitSessionsChanged(open.workspacePath);
    return outcome;
  }

  permissionMode(sessionId: string): { mode: PermissionMode; planEnabled: boolean } | null {
    const open = this.sessions.get(sessionId);
    return open ? open.gate.getMode() : null;
  }

  async abort(sessionId: string): Promise<void> {
    const open = this.sessions.get(sessionId);
    if (!open) return;
    // Settle the hook before aborting so the run cannot stay paused on an approval.
    open.gate.abortPending();
    await open.session.abort();
    await this.metadata.upsert(open.workspacePath, sessionId, {
      phase: "completedInterrupted",
      lastActivityAt: Date.now(),
    });
    this.emitSessionsChanged(open.workspacePath);
  }

  /**
   * Guide delivery: hand the text to pi's steering queue. pi delivers it at the next
   * tool-batch boundary inside the *current* run (`agent-loop.ts:176`), so the runner keeps
   * the item as a pending guide row until `queue_update`/`message_start` shows it consumed.
   */
  async steer(sessionId: string, text: string): Promise<boolean> {
    const open = this.sessions.get(sessionId);
    if (!open || !open.session.isStreaming) return false;
    await open.session.steer(text);
    return true;
  }

  runState(sessionId: string): EngineRunState {
    const open = this.sessions.get(sessionId);
    if (!open) return { streaming: false, idle: true };
    return { streaming: open.session.isStreaming, idle: open.session.isIdle };
  }

  async waitForIdle(sessionId: string): Promise<void> {
    const open = this.sessions.get(sessionId);
    if (!open) return;
    await open.session.waitForIdle();
  }

  clearPiQueue(sessionId: string): void {
    this.sessions.get(sessionId)?.session.clearQueue();
  }

  async rename(sessionId: string, title: string): Promise<void> {
    const open = this.requireOpen(sessionId);
    open.session.setSessionName(title);
    await this.metadata.upsert(open.workspacePath, sessionId, {
      title,
      titleSource: "custom",
      lastActivityAt: Date.now(),
    });
    this.emitSessionsChanged(open.workspacePath);
  }

  async setModel(sessionId: string, selection: ModelSelection | undefined): Promise<void> {
    if (!selection) return;
    await this.applySelection(sessionId, selection);
  }

  async switchModelConfig(
    workspacePath: string,
    sessionId: string,
    input: SwitchModelConfigInput,
  ): Promise<SwitchModelConfigOutcome> {
    return this.configOps.switchModelConfig(workspacePath, sessionId, input);
  }

  async setFollowupMode(
    workspacePath: string,
    sessionId: string,
    mode: EngineFollowupMode,
  ): Promise<{ previous: EngineFollowupMode; mode: EngineFollowupMode }> {
    return this.configOps.setFollowupMode(workspacePath, sessionId, mode);
  }

  /**
   * Sidebar pin/archive (docs/next/task-state.md). The metadata row is the single source
   * the list filters and the event diff read.
   */
  async setTaskState(
    workspacePath: string,
    sessionId: string,
    patch: { pinned?: boolean; archived?: boolean },
  ): Promise<void> {
    await this.init();
    await this.taskApi.setTaskState(workspacePath, sessionId, patch);
  }

  async setTaskUnread(
    workspacePath: string,
    sessionId: string,
    unreadAt: number | null,
    expectedUnreadAt?: number,
  ): Promise<EngineUnreadState> {
    await this.init();
    return this.taskApi.setTaskUnread(workspacePath, sessionId, unreadAt, expectedUnreadAt);
  }

  async deleteTaskIfArchived(workspacePath: string, sessionId: string): Promise<boolean> {
    await this.init();
    return this.taskApi.deleteTaskIfArchived(workspacePath, sessionId);
  }

  async catalog(): Promise<EngineCatalog> {
    await this.init();
    if (this.catalogCache) return this.catalogCache;
    this.catalogCache = await buildCatalog(this.requireRuntime(), this.settingsManager);
    return this.catalogCache;
  }

  onSessionsChanged(listener: (workspacePath: string) => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  async dispose(): Promise<void> {
    for (const open of this.sessions.values()) void teardownSession(open);
    this.sessions.clear();
    this.listeners.clear();
  }

  // ── internals ──

  private attach(open: OpenSession): void {
    const sessionId = open.session.sessionId;
    this.sessions.get(sessionId)?.unsubscribe();
    this.sessions.set(sessionId, open);
  }

  private onPiEvent(
    workspacePath: string,
    sessionId: string,
    event: AgentSessionEvent,
  ): Promise<void> {
    // Row identity (docs/next/conversation-features.md decision 1): pi persists the
    // message right after this listener returns, so a microtask resolves the entry id.
    reportMessageEntry(
      event,
      () => this.sessions.get(sessionId),
      (identity) => this.emitEngineEvent(sessionId, identity),
    );
    return handlePiSessionEvent(
      {
        metadata: this.metadata,
        logger: this.options.logger,
        sessionFor: (id) => this.sessions.get(id)?.session,
        sessionsChanged: (path) => this.emitSessionsChanged(path),
      },
      workspacePath,
      sessionId,
      event,
    );
  }

  private promptOps(): PromptOpsDeps {
    return {
      metadata: this.metadata,
      logger: this.options.logger,
      sessionsChanged: (path) => this.emitSessionsChanged(path),
      runtime: () => this.requireRuntime(),
      configOps: this.configOps,
    };
  }

  private async startPrompt(
    workspacePath: string,
    sessionId: string,
    text: string,
    requestId?: string,
  ): Promise<string> {
    return startEnginePrompt(
      this.promptOps(),
      this.requireOpen(sessionId),
      workspacePath,
      sessionId,
      text,
      requestId,
    );
  }

  private async applySelection(sessionId: string, selection: ModelSelection): Promise<void> {
    await applyEngineSelection(this.promptOps(), this.requireOpen(sessionId), sessionId, selection);
  }

  /** Synthetic engine events (title/model/followup) travel the same stream as pi events. */
  private emitEngineEvent(sessionId: string, event: EngineEvent): void {
    const open = this.sessions.get(sessionId);
    if (!open) return;
    for (const listener of open.listeners) listener(event);
  }

  private requireRuntime(): ModelRuntime {
    if (!this.modelRuntime) throw new Error("Engine is not initialized");
    return this.modelRuntime;
  }

  private requireOpen(sessionId: string): OpenSession {
    const open = this.sessions.get(sessionId);
    if (!open) throw new Error(`Session ${sessionId} is not open`);
    return open;
  }

  private emitSessionsChanged(workspacePath: string): void {
    for (const listener of this.listeners) listener(workspacePath);
  }

  private sessionFactoryDeps(): Parameters<typeof openPiSession>[0] {
    return {
      ...(this.options.createSettingsManager
        ? { createSettingsManager: this.options.createSettingsManager }
        : {}),
      fallbackSettings: () => this.settingsManager ?? SettingsManager.create(process.cwd()),
      modelRuntime: () => this.requireRuntime(),
      onPiEvent: (workspacePath, sessionId, event) => {
        void this.onPiEvent(workspacePath, sessionId, event);
      },
    };
  }
}
