import type { ServerWorkspaceOption } from "@nex/shared";
import { parseServerWorkspaceIdentity } from "@nex/shared";

/**
 * Runner workspace 选择器的纯数据模型。
 *
 * 数据来自 `IPlatformService.listServerWorkspaces()`（web 平台读取 `/api/server-info`）。
 * 分组和排序都放在纯函数里，便于单测；组件只负责渲染和发动作。
 */

export interface RunnerWorkspacePickerGroup {
  /** 稳定的分组 key；缺失 runnerName 时回退 runnerId。 */
  key: string;
  /** 机器展示名。 */
  runnerName: string;
  /** 机器操作系统；未知时为 undefined，UI 不显示 os 徽标。 */
  runnerOs?: string;
  workspaces: ServerWorkspaceOption[];
}

/**
 * 按机器分组：先按 runnerName 归并，组内按 label/路径排序，组间保持 server 返回的
 * runner 顺序（同一 runner 的多个 workspace 天然相邻）。
 *
 * 无 runnerName 的条目使用 runnerId 作为展示名；两者都缺失时归入同一个
 * “本机”分组（单 runner 旧 server 的兼容形态）。
 */
export function groupRunnerWorkspaceOptions(
  workspaces: readonly ServerWorkspaceOption[],
): RunnerWorkspacePickerGroup[] {
  const groups = new Map<string, RunnerWorkspacePickerGroup>();

  for (const workspace of workspaces) {
    const runnerName = workspace.runnerName?.trim() || workspace.runnerId?.trim() || "";
    const key = workspace.runnerId?.trim() || runnerName || "__local__";
    const existing = groups.get(key);
    if (existing) {
      existing.workspaces.push(workspace);
      if (!existing.runnerOs && workspace.runnerOs?.trim()) {
        existing.runnerOs = workspace.runnerOs.trim();
      }
      continue;
    }
    groups.set(key, {
      key,
      runnerName,
      ...(workspace.runnerOs?.trim() ? { runnerOs: workspace.runnerOs.trim() } : {}),
      workspaces: [workspace],
    });
  }

  return [...groups.values()].map((group) => ({
    ...group,
    workspaces: [...group.workspaces].sort(compareRunnerWorkspaceOptions),
  }));
}

function compareRunnerWorkspaceOptions(
  left: ServerWorkspaceOption,
  right: ServerWorkspaceOption,
): number {
  const leftLabel = left.label?.trim() || left.path;
  const rightLabel = right.label?.trim() || right.path;
  return leftLabel.localeCompare(rightLabel) || left.path.localeCompare(right.path);
}

/** 只有至少一个可打开路径时才展示机器选择器；否则回落目录浏览器。 */
export function hasSelectableRunnerWorkspaces(
  workspaces: readonly ServerWorkspaceOption[] | null | undefined,
): workspaces is readonly ServerWorkspaceOption[] {
  return Boolean(workspaces?.some((workspace) => workspace.path.trim()));
}

/** 用于列表主标题：优先 server 提供的 label，否则取路径叶子。 */
export function resolveRunnerWorkspaceTitle(workspace: ServerWorkspaceOption): string {
  const label = workspace.label?.trim();
  if (label) {
    return label;
  }
  const normalized = workspace.path.replace(/\\/g, "/").replace(/\/+$/, "");
  const segments = normalized.split("/").filter(Boolean);
  return segments[segments.length - 1] ?? workspace.path;
}

/**
 * 从 `runner:` 身份解析机器展示名。
 *
 * 先查 server-info 缓存（`runnerNameByIdentity`），没有缓存时退回 runnerId，
 * 保证 header 至少在已知身份时永远能说明“这台 workspace 属于哪台机器”。
 * 非 runner 身份（本地/remote）返回 null，header 保持原样。
 */
export function resolveRunnerWorkspaceMachineLabel(
  workspaceIdentity: string | null | undefined,
  runnerNameByIdentity: Readonly<Record<string, string>>,
): string | null {
  const identity = workspaceIdentity?.trim();
  if (!identity) {
    return null;
  }
  const parsed = parseServerWorkspaceIdentity(identity);
  if (!parsed) {
    return null;
  }
  return runnerNameByIdentity[identity]?.trim() || parsed.runnerId;
}
