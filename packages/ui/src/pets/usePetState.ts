/**
 * 宠物状态订阅 hook：聚合当前窗口所有打开 workspace 的 sessions-index，
 * 输出全局宠物语义状态（纯派生，不落盘）。
 *
 * 设计：宠物是桌面级伴侣，不随单个 workspace 切换而重置；状态优先级取所有
 * workspace 的最高档（任何一个 workspace 有 pending 审批就整体 waiting）。
 * 订阅模式照抄 useWorkspaceTerminalTaskNotifications：acquireSessionsIndex +
 * releaseSessionsIndex 清理，diff 与读帧抽到 petSessionsSubscriptions，让「配平 →
 * 建立」的顺序可单独测试。详见 docs/specs/desktop-pets.md。
 *
 * 2.0 的远程 workspace 各有自己的远程代理：主 workspace 用
 * useWorkspaceServicesResolution 解析出对应 endpoint 的 services（沿用 remote-waiting
 * 闸门），本地额外打开的 workspace 与应用级 services 是同一个 Host，直接复用。
 * 主窗口没有 workspace tab（例如只剩设置页）时退回应用级 services。
 */
import { useEffect, useMemo, useRef, useState } from "react";
import type { PetSemanticState } from "@nex/shared";
import type { SessionSummary } from "@nex/shared/nex-protocol-v4";

import { useServices } from "@/hooks/useServices.js";
import {
  useWorkspaceServicesResolution,
  type WorkspaceServicesResolution,
} from "@/hooks/useWorkspaceServices.js";
import { useTabStore } from "@/store/TabStoreProvider.js";
import { isWorkspaceTab, type WorkspaceTabState } from "@/store/tabStore.js";
import type { SessionsIndexAgentService } from "@/v4/sessionsIndexRegistry.js";

import { reconcilePetSessionsSubscriptions } from "./petSessionsSubscriptions.js";
import { derivePetState, type PetStateSnapshot } from "./petStateMachine.js";

const trimOptional = (value: string | null | undefined) => value?.trim() || undefined;

export interface PetRuntimeState {
  state: PetSemanticState;
  /** 至少一个 workspace 的订阅就绪（store live）。 */
  ready: boolean;
}

export interface WorkspaceSubscriptionTarget {
  workspacePath: string;
  workspaceIdentity?: string;
  remoteSessionId?: string;
  workspaceKey: string;
  /** 订阅读取面：主 workspace 可能是远程代理，其余是应用级 Host。 */
  agentService: SessionsIndexAgentService;
}

/**
 * 把 tab 整理成订阅目标。远程 tab 暂不纳入聚合：它们的 endpoint 需要
 * RemoteWorkspaceControllerProvider 上下文；只跳过而不报错，主 workspace 照常输出。
 */
function toSubscriptionTarget(
  tab: WorkspaceTabState,
  localAgentService: SessionsIndexAgentService,
): WorkspaceSubscriptionTarget | null {
  const workspacePath = tab.workspacePath;
  const remoteSessionId = trimOptional(tab.remoteSessionId);
  if (!workspacePath || remoteSessionId) return null;
  const workspaceIdentity = trimOptional(tab.workspaceIdentity);
  return {
    workspacePath,
    ...(workspaceIdentity ? { workspaceIdentity } : {}),
    workspaceKey: workspaceIdentity ?? workspacePath,
    agentService: localAgentService,
  };
}

/**
 * 主 workspace endpoint 的解析结果。宠物运行时挂在 Root 应用级，主窗口没有
 * workspace tab 时退回应用级 services，不把当前目录误当远程目标去预热。
 */
function usePrimaryWorkspaceAgentService(enabled: boolean): SessionsIndexAgentService {
  const baseServices = useServices();
  const activeTab = useTabStore((state) => {
    const tab = state.activeTabId
      ? state.tabs.find((candidate) => candidate.id === state.activeTabId)
      : undefined;
    return tab && isWorkspaceTab(tab) ? tab : null;
  });
  const resolution: WorkspaceServicesResolution = useWorkspaceServicesResolution(
    enabled ? activeTab?.workspacePath : undefined,
    activeTab?.remoteSessionId ?? null,
    activeTab?.workspaceIdentity ?? null,
    activeTab?.remoteTarget,
    { unscopedServices: baseServices, forceUnscoped: !enabled },
  );
  return resolution.services.nexAgentService;
}

/**
 * 当前窗口所有 workspace tab 去重（workspaceIdentity 优先）；签名只取三元组，
 * 用于 effect 依赖，避免每次渲染重建订阅。
 */
function useWorkspaceTargets(): {
  targets: WorkspaceSubscriptionTarget[];
  signature: string;
} {
  const tabs = useTabStore((state) => state.tabs);
  const hasLocalWorkspace = useMemo(
    () => tabs.some((tab) => isWorkspaceTab(tab) && !trimOptional(tab.remoteSessionId)),
    [tabs],
  );
  const primaryAgentService = usePrimaryWorkspaceAgentService(hasLocalWorkspace);

  const targets = useMemo(() => {
    const seen = new Map<string, WorkspaceSubscriptionTarget>();
    for (const tab of tabs) {
      if (!isWorkspaceTab(tab)) continue;
      const target = toSubscriptionTarget(tab, primaryAgentService);
      if (!target || seen.has(target.workspaceKey)) continue;
      seen.set(target.workspaceKey, target);
    }
    return [...seen.values()];
  }, [tabs, primaryAgentService]);

  const signature = useMemo(
    () =>
      JSON.stringify(
        targets.map((target) => ({
          workspacePath: target.workspacePath,
          workspaceIdentity: target.workspaceIdentity ?? null,
        })),
      ),
    [targets],
  );
  return { targets, signature };
}

/**
 * 聚合当前窗口所有 workspace tab 的宠物语义状态。
 * enabled=false 或无 workspace tab 时返回 idle/ready=false，且不发起任何订阅。
 */
export function usePetState(enabled: boolean): PetRuntimeState {
  const { targets, signature } = useWorkspaceTargets();
  const [snapshot, setSnapshot] = useState<PetStateSnapshot>({ state: "idle", since: 0 });
  const [ready, setReady] = useState(false);
  const previousRef = useRef<PetStateSnapshot | null>(null);
  // 已读水位：订阅建立时刻之前的终态不视为「未读完成」。
  const lastSeenActivityAtRef = useRef(0);
  // 每个 workspace 的最新 sessions 快照（跨 subscription 聚合）。
  const sessionsByWorkspaceRef = useRef<Map<string, readonly SessionSummary[]>>(new Map());
  const liveByWorkspaceRef = useRef<Map<string, boolean>>(new Map());

  useEffect(() => {
    previousRef.current = null;
    lastSeenActivityAtRef.current = Date.now();
    sessionsByWorkspaceRef.current = new Map();
    liveByWorkspaceRef.current = new Map();
    setSnapshot({ state: "idle", since: Date.now() });
    setReady(false);
    if (!enabled || targets.length === 0) {
      return;
    }

    const derive = () => {
      const next = derivePetState({
        sessions: [...sessionsByWorkspaceRef.current.values()].flat(),
        now: Date.now(),
        previous: previousRef.current,
        lastSeenActivityAt: lastSeenActivityAtRef.current,
      });
      previousRef.current = next;
      setSnapshot(next);
      setReady([...liveByWorkspaceRef.current.values()].some(Boolean));
    };
    // 状态生命周期（running 3min / failed 1h / review 7d）到期需要自然回落 idle，
    // sessions-index 不会为此推帧——每 30s 本地重推导一次。
    const expiryTimer = setInterval(derive, 30_000);
    const disposeSubscriptions = reconcilePetSessionsSubscriptions(targets, {
      onSessions: (workspaceKey, sessions) => {
        sessionsByWorkspaceRef.current.set(workspaceKey, sessions);
        derive();
      },
      onLive: (workspaceKey, live) => {
        liveByWorkspaceRef.current.set(workspaceKey, live);
        derive();
      },
      onReleased: (workspaceKey) => {
        sessionsByWorkspaceRef.current.delete(workspaceKey);
        liveByWorkspaceRef.current.delete(workspaceKey);
      },
    });

    return () => {
      clearInterval(expiryTimer);
      disposeSubscriptions();
    };
    // 依赖只取 signature（三元组列表）：同一 workspace 的 services 换代由 registry 内部
    // replaceTransport 处理，targets 里的 agentService 只在建立订阅时读取一次。
  }, [enabled, signature, targets]);

  return { state: snapshot.state, ready };
}
