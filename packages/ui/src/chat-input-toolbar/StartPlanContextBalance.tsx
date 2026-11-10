import type { UsageEntitlementSnapshot } from "@nex/shared";

/**
 * Nex 无账号体系：Start Plan 今日余额面板已移除（权益查询需要账号）。
 * 保留配置类型与 has* 判定，让 Composer 工具条继续编译；面板恒不渲染。
 */
export interface ChatStartPlanBalanceConfig {
  loading: boolean;
  onAccess?: () => Promise<void> | void;
  onUpgradeClick?: () => void;
  refreshing?: boolean;
  snapshot: UsageEntitlementSnapshot | null;
}

export function hasChatStartPlanBalance(
  _config?: ChatStartPlanBalanceConfig | null,
): boolean {
  return false;
}

export function ChatStartPlanBalancePanel(_props: {
  config: ChatStartPlanBalanceConfig;
  intl: unknown;
  locale: string;
  separated?: boolean;
}) {
  return null;
}
