import type { AgentSession, ModelRuntime } from "@earendil-works/pi-coding-agent";
import type { Model } from "@earendil-works/pi-ai";
import type { Logger } from "../../logger.js";
import type { EngineEvent, EngineTitleSource } from "../types.js";
import type { SessionMetaStore } from "./metaStore.js";
import {
  runGeneratedTitle,
  shouldGenerateTitle,
  type TitleCompletion,
  type TitleCompletionRequest,
  type TitleGenerationSettings,
  type TitleModelRef,
} from "./titleGenerator.js";

export interface TitleCoordinatorDeps {
  logger: Logger;
  metadata: SessionMetaStore;
  settings: TitleGenerationSettings;
  sessionFor: (sessionId: string) => AgentSession | undefined;
  getModel: (providerId: string, modelId: string) => Model<never> | undefined;
  /** Replaceable completion (tests; the engine supplies the runtime call). */
  complete: TitleCompletion;
  emit: (sessionId: string, event: EngineEvent) => void;
  sessionsChanged: (workspacePath: string) => void;
}

/**
 * Owns "was a title attempted for this session" and starts the fire-and-forget run
 * (docs/next/conversation-features.md decision 8). The caller never awaits it.
 */
export class TitleCoordinator {
  private readonly attempted = new Set<string>();

  constructor(private readonly deps: TitleCoordinatorDeps) {}

  /**
   * Called after the first prompt has been started. `meta` must be the metadata row read
   * BEFORE this submission: its `seenRequestIds` decides whether this is turn 0.
   */
  maybeStart(
    workspacePath: string,
    sessionId: string,
    text: string,
    meta: { titleSource?: EngineTitleSource; seenRequestIds?: string[] } | undefined,
  ): void {
    const session = this.deps.sessionFor(sessionId);
    if (!session) return;
    if (
      !shouldGenerateTitle({
        text,
        isFirstTurn: (meta?.seenRequestIds?.length ?? 0) === 0,
        settings: this.deps.settings,
        titleSource: meta?.titleSource,
        alreadyAttempted: this.attempted.has(sessionId),
      })
    ) {
      return;
    }
    const model = this.resolveModel(session);
    if (!model) {
      this.deps.logger.debug("session title generation skipped", {
        sessionId,
        reason: "no_model",
      });
      return;
    }
    this.attempted.add(sessionId);
    void runGeneratedTitle(
      {
        logger: this.deps.logger,
        metadata: this.deps.metadata,
        completion: (request: TitleCompletionRequest) => this.requireModel(request),
      },
      {
        sessionFor: (id) => this.deps.sessionFor(id),
        emit: (id, event) => this.deps.emit(id, event),
        sessionsChanged: (path) => this.deps.sessionsChanged(path),
      },
      { workspacePath, sessionId, text, model },
    ).catch((error: unknown) => {
      // `runGeneratedTitle` handles provider failures; this guards metadata/emit errors so
      // a background title can never surface as an unhandled rejection.
      this.deps.logger.warn("session title persistence failed", {
        sessionId,
        message: error instanceof Error ? error.message : String(error),
      });
    });
  }

  private resolveModel(session: AgentSession): TitleModelRef | null {
    if (this.deps.settings.model) return this.deps.settings.model;
    const model = session.model as Model<never> | undefined;
    return model ? { providerId: model.provider, modelId: model.id } : null;
  }

  /** Rejects early when the configured override names a model the runtime lacks. */
  private async requireModel(request: TitleCompletionRequest): Promise<string> {
    if (!this.deps.settings.model) return this.deps.complete(request);
    if (!this.deps.getModel(request.model.providerId, request.model.modelId)) {
      throw new Error(`Title model ${request.model.providerId}/${request.model.modelId} not found`);
    }
    return this.deps.complete(request);
  }
}

/**
 * Default title completion: one `ModelRuntime.completeSimple` call with the title system
 * prompt and the user text as the only message (pi-ai `Context`:
 * `{systemPrompt, messages:[{role:"user",content,timestamp}]}`).
 */
export async function completeTitleWithRuntime(
  runtime: ModelRuntime,
  request: TitleCompletionRequest,
): Promise<string> {
  const model = runtime.getModel(request.model.providerId, request.model.modelId);
  if (!model) {
    throw new Error(`Title model ${request.model.providerId}/${request.model.modelId} not found`);
  }
  const message = await runtime.completeSimple(
    model,
    {
      systemPrompt: request.systemPrompt,
      messages: [{ role: "user", content: request.userText, timestamp: Date.now() }],
    },
    { signal: request.signal },
  );
  return message.content
    .map((part) => (part.type === "text" ? part.text : ""))
    .join("")
    .trim();
}
