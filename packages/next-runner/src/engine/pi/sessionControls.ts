import type { SessionMetaStore } from "./metaStore.js";
import type { OpenSession } from "./sessionLifecycle.js";
import type { EngineRunState, PermissionMode } from "../types.js";

/** Run/ownership controls over open sessions (abort, stop, guide, idle), from `piEngine.ts`. */
export type SessionControls = ReturnType<typeof createSessionControls>;

export function createSessionControls(deps: {
  sessions: Map<string, OpenSession>;
  openOf: (sessionId: string) => OpenSession | undefined;
  metadata: SessionMetaStore;
  sessionsChanged: (workspacePath: string) => void;
}) {
  const openOf = (sessionId: string) => deps.openOf(sessionId);
  return {
    /** Guide delivery into pi's steering queue; delivered at the next tool-batch boundary. */
    async steer(sessionId: string, text: string): Promise<boolean> {
      const open = openOf(sessionId);
      if (!open?.session.isStreaming) return false;
      await open.session.steer(text);
      return true;
    },

    runState(sessionId: string): EngineRunState {
      const open = openOf(sessionId);
      return open
        ? { streaming: open.session.isStreaming, idle: open.session.isIdle }
        : { streaming: false, idle: true };
    },

    waitForIdle(sessionId: string): Promise<void> {
      return openOf(sessionId)?.session.waitForIdle() ?? Promise.resolve();
    },

    clearPiQueue(sessionId: string): void {
      openOf(sessionId)?.session.clearQueue();
    },

    permissionMode(sessionId: string): { mode: PermissionMode; planEnabled: boolean } | null {
      return openOf(sessionId)?.gate.getMode() ?? null;
    },

    async abort(sessionId: string): Promise<void> {
      const open = openOf(sessionId);
      if (!open) return;
      open.gate.abortPending();
      await open.session.abort();
      await deps.metadata.upsert(open.workspacePath, sessionId, {
        phase: "completedInterrupted",
        lastActivityAt: Date.now(),
      });
      deps.sessionsChanged(open.workspacePath);
    },
  };
}
