import type { ModelRuntime } from "@earendil-works/pi-coding-agent";
import type { Model } from "@earendil-works/pi-ai";
import type { Logger } from "../../logger.js";
import type { EngineEvent } from "../types.js";
import type { SessionMetaStore } from "./metaStore.js";
import { SessionConfigOps } from "./sessionConfigOps.js";
import type { OpenSession } from "./sessionLifecycle.js";
import {
  resolveTitleSettings,
  type TitleCompletionRequest,
  type TitleGenerationSettings,
} from "./titleGenerator.js";
import { completeTitleWithRuntime, TitleCoordinator } from "./titleRunner.js";

export interface EngineOps {
  configOps: SessionConfigOps;
  titles: TitleCoordinator;
  titleSettings: TitleGenerationSettings;
}

/**
 * Builds the two config/title collaborators from the engine's live hooks. Extracted from
 * the `PiEngine` constructor so that class stays inside the file budget; both collaborators
 * only ever call back into the engine's emit/metadata surfaces.
 */
export function createEngineOps(input: {
  logger: Logger;
  metadata: SessionMetaStore;
  titleSettings?: TitleGenerationSettings;
  completeTitle?: (request: TitleCompletionRequest) => Promise<string>;
  runtime: () => ModelRuntime;
  sessionFor: (sessionId: string) => OpenSession["session"] | undefined;
  emit: (sessionId: string, event: EngineEvent) => void;
  sessionsChanged: (workspacePath: string) => void;
}): EngineOps {
  const titleSettings = input.titleSettings ?? resolveTitleSettings();
  const getModel = (providerId: string, modelId: string): Model<never> | undefined =>
    input.runtime().getModel(providerId, modelId) as Model<never> | undefined;
  const configOps = new SessionConfigOps({
    logger: input.logger,
    metadata: input.metadata,
    sessionFor: input.sessionFor,
    getModel,
    emit: input.emit,
    sessionsChanged: input.sessionsChanged,
  });
  const titles = new TitleCoordinator({
    logger: input.logger,
    metadata: input.metadata,
    settings: titleSettings,
    sessionFor: input.sessionFor,
    getModel,
    complete: (request) =>
      input.completeTitle
        ? input.completeTitle(request)
        : completeTitleWithRuntime(input.runtime(), request),
    emit: input.emit,
    sessionsChanged: input.sessionsChanged,
  });
  return { configOps, titles, titleSettings };
}
