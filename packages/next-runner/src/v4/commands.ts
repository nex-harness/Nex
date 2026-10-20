import type {
  CommandAck,
  CommandEnvelope,
  ConversationSnapshot,
  ModelSelection,
} from "@nex/shared/nex-protocol-v4";
import type { Logger } from "../logger.js";
import type { Engine } from "../engine/types.js";
import { buildConversationSnapshot } from "../projection/snapshot.js";
import type { SessionRuntime, SessionRuntimeStore } from "./sessionStore.js";
import {
  acceptedAck,
  createSessionResult,
  duplicateAck,
  inputAcceptedResult,
  rejectedAck,
} from "./envelopes.js";

/**
 * v4 command admission. One owner: `acceptedCommands` per session (plus a per-workspace
 * bucket for createSession, whose envelope carries sessionId:null). A duplicate
 * commandId replays the stored ACK verbatim with status "duplicate".
 */
export class CommandHandler {
  private readonly workspaceCommands = new Map<string, Map<string, CommandAck>>();

  constructor(
    private readonly engine: Engine,
    private readonly sessions: SessionRuntimeStore,
    private readonly logger: Logger,
  ) {}

  async handle(workspacePath: string, envelope: CommandEnvelope): Promise<CommandAck> {
    const runtime = envelope.sessionId ? this.sessions.get(envelope.sessionId) : undefined;
    const previous =
      runtime?.acceptedCommands.get(envelope.commandId) ??
      this.workspaceCommands.get(workspacePath)?.get(envelope.commandId);
    if (previous) {
      return duplicateAck(
        envelope.commandId,
        runtime ? runtime.projection.revision : previous.revisionAtDecision,
        previous.result,
      );
    }
    switch (envelope.type) {
      case "createSession":
        return this.createSession(workspacePath, envelope);
      case "sendText":
        return this.sendText(workspacePath, envelope);
      case "deleteSession":
        return this.deleteSession(workspacePath, envelope);
      case "renameSession":
        return this.rename(workspacePath, envelope);
      case "stop":
        return this.stop(workspacePath, envelope);
      case "resolveInteraction":
        return this.noop(workspacePath, envelope);
      default:
        return rejectedAck(
          envelope.commandId,
          runtime?.projection.revision ?? 0,
          "unsupported_command",
          `Command ${String(envelope.type)} is not supported by this runner`,
        );
    }
  }

  query(commands: Array<{ sessionId: string | null; commandId: string }>): {
    results: Array<{ key: unknown; result: CommandAck | "unknown" }>;
  } {
    return {
      results: commands.map((key) => {
        const runtime = key.sessionId ? this.sessions.get(key.sessionId) : undefined;
        const ack = runtime?.acceptedCommands.get(key.commandId);
        return { key, result: ack ?? "unknown" };
      }),
    };
  }

  snapshotFor(workspacePath: string, sessionId: string): ConversationSnapshot {
    const runtime = this.sessions.ensure(workspacePath, sessionId);
    return buildConversationSnapshot(runtime.projection);
  }

  plans(workspacePath: string, sessionId: string) {
    const runtime = this.sessions.ensure(workspacePath, sessionId);
    return { plans: [], atSeq: runtime.projection.seq, atLogEpoch: runtime.logEpoch };
  }

  rowsRange(workspacePath: string, sessionId: string) {
    const runtime = this.sessions.ensure(workspacePath, sessionId);
    return {
      rows: [] as Array<never>,
      atSeq: runtime.projection.seq,
      atRevision: runtime.projection.revision,
      atLogEpoch: runtime.logEpoch,
      hasMore: false,
    };
  }

  dispose(): void {
    this.workspaceCommands.clear();
  }

  private async createSession(
    workspacePath: string,
    envelope: CommandEnvelope,
  ): Promise<CommandAck> {
    const payload = envelope.payload as {
      firstInput?: { text?: string; modelSelection?: ModelSelection };
      config?: { modelSelection?: ModelSelection };
    };
    const modelSelection = payload.firstInput?.modelSelection ?? payload.config?.modelSelection;
    try {
      const { sessionId } = await this.engine.createSession(
        workspacePath,
        modelSelection ? { modelSelection } : {},
      );
      const runtime = this.sessions.ensure(workspacePath, sessionId);
      const text = payload.firstInput?.text;
      if (text !== undefined && text.trim().length > 0) {
        await this.engine.submit(sessionId, text, {
          requestId: envelope.commandId,
          ...(modelSelection ? { modelSelection } : {}),
        });
      }
      const ack = acceptedAck(
        envelope.commandId,
        runtime.projection.revision,
        createSessionResult(sessionId),
      );
      runtime.acceptedCommands.set(envelope.commandId, ack);
      this.remember(workspacePath, envelope.commandId, ack);
      runtime.throttle.request();
      return ack;
    } catch (error) {
      return rejectedAck(envelope.commandId, 0, "create_failed", messageOf(error));
    }
  }

  private async sendText(workspacePath: string, envelope: CommandEnvelope): Promise<CommandAck> {
    const sessionId = envelope.sessionId;
    if (!sessionId) {
      return rejectedAck(envelope.commandId, 0, "session_required", "sendText needs sessionId");
    }
    const payload = envelope.payload as { text?: string; modelSelection?: ModelSelection };
    try {
      const runtime = this.sessions.ensure(workspacePath, sessionId);
      // A restored session is opened asynchronously; submit must not race openSession.
      await runtime.ready;
      await this.engine.submit(sessionId, payload.text ?? "", {
        requestId: envelope.commandId,
        ...(payload.modelSelection ? { modelSelection: payload.modelSelection } : {}),
      });
      runtime.throttle.request();
      const ack = acceptedAck(
        envelope.commandId,
        runtime.projection.revision,
        inputAcceptedResult(envelope.commandId),
      );
      runtime.acceptedCommands.set(envelope.commandId, ack);
      return ack;
    } catch (error) {
      return rejectedAck(
        envelope.commandId,
        this.sessions.get(sessionId)?.projection.revision ?? 0,
        "send_failed",
        messageOf(error),
      );
    }
  }

  private async deleteSession(
    workspacePath: string,
    envelope: CommandEnvelope,
  ): Promise<CommandAck> {
    const sessionId = envelope.sessionId;
    if (!sessionId) {
      return rejectedAck(
        envelope.commandId,
        0,
        "session_required",
        "deleteSession needs sessionId",
      );
    }
    const runtime = this.sessions.get(sessionId);
    const revision = runtime?.projection.revision ?? 0;
    try {
      await this.engine.deleteSession(workspacePath, sessionId);
      if (runtime) {
        // Keep the ACK for duplicate replay, then drop the projection.
        runtime.acceptedCommands.set(envelope.commandId, acceptedAck(envelope.commandId, revision));
        this.sessions.disposeRuntime(sessionId);
      }
      return acceptedAck(envelope.commandId, revision);
    } catch (error) {
      return rejectedAck(envelope.commandId, revision, "delete_failed", messageOf(error));
    }
  }

  private async rename(workspacePath: string, envelope: CommandEnvelope): Promise<CommandAck> {
    const sessionId = envelope.sessionId;
    if (!sessionId) {
      return rejectedAck(
        envelope.commandId,
        0,
        "session_required",
        "renameSession needs sessionId",
      );
    }
    const payload = envelope.payload as { title?: string };
    try {
      const runtime = this.sessions.ensure(workspacePath, sessionId);
      await runtime.ready;
      await this.engine.rename(sessionId, payload.title ?? "");
      runtime.projection = {
        ...runtime.projection,
        seq: runtime.projection.seq + 1,
        revision: runtime.projection.revision + 1,
        title: payload.title ?? "",
        titleSource: "custom",
        lastActivityAt: Date.now(),
      };
      runtime.throttle.request();
      const ack = acceptedAck(envelope.commandId, runtime.projection.revision);
      runtime.acceptedCommands.set(envelope.commandId, ack);
      return ack;
    } catch (error) {
      return rejectedAck(envelope.commandId, 0, "rename_failed", messageOf(error));
    }
  }

  private async stop(workspacePath: string, envelope: CommandEnvelope): Promise<CommandAck> {
    const sessionId = envelope.sessionId;
    if (!sessionId) {
      return rejectedAck(envelope.commandId, 0, "session_required", "stop needs sessionId");
    }
    try {
      const runtime = this.sessions.ensure(workspacePath, sessionId);
      await runtime.ready;
      await this.engine.abort(sessionId);
      runtime.throttle.request();
      const ack = acceptedAck(envelope.commandId, runtime.projection.revision);
      runtime.acceptedCommands.set(envelope.commandId, ack);
      return ack;
    } catch (error) {
      return rejectedAck(envelope.commandId, 0, "stop_failed", messageOf(error));
    }
  }

  private noop(workspacePath: string, envelope: CommandEnvelope): CommandAck {
    const runtime = envelope.sessionId
      ? this.sessions.ensure(workspacePath, envelope.sessionId)
      : undefined;
    const ack = acceptedAck(envelope.commandId, runtime?.projection.revision ?? 0);
    runtime?.acceptedCommands.set(envelope.commandId, ack);
    return ack;
  }

  private remember(workspacePath: string, commandId: string, ack: CommandAck): void {
    const map = this.workspaceCommands.get(workspacePath) ?? new Map<string, CommandAck>();
    map.set(commandId, ack);
    if (map.size > 512) {
      const oldest = map.keys().next().value;
      if (oldest !== undefined) map.delete(oldest);
    }
    this.workspaceCommands.set(workspacePath, map);
  }
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export type { SessionRuntime };
