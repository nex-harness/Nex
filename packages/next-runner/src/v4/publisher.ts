import type { CommandAck, CommandEnvelope } from "@nex/shared/nex-protocol-v4";
import type { Logger } from "../logger.js";
import type { Engine, EngineSessionState } from "../engine/types.js";
import { CommandHandler } from "./commands.js";
import type { CompleteConversationWireFrame, CompleteSessionsIndexWireFrame } from "./envelopes.js";
import { SessionsIndexPublisher } from "./indexPublisher.js";
import { SessionRuntimeStore } from "./sessionStore.js";

export interface PublisherOptions {
  engine: Engine;
  logger: Logger;
}

/**
 * v4 publisher facade. Split into three single-owner pieces:
 * - `SessionRuntimeStore`: engine events → per-session projection, conversation frames
 * - `SessionsIndexPublisher`: sessions-index snapshots
 * - `CommandHandler`: command admission/idempotency
 *
 * Every change is published as a full snapshot frame (`fromSeq: 0`, `toSeq: seq`,
 * `snapshot.seq === toSeq`); the UI replaces state on snapshots, so no delta log exists.
 */
export class ConversationPublisher {
  readonly sessions: SessionRuntimeStore;
  readonly index: SessionsIndexPublisher;
  private readonly commands: CommandHandler;
  private readonly engineUnsubscribe: () => void;

  constructor(private readonly options: PublisherOptions) {
    this.sessions = new SessionRuntimeStore(options.engine, options.logger);
    this.index = new SessionsIndexPublisher(options.engine, options.logger);
    this.commands = new CommandHandler(options.engine, this.sessions, options.logger);
    this.engineUnsubscribe = options.engine.onSessionsChanged((workspacePath) => {
      this.index.publishWorkspace(workspacePath);
    });
  }

  addConversationSink(
    workspacePath: string,
    emit: (frame: CompleteConversationWireFrame) => void,
  ): { dispose(): void } {
    return this.sessions.addSink(workspacePath, emit);
  }

  addSessionsIndexSink(
    workspacePath: string,
    emit: (frame: CompleteSessionsIndexWireFrame) => void,
  ): { dispose(): void } {
    return this.index.addSink(workspacePath, emit);
  }

  subscribeConversation(params: { workspacePath: string; sessionId: string }): {
    ack: { subscriptionId: string; mode: "snapshot"; logEpoch: string };
  } {
    const { subscriptionId, logEpoch } = this.sessions.subscribe(
      params.workspacePath,
      params.sessionId,
    );
    return { ack: { subscriptionId, mode: "snapshot", logEpoch } };
  }

  resyncConversation(params: { workspacePath: string; subscriptionId: string }): {
    ack: { subscriptionId: string; mode: "snapshot"; logEpoch: string };
  } {
    const runtime = this.sessions.findBySubscription(params.subscriptionId);
    if (!runtime) {
      return { ack: { subscriptionId: params.subscriptionId, mode: "snapshot", logEpoch: "" } };
    }
    this.sessions.resync(runtime, params.subscriptionId);
    return {
      ack: { subscriptionId: params.subscriptionId, mode: "snapshot", logEpoch: runtime.logEpoch },
    };
  }

  unsubscribeConversation(workspacePath: string, subscriptionId: string): void {
    const runtime = this.sessions.findBySubscription(subscriptionId);
    if (!runtime || runtime.workspacePath !== workspacePath) return;
    this.sessions.unsubscribe(runtime, subscriptionId);
  }

  subscribeSessionsIndex(params: { workspacePath: string }): {
    ack: { subscriptionId: string; mode: "snapshot"; logEpoch: string };
  } {
    return this.index.subscribe(params.workspacePath);
  }

  resyncSessionsIndex(params: { workspacePath: string; subscriptionId: string }): {
    ack: { subscriptionId: string; mode: "snapshot"; logEpoch: string };
  } {
    return this.index.resync(params.workspacePath, params.subscriptionId);
  }

  unsubscribeSessionsIndex(subscriptionId: string): void {
    this.index.unsubscribe(subscriptionId);
  }

  sendCommand(params: { workspacePath: string; envelope: CommandEnvelope }): Promise<CommandAck> {
    return this.commands.handle(params.workspacePath, params.envelope);
  }

  queryCommands(params: { commands: Array<{ sessionId: string | null; commandId: string }> }): {
    results: Array<{ key: unknown; result: CommandAck | "unknown" }>;
  } {
    return this.commands.query(params.commands);
  }

  plans(workspacePath: string, sessionId: string) {
    return this.commands.plans(workspacePath, sessionId);
  }

  rowsRange(workspacePath: string, sessionId: string) {
    return this.commands.rowsRange(workspacePath, sessionId);
  }

  sessionStates(workspacePath: string): Promise<EngineSessionState[]> {
    return this.sessions.sessionStates(workspacePath);
  }

  dispose(): void {
    this.engineUnsubscribe();
    this.commands.dispose();
    this.index.dispose();
    this.sessions.dispose();
    void this.options;
  }
}
