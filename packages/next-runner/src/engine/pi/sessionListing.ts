import type { SessionMetaStore } from "./metaStore.js";
import type { OpenSession } from "./sessionLifecycle.js";
import type { EngineSessionState } from "../types.js";
import { toEngineSessionStates } from "./sessionViews.js";
import { EMPTY_USAGE, selectionFor, usageFromSession } from "./sessionHelpers.js";

/** Session listings over the metadata store + live sessions (kept out of `piEngine.ts`). */
export function listEngineSessions(deps: {
  init: () => Promise<void>;
  metadata: SessionMetaStore;
  openOf: (sessionId: string) => OpenSession | undefined;
}) {
  return async (workspacePath: string): Promise<EngineSessionState[]> => {
    await deps.init();
    const live = (sessionId: string) => {
      const open = deps.openOf(sessionId);
      if (!open) return undefined;
      const { mode, planEnabled } = open.gate.getMode();
      return { session: open.session, streaming: open.session.isStreaming, mode, planEnabled };
    };
    const entries = await deps.metadata.list(workspacePath);
    return toEngineSessionStates(workspacePath, entries, live, {
      selectionFor,
      usageFromSession,
      emptyUsage: EMPTY_USAGE,
    });
  };
}

export function listDeletedSessions(deps: {
  init: () => Promise<void>;
  metadata: SessionMetaStore;
}) {
  return async (workspacePath: string): Promise<string[]> => {
    await deps.init();
    const entries = await deps.metadata.list(workspacePath);
    return entries.filter((entry) => entry.deleted).map((entry) => entry.sessionId);
  };
}
