import type { CommandAck, CommandEnvelope } from "@nex/shared/nex-protocol-v4";
import type { Engine, EngineFollowupMode, SwitchModelConfigInput } from "../engine/types.js";
import type { SessionRuntime, SessionRuntimeStore } from "../v4/sessionStore.js";
import { acceptedAck, noopAck, rejectedAck } from "../v4/envelopes.js";

/**
 * `switchModelConfig` / `setFollowupMode` admission (docs/next/conversation-features.md
 * decision 9). Split out of `CommandHandler` to keep that file inside the line budget;
 * the handler passes itself in so idempotent ACKs stay in the same per-session map.
 */

export interface ConfigCommandContext {
  engine: Engine;
  sessions: SessionRuntimeStore;
  remember(runtime: SessionRuntime, commandId: string, ack: CommandAck): void;
}

export async function handleSwitchModelConfig(
  ctx: ConfigCommandContext,
  workspacePath: string,
  envelope: CommandEnvelope,
): Promise<CommandAck> {
  const sessionId = envelope.sessionId;
  if (!sessionId) {
    return rejectedAck(
      envelope.commandId,
      0,
      "session_required",
      "switchModelConfig needs sessionId",
    );
  }
  const payload = envelope.payload as Partial<SwitchModelConfigInput>;
  const runtime = ctx.sessions.ensure(workspacePath, sessionId);
  try {
    await runtime.ready;
    const outcome = await ctx.engine.switchModelConfig(workspacePath, sessionId, {
      provider: payload.provider ?? "",
      model: payload.model ?? "",
      thought: payload.thought ?? "",
    });
    runtime.throttle.request();
    // failed/noop 也写进 acceptedCommands：重复 commandId 必须回放同一个判定，
    // 否则客户端 stale-retry 会把“本就没有变化”变成新的 failure。
    const ack: CommandAck =
      outcome.status === "failed"
        ? {
            commandId: envelope.commandId,
            status: "failed",
            reasonCode: outcome.reasonCode ?? "model.switchFailed",
            ...(outcome.message ? { message: outcome.message } : {}),
            revisionAtDecision: runtime.projection.revision,
          }
        : outcome.status === "unchanged"
          ? noopAck(
              envelope.commandId,
              runtime.projection.revision,
              outcome.reasonCode ?? "config.unchanged",
            )
          : acceptedAck(envelope.commandId, runtime.projection.revision);
    ctx.remember(runtime, envelope.commandId, ack);
    return ack;
  } catch (error) {
    return rejectedAck(
      envelope.commandId,
      runtime.projection.revision,
      "model_switch_failed",
      error instanceof Error ? error.message : String(error),
    );
  }
}

export async function handleSetFollowupMode(
  ctx: ConfigCommandContext,
  workspacePath: string,
  envelope: CommandEnvelope,
): Promise<CommandAck> {
  const sessionId = envelope.sessionId;
  if (!sessionId) {
    return rejectedAck(
      envelope.commandId,
      0,
      "session_required",
      "setFollowupMode needs sessionId",
    );
  }
  const payload = envelope.payload as { mode?: EngineFollowupMode };
  const mode: EngineFollowupMode = payload.mode === "guide" ? "guide" : "queue";
  const runtime = ctx.sessions.ensure(workspacePath, sessionId);
  try {
    await runtime.ready;
    const outcome = await ctx.engine.setFollowupMode(workspacePath, sessionId, mode);
    runtime.throttle.request();
    const ack: CommandAck =
      outcome.previous === mode
        ? noopAck(envelope.commandId, runtime.projection.revision, "config.unchanged")
        : acceptedAck(envelope.commandId, runtime.projection.revision);
    ctx.remember(runtime, envelope.commandId, ack);
    return ack;
  } catch (error) {
    return rejectedAck(
      envelope.commandId,
      runtime.projection.revision,
      "followup_mode_failed",
      error instanceof Error ? error.message : String(error),
    );
  }
}
