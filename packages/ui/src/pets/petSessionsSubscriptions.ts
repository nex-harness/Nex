/**
 * 宠物 sessions-index 订阅的 diff 与生命周期（纯函数 + 注册表调用）。
 * 从 usePetState 抽出来，使「先释放旧 scope，再建立新 scope」的配平顺序可以单独验证，
 * 不依赖 React 或真实 RPC。订阅语义见 docs/specs/desktop-pets.md。
 */
import type { SessionSummary } from "@nex/shared/nex-protocol-v4";

import {
  acquireSessionsIndex,
  releaseSessionsIndex,
  type SessionsIndexScope,
} from "@/v4/sessionsIndexRegistry.js";

/** 订阅目标的最小形状（usePetState 的 WorkspaceSubscriptionTarget）。 */
export interface PetSessionsSubscriptionTarget {
  workspaceKey: string;
  workspacePath: string;
  workspaceIdentity?: string;
  remoteSessionId?: string;
  agentService: Parameters<typeof acquireSessionsIndex>[1];
}

export interface PetSessionsSubscriptionCallbacks {
  onSessions(workspaceKey: string, sessions: readonly SessionSummary[]): void;
  onLive(workspaceKey: string, live: boolean): void;
  onReleased(workspaceKey: string): void;
}

export function petWorkspaceScope(target: PetSessionsSubscriptionTarget): SessionsIndexScope {
  return {
    workspaceKey: target.workspaceKey,
    workspacePath: target.workspacePath,
    ...(target.workspaceIdentity ? { workspaceIdentity: target.workspaceIdentity } : {}),
    ...(target.remoteSessionId ? { endpointKey: target.remoteSessionId } : {}),
  };
}

/**
 * 需要新增的 workspaceKey（没有现成订阅）与需要释放的 workspaceKey（已不在目标里）。
 * 同一 workspaceKey 但 agentService 换代不在此重建：registry 会按其 generation 串行切换 transport。
 */
export function diffPetSessionsSubscriptions(
  active: ReadonlyMap<string, unknown>,
  next: readonly PetSessionsSubscriptionTarget[],
): { acquire: PetSessionsSubscriptionTarget[]; release: string[] } {
  const nextKeys = new Set(next.map((target) => target.workspaceKey));
  return {
    acquire: next.filter((target) => !active.has(target.workspaceKey)),
    release: [...active.keys()].filter((workspaceKey) => !nextKeys.has(workspaceKey)),
  };
}

/**
 * 让当前订阅与目标集合一致：先释放不再需要的 scope（避免同一 scope 短暂双持），
 * 再建立新增 scope。返回释放全部订阅的 disposer，可重复调用。
 */
export function reconcilePetSessionsSubscriptions(
  targets: readonly PetSessionsSubscriptionTarget[],
  callbacks: PetSessionsSubscriptionCallbacks,
): () => void {
  const active = new Map<string, { dispose: () => void }>();

  const release = (workspaceKey: string) => {
    const entry = active.get(workspaceKey);
    if (!entry) return;
    active.delete(workspaceKey);
    entry.dispose();
    callbacks.onReleased(workspaceKey);
  };

  const acquire = (target: PetSessionsSubscriptionTarget) => {
    const scope = petWorkspaceScope(target);
    const store = acquireSessionsIndex(scope, target.agentService);
    const sync = () => {
      callbacks.onSessions(target.workspaceKey, store.getSessions());
      callbacks.onLive(target.workspaceKey, store.getStatus() === "live");
    };
    const unsubscribe = store.subscribe(sync);
    active.set(target.workspaceKey, {
      dispose: () => {
        unsubscribe();
        releaseSessionsIndex(scope, store);
      },
    });
    sync();
  };

  const { acquire: toAcquire, release: toRelease } = diffPetSessionsSubscriptions(active, targets);
  for (const workspaceKey of toRelease) release(workspaceKey);
  for (const target of toAcquire) acquire(target);

  return () => {
    // 释放期间会删除 map 条目：先取下 key 列表再逐个释放。
    const workspaceKeys = Array.from(active.keys());
    for (const workspaceKey of workspaceKeys) release(workspaceKey);
  };
}
