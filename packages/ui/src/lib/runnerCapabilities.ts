/**
 * runner 能力判定（docs/next/runner-update.md 决策 3）。
 *
 * 能力由 runner 在 hello 里自报，server 透传到 `/api/server-info` 的
 * `runnerCapabilities`。缺失（旧 runner / 旧 server）必须视为“没有任何可选能力”：
 * 旧 runner 收到未实现的方法会回 `Unknown method ...`，UI 不能靠版本号猜。
 */

/** 本批次 UI 依赖的 runner 能力名。 */
export const RUNNER_CAPABILITY_WORKSPACES = "runner-workspaces";

/** 能力列表是否包含 `name`；大小写与空白差异不算能力不同。 */
export function runnerSupports(
  capabilities: readonly string[] | null | undefined,
  name: string,
): boolean {
  const wanted = name.trim().toLowerCase();
  if (!wanted || !capabilities?.length) {
    return false;
  }
  return capabilities.some((capability) => capability.trim().toLowerCase() === wanted);
}

/**
 * 某台机器的「Choose folder…」展示决策。
 *
 * - `hidden`：旧 server 分组没有 runnerId，或平台不提供添加文件夹（保持原行为）。
 * - `enabled`：runner 上报了 `runner-workspaces`。
 * - `disabled`：能力缺失，按钮置灰并提示更新；`showUpdate` 表示平台能否触发更新。
 */
export type RunnerChooseFolderState =
  | { kind: "hidden" }
  | { kind: "enabled" }
  | { kind: "disabled"; showUpdate: boolean };

export function resolveRunnerChooseFolderState(input: {
  runnerId?: string | null;
  capabilities?: readonly string[] | null;
  /** 平台实现了 `addRunnerWorkspace`（即调用方传了 onChooseFolder）。 */
  canChooseFolder: boolean;
  /** 平台实现了 `updateRunner`。 */
  canUpdate: boolean;
}): RunnerChooseFolderState {
  if (!input.canChooseFolder || !input.runnerId?.trim()) {
    return { kind: "hidden" };
  }
  if (runnerSupports(input.capabilities, RUNNER_CAPABILITY_WORKSPACES)) {
    return { kind: "enabled" };
  }
  return { kind: "disabled", showUpdate: input.canUpdate };
}
