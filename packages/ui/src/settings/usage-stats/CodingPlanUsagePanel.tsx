import type { CodingPlanUsageSource } from "@/lib/codingPlanUsageSources.js";

/**
 * Nex 无账号体系：Coding Plan 用量面板已移除（权益/额度查询需要账号）。
 * 保留同名组件与来源构造函数，让设置页用量分区继续编译；面板渲染为空。
 */
export { buildCodingPlanUsageSources, type CodingPlanUsageSource } from "@/lib/codingPlanUsageSources.js";

export function CodingPlanUsagePanel(_props: {
  loadingSources?: boolean;
  workspaceIdentity?: string;
  workspacePath?: string;
  selectedSource?: CodingPlanUsageSource | null;
}) {
  return null;
}
