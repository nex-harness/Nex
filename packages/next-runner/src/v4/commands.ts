import {
  COMMANDS_REQUIRING_BASE_REVISION,
  type AttachmentRef,
  type CommandAck,
  type CommandEnvelope,
  type ConversationSnapshot,
  type ModelSelection,
} from "@nex/shared/nex-protocol-v4";
import type { Logger } from "../logger.js";
import type { Engine } from "../engine/types.js";
import { buildConversationSnapshot } from "../projection/snapshot.js";
import {
  handleSetFollowupMode,
  handleSwitchModelConfig,
  type ConfigCommandContext,
} from "./configCommands.js";
import { checkRevisionCas } from "./revisionCas.js";
import { normalizeAdmittedRefs } from "../attachments/admitted.js";
import type { AttachmentRegistry } from "../attachments/registry.js";
import { handleCompact, type CompactCommandContext } from "./compactCommands.js";
import { handleForkAssistant, type ForkCommandContext } from "./forkCommands.js";
import { handleApplyFileRewind, type FileRewindCommandContext } from "./fileRewindCommands.js";
import {
  handleEditUserQuery,
  handleRetryTurn,
  type RewindCommandContext,
} from "./rewindCommands.js";
import {
  handleDeleteQueueItem,
  handleEditQueueItem,
  handleReorderQueueItem,
  handleSendQueuedNow,
  handleSetAutoDrain,
  type QueueCommandContext,
} from "./queueCommands.js";
import {
  applyModeToProjection,
  handleSendText,
  type SendTextCommandContext,
} from "./sendTextCommand.js";
import type { SessionRuntime, SessionRuntimeStore } from "./sessionStore.js";
import {
  acceptedAck,
  createSessionResult,
  duplicateAck,
  noopAck,
  rejectedAck,
  resolveInteractionResult,
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
    /** Shared ref authority (spec A); absent in tests that never send attachments. */
    private readonly attachments: AttachmentRegistry | null = null,
  ) {}

  private runtimeAttachments(): AttachmentRegistry {
    if (!this.attachments) throw new Error("attachments are not configured");
    return this.attachments;
  }

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
    // CAS gate (decision 3): every command that carries a base revision must match the
    // projection's current revision, and row-targeting commands must be in the same log
    // epoch. `revisionAtDecision` is the runner's real revision, so the client's stale-retry
    // converges instead of guessing. Restored sessions are hydrated first: the history fold
    // decides the base revision, and comparing against the pre-hydrate 0 would be a lie.
    const casRuntime =
      envelope.sessionId && COMMANDS_REQUIRING_BASE_REVISION.has(envelope.type)
        ? this.sessions.ensure(workspacePath, envelope.sessionId)
        : undefined;
    if (casRuntime) {
      await casRuntime.ready;
      const stale = checkRevisionCas(casRuntime, envelope);
      if (stale) return stale;
    }
    switch (envelope.type) {
      case "createSession":
        return this.createSession(workspacePath, envelope);
      case "sendText":
        return handleSendText(this.sendTextContext(), workspacePath, envelope);
      case "deleteSession":
        return this.deleteSession(workspacePath, envelope);
      case "renameSession":
        return this.rename(workspacePath, envelope);
      case "stop":
        return this.stop(workspacePath, envelope);
      case "resolveInteraction":
        return this.resolveInteraction(workspacePath, envelope);
      case "switchModelConfig":
        return handleSwitchModelConfig(this.configContext(), workspacePath, envelope);
      case "setFollowupMode":
        return handleSetFollowupMode(this.configContext(), workspacePath, envelope);
      case "deleteQueueItem":
        return handleDeleteQueueItem(this.queueContext(), workspacePath, envelope);
      case "editQueueItem":
        return handleEditQueueItem(this.queueContext(), workspacePath, envelope);
      case "reorderQueueItem":
        return handleReorderQueueItem(this.queueContext(), workspacePath, envelope);
      case "sendQueuedNow":
        return handleSendQueuedNow(this.queueContext(), workspacePath, envelope);
      case "setAutoDrain":
        return handleSetAutoDrain(this.queueContext(), workspacePath, envelope);
      case "compact":
        return handleCompact(this.compactContext(), workspacePath, envelope);
      case "editUserQuery":
        return handleEditUserQuery(this.rewindContext(), workspacePath, envelope);
      case "retryTurn":
        return handleRetryTurn(this.rewindContext(), workspacePath, envelope);
      case "forkAssistant":
        return handleForkAssistant(this.forkContext(), workspacePath, envelope);
      case "applyFileRewind":
        return handleApplyFileRewind(this.fileRewindContext(), workspacePath, envelope);
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
      firstInput?: {
        text?: string;
        /** Admitted attachment refs (spec A); registered before the prompt starts. */
        attachments?: AttachmentRef[];
        modelSelection?: ModelSelection;
        mode?: string;
        planEnabled?: boolean;
      };
      config?: { modelSelection?: ModelSelection; mode?: string; planEnabled?: boolean };
    };
    const modelSelection = payload.firstInput?.modelSelection ?? payload.config?.modelSelection;
    try {
      const { sessionId } = await this.engine.createSession(
        workspacePath,
        modelSelection ? { modelSelection } : {},
      );
      const runtime = this.sessions.ensure(workspacePath, sessionId);
      const text = payload.firstInput?.text;
      const attachments = normalizeAdmittedRefs(payload.firstInput?.attachments);
      // The mode travels with the submission, never as a live command: firstInput (the
      // actual send) wins over the config override.
      const mode = payload.firstInput?.mode ?? payload.config?.mode;
      const planEnabled = payload.firstInput?.planEnabled ?? payload.config?.planEnabled;
      if ((text !== undefined && text.trim().length > 0) || attachments.length > 0) {
        if (attachments.length > 0) this.runtimeAttachments().addAdmitted(sessionId, attachments);
        await this.engine.submit(sessionId, text ?? "", {
          requestId: envelope.commandId,
          ...(attachments.length > 0 ? { attachments } : {}),
          ...(modelSelection ? { modelSelection } : {}),
          ...(mode ? { mode } : {}),
          ...(planEnabled !== undefined ? { planEnabled } : {}),
        });
        if (attachments.length > 0 && (text ?? "").trim().length === 0) {
          // Attachments only: pi persists no user message, so the row is appended here.
          this.sessions.appendTextlessInput(runtime, "", attachments);
        }
      } else if (mode !== undefined || planEnabled !== undefined) {
        this.applyModeToProjection(runtime, mode, planEnabled);
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
      // A deleted session's blobs have no writer left to read them (spec A cleanup).
      await this.attachments?.purgeSession(sessionId).catch(() => undefined);
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
      // Stopping is the user saying "not this queue either": pause before aborting so the
      // settle handler sees autoDrain=false and does not drain the next item (decision 4).
      runtime.queue.pauseForStop();
      await this.engine.abort(sessionId);
      runtime.throttle.request();
      const ack = acceptedAck(envelope.commandId, runtime.projection.revision);
      runtime.acceptedCommands.set(envelope.commandId, ack);
      return ack;
    } catch (error) {
      return rejectedAck(envelope.commandId, 0, "stop_failed", messageOf(error));
    }
  }

  /**
   * `resolveInteraction {interactionId, answer:{optionId,freeText?}}`. The gate owns the
   * pending state; this only forwards the answer. Unknown or already-resolved ids are an
   * idempotent no-op (`noop`), matching the UI's retry semantics.
   */
  private async resolveInteraction(
    workspacePath: string,
    envelope: CommandEnvelope,
  ): Promise<CommandAck> {
    const sessionId = envelope.sessionId;
    const payload = envelope.payload as {
      interactionId?: string;
      answer?: { optionId?: string; freeText?: string };
    };
    const interactionId = payload.interactionId ?? "";
    if (!sessionId) {
      return rejectedAck(
        envelope.commandId,
        0,
        "session_required",
        "resolveInteraction needs sessionId",
      );
    }
    const runtime = this.sessions.ensure(workspacePath, sessionId);
    const revision = runtime.projection.revision;
    if (!interactionId) {
      return rejectedAck(
        envelope.commandId,
        revision,
        "invalid_payload",
        "interactionId is required",
      );
    }
    const result = resolveInteractionResult(envelope.clientId, payload.answer?.optionId);
    try {
      await runtime.ready;
      const outcome = await this.engine.resolveApproval(sessionId, interactionId, {
        ...(payload.answer?.optionId !== undefined ? { optionId: payload.answer.optionId } : {}),
        ...(payload.answer?.freeText !== undefined ? { freeText: payload.answer.freeText } : {}),
      });
      this.applyModeToProjection(runtime);
      runtime.throttle.request();
      // Unknown/already-resolved ids still return the resolvedBy result so a retried ACK
      // replays the same shape; the status tells the client nothing changed.
      const ack = outcome.resolved
        ? acceptedAck(envelope.commandId, runtime.projection.revision, result)
        : noopAck(envelope.commandId, runtime.projection.revision, "already_resolved", result);
      runtime.acceptedCommands.set(envelope.commandId, ack);
      return ack;
    } catch (error) {
      return rejectedAck(envelope.commandId, revision, "resolve_failed", messageOf(error));
    }
  }

  /** Delegates to the shared adapter so sendText and createSession agree. */
  private applyModeToProjection(
    runtime: SessionRuntime | undefined,
    mode?: string,
    planEnabled?: boolean,
  ): void {
    applyModeToProjection(this.sendTextContext(), runtime, mode, planEnabled);
  }

  private sendTextContext(): SendTextCommandContext {
    return {
      sessions: this.sessions,
      permissionMode: (sessionId) => this.engine.permissionMode(sessionId),
      attachments: this.attachments,
    };
  }

  private queueContext(): QueueCommandContext {
    return { sessions: this.sessions };
  }

  private compactContext(): CompactCommandContext {
    return { sessions: this.sessions };
  }

  private rewindContext(): RewindCommandContext {
    return { engine: this.engine, sessions: this.sessions, attachments: this.attachments };
  }

  private forkContext(): ForkCommandContext {
    return { engine: this.engine, sessions: this.sessions };
  }

  private fileRewindContext(): FileRewindCommandContext {
    return { engine: this.engine, sessions: this.sessions };
  }

  private configContext(): ConfigCommandContext {
    return {
      engine: this.engine,
      sessions: this.sessions,
      remember: (runtime, commandId, ack) => runtime.acceptedCommands.set(commandId, ack),
    };
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
