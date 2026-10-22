import type { Logger } from "../../logger.js";
import type { ModelRuntime } from "@earendil-works/pi-coding-agent";
import type { ConversationOpsDeps } from "./piConversationOps.js";
import type { PiCompactOps } from "./piCompactOps.js";
import type { SessionMetaStore } from "./metaStore.js";
import type { OpenSession, SessionFactoryDeps } from "./sessionLifecycle.js";

/** Engine inputs the conversation ops need (rewind / compact / fork), for the file budget. */
export interface ConversationHost {
  logger: Logger;
  metadata: SessionMetaStore;
  compactOps: PiCompactOps;
  compactionEntryFor: (sessionId: string) => string | undefined;
  open: (sessionId: string) => OpenSession;
  sessionsChanged: (workspacePath: string) => void;
  runtime: () => ModelRuntime;
  factoryDeps: () => SessionFactoryDeps;
  attach: (open: OpenSession) => void;
}

/** Binds one engine's session map to the conversation operations. */
export function createConversationOps(host: ConversationHost): ConversationOpsDeps {
  return {
    logger: host.logger,
    metadata: host.metadata,
    compactOps: host.compactOps,
    compactionEntryFor: (id) => host.compactionEntryFor(id),
    open: (id) => host.open(id),
    sessionsChanged: (path) => host.sessionsChanged(path),
    forkDeps: () => ({
      metadata: host.metadata,
      runtime: () => host.runtime(),
      factoryDeps: () => host.factoryDeps(),
      attach: (open) => host.attach(open),
      sessionsChanged: (path) => host.sessionsChanged(path),
    }),
  };
}
