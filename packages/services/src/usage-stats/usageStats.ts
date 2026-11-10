import type { AppUsageRequest, AppUsageSnapshot } from "@nex/shared";
import { ServiceChannels } from "@nex/shared";
import { createServiceDescriptor } from "../descriptors.js";

export interface IUsageStatsService {
  getAppUsageSnapshot(request: AppUsageRequest): Promise<AppUsageSnapshot>;
}

export const IUsageStatsService = createServiceDescriptor<IUsageStatsService>(
  ServiceChannels.UsageStats,
);
