import type { AgentSessionEvent } from "@earendil-works/pi-coding-agent";
import type { Logger } from "../../logger.js";
import type { EngineEvent } from "../types.js";
import { reportMessageEntry } from "./messageEntries.js";
import type { SessionMetaStore } from "./metaStore.js";
import type { PiCompactOps } from "./piCompactOps.js";
import { handlePiSessionEvent } from "./piSessionOps.js";
import type { OpenSession } from "./sessionLifecycle.js";

/**
 * The engine's single pi-event hook. Kept out of `PiEngine` for the file budget; ownership
 * is unchanged — every collaborator here is handed in by the engine.
 *
 * Order matters and is contractual:
 * 1. compaction bookkeeping (attributes pi's command-less compaction events, remembers the
 *    written entry, records automatic outcomes);
 * 2. row identity (`message_entry` after the message was persisted; decision 1);
 * 3. metadata transitions (phase/usage/model on message/settle).
 */
export interface PiEventHookDeps {
  logger: Logger;
  metadata: SessionMetaStore;
  compactOps: PiCompactOps;
  sessions: Map<string, OpenSession>;
  emit: (sessionId: string, event: EngineEvent) => void;
  sessionsChanged: (workspacePath: string) => void;
  rememberCompactionEntry: (sessionId: string, entryId: string) => void;
}

export function handleEnginePiEvent(
  deps: PiEventHookDeps,
  workspacePath: string,
  sessionId: string,
  event: AgentSessionEvent,
): Promise<void> {
  deps.compactOps.observe(sessionId, event, (entryId) =>
    deps.rememberCompactionEntry(sessionId, entryId),
  );
  reportMessageEntry(
    event,
    () => deps.sessions.get(sessionId),
    (identity) => deps.emit(sessionId, identity),
  );
  return handlePiSessionEvent(
    {
      metadata: deps.metadata,
      logger: deps.logger,
      sessionFor: (id) => deps.sessions.get(id)?.session,
      sessionsChanged: deps.sessionsChanged,
    },
    workspacePath,
    sessionId,
    event,
  );
}
