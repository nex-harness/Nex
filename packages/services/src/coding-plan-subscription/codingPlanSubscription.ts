import type { DynamicWorkflowClientConfig } from "@nex/shared";
import { ServiceChannels } from "@nex/shared";
import { createServiceDescriptor } from "../descriptors.js";

/**
 * Nex 无账号体系：Coding Plan 订阅/购买/权益已移除。
 * 这个服务通道只保留动态工作流灰度快照，避免 UI 与 Agent 两侧各自发一份远端请求。
 */
export interface ICodingPlanSubscriptionService {
  /**
   * 动态工作流灰度快照：远端 `configs.dynamicWorkflow.mode`
   * 与本地覆盖折叠后的结果；forceRefresh 绕过 1h 快照缓存。请求失败 fail-closed（disabled/default）。
   */
  getDynamicWorkflowClientConfig(options?: {
    forceRefresh?: boolean;
  }): Promise<DynamicWorkflowClientConfig>;
}

export const ICodingPlanSubscriptionService =
  createServiceDescriptor<ICodingPlanSubscriptionService>(ServiceChannels.CodingPlanSubscription);
