import { unlink } from "node:fs/promises";
import type { ModelRuntime } from "@earendil-works/pi-coding-agent";
import type { openPiSession } from "./sessionLifecycle.js";
import type { SessionMetaStore } from "./metaStore.js";
import { findSessionFile as findSessionFileInDir, sessionDirFor } from "./piSessionFiles.js";
import type { OpenSession } from "./sessionLifecycle.js";
import type { TaskStateHooks } from "./taskStateOps.js";
import type { SessionCreateDeps } from "./sessionCreateOps.js";

export interface HookSources {
  dataDir: string;
  metadata: SessionMetaStore;
  sessions: Map<string, OpenSession>;
  sessionDir: (workspacePath: string) => string;
  attach: (open: OpenSession) => void;
  teardown: (open: OpenSession) => Promise<void>;
  sessionsChanged: (workspacePath: string) => void;
  factoryDeps: () => Parameters<typeof openPiSession>[0];
  runtime: () => ModelRuntime;
}

/**
 * Factories for the engine's collaborator hooks (`TaskStateHooks`, `SessionCreateDeps`).
 * Split from `PiEngine` for the file budget; all state still lives in the engine.
 */
export function createHookFactories(sources: HookSources) {
  const findSessionFile = (workspacePath: string, sessionId: string): Promise<string | undefined> =>
    findSessionFileInDir(sources.sessionDir(workspacePath), sessionId);

  const taskStateHooks = (): TaskStateHooks => ({
    metadata: sources.metadata,
    sessionFor: (sessionId) => sources.sessions.get(sessionId)?.session,
    closeSession: (sessionId) => {
      const open = sources.sessions.get(sessionId);
      if (!open) return;
      sources.sessions.delete(sessionId);
      void sources.teardown(open);
    },
    removeSessionFile: async (workspacePath, sessionId) => {
      const file = await findSessionFile(workspacePath, sessionId);
      if (file) await unlink(file).catch(() => undefined);
    },
    sessionsChanged: sources.sessionsChanged,
  });

  const sessionCreateDeps = (): SessionCreateDeps => ({
    metadata: sources.metadata,
    runtime: sources.runtime,
    sessionDir: sources.sessionDir,
    findSessionFile,
    factoryDeps: sources.factoryDeps,
    attach: sources.attach,
    sessionsChanged: sources.sessionsChanged,
  });

  return { taskStateHooks, sessionCreateDeps, findSessionFile };
}

export function sessionDirFactory(dataDir: string) {
  return (workspacePath: string): string => sessionDirFor(dataDir, workspacePath);
}
