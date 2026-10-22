import type { AgentSession } from "@earendil-works/pi-coding-agent";
import type { Model } from "@earendil-works/pi-ai";
import type { Logger } from "../../logger.js";
import type {
  EngineEvent,
  EngineFollowupMode,
  EngineModelState,
  SwitchModelConfigInput,
  SwitchModelConfigOutcome,
} from "../types.js";
import { applySwitchModelConfig } from "./modelConfigOps.js";
import type { SessionMetaStore } from "./metaStore.js";

export interface SessionConfigOpsDeps {
  logger: Logger;
  metadata: SessionMetaStore;
  sessionFor: (sessionId: string) => AgentSession | undefined;
  getModel: (providerId: string, modelId: string) => Model<never> | undefined;
  emit: (sessionId: string, event: EngineEvent) => void;
  sessionsChanged: (workspacePath: string) => void;
}

/**
 * Mid-session config mutations (docs/next/conversation-features.md decision 9): the engine
 * surface for `switchModelConfig` / `setFollowupMode`, kept out of `PiEngine` so both stay
 * within the file budget and the serialization rule lives in one place.
 *
 * Serialization: pi's `setModel` is async and appends `model_change`; every model/thought
 * write for one session runs through the per-session tail (old CLI's
 * `runSessionModelConfigMutation`), so a directory fallback and a user switch cannot
 * interleave into an out-of-order transcript.
 */
export class SessionConfigOps {
  private readonly tails = new Map<string, Promise<void>>();

  constructor(private readonly deps: SessionConfigOpsDeps) {}

  async switchModelConfig(
    workspacePath: string,
    sessionId: string,
    input: SwitchModelConfigInput,
  ): Promise<SwitchModelConfigOutcome> {
    return this.serialize(sessionId, async () => {
      const outcome = await applySwitchModelConfig(
        { sessionFor: this.deps.sessionFor, getModel: this.deps.getModel },
        sessionId,
        input,
      );
      if (outcome.status === "failed") return outcome;
      await this.deps.metadata.upsert(workspacePath, sessionId, {
        ...(outcome.model ? { model: outcome.model } : {}),
        lastActivityAt: Date.now(),
      });
      const to: EngineModelState | undefined = outcome.model;
      if (outcome.status === "changed" && to) {
        this.deps.emit(sessionId, { type: "model_changed", at: Date.now(), model: to });
        // marker 只在模型身份真正改变时出现（旧 CLI：thought-only 切换只改 config.thought）。
        if (outcome.modelIdentityChanged !== false) {
          this.deps.emit(sessionId, {
            type: "model_change_marker",
            at: Date.now(),
            ...(outcome.previous ? { from: outcome.previous } : {}),
            to: { provider: to.providerId, model: to.modelId, thought: to.thought },
          });
        }
      }
      this.deps.sessionsChanged(workspacePath);
      return outcome;
    });
  }

  async setFollowupMode(
    workspacePath: string,
    sessionId: string,
    mode: EngineFollowupMode,
  ): Promise<{ previous: EngineFollowupMode; mode: EngineFollowupMode }> {
    const current = await this.deps.metadata.get(workspacePath, sessionId);
    const previous = current?.followupMode ?? "queue";
    if (previous === mode) return { previous, mode };
    await this.deps.metadata.upsert(workspacePath, sessionId, {
      followupMode: mode,
      lastActivityAt: Date.now(),
    });
    this.deps.emit(sessionId, { type: "followup_mode_changed", at: Date.now(), mode });
    this.deps.sessionsChanged(workspacePath);
    return { previous, mode };
  }

  /** Runs arbitrary model writes (submit's per-message selection) inside the same tail. */
  runModelMutation<T>(sessionId: string, operation: () => Promise<T>): Promise<T> {
    return this.serialize(sessionId, operation);
  }

  private async serialize<T>(sessionId: string, operation: () => Promise<T>): Promise<T> {
    const previous = this.tails.get(sessionId) ?? Promise.resolve();
    const current = previous.catch(() => undefined).then(operation);
    const tail = current.then(
      () => undefined,
      () => undefined,
    );
    this.tails.set(sessionId, tail);
    try {
      return await current;
    } finally {
      if (this.tails.get(sessionId) === tail) this.tails.delete(sessionId);
    }
  }
}
