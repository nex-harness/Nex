import type { ServerWorkspaceOption } from "@nex/shared";
import { groupRunnerWorkspaceOptions } from "@/lib/runnerWorkspaceOptions.js";

/**
 * DirectoryBrowser 的机器选择（docs/next/workspace-tools.md Decision 1，多 runner）。
 *
 * 目录浏览器是 host-global 的文件夹选择器：web 上 server 可以挂载多台 runner，
 * 每台机器有自己的文件系统。这里把 `listServerWorkspaces()` 的分组结果收敛成
 * “一台机器一个选项”（runnerName + OS），用于在浏览器上方切换浏览哪台机器。
 *
 * 路由尚未打通前，选择机器只改 UI 目标并给出明确提示；server 侧按 runnerId 路由后
 * 由 `machineTarget` 把 readdir/system.info 发到该 runner（见 DirectoryBrowser）。
 */

export interface DirectoryBrowserMachineOption {
  /** 稳定 key：优先 runnerId，缺失时退回分组 key（旧 server 的“本机”为 "__local__"）。 */
  id: string;
  /** 机器展示名；旧 server 无 runner 信息时为空串，UI 回退显示“本机”。 */
  name: string;
  /** 机器操作系统（darwin/windows/linux…）；未知时省略。 */
  os?: string;
  /** 该机器上 server 已编目的 workspace 根路径（可作为浏览起点）。 */
  workspacePaths: string[];
}

function parseRunnerIdFromIdentity(identity: string | undefined): string | undefined {
  const trimmed = identity?.trim();
  if (!trimmed) {
    return undefined;
  }
  // `runner:<runnerId>:<path>` 只有前两个 ":" 是结构分隔符。
  const rest = trimmed.startsWith("runner:") ? trimmed.slice("runner:".length) : "";
  const separatorIndex = rest.indexOf(":");
  return separatorIndex > 0 ? rest.slice(0, separatorIndex) : undefined;
}

/** 从 runner workspace 目录推导机器选项；保持 server 返回的机器顺序。 */
export function buildDirectoryBrowserMachineOptions(
  workspaces: readonly ServerWorkspaceOption[] | null | undefined,
): DirectoryBrowserMachineOption[] {
  if (!workspaces || workspaces.length === 0) {
    return [];
  }

  return groupRunnerWorkspaceOptions(workspaces).map((group) => {
    const first = group.workspaces[0];
    const id =
      first?.runnerId?.trim() ||
      parseRunnerIdFromIdentity(first?.workspaceIdentity) ||
      first?.workspaceIdentity?.trim() ||
      group.key;
    return {
      id,
      name: group.runnerName,
      ...(group.runnerOs ? { os: group.runnerOs } : {}),
      workspacePaths: group.workspaces.map((workspace) => workspace.path),
    };
  });
}

/**
 * 需要展示机器选择器的条件：多于一台机器。
 * 单 runner（或旧 server）保持原目录浏览器行为，不增加视觉噪声。
 */
export function shouldShowDirectoryBrowserMachineSelector(
  options: readonly DirectoryBrowserMachineOption[],
): boolean {
  return options.length > 1;
}

/**
 * 切换机器后的浏览起点：优先该机器第一个目录存在的 workspace 根，
 * 否则沿用当前 path（让用户看到服务器实际报错，而不是静默跳到别处）。
 */
export function resolveDirectoryBrowserMachineStartPath(
  option: DirectoryBrowserMachineOption | null | undefined,
  currentPath: string,
): string {
  const firstRoot = option?.workspacePaths.map((path) => path.trim()).find(Boolean);
  return firstRoot ?? currentPath;
}
