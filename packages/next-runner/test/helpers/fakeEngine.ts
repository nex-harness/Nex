import type * as T from "../../src/engine/types.js";
import { defaultTitle, modelState, SAMPLE_MODEL } from "./fakeEngineData.js";
import { FakeConversationOps } from "./fakeEngineConversation.js";
import type { Engine } from "../../src/engine/types.js";

/** In-process Engine double: never touches pi; tests drive `emit()` and inspect writes. */
export class FakeEngine implements Engine {
  readonly sessions = new Map<string, T.EngineSessionState>();
  readonly histories = new Map<string, T.EngineMessage[]>();
  readonly listeners = new Map<string, Set<(event: T.EngineEvent) => void>>();
  readonly metadataListeners = new Set<(workspacePath: string) => void>();
  readonly submitted: Array<{ sessionId: string; text: string; requestId: string }> = [];
  readonly deleted: Array<{ workspacePath: string; sessionId: string }> = [];
  readonly renamed: Array<{ sessionId: string; title: string }> = [];
  readonly aborted: string[] = [];
  /** Test hook: interactionId → optionId, consumed by resolveApproval. */
  readonly approvals = new Map<string, string>();
  readonly resolvedApprovals: Array<{ sessionId: string; interactionId: string }> = [];
  /** Guide deliveries (`steer`) in call order. */
  readonly steered: Array<{ sessionId: string; text: string }> = [];
  /** pi's steering queue per session (what `queue_update` reports). */
  readonly steering = new Map<string, string[]>();
  /** Sessions with an active run; drives `runState`/`steer`/`waitForIdle`. */
  readonly streaming = new Set<string>();
  /** Test hook: abort emits `settled{aborted}`. */
  abortSettles = true;
  /** Compact / rewind / fork doubles; the hooks live on `conversation` (test side). */
  readonly conversation = new FakeConversationOps(this);
  /** Test hook: `submit` holds this long, exposing the drain→visible window. */
  submitDelayMs = 0;
  /** Set by `submit` while a delayed submission is in flight. */
  submitting: Promise<void> | null = null;
  private readonly idleWaiters = new Map<string, Array<() => void>>();
  private readonly modes = new Map<string, { mode: T.PermissionMode; planEnabled: boolean }>();
  private readonly tombstones = new Set<string>();
  private sequence = 0;
  private catalogValue: T.EngineCatalog = {
    models: [SAMPLE_MODEL],
    defaultSelection: {
      providerId: SAMPLE_MODEL.providerId,
      modelId: SAMPLE_MODEL.modelId,
      options: { reasoningLevel: "medium" },
    },
  };

  async init(): Promise<void> {}

  /** Test hook: adds a catalog model so a switch can change the model identity. */
  registerModel(info: T.EngineModelInfo): void {
    this.catalogValue = { ...this.catalogValue, models: [...this.catalogValue.models, info] };
  }

  async createSession(
    workspacePath: string,
    options: T.CreateEngineSessionOptions,
  ): Promise<{ sessionId: string }> {
    this.sequence += 1;
    const sessionId = `sess-${this.sequence}`;
    const now = 1_700_000_000_000 + this.sequence;
    this.sessions.set(sessionId, {
      sessionId,
      workspacePath,
      title: "",
      titleSource: "default",
      createdAt: now,
      lastActivityAt: now,
      phase: "draft",
      model: modelState(options.modelSelection),
      usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      isRunning: false,
    });
    this.histories.set(sessionId, []);
    this.emitSessionsChanged(workspacePath);
    return { sessionId };
  }

  async openSession(_workspacePath: string, sessionId: string): Promise<void> {
    if (this.tombstones.has(sessionId) || !this.sessions.has(sessionId)) {
      throw new Error(`session ${sessionId} not found`);
    }
  }

  async deleteSession(workspacePath: string, sessionId: string): Promise<void> {
    this.deleted.push({ workspacePath, sessionId });
    this.sessions.delete(sessionId);
    this.tombstones.add(sessionId);
    this.emitSessionsChanged(workspacePath);
  }

  async listSessions(workspacePath: string): Promise<T.EngineSessionState[]> {
    return [...this.sessions.values()].filter((item) => item.workspacePath === workspacePath);
  }

  async listDeletedSessionIds(_workspacePath?: string): Promise<string[]> {
    return [...this.tombstones];
  }

  async history(sessionId: string): Promise<T.EngineMessage[]> {
    return this.histories.get(sessionId) ?? [];
  }

  subscribe(sessionId: string, listener: (e: T.EngineEvent) => void): T.EngineEventSubscription {
    const set = this.listeners.get(sessionId) ?? new Set();
    set.add(listener);
    this.listeners.set(sessionId, set);
    return { dispose: () => set.delete(listener) };
  }

  async submit(
    sessionId: string,
    text: string,
    options: T.SubmitOptions,
  ): Promise<T.SubmitOutcome> {
    if (this.submitDelayMs > 0) {
      this.submitting = new Promise((resolve) => setTimeout(resolve, this.submitDelayMs));
      await this.submitting;
      this.submitting = null;
    }
    this.submitted.push({ sessionId, text, requestId: options.requestId });
    if (options.mode) {
      const mode: T.PermissionMode =
        options.mode === "edit" || options.mode === "yolo" ? options.mode : "build";
      this.modes.set(sessionId, { mode, planEnabled: options.planEnabled ?? false });
    }
    const session = this.sessions.get(sessionId);
    if (text.trim().length > 0) {
      // pi persists the user message and emits message_end (translated to user_input).
      const history = this.histories.get(sessionId) ?? [];
      this.histories.set(sessionId, [...history, { role: "user", at: Date.now(), text }]);
      this.streaming.add(sessionId);
      this.emit(sessionId, { type: "user_input", at: Date.now(), text });
    }
    if (session) {
      this.sessions.set(sessionId, {
        ...session,
        phase: "running",
        title: session.title || defaultTitle(text),
        lastActivityAt: Date.now(),
      });
    }
    this.emitSessionsChanged(session?.workspacePath ?? "");
    return { status: "accepted", inputId: options.requestId };
  }

  async resolveApproval(
    sessionId: string,
    interactionId: string,
    answer: { optionId?: string; freeText?: string },
  ): Promise<T.ResolveApprovalResult> {
    if (!this.approvals.has(interactionId)) return { resolved: false };
    this.approvals.delete(interactionId);
    this.resolvedApprovals.push({ sessionId, interactionId });
    const outcome = answer.optionId === "deny" ? "deny" : "allow";
    const toolCallId = `call-${interactionId}`;
    this.emit(sessionId, {
      type: "approval_settled",
      at: Date.now(),
      interactionId,
      toolCallId,
      outcome,
    });
    return { resolved: true, outcome };
  }

  permissionMode(sessionId: string): { mode: T.PermissionMode; planEnabled: boolean } | null {
    return this.modes.get(sessionId) ?? null;
  }

  async abort(sessionId: string): Promise<void> {
    this.aborted.push(sessionId);
    // Mirrors pi's abort→settle (the fold/queue would otherwise stay "running").
    this.streaming.delete(sessionId);
    if (this.abortSettles) this.emit(sessionId, { type: "settled", at: Date.now(), aborted: true });
  }

  /** Guide double: records the steer and emits pi's `queue_update`. */
  async steer(sessionId: string, text: string): Promise<boolean> {
    if (!this.streaming.has(sessionId)) return false;
    this.steered.push({ sessionId, text });
    this.steering.set(sessionId, [...(this.steering.get(sessionId) ?? []), text]);
    this.emitQueueUpdate(sessionId);
    return true;
  }

  runState(sessionId: string): { streaming: boolean; idle: boolean } {
    const streaming = this.streaming.has(sessionId);
    return { streaming, idle: !streaming };
  }

  async waitForIdle(sessionId: string): Promise<void> {
    if (!this.streaming.has(sessionId)) return;
    await new Promise<void>((resolve) =>
      this.idleWaiters.set(sessionId, [...(this.idleWaiters.get(sessionId) ?? []), resolve]),
    );
  }

  clearPiQueue(sessionId: string): void {
    this.steering.set(sessionId, []);
    this.emitQueueUpdate(sessionId);
  }

  // ── queue test hooks ──

  /** Test hook: finish the current run the way pi would (assistant_end + settled). */
  settle(sessionId: string, options: { aborted?: boolean } = {}): void {
    const aborted = options.aborted ?? false;
    this.streaming.delete(sessionId);
    this.emit(sessionId, { type: "settled", at: Date.now(), aborted });
    const waiters = this.idleWaiters.get(sessionId) ?? [];
    this.idleWaiters.delete(sessionId);
    for (const resolve of waiters) resolve();
  }

  /** Test hook: pi consumes one steering text at the next tool-batch boundary. */
  consumeSteer(sessionId: string, text?: string): void {
    const queue = this.steering.get(sessionId) ?? [];
    const next = text === undefined ? queue.slice(1) : queue.filter((entry) => entry !== text);
    this.steering.set(sessionId, next);
    this.emitQueueUpdate(sessionId);
  }

  private emitQueueUpdate(sessionId: string): void {
    this.emit(sessionId, {
      type: "pi_queue_update",
      at: Date.now(),
      steering: [...(this.steering.get(sessionId) ?? [])],
      followUp: [],
    });
  }

  async rename(sessionId: string, title: string): Promise<void> {
    this.renamed.push({ sessionId, title });
    const session = this.sessions.get(sessionId);
    if (session) {
      this.sessions.set(sessionId, { ...session, title, titleSource: "custom" });
      // Mirrors PiEngine.rename: one metadata write, one sessions-changed fan-out.
      this.emitSessionsChanged(session.workspacePath);
    }
  }

  async setModel(): Promise<void> {}

  async switchModelConfig(
    workspacePath: string,
    sessionId: string,
    input: T.SwitchModelConfigInput,
  ): Promise<T.SwitchModelConfigOutcome> {
    const session = this.sessions.get(sessionId);
    if (!session) return { status: "failed", reasonCode: "session.notOpen" };
    const providerId = input.provider.trim();
    const modelId = input.model.trim();
    const known = this.catalogValue.models.find(
      (model) => model.providerId === providerId && model.modelId === modelId,
    );
    if (!known) return { status: "failed", reasonCode: "provider.notInRegistry" };
    const current = session.model;
    const previous = current ? { provider: current.providerId, model: current.modelId } : undefined;
    const identityChanged =
      !current || current.providerId !== providerId || current.modelId !== modelId;
    const thought = input.thought.trim() || current?.thought || "off";
    if (!identityChanged && thought === current?.thought) {
      return {
        status: "unchanged",
        reasonCode: "config.unchanged",
        ...(current ? { model: current } : {}),
        modelIdentityChanged: false,
        ...(previous ? { previous } : {}),
      };
    }
    const model: T.EngineModelState = {
      providerId,
      modelId,
      displayName: known.modelName,
      thought,
      thoughtLevels: known.reasoningLevels,
    };
    this.sessions.set(sessionId, { ...session, model });
    this.emit(sessionId, { type: "model_changed", at: Date.now(), model });
    if (identityChanged) {
      this.emit(sessionId, {
        type: "model_change_marker",
        at: Date.now(),
        ...(previous ? { from: previous } : {}),
        to: { provider: providerId, model: modelId, thought },
      });
    }
    this.emitSessionsChanged(workspacePath);
    return {
      status: "changed",
      model,
      modelIdentityChanged: identityChanged,
      ...(previous ? { previous } : {}),
    };
  }

  async setFollowupMode(
    workspacePath: string,
    sessionId: string,
    mode: T.EngineFollowupMode,
  ): Promise<{ previous: T.EngineFollowupMode; mode: T.EngineFollowupMode }> {
    const session = this.sessions.get(sessionId);
    const previous = session?.followupMode ?? "queue";
    if (session && previous !== mode) {
      this.sessions.set(sessionId, { ...session, followupMode: mode });
      this.emit(sessionId, { type: "followup_mode_changed", at: Date.now(), mode });
      this.emitSessionsChanged(workspacePath);
    }
    return { previous, mode };
  }

  /** Sidebar state double: single-writer semantics like the metadata store. */
  async setTaskState(
    workspacePath: string,
    sessionId: string,
    patch: { pinned?: boolean; archived?: boolean },
  ): Promise<void> {
    const session = this.sessions.get(sessionId);
    if (!session) throw new Error(`session ${sessionId} not found`);
    this.sessions.set(sessionId, { ...session, ...patch });
    this.emitSessionsChanged(workspacePath);
  }

  async setTaskUnread(
    workspacePath: string,
    sessionId: string,
    unreadAt: number | null,
    expectedUnreadAt?: number,
  ): Promise<T.EngineUnreadState> {
    const session = this.sessions.get(sessionId);
    if (!session) return { cleared: false };
    const unread = session.unreadAt;
    if (unreadAt === null && expectedUnreadAt !== undefined && unread !== expectedUnreadAt) {
      return { cleared: false, ...(unread !== undefined ? { unreadAt: unread } : {}) };
    }
    const next = { ...session };
    if (unreadAt === null) delete next.unreadAt;
    else next.unreadAt = unreadAt;
    this.sessions.set(sessionId, next);
    this.emitSessionsChanged(workspacePath);
    return {
      cleared: unreadAt === null,
      ...(next.unreadAt !== undefined ? { unreadAt: next.unreadAt } : {}),
    };
  }

  async deleteTaskIfArchived(workspacePath: string, sessionId: string): Promise<boolean> {
    if (this.sessions.get(sessionId)?.archived !== true || this.tombstones.has(sessionId)) {
      return false;
    }
    await this.deleteSession(workspacePath, sessionId);
    return true;
  }

  // Conversation features (decisions 5-7) delegate to the doubles in
  // `fakeEngineConversation.ts`; test hooks live on `conversation`.
  compactState(sessionId: string): T.EngineCompactState {
    return this.conversation.compactState(sessionId);
  }

  takeCompactOutcomes(sessionId: string): T.EngineCompactOutcome[] {
    return this.conversation.takeCompactOutcomes(sessionId);
  }

  compact(sessionId: string, sourceCommandId: string): Promise<T.EngineCompactOutcome> {
    return this.conversation.compact(sessionId, sourceCommandId);
  }

  reportCompactOutcome(sessionId: string, outcome: T.EngineCompactOutcome): void {
    this.conversation.reportCompactOutcome(sessionId, outcome);
  }

  rewind(sessionId: string, request: T.EngineRewindRequest): Promise<T.EngineRewindOutcome> {
    return this.conversation.rewind(sessionId, request);
  }

  userMessageBefore(
    sessionId: string,
    targetEntryId: string,
  ): Promise<T.EngineRewindTarget | null> {
    return this.conversation.userMessageBefore(sessionId, targetEntryId);
  }

  forkAssistant(
    workspacePath: string,
    sessionId: string,
    request: T.EngineForkRequest,
  ): Promise<T.EngineForkOutcome> {
    return this.conversation.forkAssistant(workspacePath, sessionId, request);
  }

  async catalog(): Promise<T.EngineCatalog> {
    return this.catalogValue;
  }

  setCatalog(catalog: T.EngineCatalog): void {
    this.catalogValue = catalog;
  }

  onSessionsChanged(listener: (workspacePath: string) => void): () => void {
    this.metadataListeners.add(listener);
    return () => this.metadataListeners.delete(listener);
  }

  async dispose(): Promise<void> {
    this.listeners.clear();
    this.metadataListeners.clear();
  }

  emit(sessionId: string, event: T.EngineEvent): void {
    // Keep the run-state double honest for tests that drive `settled` by hand.
    if (event.type === "settled") this.streaming.delete(sessionId);
    for (const listener of this.listeners.get(sessionId) ?? []) listener(event);
  }

  emitSessionsChanged(workspacePath: string): void {
    for (const listener of this.metadataListeners) listener(workspacePath);
  }

  /** Seeds an already-persisted session for restore tests. */
  seedSession(session: T.EngineSessionState, history: T.EngineMessage[]): void {
    this.sessions.set(session.sessionId, session);
    this.histories.set(session.sessionId, history);
  }
}
export { SAMPLE_MODEL } from "./fakeEngineData.js";
