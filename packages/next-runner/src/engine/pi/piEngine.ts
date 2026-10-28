import {
  ModelRuntime,
  SettingsManager,
  type AgentSessionEvent,
} from "@earendil-works/pi-coding-agent";
import type { ModelSelection } from "@nex/shared";
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
import type { PromptAttachmentPayload } from "./piSessionOps.js";

import type { ConversationOpsDeps } from "./piConversationOps.js";
import type { PiFileOps } from "./piFileOps.js";
import type { SessionControls } from "./sessionControls.js";
import type { SessionLifecycleOps } from "./sessionLifecycleOps.js";
import type { ConversationCommands } from "./piConversationCommands.js";
import type { PiCompactOps } from "./piCompactOps.js";
import type { createHookFactories } from "./engineHooks.js";
import type { createTaskStateApi } from "./taskStateOps.js";
import { SessionMetaStore } from "./metaStore.js";
import { teardownSession } from "./sessionViews.js";
import { createEngineCollaborators } from "./engineCollaborators.js";
import { buildCatalog } from "./modelCatalog.js";
import { openPiSession, type OpenSession } from "./sessionLifecycle.js";
import { createSessionFactoryDeps } from "./sessionFactoryDeps.js";
import { EMPTY_MCP_GENERATION, mcpSessionWiring } from "../../config/mcpConfig.js";
import { createMcpStatusOps } from "./mcpStatusOps.js";
import type { McpServerStatus } from "../../config/applyRuntimeConfig.js";
import { createPromptHooks } from "./piPromptHooks.js";
import { createEngineEventHook } from "./piEngineWiring.js";
import { createConversationOps } from "./piConversationWiring.js";
import type { SessionConfigOps } from "./sessionConfigOps.js";
import { AttachmentRegistry } from "../../attachments/registry.js";
import type { AttachmentPromptTable } from "./piAttachmentOps.js";
import { createAttachmentOps } from "./attachmentOpsFactory.js";
import type { TitleGenerationSettings } from "./titleGenerator.js";
import type { PiEngineOptions } from "./piEngineOptions.js";
export type { PiEngineOptions } from "./piEngineOptions.js";
import type { TitleCoordinator } from "./titleRunner.js";

/** pi-backed Engine: session files under `<dataDir>/sessions/`, one metadata file per workspace, the in-process session map. */
export class PiEngine implements Engine {
  readonly sessions = new Map<string, OpenSession>();
  private readonly listeners = new Set<(workspacePath: string) => void>();
  readonly metadata: SessionMetaStore;
  readonly configOps!: SessionConfigOps;
  readonly titleSettings!: TitleGenerationSettings;
  readonly titles!: TitleCoordinator;
  readonly taskApi!: ReturnType<typeof createTaskStateApi>;
  readonly hooks!: ReturnType<typeof createHookFactories>;
  readonly compactOps!: PiCompactOps;
  /** pi entry a manual compaction produced (persisted with its command). */
  readonly compactionEntries = new Map<string, string>();
  /** Attachment blobs + prompt feeding (spec A). */
  readonly attachments: AttachmentRegistry;
  readonly attachmentOps: AttachmentPromptTable;
  /** File surface (checkpoint services per workspace + git-backed reads). */
  readonly fileOps!: PiFileOps;
  /** Run controls over open sessions (abort/stop/guide/idle). */
  readonly sessionControls!: SessionControls;
  /** Session/transcript half of the Engine surface (created in the constructor). */
  readonly sessionLifecycle!: SessionLifecycleOps;
  /** 全文搜索正文读取器（`SessionManager.list`），见 `sessionSearchText.ts`。 */
  readonly sessionSearchText!: (workspacePath: string) => Promise<Map<string, string>>;
  modelRuntime: ModelRuntime | null = null;
  settingsManager: SettingsManager | null = null;
  private catalogCache: EngineCatalog | null = null;
  private initialized = false;
  private readonly mcpOps: ReturnType<typeof createMcpStatusOps>;

  constructor(readonly options: PiEngineOptions) {
    this.mcpOps = createMcpStatusOps({
      options,
      sessions: this.sessions,
      init: () => this.init(),
      settingsManager: () => this.settingsManager,
      runtime: () => this.requireRuntime(),
    });
    this.metadata = new SessionMetaStore(options.dataDir, options.logger);
    this.attachments =
      options.attachments ??
      new AttachmentRegistry({
        dataDir: options.dataDir,
        ...(options.attachmentSessionQuotaBytes !== undefined
          ? { sessionQuotaBytes: options.attachmentSessionQuotaBytes }
          : {}),
      });
    this.attachmentOps = createAttachmentOps({ host: this.attachments, logger: options.logger });
    const wiring = createEngineCollaborators(this);
    Object.assign(this, {
      fileOps: wiring.fileOps,
      sessionControls: wiring.sessionControls,
      hooks: wiring.hooks,
      taskApi: wiring.taskApi,
      compactOps: wiring.compactOps,
      configOps: wiring.configOps,
      titles: wiring.titles,
      titleSettings: wiring.titleSettings,
      sessionLifecycle: wiring.sessionLifecycle,
      conversation: wiring.conversation,
      sessionSearchText: wiring.sessionSearchText,
    });
  }

  /** Conversation rewind/compact/fork commands (created after the compact owner). */
  readonly conversation!: ConversationCommands;

  async init(): Promise<void> {
    if (this.initialized) return;
    if (this.options.runtimeConfig) {
      // Server-owned config decides the runtime per generation. Until the first
      // `config` frame arrives (or while the doc has no providers) the runner
      // keeps the local ~/.pi/agent behavior.
      this.modelRuntime = await this.options.runtimeConfig.ensureFallbackRuntime(() =>
        this.options.createModelRuntime ? this.options.createModelRuntime() : ModelRuntime.create(),
      );
      this.settingsManager = this.options.runtimeConfig.settingsManager();
    } else {
      // Defaults read ~/.pi/agent/auth.json + models.json: the user's pi credentials, no push.
      this.modelRuntime = this.options.createModelRuntime
        ? await this.options.createModelRuntime()
        : await ModelRuntime.create();
      this.settingsManager = SettingsManager.create(process.cwd());
    }
    await this.metadata.load();
    this.attachments.startSweeper();
    this.initialized = true;
  }

  createSession(w: string, o: CreateEngineSessionOptions) {
    return this.sessionLifecycle.createSession(w, o);
  }
  openSession(w: string, id: string): Promise<void> {
    return this.sessionLifecycle.openSession(w, id);
  }
  deleteSession(w: string, id: string): Promise<void> {
    return this.sessionLifecycle.deleteSession(w, id);
  }
  listSessions(w: string): Promise<EngineSessionState[]> {
    return this.sessionLifecycle.listSessions(w);
  }
  listDeletedSessionIds(w: string): Promise<string[]> {
    return this.sessionLifecycle.listDeletedSessionIds(w);
  }
  listSessionSearchText(w: string): Promise<Map<string, string>> {
    return this.sessionSearchText(w);
  }
  history(id: string): Promise<EngineMessage[]> {
    return this.sessionLifecycle.history(id);
  }
  subscribe(id: string, listener: (event: EngineEvent) => void): EngineEventSubscription {
    return this.sessionLifecycle.subscribe(id, listener);
  }
  submit(id: string, text: string, options: SubmitOptions): Promise<SubmitOutcome> {
    return this.sessionLifecycle.submit(id, text, options);
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
    return this.sessionControls.permissionMode(sessionId);
  }

  abort(sessionId: string): Promise<void> {
    return this.sessionControls.abort(sessionId);
  }

  steer(sessionId: string, text: string): Promise<boolean> {
    return this.sessionControls.steer(sessionId, text);
  }
  runState(sessionId: string): EngineRunState {
    return this.sessionControls.runState(sessionId);
  }
  waitForIdle(sessionId: string): Promise<void> {
    return this.sessionControls.waitForIdle(sessionId);
  }
  clearPiQueue(sessionId: string): void {
    this.sessionControls.clearPiQueue(sessionId);
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

  // Conversation rewind/compact/fork (decisions 5-7) → `piConversationCommands.ts`.
  compactState(sessionId: string): EngineCompactState {
    return this.conversation.compactState(sessionId);
  }
  takeCompactOutcomes(sessionId: string): EngineCompactOutcome[] {
    return this.compactOps.takeOutcomes(sessionId);
  }
  compact(sessionId: string, sourceCommandId: string): Promise<EngineCompactOutcome> {
    return this.conversation.compact(sessionId, sourceCommandId);
  }
  rewind(sessionId: string, request: EngineRewindRequest): Promise<EngineRewindOutcome> {
    return this.conversation.rewind(sessionId, request);
  }
  userMessageBefore(sessionId: string, entryId: string): Promise<EngineRewindTarget | null> {
    return this.conversation.userMessageBefore(sessionId, entryId);
  }
  forkAssistant(
    workspacePath: string,
    sessionId: string,
    request: EngineForkRequest,
  ): Promise<EngineForkOutcome> {
    return this.conversation.forkAssistant(workspacePath, sessionId, request);
  }

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

  fileCheckpointFacts(w: string, s: string, userEntryId: string) {
    return this.fileOps.facts(w, s, userEntryId);
  }
  fileChanges(w: string, s: string, userEntryId: string) {
    return this.fileOps.changes(w, s, userEntryId);
  }
  fileRewindPreview(w: string, s: string, userEntryId: string) {
    return this.fileOps.preview(w, s, userEntryId);
  }
  applyFileRewind(
    w: string,
    s: string,
    userEntryId: string,
    options: { commitAfterApply?: () => Promise<void>; sourceCommandId?: string },
  ) {
    return this.fileOps.apply(w, s, userEntryId, options);
  }
  fileCheckpointsEnabled(workspacePath: string): Promise<boolean> {
    return this.fileOps.enabled(workspacePath);
  }

  async catalog(): Promise<EngineCatalog> {
    await this.init();
    if (this.catalogCache) return this.catalogCache;
    this.catalogCache = await buildCatalog(this.requireRuntime(), this.settingsManager);
    return this.catalogCache;
  }

  /**
   * Adopts a newly applied server config (docs/next/config-mcp-skills.md
   * decision 5): new sessions read the latest generation's runtime and settings;
   * running sessions keep what they were created with.
   */
  adoptRuntimeConfig(): void {
    const manager = this.options.runtimeConfig;
    if (!manager) return;
    this.modelRuntime = manager.runtime();
    this.settingsManager = manager.settingsManager();
    // The catalog is derived from the runtime; a config change replaces it.
    this.catalogCache = null;
  }

  /**
   * P1/P2 accessors live in `createMcpStatusOps` (file-size cap); the engine
   * forwards so its public surface is unchanged.
   */
  get mcpStatusOps() {
    return this.mcpOps;
  }

  reportMcpStatus(workspacePath: string, sessionId: string, statuses: McpServerStatus[]): void {
    this.mcpOps.reportMcpStatus(workspacePath, sessionId, statuses);
  }

  refreshMcpStatus() {
    return this.mcpOps.refreshMcpStatus();
  }

  requestMcpStatus(settleMs?: number) {
    return this.mcpOps.requestMcpStatus(settleMs);
  }

  piResources(workspacePath: string) {
    return this.mcpOps.piResources(workspacePath);
  }

  appliedConfig() {
    return this.mcpOps.appliedConfig();
  }

  onSessionsChanged(listener: (workspacePath: string) => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  async dispose(): Promise<void> {
    // 修复依据：此前 teardownSession 以 fire-and-forget 方式触发，abort 之后 pi
    // 仍会在后台落盘会话/元数据；测试或进程退出后临时目录被删，随即出现 ENOENT
    // rename，并作为“测试结束后的异步活动”把无关用例判失败。这里等所有会话真正
    // 关闭（写入顺序保持不变），dispose 返回后不再有后台写。
    await Promise.allSettled([...this.sessions.values()].map((open) => teardownSession(open)));
    // P1: the hidden status session owns MCP connections too; close them before exit.
    await this.mcpOps.disposeStatusProbe();
    this.sessions.clear();
    this.listeners.clear();
    this.attachments.dispose();
  }

  // ── internals ──

  attach(open: OpenSession): void {
    const sessionId = open.sessionId ?? open.session.sessionId;
    // pi events of a draft can arrive before the engine resolved it (a background
    // start racing the create call); own the id now that the session exists.
    this.sessions.get(sessionId)?.unsubscribe();
    this.sessions.set(sessionId, open);
  }

  onPiEventHook(path: string, sessionId: string, event: AgentSessionEvent): Promise<void> {
    return createEngineEventHook({
      logger: this.options.logger,
      metadata: this.metadata,
      compactOps: this.compactOps,
      attachments: this.attachmentOps,
      sessions: this.sessions,
      emit: (id, e) => this.emitEngineEvent(id, e),
      sessionsChanged: (path) => this.emitSessionsChanged(path),
      rememberCompactionEntry: (id, entryId) => this.compactionEntries.set(id, entryId),
    })(path, sessionId, event);
  }

  /** Synthetic engine events (title/model/followup) travel the same stream as pi events. */
  emitEngineEvent(sessionId: string, event: EngineEvent): void {
    for (const listener of this.sessions.get(sessionId)?.listeners ?? []) listener(event);
  }

  conversationOps(): ConversationOpsDeps {
    return createConversationOps({
      logger: this.options.logger,
      metadata: this.metadata,
      compactOps: this.compactOps,
      compactionEntryFor: (id) => this.compactionEntries.get(id),
      open: (id) => this.requireOpen(id),
      sessionsChanged: (path) => this.emitSessionsChanged(path),
      runtime: () => this.requireRuntime(),
      factoryDeps: () => this.sessionFactoryDeps(),
      attach: (open) => this.attach(open),
    });
  }

  startPrompt(
    workspacePath: string,
    sessionId: string,
    text: string,
    requestId: string | undefined,
    payload?: PromptAttachmentPayload,
    onStart?: (session: import("@earendil-works/pi-coding-agent").AgentSession) => void,
  ): Promise<string> {
    return this.promptHooks().startPrompt(
      workspacePath,
      sessionId,
      text,
      requestId,
      payload,
      onStart,
    );
  }

  applySelection(sessionId: string, selection: ModelSelection): Promise<void> {
    return this.promptHooks().applySelection(sessionId, selection);
  }

  private promptHooks() {
    return createPromptHooks({
      metadata: this.metadata,
      sessions: this.sessions,
      configOps: this.configOps,
      logger: this.options.logger,
      runtime: () => this.requireRuntime(),
      sessionsChanged: (path) => this.emitSessionsChanged(path),
      currentMcp: () =>
        mcpSessionWiring(this.options.runtimeConfig?.current.config.mcp ?? EMPTY_MCP_GENERATION),
      reopenDraft: (sessionId) => this.sessionLifecycle.reopenDraft(sessionId),
    });
  }

  requireRuntime(): ModelRuntime {
    if (!this.modelRuntime) throw new Error("Engine is not initialized");
    return this.modelRuntime;
  }

  requireOpen(sessionId: string): OpenSession {
    const open = this.sessions.get(sessionId);
    if (!open) throw new Error(`Session ${sessionId} is not open`);
    return open;
  }

  emitSessionsChanged(workspacePath: string): void {
    for (const listener of this.listeners) listener(workspacePath);
  }

  sessionFactoryDeps(): Parameters<typeof openPiSession>[0] {
    return createSessionFactoryDeps({
      options: this.options,
      settingsManager: () => this.settingsManager,
      runtime: () => this.requireRuntime(),
      compactOps: this.compactOps,
      fileOps: this.fileOps,
      attachmentOps: this.attachmentOps,
      sessions: this.sessions,
      metadata: this.metadata,
      onPiEvent: (w, id, event) => void this.onPiEventHook(w, id, event),
      onMcpStatus: (workspacePath, sessionId, statuses) =>
        this.reportMcpStatus(workspacePath, sessionId, statuses),
    });
  }
}
