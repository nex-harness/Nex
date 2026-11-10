import { createContext, useContext, useMemo, type ReactNode } from "react";
export interface CodingPlanEntryInventory {
  entryPlanList: string;
  status: "loading" | "error" | "ready";
  retry: () => void;
}

/**
 * 购买面板目标。Nex 无账号体系后不再有购买页，仅保留形状让既有入口类型不变。
 */
export interface CodingPlanUpgradeDialogTarget {
  providerId: string;
  initialAudience?: "personal" | "team" | "enterprise";
  initialTeamPlanKey?: string;
  funnelContext?: unknown;
}

interface CodingPlanUpgradeDialogContextValue {
  inventory: CodingPlanEntryInventory;
  openCodingPlanUpgrade: (
    target: CodingPlanUpgradeDialogTarget,
    observation?: { signal: AbortSignal; onResult: (opened: boolean) => void },
  ) => boolean;
}

/**
 * Nex 无账号体系：Coding Plan 购买面板与套餐库存查询已移除。
 * 这里保留同名 Provider 与 hook，让既有入口（错误横幅、侧栏、活动投放）
 * 继续编译，但打开动作恒为失败——不会再出现任何购买 UI 或远端请求。
 */
const CodingPlanUpgradeDialogContext = createContext<CodingPlanUpgradeDialogContextValue | null>(
  null,
);

const INERT_INVENTORY: CodingPlanEntryInventory = {
  status: "ready",
  entryPlanList: "",
  retry: () => {},
};

export function CodingPlanUpgradeDialogProvider({ children }: { children: ReactNode }) {
  const value = useMemo<CodingPlanUpgradeDialogContextValue>(
    () => ({
      inventory: INERT_INVENTORY,
      openCodingPlanUpgrade: (_target, observation) => {
        if (observation?.signal.aborted) return false;
        observation?.onResult(false);
        return false;
      },
    }),
    [],
  );
  return (
    <CodingPlanUpgradeDialogContext.Provider value={value}>
      {children}
    </CodingPlanUpgradeDialogContext.Provider>
  );
}

export function useCodingPlanUpgradeDialog() {
  const context = useContext(CodingPlanUpgradeDialogContext);
  if (!context) {
    throw new Error(
      "useCodingPlanUpgradeDialog must be used within CodingPlanUpgradeDialogProvider",
    );
  }
  return context;
}

/** 可独立挂载的 conversation pane 使用可选上下文。 */
export function useOptionalCodingPlanUpgradeDialog() {
  return useContext(CodingPlanUpgradeDialogContext);
}
