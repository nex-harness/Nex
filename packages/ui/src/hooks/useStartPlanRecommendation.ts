import { useCallback } from "react";
import type { ModelSelectionView } from "@nex/provider";
import type { ModelSelection } from "@nex/shared";

/**
 * Nex 无账号体系：Start Plan / Coding Plan 推荐已移除。
 * 保留同名 hook，让既有提交路径（副屏继承、首发选择）行为退化为「原选择直通」，
 * 不再等待权益查询或弹出升级对话框。
 */
export function useStartPlanRecommendation(
  _view: ModelSelectionView | null | undefined,
  _surface?: "subagent",
): (selection: ModelSelection) => Promise<ModelSelection | null> {
  return useCallback(async (selection: ModelSelection) => selection, []);
}
