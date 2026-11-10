import type { NexSessionStateSnapshot } from "@nex/shared";
import type {
  NexSessionWorkspaceTarget,
  NexTaskTarget,
} from "#src/nex-session/nexSession.js";

function getWorkspaceKey(target: NexSessionWorkspaceTarget): string {
  return target.workspaceIdentity?.trim() || target.workspacePath;
}

function getSessionScopedKey(target: NexTaskTarget): string {
  return `${getWorkspaceKey(target)}\0${target.sessionId}`;
}

export function createNexDeferredDraftRegistry() {
  const sessionKeys = new Set<string>();

  return {
    remember(params: NexSessionWorkspaceTarget, snapshot: NexSessionStateSnapshot): void {
      sessionKeys.add(
        getSessionScopedKey({
          workspacePath: snapshot.session.workspace.workspacePath,
          workspaceIdentity:
            snapshot.session.workspace.workspaceIdentity ?? params.workspaceIdentity,
          sessionId: snapshot.session.sessionId,
        }),
      );
    },

    has(target: NexTaskTarget): boolean {
      return sessionKeys.has(getSessionScopedKey(target));
    },

    forget(target: NexTaskTarget): void {
      sessionKeys.delete(getSessionScopedKey(target));
    },
  };
}
