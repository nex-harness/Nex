import type { SessionSummary, SessionsIndexSnapshot } from "@nex/shared/nex-protocol-v4";
import type { EngineSessionState } from "../engine/types.js";

/** Live approval facts for one session, supplied by the projection owner. */
export interface SessionPendingInteraction {
  interactionId: string;
  toolName: string;
}

/**
 * Full sessions-index snapshot; the UI replaces its whole list on every one.
 *
 * `workspaceId` is the UI's workspace key (`workspaceIdentity?.trim() || workspacePath`):
 * the client subscribes with topic `sessions-index/<workspaceKey>` and replaces the
 * store whose `workspaceId` came from this field, so both must echo the identity the
 * request carried. Two runners serving the same path stay distinct scopes.
 *
 * `pendingInteractions` maps sessionId → the first live approval. The projection store is
 * the single source; the engine's metadata only carries phase/title.
 */
export function buildSessionsIndexSnapshot(
  workspaceId: string,
  logEpoch: string,
  sessions: EngineSessionState[],
  pendingInteractions: ReadonlyMap<string, SessionPendingInteraction[]> = new Map(),
): SessionsIndexSnapshot {
  // 修复依据：draft 是 createSession 预热的纯内存会话（无输入/标题），真实 UI 的
  // tasks-index syncer 明确跳过 `phase === "draft"`（nexTaskIndexSyncer.ts
  // processSummary/seedMissingRowsFromInitialSnapshot）。runner 若把它投影进
  // sessions-index，侧栏会在每次预热时多出一条 “New task”；发送后 phase 变
  // running，同一 sessionId 自然进入列表。
  return {
    protocolVersion: 1,
    workspaceId,
    logEpoch,
    sessions: sessions
      .filter(isVisibleInIndex)
      .map((session) =>
        toSessionSummary(workspaceId, session, pendingInteractions.get(session.sessionId)),
      ),
  };
}

export function isVisibleInIndex(session: EngineSessionState): boolean {
  return session.phase !== "draft";
}

function toSessionSummary(
  workspaceId: string,
  session: EngineSessionState,
  pending: SessionPendingInteraction[] | undefined,
): SessionSummary {
  const first = pending?.[0];
  return {
    sessionId: session.sessionId,
    workspaceId,
    title: session.title,
    titleSource: session.titleSource === "custom" ? "custom" : session.titleSource,
    phase: session.phase,
    sessionEnded:
      session.phase === "completedSuccess" ||
      session.phase === "completedInterrupted" ||
      session.phase === "error",
    hasBackgroundWork: false,
    lastActivityAt: session.lastActivityAt,
    createdAt: session.createdAt,
    ...(first
      ? {
          pendingInteraction: {
            interactionId: first.interactionId,
            kind: "permission" as const,
            toolName: first.toolName,
          },
          pendingInteractionSummary: {
            permissionCount: pending?.length ?? 0,
            userInputCount: 0,
          },
        }
      : {}),
  };
}
