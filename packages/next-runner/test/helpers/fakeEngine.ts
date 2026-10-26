import type * as T from "../../src/engine/types.js";
import { defaultTitle, SAMPLE_MODEL } from "./fakeEngineData.js";
import { FakeConversationOps } from "./fakeEngineConversation.js";
import { FakeFileOps, FileOpsDelegation } from "./fakeEngineFiles.js";
import type { WithFileOps } from "./fakeEngineFiles.js";
import type { Engine } from "../../src/engine/types.js";
import { SessionLifecycle } from "./fakeEngineSession.js";

/** In-process Engine double: never touches pi; tests drive `emit()` and inspect writes. */
export class FakeEngine implements Engine, WithFileOps {
  readonly sessions = new Map<string, T.EngineSessionState>();
  readonly histories = new Map<string, T.EngineMessage[]>();
  readonly listeners = new Map<string, Set<(event: T.EngineEvent) => void>>();
  readonly metadataListeners = new Set<(workspacePath: string) => void>();
  readonly submitted: Array<{
    sessionId: string;
    text: string;
    requestId: string;
    attachments?: import("@nex/shared/nex-protocol-v4").AttachmentRef[];
  }> = [];
  readonly deleted: Array<{ workspacePath: string; sessionId: string }> = [];
  readonly renamed: Array<{ sessionId: string; title: string }> = [];
  readonly aborted: string[] = [];
  /** Test hook: interactionId → optionId, consumed by resolveApproval. */
  readonly approvals = new Map<string, string>();
  readonly resolvedApprovals: Array<{ sessionId: string; interactionId: string }> = [];
  /** Guide deliveries (`steer`) in call order (spec A: with their attachments). */
  readonly steered: Array<{
    sessionId: string;
    text: string;
    attachments?: import("@nex/shared/nex-protocol-v4").AttachmentRef[];
  }> = [];
  /** Sessions with an active run; drives `runState`/`steer`/`waitForIdle`. */
  readonly streaming = new Set<string>();
  /** Test hook: abort emits `settled{aborted}`. */
  abortSettles = true;
  /** Compact / rewind / fork doubles; the hooks live on `conversation` (test side). */
  readonly conversation = new FakeConversationOps(this);
  /** File-change / rewind doubles (spec B). */
  readonly files = new FakeFileOps();
  /** The `Engine` file-surface methods (kept in `fakeEngineFiles.ts` for the file budget). */
  private readonly fileOps = new FileOpsDelegation(this);

  fileCheckpointFacts = this.fileOps.fileCheckpointFacts;
  fileChanges = this.fileOps.fileChanges;
  fileRewindPreview = this.fileOps.fileRewindPreview;
  applyFileRewind = this.fileOps.applyFileRewind;
  fileCheckpointsEnabled = this.fileOps.fileCheckpointsEnabled;

  /** pi's steering queue per session (what `queue_update` reports). */
  readonly steering = new Map<string, string[]>();
  /** Test hooks: `submitDelayMs` holds `submit`, exposing the drain→visible window. */
  submitDelayMs = 0;
  submitting: Promise<void> | null = null;
  private readonly idleWaiters = new Map<string, Array<() => void>>();
  /** Session lifecycle methods live in `fakeEngineSession.ts` (file budget). */
  private readonly lifecycle = new SessionLifecycle(this);

  init = this.lifecycle.init;
  registerModel = this.lifecycle.registerModel;
  createSession = this.lifecycle.createSession;
  openSession = this.lifecycle.openSession;
  deleteSession = this.lifecycle.deleteSession;
  listSessions = this.lifecycle.listSessions;
  listDeletedSessionIds = this.lifecycle.listDeletedSessionIds;
  history = this.lifecycle.history;
  subscribe = this.lifecycle.subscribe;
  private readonly modes = new Map<string, { mode: T.PermissionMode; planEnabled: boolean }>();
  readonly tombstones = new Set<string>();
  sequence = 0;
  catalogValue: T.EngineCatalog = {
    models: [SAMPLE_MODEL],
    defaultSelection: {
      providerId: SAMPLE_MODEL.providerId,
      modelId: SAMPLE_MODEL.modelId,
      options: { reasoningLevel: "medium" },
    },
  };

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
    this.submitted.push({
      sessionId,
      text,
      requestId: options.requestId,
      ...(options.attachments?.length ? { attachments: options.attachments } : {}),
    });
    if (options.mode) {
      const mode: T.PermissionMode =
        options.mode === "edit" || options.mode === "yolo" ? options.mode : "build";
      this.modes.set(sessionId, { mode, planEnabled: options.planEnabled ?? false });
    }
    const session = this.sessions.get(sessionId);
    if (text.trim().length > 0) {
      // pi persists the user message and emits message_end (translated to user_input).
      const history = this.histories.get(sessionId) ?? [];
      this.histories.set(sessionId, [
        ...history,
        {
          role: "user",
          at: Date.now(),
          text,
          ...(options.attachments?.length ? { attachments: options.attachments } : {}),
        },
      ]);
      this.streaming.add(sessionId);
      this.emit(sessionId, {
        type: "user_input",
        at: Date.now(),
        text,
        ...(options.attachments?.length ? { attachments: options.attachments } : {}),
      });
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
  async steer(
    sessionId: string,
    text: string,
    attachments?: import("@nex/shared/nex-protocol-v4").AttachmentRef[],
  ): Promise<boolean> {
    if (!this.streaming.has(sessionId)) return false;
    this.steered.push({
      sessionId,
      text,
      ...(attachments && attachments.length > 0 ? { attachments } : {}),
    });
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

  /** Set by the attachment command tests: refs a restore must re-join (spec A). */
  setSweepGuard(guard: ((ref: string) => boolean) | null): void {
    this.sweepGuard = guard;
  }

  sweepGuard: ((ref: string) => boolean) | null = null;

  async catalog(): Promise<T.EngineCatalog> {
    return this.catalogValue;
  }

  /** P1: an empty live status set unless a test sets one. */
  mcpStatusValue: { owned: boolean; statuses: Record<string, never> } = {
    owned: false,
    statuses: {},
  };

  refreshMcpStatus(): { owned: boolean; statuses: Record<string, never> } {
    return this.mcpStatusValue;
  }

  async requestMcpStatus(): Promise<{ owned: boolean; statuses: Record<string, never> }> {
    return this.mcpStatusValue;
  }

  /** P2: skill commands listed by default. */
  appliedConfig(): { skillsEnabled: boolean } {
    return { skillsEnabled: true };
  }

  /** P2: no pi resources by default. */
  async piResources(): Promise<{ skills: []; prompts: [] }> {
    return { skills: [], prompts: [] };
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
