import type {
  EngineCompactOutcome,
  EngineCompactState,
  EngineForkOutcome,
  EngineForkRequest,
  EngineRewindOutcome,
  EngineRewindRequest,
  EngineRewindTarget,
} from "../types.js";
import type { PiCompactOps } from "./piCompactOps.js";
import { forkAssistantSession, type ForkOpsDeps } from "./piForkOps.js";
import { rewindSession, userMessageBefore } from "./piRewindOps.js";
import type { OpenSession } from "./sessionLifecycle.js";
import type { SessionMetaStore } from "./metaStore.js";
import type { Logger } from "../../logger.js";

/**
 * Conversation rewind / compact / fork surface (docs/next/conversation-features.md
 * decisions 5-7), kept out of `PiEngine` for the file budget. The engine remains the
 * owner of the session map; this only sequences the three ops against one open session and
 * the metadata row.
 */
export interface ConversationOpsDeps {
  logger: Logger;
  metadata: SessionMetaStore;
  compactOps: PiCompactOps;
  /** Compaction entry a manual operation just wrote (tracked by the engine's event hook). */
  compactionEntryFor: (sessionId: string) => string | undefined;
  open: (sessionId: string) => OpenSession;
  sessionsChanged: (workspacePath: string) => void;
  forkDeps: () => ForkOpsDeps;
}

export async function runManualCompact(
  deps: ConversationOpsDeps,
  sessionId: string,
  sourceCommandId: string,
): Promise<EngineCompactOutcome> {
  const open = deps.open(sessionId);
  const outcome = await deps.compactOps.compact(sessionId, sourceCommandId);
  const entryId = deps.compactionEntryFor(sessionId);
  if (outcome.status === "success" && entryId) {
    // pi's file cannot record "manual", so the metadata row remembers which entry this
    // command produced; restore (`translate.ts`) reads it back into the marker.
    await deps.metadata.upsert(open.workspacePath, sessionId, {
      lastCompact: { entryId, sourceCommandId },
    });
  }
  return outcome;
}

export async function runRewind(
  deps: ConversationOpsDeps,
  sessionId: string,
  request: EngineRewindRequest,
): Promise<EngineRewindOutcome> {
  const open = deps.open(sessionId);
  return rewindSession(
    {
      metadata: deps.metadata,
      logger: deps.logger,
      sessionsChanged: (path) => deps.sessionsChanged(path),
      open,
    },
    open.workspacePath,
    sessionId,
    request,
  );
}

export function resolveRewindTarget(
  deps: ConversationOpsDeps,
  sessionId: string,
  targetEntryId: string,
): EngineRewindTarget | null {
  return userMessageBefore(deps.open(sessionId).session, targetEntryId);
}

export async function runFork(
  deps: ConversationOpsDeps,
  workspacePath: string,
  sessionId: string,
  request: EngineForkRequest,
): Promise<EngineForkOutcome> {
  return forkAssistantSession(deps.forkDeps(), workspacePath, deps.open(sessionId), request);
}

export function compactStateOf(deps: ConversationOpsDeps, sessionId: string): EngineCompactState {
  return deps.compactOps.state(sessionId, false);
}
