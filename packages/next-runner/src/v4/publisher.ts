import type {
  CommandAck,
  CommandEnvelope,
  V4ConversationFileChangesParams,
  V4ConversationFileRewindPreviewParams,
} from "@nex/shared/nex-protocol-v4";
import type { Logger } from "../logger.js";
import type { Engine, EngineSessionState } from "../engine/types.js";
import { CommandHandler } from "./commands.js";
import type { CompleteConversationWireFrame, CompleteSessionsIndexWireFrame } from "./envelopes.js";
import { SessionsIndexPublisher } from "./indexPublisher.js";
import { SessionRuntimeStore } from "./sessionStore.js";
import type { AttachmentRegistry } from "../attachments/registry.js";
import {
  conversationFileChanges,
  conversationFileRewindPreview,
  type FileQueryContext,
} from "./fileRewindMethods.js";

export interface PublisherOptions {
  engine: Engine;
  logger: Logger;
  /** Shared attachment ref authority (spec A); the publisher posts listings into it. */
  attachments?: AttachmentRegistry | null;
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
  private readonly attachments: AttachmentRegistry | null;
  readonly index: SessionsIndexPublisher;
  private readonly commands: CommandHandler;
  private readonly engineUnsubscribe: () => void;
  private readonly pendingUnsubscribe: () => void;

  constructor(private readonly options: PublisherOptions) {
    this.sessions = new SessionRuntimeStore(
      options.engine,
      options.logger,
      options.attachments ?? null,
    );
    this.index = new SessionsIndexPublisher(options.engine, options.logger);
    this.commands = new CommandHandler(
      options.engine,
      this.sessions,
      options.logger,
      options.attachments ?? null,
    );
    this.attachments = options.attachments ?? null;
    this.index.setPendingInteractionsSource((workspacePath) =>
      this.sessions.pendingInteractions(workspacePath),
    );
    this.pendingUnsubscribe = this.sessions.onPendingApprovalsChanged((workspacePath) => {
      this.index.publishWorkspace(workspacePath);
    });
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

  subscribeSessionsIndex(params: { workspacePath: string; workspaceIdentity?: string }): {
    ack: { subscriptionId: string; mode: "snapshot"; logEpoch: string };
  } {
    return this.index.subscribe(params);
  }

  resyncSessionsIndex(params: {
    workspacePath: string;
    workspaceIdentity?: string;
    subscriptionId: string;
  }): {
    ack: { subscriptionId: string; mode: "snapshot"; logEpoch: string };
  } {
    return this.index.resync(params);
  }

  unsubscribeSessionsIndex(subscriptionId: string): void {
    this.index.unsubscribe(subscriptionId);
  }

  /** `conversationFileChangesV4` (read-only; spec B). */
  fileChanges(workspacePath: string, params: V4ConversationFileChangesParams) {
    return conversationFileChanges(this.fileQueryContext(), workspacePath, params);
  }

  /** `conversationFileRewindPreviewV4` (read-only; spec B). */
  fileRewindPreview(workspacePath: string, params: V4ConversationFileRewindPreviewParams) {
    return conversationFileRewindPreview(this.fileQueryContext(), workspacePath, params);
  }

  private fileQueryContext(): FileQueryContext {
    return () => ({
      engine: this.options.engine,
      sessions: this.sessions,
      logger: this.options.logger,
    });
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

  /** The attachment authority (read/preview commands authorize against it). */
  attachmentRegistry(): AttachmentRegistry | null {
    return this.attachments;
  }

  dispose(): void {
    this.engineUnsubscribe();
    this.pendingUnsubscribe();
    this.commands.dispose();
    this.index.dispose();
    this.sessions.dispose();
    void this.options;
  }
}
