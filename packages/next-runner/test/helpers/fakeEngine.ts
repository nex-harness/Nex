import type {
  CreateEngineSessionOptions,
  Engine,
  EngineCatalog,
  EngineEvent,
  EngineEventSubscription,
  EngineMessage,
  EngineModelInfo,
  EngineModelState,
  EngineSessionState,
  PermissionMode,
  ResolveApprovalResult,
  SubmitOptions,
  SubmitOutcome,
} from "../../src/engine/types.js";

/**
 * In-process Engine double for projection/publisher tests. It never touches pi; tests
 * drive it with `emit()` and inspect what the runner recorded.
 */
export class FakeEngine implements Engine {
  readonly sessions = new Map<string, EngineSessionState>();
  readonly histories = new Map<string, EngineMessage[]>();
  readonly listeners = new Map<string, Set<(event: EngineEvent) => void>>();
  readonly metadataListeners = new Set<(workspacePath: string) => void>();
  readonly submitted: Array<{ sessionId: string; text: string; requestId: string }> = [];
  readonly deleted: Array<{ workspacePath: string; sessionId: string }> = [];
  readonly renamed: Array<{ sessionId: string; title: string }> = [];
  readonly aborted: string[] = [];
  /** Test hook: interactionId → optionId, consumed by resolveApproval. */
  readonly approvals = new Map<string, string>();
  readonly resolvedApprovals: Array<{ sessionId: string; interactionId: string }> = [];
  private readonly modes = new Map<string, { mode: PermissionMode; planEnabled: boolean }>();
  private readonly tombstones = new Set<string>();
  private sequence = 0;
  private catalogValue: EngineCatalog = {
    models: [SAMPLE_MODEL],
    defaultSelection: {
      providerId: SAMPLE_MODEL.providerId,
      modelId: SAMPLE_MODEL.modelId,
      options: { reasoningLevel: "medium" },
    },
  };

  async init(): Promise<void> {}

  async createSession(
    workspacePath: string,
    options: CreateEngineSessionOptions,
  ): Promise<{ sessionId: string }> {
    this.sequence += 1;
    const sessionId = `sess-${this.sequence}`;
    const model = modelState(options.modelSelection);
    this.sessions.set(sessionId, {
      sessionId,
      workspacePath,
      title: "",
      titleSource: "default",
      createdAt: 1_700_000_000_000 + this.sequence,
      lastActivityAt: 1_700_000_000_000 + this.sequence,
      phase: "draft",
      model,
      usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      isRunning: false,
    });
    this.histories.set(sessionId, []);
    this.emitSessionsChanged(workspacePath);
    return { sessionId };
  }

  async openSession(_workspacePath: string, sessionId: string): Promise<void> {
    if (this.tombstones.has(sessionId)) throw new Error("deleted");
    if (!this.sessions.has(sessionId)) throw new Error(`session ${sessionId} not found`);
  }

  async deleteSession(workspacePath: string, sessionId: string): Promise<void> {
    this.deleted.push({ workspacePath, sessionId });
    this.sessions.delete(sessionId);
    this.tombstones.add(sessionId);
    this.emitSessionsChanged(workspacePath);
  }

  async listSessions(workspacePath: string): Promise<EngineSessionState[]> {
    return [...this.sessions.values()].filter((item) => item.workspacePath === workspacePath);
  }

  async listDeletedSessionIds(_workspacePath?: string): Promise<string[]> {
    return [...this.tombstones];
  }

  async history(sessionId: string): Promise<EngineMessage[]> {
    return this.histories.get(sessionId) ?? [];
  }

  subscribe(sessionId: string, listener: (event: EngineEvent) => void): EngineEventSubscription {
    const set = this.listeners.get(sessionId) ?? new Set();
    set.add(listener);
    this.listeners.set(sessionId, set);
    return { dispose: () => set.delete(listener) };
  }

  async submit(sessionId: string, text: string, options: SubmitOptions): Promise<SubmitOutcome> {
    this.submitted.push({ sessionId, text, requestId: options.requestId });
    if (options.mode) {
      this.modes.set(sessionId, {
        mode: (options.mode === "edit" || options.mode === "yolo"
          ? options.mode
          : "build") as PermissionMode,
        planEnabled: options.planEnabled ?? false,
      });
    }
    const session = this.sessions.get(sessionId);
    if (text.trim().length > 0) {
      // pi persists the user message and emits message_end (translated to user_input).
      const history = this.histories.get(sessionId) ?? [];
      history.push({ role: "user", at: Date.now(), text });
      this.histories.set(sessionId, history);
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
  ): Promise<ResolveApprovalResult> {
    if (!this.approvals.has(interactionId)) return { resolved: false };
    this.approvals.delete(interactionId);
    this.resolvedApprovals.push({ sessionId, interactionId });
    const outcome = answer.optionId === "deny" ? "deny" : "allow";
    this.emit(sessionId, {
      type: "approval_settled",
      at: Date.now(),
      interactionId,
      toolCallId: `call-${interactionId}`,
      outcome,
    });
    return { resolved: true, outcome };
  }

  permissionMode(sessionId: string): { mode: PermissionMode; planEnabled: boolean } | null {
    return this.modes.get(sessionId) ?? null;
  }

  async abort(sessionId: string): Promise<void> {
    this.aborted.push(sessionId);
  }

  async rename(sessionId: string, title: string): Promise<void> {
    this.renamed.push({ sessionId, title });
    const session = this.sessions.get(sessionId);
    if (session) {
      this.sessions.set(sessionId, { ...session, title, titleSource: "custom" });
    }
  }

  async setModel(): Promise<void> {}

  async catalog(): Promise<EngineCatalog> {
    return this.catalogValue;
  }

  setCatalog(catalog: EngineCatalog): void {
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

  emit(sessionId: string, event: EngineEvent): void {
    for (const listener of this.listeners.get(sessionId) ?? []) listener(event);
  }

  emitSessionsChanged(workspacePath: string): void {
    for (const listener of this.metadataListeners) listener(workspacePath);
  }

  /** Seeds an already-persisted session for restore tests. */
  seedSession(session: EngineSessionState, history: EngineMessage[]): void {
    this.sessions.set(session.sessionId, session);
    this.histories.set(session.sessionId, history);
  }
}

export const SAMPLE_MODEL: EngineModelInfo = {
  providerId: "anthropic",
  providerName: "Anthropic",
  modelId: "claude-sonnet-4-5",
  modelName: "Claude Sonnet 4.5",
  apiType: "anthropic-messages",
  baseUrl: "https://api.anthropic.com",
  reasoningLevels: ["off", "low", "medium", "high"],
  maxOutputTokens: 8192,
  contextWindow: 200_000,
  supportsImage: true,
  supportsVideo: false,
  supportsAudio: false,
  supportsPdf: false,
};

function modelState(selection: CreateEngineSessionOptions["modelSelection"]): EngineModelState {
  return {
    providerId: selection?.providerId ?? SAMPLE_MODEL.providerId,
    modelId: selection?.modelId ?? SAMPLE_MODEL.modelId,
    displayName: SAMPLE_MODEL.modelName,
    thought: selection?.options?.reasoningLevel ?? "medium",
    thoughtLevels: SAMPLE_MODEL.reasoningLevels,
  };
}

function defaultTitle(text: string): string {
  const trimmed = text.trim().replace(/\s+/gu, " ");
  return trimmed.length > 60 ? `${trimmed.slice(0, 60)}…` : trimmed;
}
