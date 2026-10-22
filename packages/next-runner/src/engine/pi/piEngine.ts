import { mkdir, unlink } from "node:fs/promises";
import {
  ModelRuntime,
  SessionManager,
  SettingsManager,
  type AgentSessionEvent,
} from "@earendil-works/pi-coding-agent";
import type { Model } from "@earendil-works/pi-ai";
import type { ModelSelection } from "@nex/shared";
import type { Logger } from "../../logger.js";
import type {
  CreateEngineSessionOptions,
  Engine,
  EngineCatalog,
  EngineEvent,
  EngineEventSubscription,
  EngineMessage,
  EngineSessionState,
  EngineUnreadState,
  PermissionMode,
  ResolveApprovalResult,
  SubmitOptions,
  SubmitOutcome,
} from "../types.js";
import { SessionMetaStore } from "./metaStore.js";
import { toEngineSessionStates, teardownSession } from "./sessionViews.js";
import {
  deleteTaskIfArchivedImpl,
  deleteTaskImpl,
  setTaskStateImpl,
  setTaskUnreadImpl,
  type TaskStateHooks,
} from "./taskStateOps.js";
import { EMPTY_USAGE, defaultTitle, selectionFor, usageFromSession } from "./sessionHelpers.js";
import { buildCatalog, resolveModel } from "./modelCatalog.js";
import { toEngineHistory } from "./translate.js";
import { normalizeMode } from "../permissions/policy.js";
import { openPiSession, type OpenSession } from "./sessionLifecycle.js";
import { findSessionFile as findSessionFileInDir, sessionDirFor } from "./piSessionFiles.js";
import { applyModelSelection, handlePiSessionEvent, startPromptRun } from "./piSessionOps.js";

export interface PiEngineOptions {
  dataDir: string;
  logger: Logger;
  /** Test seam: a faux-backed ModelRuntime instead of `~/.pi/agent` credentials. */
  createModelRuntime?: () => Promise<ModelRuntime>;
  /** Test seam: in-memory settings instead of the user's settings.json. */
  createSettingsManager?: (workspacePath: string) => SettingsManager;
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
  private modelRuntime: ModelRuntime | null = null;
  private settingsManager: SettingsManager | null = null;
  private catalogCache: EngineCatalog | null = null;
  private initialized = false;

  constructor(private readonly options: PiEngineOptions) {
    this.metadata = new SessionMetaStore(options.dataDir, options.logger);
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
    const runtime = this.requireRuntime();
    const model = await resolveModel(options.modelSelection, runtime);
    const sessionDir = this.sessionDir(workspacePath);
    await mkdir(sessionDir, { recursive: true });
    const sessionManager = SessionManager.create(workspacePath, sessionDir);
    const { session, open } = await openPiSession(
      this.sessionFactoryDeps(),
      workspacePath,
      sessionManager,
      model,
    );
    const sessionId = session.sessionId;
    const now = Date.now();
    const selection = model ? selectionFor(session) : undefined;
    await this.metadata.upsert(workspacePath, sessionId, {
      createdAt: now,
      lastActivityAt: now,
      phase: "draft",
      title: "",
      titleSource: "default",
      deleted: false,
      mode: "build",
      planEnabled: false,
      ...(selection ? { model: selection } : {}),
    });
    this.attach(open);
    this.emitSessionsChanged(workspacePath);
    return { sessionId };
  }

  async openSession(workspacePath: string, sessionId: string): Promise<void> {
    await this.init();
    if (this.sessions.has(sessionId)) return;
    const meta = await this.metadata.get(workspacePath, sessionId);
    if (meta?.deleted) throw new Error(`Session ${sessionId} is deleted`);
    const sessionFile = await this.findSessionFile(workspacePath, sessionId);
    if (!sessionFile) throw new Error(`Session ${sessionId} not found`);
    const sessionManager = SessionManager.open(sessionFile, this.sessionDir(workspacePath));
    const runtime = this.requireRuntime();
    const restoredModel = meta?.model
      ? (runtime.getModel(meta.model.providerId, meta.model.modelId) as Model<never> | undefined)
      : undefined;
    const { session, gate, open } = await openPiSession(
      this.sessionFactoryDeps(),
      workspacePath,
      sessionManager,
      restoredModel,
    );
    if (meta?.model?.thought) {
      session.setThinkingLevel(meta.model.thought as never);
    }
    gate.setMode(meta?.mode ?? "build", meta?.planEnabled ?? false);
    this.attach(open);
    await this.metadata.upsert(workspacePath, sessionId, {
      createdAt: meta?.createdAt ?? Date.now(),
      lastActivityAt: meta?.lastActivityAt ?? Date.now(),
      title: meta?.title ?? "",
      titleSource: meta?.titleSource ?? "default",
      model: selectionFor(session) ?? meta?.model,
      phase: session.isStreaming ? "running" : (meta?.phase ?? "completedSuccess"),
    });
  }

  async deleteSession(workspacePath: string, sessionId: string): Promise<void> {
    await this.init();
    await deleteTaskImpl(this.taskStateHooks(), workspacePath, sessionId);
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
    open.session.refreshContext();
    return toEngineHistory(open.session.messages);
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
    await setTaskStateImpl(this.taskStateHooks(), workspacePath, sessionId, patch);
  }

  async setTaskUnread(
    workspacePath: string,
    sessionId: string,
    unreadAt: number | null,
    expectedUnreadAt?: number,
  ): Promise<EngineUnreadState> {
    await this.init();
    return setTaskUnreadImpl(
      this.taskStateHooks(),
      workspacePath,
      sessionId,
      unreadAt,
      expectedUnreadAt,
    );
  }

  async deleteTaskIfArchived(workspacePath: string, sessionId: string): Promise<boolean> {
    await this.init();
    return deleteTaskIfArchivedImpl(this.taskStateHooks(), workspacePath, sessionId);
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

  private async startPrompt(
    workspacePath: string,
    sessionId: string,
    text: string,
    requestId?: string,
  ): Promise<string> {
    const open = this.requireOpen(sessionId);
    const inputId = requestId ?? `input_${Date.now()}_${Math.random().toString(36).slice(2, 10)}`;
    await this.metadata.upsert(workspacePath, sessionId, {
      lastActivityAt: Date.now(),
      phase: "running",
    });
    startPromptRun(
      {
        metadata: this.metadata,
        logger: this.options.logger,
        sessionsChanged: (path) => this.emitSessionsChanged(path),
      },
      open,
      workspacePath,
      sessionId,
      text,
    );
    this.emitSessionsChanged(workspacePath);
    return inputId;
  }

  private async applySelection(sessionId: string, selection: ModelSelection): Promise<void> {
    const open = this.requireOpen(sessionId);
    const runtime = this.requireRuntime();
    await applyModelSelection(open.session, selection, (providerId, modelId) =>
      runtime.getModel(providerId, modelId),
    );
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

  private sessionDir(workspacePath: string): string {
    return sessionDirFor(this.options.dataDir, workspacePath);
  }

  private findSessionFile(workspacePath: string, sessionId: string): Promise<string | undefined> {
    return findSessionFileInDir(this.sessionDir(workspacePath), sessionId);
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

  /** Task-state hooks: the store stays the single writer; the engine owns session lives. */
  private taskStateHooks(): TaskStateHooks {
    return {
      metadata: this.metadata,
      sessionFor: (sessionId) => this.sessions.get(sessionId)?.session,
      closeSession: (sessionId) => {
        const open = this.sessions.get(sessionId);
        if (!open) return;
        this.sessions.delete(sessionId);
        void teardownSession(open);
      },
      removeSessionFile: async (workspacePath, sessionId) => {
        const file = await this.findSessionFile(workspacePath, sessionId);
        if (file) await unlink(file).catch(() => undefined);
      },
      sessionsChanged: (workspacePath) => this.emitSessionsChanged(workspacePath),
    };
  }
}
