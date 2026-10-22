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
  EngineCompactOutcome,
  EngineCompactState,
  EngineEvent,
  EngineEventSubscription,
  EngineFollowupMode,
  EngineForkOutcome,
  EngineForkRequest,
  EngineMessage,
  EngineRewindOutcome,
  EngineRewindRequest,
  EngineRewindTarget,
  EngineSessionState,
  EngineRunState,
  EngineUnreadState,
  PermissionMode,
  ResolveApprovalResult,
  SubmitOptions,
  SubmitOutcome,
  SwitchModelConfigInput,
} from "../types.js";
import { PiCompactOps } from "./piCompactOps.js";
import { submitPrompt } from "./piSessionOps.js";

import {
  compactStateOf,
  resolveRewindTarget,
  runFork,
  runManualCompact,
  runRewind,
  type ConversationOpsDeps,
} from "./piConversationOps.js";
import type { PiTranslateContext } from "./events.js";
import { handleEnginePiEvent } from "./piEventHook.js";
import { SessionMetaStore } from "./metaStore.js";
import { toEngineSessionStates, teardownSession } from "./sessionViews.js";
import { createTaskStateApi, deleteTaskImpl } from "./taskStateOps.js";
import { EMPTY_USAGE, selectionFor, usageFromSession } from "./sessionHelpers.js";
import { buildCatalog } from "./modelCatalog.js";
import { toEngineBranchHistory } from "./translate.js";
import { openPiSession, type OpenSession } from "./sessionLifecycle.js";
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
  /** Test seams: in-memory settings, title settings and a fake completion. */
  createSettingsManager?: (workspacePath: string) => SettingsManager;
  titleSettings?: TitleGenerationSettings;
  completeTitle?: (request: TitleCompletionRequest) => Promise<string>;
}

/** pi-backed Engine: session files under `<dataDir>/sessions/`, one metadata file per workspace, the in-process session map. */
export class PiEngine implements Engine {
  private readonly sessions = new Map<string, OpenSession>();
  private readonly listeners = new Set<(workspacePath: string) => void>();
  private readonly metadata: SessionMetaStore;
  private readonly configOps: SessionConfigOps;
  private readonly titleSettings: TitleGenerationSettings;
  private readonly titles: TitleCoordinator;
  private readonly taskApi: ReturnType<typeof createTaskStateApi>;
  private readonly hooks: ReturnType<typeof createHookFactories>;
  private readonly compactOps: PiCompactOps;
  /** pi entry a manual compaction produced, so its outcome can be persisted with the command. */
  private readonly compactionEntries = new Map<string, string>();
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
    const sessionFor = (sessionId: string) => this.sessions.get(sessionId)?.session;
    this.compactOps = new PiCompactOps({ logger: options.logger, sessionFor });
    const ops = createEngineOps({
      logger: options.logger,
      metadata: this.metadata,
      ...(options.titleSettings ? { titleSettings: options.titleSettings } : {}),
      ...(options.completeTitle ? { completeTitle: options.completeTitle } : {}),
      runtime: () => this.requireRuntime(),
      sessionFor,
      emit: (sessionId, event) => this.emitEngineEvent(sessionId, event),
      sessionsChanged: (workspacePath) => this.emitSessionsChanged(workspacePath),
    });
    this.configOps = ops.configOps;
    this.titles = ops.titles;
    this.titleSettings = ops.titleSettings;
  }

  async init(): Promise<void> {
    if (this.initialized) return;
    // Defaults read ~/.pi/agent/auth.json + models.json: the user's pi credentials, no push.
    this.modelRuntime = this.options.createModelRuntime
      ? await this.options.createModelRuntime()
      : await ModelRuntime.create();
    this.settingsManager = SettingsManager.create(process.cwd());
    await this.metadata.load();
    this.initialized = true;
  }

  createSession(
    workspacePath: string,
    options: CreateEngineSessionOptions,
  ): Promise<{ sessionId: string }> {
    return this.init().then(() =>
      createEngineSession(this.hooks.sessionCreateDeps(), workspacePath, options),
    );
  }

  openSession(workspacePath: string, sessionId: string): Promise<void> {
    if (this.sessions.has(sessionId)) return Promise.resolve();
    return this.init().then(() =>
      restoreEngineSession(this.hooks.sessionCreateDeps(), workspacePath, sessionId),
    );
  }

  deleteSession(workspacePath: string, sessionId: string): Promise<void> {
    return this.init().then(() =>
      deleteTaskImpl(this.hooks.taskStateHooks(), workspacePath, sessionId),
    );
  }

  async listSessions(workspacePath: string): Promise<EngineSessionState[]> {
    await this.init();
    const live = (sessionId: string) => {
      const open = this.sessions.get(sessionId);
      if (!open) return undefined;
      const { mode, planEnabled } = open.gate.getMode();
      return { session: open.session, streaming: open.session.isStreaming, mode, planEnabled };
    };
    const entries = await this.metadata.list(workspacePath);
    return toEngineSessionStates(workspacePath, entries, live, {
      selectionFor,
      usageFromSession,
      emptyUsage: EMPTY_USAGE,
    });
  }

  async listDeletedSessionIds(workspacePath: string): Promise<string[]> {
    await this.init();
    const entries = await this.metadata.list(workspacePath);
    return entries.filter((entry) => entry.deleted).map((entry) => entry.sessionId);
  }

  /** 修复依据（research §0-b）：`session.messages` 是投影上下文，压缩后丢旧消息、branch() 后丢废弃分支；restore 必须读 pi 原始分支。 */
  async history(sessionId: string): Promise<EngineMessage[]> {
    const open = this.requireOpen(sessionId);
    const meta = await this.metadata.get(open.workspacePath, sessionId);
    return toEngineBranchHistory(open.session.sessionManager.getBranch(), meta?.lastCompact);
  }

  subscribe(sessionId: string, listener: (event: EngineEvent) => void): EngineEventSubscription {
    const open = this.requireOpen(sessionId);
    open.listeners.add(listener);
    return { dispose: () => open.listeners.delete(listener) };
  }

  submit(sessionId: string, text: string, options: SubmitOptions): Promise<SubmitOutcome> {
    const open = this.requireOpen(sessionId);
    return submitPrompt(
      {
        metadata: this.metadata,
        titles: this.titles,
        sessionsChanged: (path) => this.emitSessionsChanged(path),
        startPrompt: (next: string, requestId: string) =>
          this.startPrompt(open.workspacePath, sessionId, next, requestId),
        applySelection: (selection: ModelSelection) => this.applySelection(sessionId, selection),
      },
      open,
      text,
      options,
    );
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
    return this.sessions.get(sessionId)?.gate.getMode() ?? null;
  }

  async abort(sessionId: string): Promise<void> {
    const open = this.sessions.get(sessionId);
    if (!open) return;
    open.gate.abortPending();
    await open.session.abort();
    const phase = "completedInterrupted" as const;
    await this.metadata.upsert(open.workspacePath, sessionId, {
      phase,
      lastActivityAt: Date.now(),
    });
    this.emitSessionsChanged(open.workspacePath);
  }

  /** Guide delivery into pi's steering queue; delivered at the next tool-batch boundary. */
  async steer(sessionId: string, text: string): Promise<boolean> {
    const open = this.sessions.get(sessionId);
    if (!open?.session.isStreaming) return false;
    await open.session.steer(text);
    return true;
  }

  runState(sessionId: string): EngineRunState {
    const open = this.sessions.get(sessionId);
    return open
      ? { streaming: open.session.isStreaming, idle: open.session.isIdle }
      : { streaming: false, idle: true };
  }

  waitForIdle(sessionId: string): Promise<void> {
    return this.sessions.get(sessionId)?.session.waitForIdle() ?? Promise.resolve();
  }

  clearPiQueue(sessionId: string): void {
    this.sessions.get(sessionId)?.session.clearQueue();
  }

  /** User rename: `custom`, never overwritten by a generated title. */
  async rename(sessionId: string, title: string): Promise<void> {
    const open = this.requireOpen(sessionId);
    open.session.setSessionName(title);
    const patch = { title, titleSource: "custom" as const, lastActivityAt: Date.now() };
    await this.metadata.upsert(open.workspacePath, sessionId, patch);
    this.emitSessionsChanged(open.workspacePath);
  }

  setModel(sessionId: string, selection: ModelSelection | undefined): Promise<void> {
    return selection ? this.applySelection(sessionId, selection) : Promise.resolve();
  }
  switchModelConfig(w: string, s: string, input: SwitchModelConfigInput) {
    return this.configOps.switchModelConfig(w, s, input);
  }
  setFollowupMode(w: string, s: string, mode: EngineFollowupMode) {
    return this.configOps.setFollowupMode(w, s, mode);
  }

  // Conversation rewind/compact/fork (decisions 5-7) delegate to `piConversationOps`.
  compactState(sessionId: string): EngineCompactState {
    return compactStateOf(this.conversationOps(), sessionId);
  }
  takeCompactOutcomes(sessionId: string): EngineCompactOutcome[] {
    return this.compactOps.takeOutcomes(sessionId);
  }
  compact(sessionId: string, sourceCommandId: string): Promise<EngineCompactOutcome> {
    this.compactionEntries.delete(sessionId);
    return runManualCompact(this.conversationOps(), sessionId, sourceCommandId).finally(() =>
      this.compactionEntries.delete(sessionId),
    );
  }
  rewind(sessionId: string, request: EngineRewindRequest): Promise<EngineRewindOutcome> {
    return runRewind(this.conversationOps(), sessionId, request);
  }
  userMessageBefore(sessionId: string, entryId: string): Promise<EngineRewindTarget | null> {
    return Promise.resolve(resolveRewindTarget(this.conversationOps(), sessionId, entryId));
  }
  forkAssistant(
    workspacePath: string,
    sessionId: string,
    request: EngineForkRequest,
  ): Promise<EngineForkOutcome> {
    return runFork(this.conversationOps(), workspacePath, sessionId, request);
  }

  /** Sidebar pin/archive (docs/next/task-state.md). */
  setTaskState(
    workspacePath: string,
    sessionId: string,
    patch: { pinned?: boolean; archived?: boolean },
  ): Promise<void> {
    return this.taskApi.setTaskState(workspacePath, sessionId, patch);
  }
  setTaskUnread(
    workspacePath: string,
    sessionId: string,
    unreadAt: number | null,
    expectedUnreadAt?: number,
  ): Promise<EngineUnreadState> {
    return this.taskApi.setTaskUnread(workspacePath, sessionId, unreadAt, expectedUnreadAt);
  }
  deleteTaskIfArchived(workspacePath: string, sessionId: string): Promise<boolean> {
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
    return () => this.listeners.delete(listener);
  }

  async dispose(): Promise<void> {
    for (const open of this.sessions.values()) void teardownSession(open);
    this.sessions.clear();
    this.listeners.clear();
  }

  // ── internals ──

  private attach(open: OpenSession): void {
    this.sessions.get(open.session.sessionId)?.unsubscribe();
    this.sessions.set(open.session.sessionId, open);
  }

  private onPiEvent(path: string, sessionId: string, event: AgentSessionEvent): Promise<void> {
    return handleEnginePiEvent(this.eventHookDeps(), path, sessionId, event);
  }

  private eventHookDeps() {
    return {
      logger: this.options.logger,
      metadata: this.metadata,
      compactOps: this.compactOps,
      sessions: this.sessions,
      emit: (id: string, e: EngineEvent) => this.emitEngineEvent(id, e),
      sessionsChanged: (path: string) => this.emitSessionsChanged(path),
      rememberCompactionEntry: (id: string, entryId: string) =>
        this.compactionEntries.set(id, entryId),
    };
  }

  private promptOps(): PromptOpsDeps {
    return {
      metadata: this.metadata,
      logger: this.options.logger,
      sessionsChanged: (p) => this.emitSessionsChanged(p),
      runtime: () => this.requireRuntime(),
      configOps: this.configOps,
    };
  }

  private startPrompt(workspacePath: string, sessionId: string, text: string, requestId?: string) {
    const open = this.requireOpen(sessionId);
    return startEnginePrompt(this.promptOps(), open, workspacePath, sessionId, text, requestId);
  }

  private applySelection(sessionId: string, selection: ModelSelection): Promise<void> {
    return applyEngineSelection(
      this.promptOps(),
      this.requireOpen(sessionId),
      sessionId,
      selection,
    );
  }

  /** Synthetic engine events (title/model/followup) travel the same stream as pi events. */
  private emitEngineEvent(sessionId: string, event: EngineEvent): void {
    for (const listener of this.sessions.get(sessionId)?.listeners ?? []) listener(event);
  }

  private conversationOps(): ConversationOpsDeps {
    return {
      logger: this.options.logger,
      metadata: this.metadata,
      compactOps: this.compactOps,
      compactionEntryFor: (id: string) => this.compactionEntries.get(id),
      open: (id: string) => this.requireOpen(id),
      sessionsChanged: (path: string) => this.emitSessionsChanged(path),
      forkDeps: () => ({
        metadata: this.metadata,
        runtime: () => this.requireRuntime(),
        factoryDeps: () => this.sessionFactoryDeps(),
        attach: (open: OpenSession) => this.attach(open),
        sessionsChanged: (path: string) => this.emitSessionsChanged(path),
      }),
    };
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
      translateContext: (sessionId): PiTranslateContext => {
        const compactCommandId = this.compactOps.activeCommandId(sessionId);
        return compactCommandId ? { compactCommandId } : {};
      },
      onPiEvent: (workspacePath, sessionId, event) => {
        void this.onPiEvent(workspacePath, sessionId, event);
      },
    };
  }
}
