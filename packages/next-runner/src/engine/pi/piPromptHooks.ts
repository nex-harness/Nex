import type { ModelSelection } from "@nex/shared";
import type { Logger } from "../../logger.js";
import type { ModelRuntime } from "@earendil-works/pi-coding-agent";
import { applyEngineSelection, startEnginePrompt, type PromptOpsDeps } from "./promptOps.js";
import type { PromptAttachmentPayload } from "./piSessionOps.js";
import type { OpenSession } from "./sessionLifecycle.js";
import type { SessionConfigOps } from "./sessionConfigOps.js";
import type { SessionMetaStore } from "./metaStore.js";

/** Facts the prompt helpers read from the engine (kept apart from `piEngine.ts`). */
export interface PromptHookHost {
  metadata: SessionMetaStore;
  sessions: Map<string, OpenSession>;
  configOps: SessionConfigOps;
  logger: Logger;
  runtime: () => ModelRuntime;
  sessionsChanged: (workspacePath: string) => void;
}

/** Prompt start / attachment payload / model selection helpers bound to one engine. */
export function createPromptHooks(host: PromptHookHost) {
  const requireOpen = (sessionId: string): OpenSession => {
    const open = host.sessions.get(sessionId);
    if (!open) throw new Error(`Session ${sessionId} is not open`);
    return open;
  };
  const promptOps = (): PromptOpsDeps => ({
    metadata: host.metadata,
    logger: host.logger,
    sessionsChanged: (p) => host.sessionsChanged(p),
    runtime: () => host.runtime(),
    configOps: host.configOps,
  });
  return {
    promptOps,
    /** Returns the input id (the link's ACK carries it as `inputId`). */
    startPrompt(
      workspacePath: string,
      sessionId: string,
      text: string,
      requestId: string | undefined,
      payload?: PromptAttachmentPayload,
      onStart?: (session: import("@earendil-works/pi-coding-agent").AgentSession) => void,
    ): Promise<string> {
      return startEnginePrompt(
        promptOps(),
        requireOpen(sessionId),
        workspacePath,
        sessionId,
        text,
        requestId,
        payload
          ? {
              images: payload.images,
              streamingBehavior: payload.streamingBehavior,
              promptText: payload.promptText,
              ...(onStart ? { onStart } : {}),
            }
          : onStart
            ? { images: [], streamingBehavior: "followUp" as const, onStart }
            : undefined,
      );
    },
    applySelection(sessionId: string, selection: ModelSelection): Promise<void> {
      return applyEngineSelection(promptOps(), requireOpen(sessionId), sessionId, selection);
    },
  };
}
