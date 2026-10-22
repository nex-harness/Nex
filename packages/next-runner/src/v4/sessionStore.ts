import { setImmediate as deferImmediate } from "node:timers";
import type { CommandAck } from "@nex/shared/nex-protocol-v4";
import type { Logger } from "../logger.js";
import { newLogEpoch, randomId } from "../util/ids.js";
import type {
  Engine,
  EngineEvent,
  EngineEventSubscription,
  EngineSessionState,
} from "../engine/types.js";
import { applyEngineEvent } from "../projection/fold.js";
import { projectHistory } from "../projection/restore.js";
import { buildConversationSnapshot } from "../projection/snapshot.js";
import { createProjection, type ProjectionState } from "../projection/state.js";
import {
  FrameOrdinal,
  type CompleteConversationWireFrame,
  buildConversationFrame,
  conversationTopic,
} from "./envelopes.js";
import type { SessionPendingInteraction } from "./sessionsIndex.js";
import { SnapshotThrottle } from "./throttle.js";

export type FrameSink = (frame: CompleteConversationWireFrame) => void;

export interface SessionRuntime {
  sessionId: string;
  workspacePath: string;
  logEpoch: string;
  projection: ProjectionState;
  engineSubscription: EngineEventSubscription | null;
  hydrated: boolean;
  /** Resolves when history is folded and the engine session is open; never rejects. */
  ready: Promise<void>;
  /** Events observed while history was being read; replayed after the base projection. */
  bufferedEvents: EngineEvent[];
  throttle: SnapshotThrottle;
  ordinals: Map<string, FrameOrdinal>;
  subscribers: Set<string>;
  /** commandId → ACK; owned by CommandHandler, stored here so it dies with the session. */
  acceptedCommands: Map<string, CommandAck>;
  disposed: boolean;
}

/**
 * Owns the in-memory session projections used for conversation frames. State is derived:
 * the engine owns pi sessions, history/events are folded into `projection`, and sinks
 * only receive snapshots. No other component writes these fields.
 */
export class SessionRuntimeStore {
  readonly runtimes = new Map<string, SessionRuntime>();
  private readonly sinks = new Set<{ workspacePath: string; emit: FrameSink }>();
  private readonly pendingListeners = new Set<(workspacePath: string) => void>();

  constructor(
    private readonly engine: Engine,
    private readonly logger: Logger,
  ) {}

  addSink(workspacePath: string, emit: FrameSink): { dispose(): void } {
    const entry = { workspacePath, emit };
    this.sinks.add(entry);
    return { dispose: () => this.sinks.delete(entry) };
  }

  /**
   * Fires when a session's live approval set changes. The sessions index (sidebar attention
   * dot) is derived from that set, so it republishes without the projection owning it.
   */
  onPendingApprovalsChanged(listener: (workspacePath: string) => void): () => void {
    this.pendingListeners.add(listener);
    return () => {
      this.pendingListeners.delete(listener);
    };
  }

  /** Live approvals per session for one workspace (first item is the sidebar's summary). */
  pendingInteractions(workspacePath: string): Map<string, SessionPendingInteraction[]> {
    const result = new Map<string, SessionPendingInteraction[]>();
    for (const runtime of this.runtimes.values()) {
      if (runtime.workspacePath !== workspacePath) continue;
      const entries = Object.values(runtime.projection.pendingApprovals).map((approval) => ({
        interactionId: approval.interactionId,
        toolName: approval.toolName,
      }));
      if (entries.length > 0) result.set(runtime.sessionId, entries);
    }
    return result;
  }

  ensure(workspacePath: string, sessionId: string): SessionRuntime {
    const existing = this.runtimes.get(sessionId);
    if (existing) return existing;
    const logEpoch = newLogEpoch();
    const runtime: SessionRuntime = {
      sessionId,
      workspacePath,
      logEpoch,
      projection: createProjection({ sessionId, logEpoch, createdAt: Date.now() }),
      engineSubscription: null,
      hydrated: false,
      ready: Promise.resolve(),
      bufferedEvents: [],
      throttle: new SnapshotThrottle({ maxPerSecond: 10, flush: () => this.flush(runtime) }),
      ordinals: new Map(),
      subscribers: new Set(),
      acceptedCommands: new Map(),
      disposed: false,
    };
    this.runtimes.set(sessionId, runtime);
    runtime.ready = this.hydrate(runtime);
    return runtime;
  }

  get(sessionId: string): SessionRuntime | undefined {
    return this.runtimes.get(sessionId);
  }

  subscribe(
    workspacePath: string,
    sessionId: string,
  ): {
    subscriptionId: string;
    logEpoch: string;
    runtime: SessionRuntime;
  } {
    const runtime = this.ensure(workspacePath, sessionId);
    const subscriptionId = randomId("sub");
    runtime.subscribers.add(subscriptionId);
    runtime.ordinals.set(subscriptionId, new FrameOrdinal());
    const ordinal = runtime.ordinals.get(subscriptionId) as FrameOrdinal;
    this.afterAck(() => {
      void this.sendSnapshot(runtime, subscriptionId, ordinal, "initial");
    });
    return { subscriptionId, logEpoch: runtime.logEpoch, runtime };
  }

  findBySubscription(subscriptionId: string, sessionId?: string): SessionRuntime | undefined {
    if (sessionId) {
      const runtime = this.runtimes.get(sessionId);
      return runtime?.subscribers.has(subscriptionId) ? runtime : undefined;
    }
    for (const runtime of this.runtimes.values()) {
      if (runtime.subscribers.has(subscriptionId)) return runtime;
    }
    return undefined;
  }

  resync(runtime: SessionRuntime, subscriptionId: string): void {
    const ordinal = runtime.ordinals.get(subscriptionId) ?? new FrameOrdinal();
    runtime.ordinals.set(subscriptionId, ordinal);
    this.afterAck(() => {
      void this.sendSnapshot(runtime, subscriptionId, ordinal, "recovery");
    });
  }

  unsubscribe(runtime: SessionRuntime, subscriptionId: string): void {
    runtime.throttle.flushPending();
    runtime.subscribers.delete(subscriptionId);
    runtime.ordinals.delete(subscriptionId);
  }

  /** Forces an immediate snapshot on every active subscription (metadata changes). */
  publish(runtime: SessionRuntime): void {
    runtime.throttle.flushPending();
    this.flush(runtime);
  }

  async sessionStates(workspacePath: string): Promise<EngineSessionState[]> {
    return this.engine.listSessions(workspacePath);
  }

  /**
   * True while at least one browser holds a conversation subscription. Background
   * completions of unwatched sessions raise the sidebar unread badge
   * (docs/next/task-state.md).
   */
  isWatched(sessionId: string): boolean {
    const runtime = this.runtimes.get(sessionId);
    return runtime !== undefined && !runtime.disposed && runtime.subscribers.size > 0;
  }

  disposeRuntime(sessionId: string): void {
    const runtime = this.runtimes.get(sessionId);
    if (!runtime) return;
    runtime.disposed = true;
    runtime.throttle.dispose();
    runtime.engineSubscription?.dispose();
    runtime.engineSubscription = null;
    this.runtimes.delete(sessionId);
  }

  dispose(): void {
    for (const sessionId of this.runtimes.keys()) this.disposeRuntime(sessionId);
    this.sinks.clear();
  }

  private async hydrate(runtime: SessionRuntime): Promise<void> {
    try {
      await this.engine.openSession(runtime.workspacePath, runtime.sessionId);
      // Subscribe before reading history so no live event falls between the two.
      runtime.engineSubscription ??= this.engine.subscribe(runtime.sessionId, (event) =>
        this.onEngineEvent(runtime, event),
      );
      const sessions = await this.engine.listSessions(runtime.workspacePath);
      const meta = sessions.find((item) => item.sessionId === runtime.sessionId);
      const history = await this.engine.history(runtime.sessionId);
      if (meta) runtime.projection = projectHistory(meta, history, runtime.logEpoch);
      // The gate is the mode authority; a mode command may have landed while history was
      // being read, so re-apply it after the base projection exists.
      const liveMode = this.engine.permissionMode(runtime.sessionId);
      if (liveMode) {
        runtime.projection = {
          ...runtime.projection,
          mode: liveMode.mode,
          planEnabled: liveMode.planEnabled,
        };
      }
      const buffered = runtime.bufferedEvents;
      runtime.bufferedEvents = [];
      runtime.hydrated = true;
      for (const event of buffered) {
        runtime.projection = applyEngineEvent(runtime.projection, event);
      }
      if (
        buffered.some(
          (event) => event.type === "approval_request" || event.type === "approval_settled",
        )
      ) {
        this.notifyPendingChanged(runtime.workspacePath);
      }
      if (buffered.length > 0) runtime.throttle.request();
    } catch (error) {
      runtime.hydrated = true;
      this.logger.warn("session hydrate failed", {
        sessionId: runtime.sessionId,
        message: error instanceof Error ? error.message : String(error),
      });
    }
  }

  private onEngineEvent(runtime: SessionRuntime, event: EngineEvent): void {
    if (runtime.disposed) return;
    if (!runtime.hydrated) {
      runtime.bufferedEvents.push(event);
      return;
    }
    runtime.projection = applyEngineEvent(runtime.projection, event);
    runtime.throttle.request();
    // Only approvals change the sidebar attention dot; every other event keeps the set.
    if (event.type === "approval_request" || event.type === "approval_settled") {
      this.notifyPendingChanged(runtime.workspacePath);
    }
  }

  private notifyPendingChanged(workspacePath: string): void {
    for (const listener of this.pendingListeners) listener(workspacePath);
  }

  private flush(runtime: SessionRuntime): void {
    if (runtime.disposed) return;
    for (const subscriptionId of runtime.subscribers) {
      const ordinal = runtime.ordinals.get(subscriptionId);
      if (!ordinal) continue;
      this.emit(runtime, subscriptionId, ordinal, "online");
    }
  }

  private async sendSnapshot(
    runtime: SessionRuntime,
    subscriptionId: string,
    ordinal: FrameOrdinal,
    deliveryKind: "initial" | "online" | "recovery",
  ): Promise<void> {
    while (!runtime.hydrated) await new Promise<void>((resolve) => setImmediate(resolve));
    if (!runtime.subscribers.has(subscriptionId) || runtime.disposed) return;
    this.emit(runtime, subscriptionId, ordinal, deliveryKind);
  }

  private emit(
    runtime: SessionRuntime,
    subscriptionId: string,
    ordinal: FrameOrdinal,
    deliveryKind: "initial" | "online" | "recovery",
  ): void {
    const snapshot = buildConversationSnapshot(runtime.projection);
    const next = ordinal.next(subscriptionId);
    const wire = buildConversationFrame({
      topic: conversationTopic(runtime.sessionId),
      subscriptionId,
      ordinal: next.ordinal,
      logicalFrameId: next.logicalFrameId,
      deliveryKind,
      snapshot,
      toSeq: runtime.projection.seq,
      sentAt: Date.now(),
    });
    for (const sink of this.sinks) {
      if (sink.workspacePath !== runtime.workspacePath) continue;
      try {
        sink.emit(wire);
      } catch (error) {
        this.logger.warn("conversation frame sink failed", {
          message: error instanceof Error ? error.message : String(error),
        });
      }
    }
  }

  /**
   * Initial/recovery frames must follow the subscribe ACK. `setImmediate` runs after the
   * current microtask queue, where the link's awaited `call` handler writes the result
   * frame; `queueMicrotask` would race ahead of that continuation.
   */
  private afterAck(task: () => void): void {
    deferImmediate(() => {
      try {
        task();
      } catch (error) {
        this.logger.warn("post-ack task failed", {
          message: error instanceof Error ? error.message : String(error),
        });
      }
    });
  }
}
