import type { CodingPlanResetType } from "@nex/shared";
import type { SidebarUsageCodingPlanSourceId } from "@/lib/sidebarUsageCodingPlanProviderPreference.js";

/**
 * Nex 无账号体系：Coding Plan 额度面板已移除（权益查询需要账号）。
 * 保留配置类型与 has* 判定，让 Composer 工具条继续编译；面板恒不渲染。
 */
export type ChatCodingPlanUsageRemainingConfig = {
  availableProviders: readonly { providerId: string; label?: string | null }[];
  entitlements: readonly unknown[];
  onAccess?: () => Promise<void> | void;
  refreshing?: boolean;
  modelProvidersLoading: boolean;
  onEntitlementRefresh?: () => void | Promise<void>;
  onProviderChange?: (providerId: SidebarUsageCodingPlanSourceId) => void;
  onUsageClick?: () => void;
  selectedProviderId?: SidebarUsageCodingPlanSourceId;
};

export type CodingPlanQuotaResetAutoConfettiArms = Record<
  CodingPlanResetType,
  number | null
>;

export function hasChatCodingPlanUsageRemaining(
  _config?: ChatCodingPlanUsageRemainingConfig | null,
): boolean {
  return false;
}

export function ChatCodingPlanUsageRemainingPanel(_props: {
  autoCelebrateArm?: CodingPlanQuotaResetAutoConfettiArms;
  config: ChatCodingPlanUsageRemainingConfig;
  intl: unknown;
  locale: string;
  quotaResetDialogOpen?: boolean;
  separated?: boolean;
  onAutoCelebrated?: (completedAt: number) => void;
  onQuotaResetDialogOpenChange?: (open: boolean) => void;
}) {
  return null;
}
