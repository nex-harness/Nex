import type * as T from "../../src/engine/types.js";

/**
 * Conversation-feature doubles (compact / rewind / fork) for `FakeEngine`
 * (docs/next/conversation-features.md decisions 5-7). Kept separate so the engine double
 * stays inside the file budget; every method is driven by test hooks on the host.
 */
export interface FakeConversationHost {
  sessions: Map<string, T.EngineSessionState>;
  histories: Map<string, T.EngineMessage[]>;
  emit(sessionId: string, event: T.EngineEvent): void;
  emitSessionsChanged(workspacePath: string): void;
  createSession(workspacePath: string, options: {}): Promise<{ sessionId: string }>;
}

export class FakeConversationOps {
  /** Outcome of the next manual compact. */
  compactOutcome: T.EngineCompactOutcome = {
    status: "success",
    origin: "manual",
    tokensBefore: 100,
    tokensAfter: 20,
  };
  /** Sessions where pi reports `isCompacting` (auto compaction in flight). */
  readonly compacting = new Set<string>();
  readonly compacts: Array<{ sessionId: string; sourceCommandId: string }> = [];
  readonly rewinds: Array<{ sessionId: string; request: T.EngineRewindRequest }> = [];
  readonly forks: Array<{ sessionId: string; request: T.EngineForkRequest }> = [];
  /** Assistant entry id → owning user entry (retry's canonical intent). */
  readonly userCauses = new Map<string, T.EngineRewindTarget>();
  private readonly pendingCompactOutcomes = new Map<string, T.EngineCompactOutcome[]>();

  constructor(private readonly host: FakeConversationHost) {}

  compactState(sessionId: string): T.EngineCompactState {
    return {
      manualInFlight: false,
      queued: false,
      compacting: this.compacting.has(sessionId),
      pendingOutcomes: [...(this.pendingCompactOutcomes.get(sessionId) ?? [])],
    };
  }

  takeCompactOutcomes(sessionId: string): T.EngineCompactOutcome[] {
    const pending = this.pendingCompactOutcomes.get(sessionId) ?? [];
    this.pendingCompactOutcomes.delete(sessionId);
    return pending;
  }

  async compact(sessionId: string, sourceCommandId: string): Promise<T.EngineCompactOutcome> {
    this.compacts.push({ sessionId, sourceCommandId });
    return this.compactOutcome;
  }

  /** Test hook: an automatic compaction pi finished on its own. */
  reportCompactOutcome(sessionId: string, outcome: T.EngineCompactOutcome): void {
    const list = this.pendingCompactOutcomes.get(sessionId) ?? [];
    this.pendingCompactOutcomes.set(sessionId, [...list, outcome]);
  }

  async rewind(sessionId: string, request: T.EngineRewindRequest): Promise<T.EngineRewindOutcome> {
    this.rewinds.push({ sessionId, request });
    // Mirrors pi: the branch cut drops the target user entry and everything after it, then
    // the new prompt appends its user message.
    const history = this.host.histories.get(sessionId) ?? [];
    const cut = history.findIndex(
      (message) => message.role === "user" && message.text === request.target.text,
    );
    const kept = cut >= 0 ? history.slice(0, cut) : history;
    const attachments = request.attachments?.length ? request.attachments : undefined;
    this.host.histories.set(sessionId, [
      ...kept,
      {
        role: "user",
        at: Date.now(),
        text: request.newText,
        ...(attachments ? { attachments } : {}),
      },
    ]);
    this.host.emit(sessionId, {
      type: "user_input",
      at: Date.now(),
      text: request.newText,
      ...(attachments ? { attachments } : {}),
    });
    return { started: true };
  }

  async userMessageBefore(
    sessionId: string,
    targetEntryId: string,
  ): Promise<T.EngineRewindTarget | null> {
    void sessionId;
    return this.userCauses.get(targetEntryId) ?? null;
  }

  /** Fork double: a child session with the parent history plus the fork notice. */
  async forkAssistant(
    workspacePath: string,
    sessionId: string,
    request: T.EngineForkRequest,
  ): Promise<T.EngineForkOutcome> {
    this.forks.push({ sessionId, request });
    const { sessionId: childId } = await this.host.createSession(workspacePath, {});
    const parent = this.host.sessions.get(sessionId);
    this.host.sessions.set(childId, {
      ...(this.host.sessions.get(childId) as T.EngineSessionState),
      title: request.title,
      titleSource: "generated",
      phase: "completedSuccess",
      ...(parent?.model ? { model: parent.model } : {}),
    });
    const notice: T.EngineMessage = {
      role: "runnerMarker",
      kind: "forkNotice",
      at: Date.now(),
      entryId: `fork-${childId}`,
      parentSessionId: request.parentSessionId,
      parentRowId: request.parentRowId,
    };
    this.host.histories.set(childId, [...(this.host.histories.get(sessionId) ?? []), notice]);
    this.host.emitSessionsChanged(workspacePath);
    return { sessionId: childId, title: request.title, titleSource: "generated" };
  }
}
