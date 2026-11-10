import type { EnterpriseCodingPlanProductDisplay } from "@/settings/model-provider-section/enterpriseCodingPlanProducts.js";

/**
 * Nex 无账号体系：企业套餐产品目录查询已移除。
 * 保留 hook 形状，恒返回空快照，不发起任何远端请求。
 */
export interface EnterpriseCodingPlanProductsState {
  snapshot: { productList: EnterpriseCodingPlanProductDisplay[] } | null;
  loading: boolean;
  error: string | null;
  refresh: () => Promise<void>;
}

export function useEnterpriseCodingPlanProducts(_input?: {
  enabled?: boolean;
  authenticated?: boolean;
  family?: string;
}): EnterpriseCodingPlanProductsState {
  return {
    snapshot: null,
    loading: false,
    error: null,
    refresh: async () => {},
  };
}
