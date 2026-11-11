/**
 * 宠物语义状态机（纯函数，docs/specs/desktop-pets.md）。
 *
 * 输入是当前活跃 workspace 的 sessions-index 会话摘要流；
 * 输出是宠物应播放的语义状态。优先级：
 *   waiting（有待处理审批/用户输入）> failed（出错，1h 回落）
 *   > review（后台完成未读，7d 回落）> running（运行中，3min 回落）> idle
 */
import { PET_STATE_LIFETIMES_MS, type PetSemanticState } from "@nex/shared";
import type { SessionSummary } from "@nex/shared/nex-protocol-v4";

export interface PetStateSnapshot {
  state: PetSemanticState;
  /** 当前状态进入时间（epoch ms）。 */
  since: number;
}

interface DerivePetStateInput {
  sessions: readonly SessionSummary[];
  now: number;
  previous: PetStateSnapshot | null;
  /** 本地已读水位：lastActivityAt <= lastSeenActivityAt 视为已读。 */
  lastSeenActivityAt: number;
}

function hasPendingInteraction(session: SessionSummary): boolean {
  if (session.pendingInteraction) return true;
  const summary = session.pendingInteractionSummary;
  return Boolean(summary && (summary.permissionCount > 0 || summary.userInputCount > 0));
}

function isRunningPhase(phase: SessionSummary["phase"]): boolean {
  return phase === "running" || phase === "prewarming";
}

function isTerminalUnread(session: SessionSummary, lastSeenActivityAt: number): boolean {
  if (session.phase !== "completedSuccess" && session.phase !== "completedInterrupted") {
    return false;
  }
  return session.lastActivityAt > lastSeenActivityAt;
}

function withExpiry(
  candidate: PetSemanticState,
  previous: PetStateSnapshot | null,
  now: number,
): PetStateSnapshot {
  if (candidate === "idle") {
    return { state: "idle", since: now };
  }
  // 同一状态延续进入时间；状态切换重置计时。
  const since = previous && previous.state === candidate ? previous.since : now;
  const lifetime = PET_STATE_LIFETIMES_MS[candidate as keyof typeof PET_STATE_LIFETIMES_MS];
  if (lifetime !== undefined && now - since > lifetime) {
    return { state: "idle", since: now };
  }
  return { state: candidate, since };
}

/** 从会话摘要流推导宠物语义状态。 */
export function derivePetState(input: DerivePetStateInput): PetStateSnapshot {
  const { sessions, now, previous, lastSeenActivityAt } = input;

  let anyRunning = false;
  let anyFailed = false;
  let anyUnreadTerminal = false;
  for (const session of sessions) {
    if (session.sessionEnded) continue;
    if (hasPendingInteraction(session)) {
      return withExpiry("waiting", previous, now);
    }
    if (session.phase === "error") anyFailed = true;
    else if (isRunningPhase(session.phase)) anyRunning = true;
    else if (isTerminalUnread(session, lastSeenActivityAt)) anyUnreadTerminal = true;
  }

  if (anyFailed) return withExpiry("failed", previous, now);
  if (anyUnreadTerminal) return withExpiry("review", previous, now);
  if (anyRunning) return withExpiry("running", previous, now);
  return { state: "idle", since: now };
}

/** 语义状态 → 默认行约定动画名（两者同构，显式映射防漂移）。 */
export function petStateToAnimation(state: PetSemanticState): string {
  switch (state) {
    case "running":
      return "running";
    case "waiting":
      return "waiting";
    case "review":
      return "review";
    case "failed":
      return "failed";
    default:
      return "idle";
  }
}
