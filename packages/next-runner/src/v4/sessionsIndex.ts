import type { SessionSummary, SessionsIndexSnapshot } from "@nex/shared/nex-protocol-v4";
import type { EngineSessionState } from "../engine/types.js";

/** Full sessions-index snapshot; the UI replaces its whole list on every one. */
export function buildSessionsIndexSnapshot(
  workspaceId: string,
  logEpoch: string,
  sessions: EngineSessionState[],
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
    sessions: sessions.filter(isVisibleInIndex).map(toSessionSummary),
  };
}

export function isVisibleInIndex(session: EngineSessionState): boolean {
  return session.phase !== "draft";
}

function toSessionSummary(session: EngineSessionState): SessionSummary {
  return {
    sessionId: session.sessionId,
    workspaceId: session.workspacePath,
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
  };
}
