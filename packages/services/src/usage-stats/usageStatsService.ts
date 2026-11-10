import type { AppUsageRequest, AppUsageSnapshot } from "@nex/shared";
import type { INexAgentService } from "../nex-agent/nexAgent.js";
import type { IUsageStatsService } from "./usageStats.js";

/**
 * Nex 无账号体系：Coding Plan / 权益 / 额度查询已移除。
 * 这里只保留读取本机 agent 数据库真实 token 统计的 App Usage。
 */
export function createUsageStatsService(dependencies: {
  /** App Usage 经 Nex Protocol 读取 agent 数据库真实统计。 */
  nexAgentService: Pick<INexAgentService, "getAppUsageStats">;
}): IUsageStatsService {
  return {
    async getAppUsageSnapshot(request: AppUsageRequest): Promise<AppUsageSnapshot> {
      return dependencies.nexAgentService.getAppUsageStats({
        range: request.range,
        timeZone: request.timeZone,
      });
    },
  };
}
