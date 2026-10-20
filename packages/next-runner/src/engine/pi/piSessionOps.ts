import { clampThinkingLevel } from "@earendil-works/pi-ai";
import type { AgentSession, AgentSessionEvent } from "@earendil-works/pi-coding-agent";
import type { ModelSelection } from "@nex/shared";
import type { Logger } from "../../logger.js";
import type { SessionMetaStore } from "./metaStore.js";
import { selectionFor, usageFromSession } from "./sessionHelpers.js";

export interface SessionMetadataHooks {
  metadata: SessionMetaStore;
  logger: Logger;
  /** Resolves the open session for usage/model readback; undefined when closing. */
  sessionFor: (sessionId: string) => AgentSession | undefined;
  sessionsChanged: (workspacePath: string) => void;
}

/**
 * Engine-owned metadata transitions driven by pi events. Projection sees the same events
 * through the translated stream; this only maintains phase/usage/model facts.
 */
export async function handlePiSessionEvent(
  hooks: SessionMetadataHooks,
  workspacePath: string,
  sessionId: string,
  event: AgentSessionEvent,
): Promise<void> {
  if (event.type === "message_end") {
    const message = event.message;
    if (message.role === "user") {
      await hooks.metadata.upsert(workspacePath, sessionId, { lastActivityAt: Date.now() });
      hooks.sessionsChanged(workspacePath);
    } else if (message.role === "assistant" && message.stopReason === "error") {
      await hooks.metadata.upsert(workspacePath, sessionId, {
        phase: "error",
        lastActivityAt: Date.now(),
      });
      hooks.sessionsChanged(workspacePath);
    }
    return;
  }
  if (event.type === "agent_settled") {
    const session = hooks.sessionFor(sessionId);
    const selection = session ? selectionFor(session) : null;
    await hooks.metadata.upsert(workspacePath, sessionId, {
      phase: event.aborted ? "completedInterrupted" : "completedSuccess",
      lastActivityAt: Date.now(),
      ...(session ? { usage: usageFromSession(session) } : {}),
      ...(selection ? { model: selection } : {}),
    });
    hooks.sessionsChanged(workspacePath);
  }
}

export interface PromptHooks {
  metadata: SessionMetaStore;
  logger: Logger;
  sessionsChanged: (workspacePath: string) => void;
}

/**
 * Starts the pi run without awaiting it: `prompt()` resolves when the whole run settles,
 * and the link's call/result must not wait for that.
 */
export function startPromptRun(
  hooks: PromptHooks,
  open: { session: AgentSession; inflight: Promise<void> | null },
  workspacePath: string,
  sessionId: string,
  text: string,
): void {
  open.inflight = open.session
    .prompt(text, { streamingBehavior: "followUp" })
    .catch(async (error: unknown) => {
      await hooks.metadata.upsert(workspacePath, sessionId, {
        phase: "error",
        lastActivityAt: Date.now(),
      });
      hooks.logger.warn("pi prompt failed", {
        sessionId,
        message: error instanceof Error ? error.message : String(error),
      });
      hooks.sessionsChanged(workspacePath);
    })
    .finally(() => {
      open.inflight = null;
    });
}

export async function applyModelSelection(
  session: AgentSession,
  selection: ModelSelection,
  getModel: (providerId: string, modelId: string) => { provider: string; id: string } | undefined,
): Promise<void> {
  const model = getModel(selection.providerId, selection.modelId);
  if (!model) throw new Error(`Unknown model ${selection.providerId}/${selection.modelId}`);
  await session.setModel(model as never);
  const level = selection.options?.reasoningLevel;
  if (level) {
    // pi clamps to the model's supported set; ask for the requested level and let it
    // land on the nearest supported one (the UI only sends levels we advertised).
    session.setThinkingLevel(clampThinkingLevel(model as never, level as never) as never);
  }
}
