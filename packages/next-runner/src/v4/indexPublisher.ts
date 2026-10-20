import { setImmediate as deferImmediate } from "node:timers";
import type { Logger } from "../logger.js";
import { newLogEpoch, randomId } from "../util/ids.js";
import type { Engine } from "../engine/types.js";
import { buildSessionsIndexSnapshot } from "./sessionsIndex.js";
import {
  FrameOrdinal,
  type CompleteSessionsIndexWireFrame,
  buildSessionsIndexFrame,
  sessionsIndexTopic,
} from "./envelopes.js";

type IndexSink = (frame: CompleteSessionsIndexWireFrame) => void;

interface IndexRuntime {
  workspaceId: string;
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

  constructor(
    private readonly engine: Engine,
    private readonly logger: Logger,
  ) {}

  addSink(workspacePath: string, emit: IndexSink): { dispose(): void } {
    const entry = { workspacePath, emit };
    this.sinks.add(entry);
    return { dispose: () => this.sinks.delete(entry) };
  }

  subscribe(workspacePath: string): {
    ack: { subscriptionId: string; mode: "snapshot"; logEpoch: string };
  } {
    const subscriptionId = randomId("si");
    const runtime: IndexRuntime = {
      workspaceId: workspacePath,
      subscriptionId,
      logEpoch: newLogEpoch(),
      ordinal: new FrameOrdinal(),
      seq: 0,
    };
    this.indexes.set(subscriptionId, runtime);
    deferImmediate(() => void this.send(runtime, "initial"));
    return { ack: { subscriptionId, mode: "snapshot", logEpoch: runtime.logEpoch } };
  }

  resync(
    workspacePath: string,
    subscriptionId: string,
  ): {
    ack: { subscriptionId: string; mode: "snapshot"; logEpoch: string };
  } {
    const runtime = this.indexes.get(subscriptionId);
    if (!runtime) return this.subscribe(workspacePath);
    deferImmediate(() => void this.send(runtime, "recovery"));
    return {
      ack: { subscriptionId, mode: "snapshot", logEpoch: runtime.logEpoch },
    };
  }

  unsubscribe(subscriptionId: string): void {
    this.indexes.delete(subscriptionId);
  }

  /** Pushes a fresh snapshot to every subscription for one workspace. */
  publishWorkspace(workspacePath: string): void {
    for (const runtime of this.indexes.values()) {
      if (runtime.workspaceId !== workspacePath) continue;
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
    const sessions = await this.engine.listSessions(runtime.workspaceId);
    const snapshot = buildSessionsIndexSnapshot(runtime.workspaceId, runtime.logEpoch, sessions);
    runtime.seq += 1;
    const next = runtime.ordinal.next(runtime.subscriptionId);
    const wire = buildSessionsIndexFrame({
      topic: sessionsIndexTopic(runtime.workspaceId),
      subscriptionId: runtime.subscriptionId,
      ordinal: next.ordinal,
      logicalFrameId: next.logicalFrameId,
      deliveryKind,
      snapshot,
      toSeq: runtime.seq,
      sentAt: Date.now(),
    });
    for (const sink of this.sinks) {
      if (sink.workspacePath !== runtime.workspaceId) continue;
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
