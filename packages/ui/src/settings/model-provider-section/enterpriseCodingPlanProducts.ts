/**
 * Nex 无账号体系：企业 Coding Plan 产品目录已移除。
 * 这里只保留类型与纯函数形状，让用量来源构造继续编译；不会产生任何远端请求。
 */
import type { ProviderFamilyDomain } from "@nex/shared";

export interface EnterpriseCodingPlanProductDisplay {
  productId: string;
  productName?: string | null;
  subscribed?: boolean;
  family?: ProviderFamilyDomain;
  organizationId?: string | null;
  organizationName?: string | null;
  projectId?: string | null;
  projectName?: string | null;
  teamProjects?: Array<{
    organizationId?: string | null;
    organizationName?: string | null;
    projectId?: string | null;
    projectName?: string | null;
  }>;
}

export function resolveEnterpriseCodingPlanProductFamily(
  product: Pick<EnterpriseCodingPlanProductDisplay, "family">,
): ProviderFamilyDomain {
  return product.family ?? "bigmodel";
}
