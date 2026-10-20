import { setImmediate as deferImmediate } from "node:timers";
import type { Logger } from "../logger.js";
import { newLogEpoch, randomId } from "../util/ids.js";
import type { Engine } from "../engine/types.js";
import { buildSessionsIndexSnapshot, type SessionPendingInteraction } from "./sessionsIndex.js";
import {
  FrameOrdinal,
  type CompleteSessionsIndexWireFrame,
  buildSessionsIndexFrame,
  sessionsIndexTopic,
} from "./envelopes.js";

type IndexSink = (frame: CompleteSessionsIndexWireFrame) => void;

/** UI workspace key: the single rule shared with the client (`identity?.trim() || path`). */
function workspaceKeyOf(params: { workspacePath: string; workspaceIdentity?: string }): string {
  const identity = params.workspaceIdentity?.trim();
  return identity || params.workspacePath;
}

interface IndexRuntime {
  /** Runner-local path: the engine keys sessions by it. */
  workspacePath: string;
  /** UI workspace key (`workspaceIdentity?.trim() || workspacePath`): topic + snapshot. */
  workspaceKey: string;
  subscriptionId: string;
  logEpoch: string;
  ordinal: FrameOrdinal;
  seq: number;
}

/**
 * sessions-index publisher. Every change is a full snapshot; the sidebar store replaces
 * its list and never gap-checks snapshots.
 */
export class SessionsIndexPublisher {
  private readonly indexes = new Map<string, IndexRuntime>();
  private readonly sinks = new Set<{ workspacePath: string; emit: IndexSink }>();
  private pendingSource: (workspacePath: string) => Map<string, SessionPendingInteraction[]> = () =>
    new Map();

  constructor(
    private readonly engine: Engine,
    private readonly logger: Logger,
  ) {}

  /** Live approvals come from the projection store; the index owns no approval state. */
  setPendingInteractionsSource(
    source: (workspacePath: string) => Map<string, SessionPendingInteraction[]>,
  ): void {
    this.pendingSource = source;
  }

  addSink(workspacePath: string, emit: IndexSink): { dispose(): void } {
    const entry = { workspacePath, emit };
    this.sinks.add(entry);
    return { dispose: () => this.sinks.delete(entry) };
  }

  subscribe(params: { workspacePath: string; workspaceIdentity?: string }): {
    ack: { subscriptionId: string; mode: "snapshot"; logEpoch: string };
  } {
    const subscriptionId = randomId("si");
    const runtime: IndexRuntime = {
      workspacePath: params.workspacePath,
      workspaceKey: workspaceKeyOf(params),
      subscriptionId,
      logEpoch: newLogEpoch(),
      ordinal: new FrameOrdinal(),
      seq: 0,
    };
    this.indexes.set(subscriptionId, runtime);
    deferImmediate(() => void this.send(runtime, "initial"));
    return { ack: { subscriptionId, mode: "snapshot", logEpoch: runtime.logEpoch } };
  }

  resync(params: { workspacePath: string; workspaceIdentity?: string; subscriptionId: string }): {
    ack: { subscriptionId: string; mode: "snapshot"; logEpoch: string };
  } {
    const runtime = this.indexes.get(params.subscriptionId);
    if (!runtime) return this.subscribe(params);
    // 同一 subscription 的 resync 必须沿用首次订阅读到的 workspaceKey；身份缺失的
    // resync 不能把已按 identity 归属的 topic 悄悄改回 path。
    deferImmediate(() => void this.send(runtime, "recovery"));
    return {
      ack: {
        subscriptionId: params.subscriptionId,
        mode: "snapshot",
        logEpoch: runtime.logEpoch,
      },
    };
  }

  unsubscribe(subscriptionId: string): void {
    this.indexes.delete(subscriptionId);
  }

  /** Pushes a fresh snapshot to every subscription for one workspace path. */
  publishWorkspace(workspacePath: string): void {
    for (const runtime of this.indexes.values()) {
      if (runtime.workspacePath !== workspacePath) continue;
      void this.send(runtime, "online");
    }
  }

  dispose(): void {
    this.indexes.clear();
    this.sinks.clear();
  }

  private async send(
    runtime: IndexRuntime,
    deliveryKind: "initial" | "online" | "recovery",
  ): Promise<void> {
    const sessions = await this.engine.listSessions(runtime.workspacePath);
    const snapshot = buildSessionsIndexSnapshot(
      runtime.workspaceKey,
      runtime.logEpoch,
      sessions,
      this.pendingSource(runtime.workspacePath),
    );
    runtime.seq += 1;
    const next = runtime.ordinal.next(runtime.subscriptionId);
    const wire = buildSessionsIndexFrame({
      topic: sessionsIndexTopic(runtime.workspaceKey),
      subscriptionId: runtime.subscriptionId,
      ordinal: next.ordinal,
      logicalFrameId: next.logicalFrameId,
      deliveryKind,
      snapshot,
      toSeq: runtime.seq,
      sentAt: Date.now(),
    });
    for (const sink of this.sinks) {
      if (sink.workspacePath !== runtime.workspacePath) continue;
      try {
        sink.emit(wire);
      } catch (error) {
        this.logger.warn("sessions index sink failed", {
          message: error instanceof Error ? error.message : String(error),
        });
      }
    }
  }
}
