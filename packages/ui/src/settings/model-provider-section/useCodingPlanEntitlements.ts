/**
 * Nex 无账号体系：Coding Plan 权益查询已移除。
 * 保留 hook 形状，恒返回空权益与空 provider 列表，不发起任何远端请求。
 */
import type { UsageEntitlementSnapshot } from "@nex/shared";

export interface CodingPlanEntitlementEntry {
  loading: boolean;
  snapshot: UsageEntitlementSnapshot | null;
  error: string | null;
}

export function useCodingPlanEntitlements(_input?: {
  providerSettingsView?: unknown;
  connectionSelections?: unknown;
  suppressProviderFingerprintAutoRefresh?: boolean;
}): {
  entitlements: Record<string, CodingPlanEntitlementEntry>;
  enabledStartPlanProviderIds: readonly string[];
  refresh: (options?: { silent?: boolean; reason?: string; force?: boolean }) => Promise<void>;
} {
  return {
    entitlements: {},
    enabledStartPlanProviderIds: [],
    refresh: async () => {},
  };
}
