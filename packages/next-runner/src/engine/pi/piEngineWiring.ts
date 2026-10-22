import type { AgentSessionEvent } from "@earendil-works/pi-coding-agent";
import type { Logger } from "../../logger.js";
import { handleEnginePiEvent } from "./piEventHook.js";
import type { PiCompactOps } from "./piCompactOps.js";
import type { AttachmentPromptTable } from "./piAttachmentOps.js";
import type { SessionMetaStore } from "./metaStore.js";
import type { OpenSession } from "./sessionLifecycle.js";
import type { EngineEvent } from "../types.js";

/** Inputs the event hook needs from the engine (kept apart from `piEngine.ts`). */
export interface EventHookHost {
  logger: Logger;
  metadata: SessionMetaStore;
  compactOps: PiCompactOps;
  attachments: AttachmentPromptTable;
  sessions: Map<string, OpenSession>;
  emit: (sessionId: string, event: EngineEvent) => void;
  sessionsChanged: (workspacePath: string) => void;
  rememberCompactionEntry: (sessionId: string, entryId: string) => void;
}

/** `onPiEvent` / event-hook wiring, moved out of `piEngine.ts` for the file budget. */
export function createEngineEventHook(host: EventHookHost) {
  return (workspacePath: string, sessionId: string, event: AgentSessionEvent): Promise<void> =>
    handleEnginePiEvent(
      {
        logger: host.logger,
        metadata: host.metadata,
        compactOps: host.compactOps,
        attachments: host.attachments,
        sessions: host.sessions,
        emit: host.emit,
        sessionsChanged: host.sessionsChanged,
        rememberCompactionEntry: host.rememberCompactionEntry,
      },
      workspacePath,
      sessionId,
      event,
    );
}
