/**
 * 安装向导「模型」步骤的状态判定（docs/next/model-onboarding.md）。
 *
 * 纯函数：输入一次 `GET /api/config/runners` 快照 + 新机器 id + 是否已超时，
 * 输出 UI 要展示的阶段。轮询、计时和副作用都留在组件里，便于单测覆盖
 * 「先 unknown、再 ready / needs-models」和候选来源筛选。
 */
import type { RunnerModelStatus } from "@nex/shared";

export type RunnerModelOnboardingState =
  | { kind: "checking" }
  | { kind: "ready"; modelCount: number }
  | { kind: "needs-models"; sources: RunnerModelStatus[] };

export function resolveRunnerModelOnboardingState(params: {
  statuses: readonly RunnerModelStatus[];
  /** 刚装好的 runner；它的状态决定本步骤走向。 */
  runnerId: string;
  /** 轮询窗口（约 15s）是否已用尽。 */
  timedOut: boolean;
}): RunnerModelOnboardingState {
  const target = params.statuses.find((status) => status.runnerId === params.runnerId);

  if (target && target.modelCount > 0) {
    return { kind: "ready", modelCount: target.modelCount };
  }

  // 刚 hello 完、配置尚未下发时 mode 仍是 unknown；此时不能判定为“没有模型”，
  // 先继续转圈，直到超时再按无模型处理（spec：超时后仍 unknown 视为 needs-models）。
  if (target?.mode === "unknown" && !params.timedOut) {
    return { kind: "checking" };
  }

  // 目标 runner 还没出现在列表里（连 hello 都未完成）同样先等待。
  if (!target && !params.timedOut) {
    return { kind: "checking" };
  }

  // 候选来源：除新机器外、已连接且确实有模型的机器。只读一次快照，
  // 因此导入按钮点击时再用 runnerId 触发服务端导入。
  const sources = params.statuses.filter(
    (status) => status.runnerId !== params.runnerId && status.connected && status.modelCount > 0,
  );

  return { kind: "needs-models", sources };
}
