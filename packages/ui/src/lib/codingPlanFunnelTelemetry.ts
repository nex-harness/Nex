/**
 * Nex 无账号体系：Coding Plan 购买漏斗埋点已移除。
 * 保留类型与调用点形状，让设置页/入口继续编译，不再产生购买漏斗事件。
 */
export interface CodingPlanFunnelContext {
  providerId: string;
  upgradeSource?: string;
  eventRegion?: string;
  eventText?: string;
  entryPlanState?: string;
  entryPlanList?: string;
  [key: string]: unknown;
}

export function createCodingPlanFunnelContext(
  input: CodingPlanFunnelContext,
): CodingPlanFunnelContext {
  return input;
}

export function createIdleTimeCodingPlanFunnelContext(
  input: CodingPlanFunnelContext,
): CodingPlanFunnelContext {
  return input;
}

export function resolveCodingPlanEntryPlanState(input: {
  providerId: string;
  displayStatus?: string;
  planLevel?: string;
}): string {
  return `${input.providerId}:${input.displayStatus ?? ""}:${input.planLevel ?? ""}`;
}

export function resolveCodingPlanEntryPlanStateFromProviderSettings(
  _view: unknown,
): string {
  return "";
}

export async function reportCodingPlanUpgradeClick(
  _platform: unknown,
  _context: CodingPlanFunnelContext,
): Promise<void> {}
