/**
 * 机器级目录浏览与「安装后打开新机器」的纯逻辑
 * （docs/next/runner-folders.md 决策 2/4/5）。
 *
 * 这里只做数据判定：装好后打开哪个 workspace、轮询何时放弃。RPC、计时器和
 * tab 打开动作都留在 hook / Root 里，便于单测覆盖。
 */
import type { ServerWorkspaceOption } from "@nex/shared";

/**
 * 要打开的 workspace：该机器上报的第一个 served folder。
 *
 * `/api/server-info` 只在 runner hello（或 workspaces 帧）到达后才包含新机器，
 * 因此调用方可能拿到的还是旧列表；返回 null 表示「还没出现，继续等」。
 *
 * 只按 runnerId 精确匹配：两台机器可以有同名路径，按路径匹配会打开错机器。
 * 没有 identity 的条目无法表达「哪台机器」，同样跳过，避免回落到 path-only 打开。
 */
export function findRunnerWorkspaceToOpen(
  workspaces: readonly ServerWorkspaceOption[] | null | undefined,
  runnerId: string,
): ServerWorkspaceOption | null {
  const trimmed = runnerId.trim();
  if (!trimmed || !workspaces) {
    return null;
  }
  const match = workspaces.find(
    (workspace) =>
      workspace.runnerId?.trim() === trimmed &&
      workspace.path.trim().length > 0 &&
      Boolean(workspace.workspaceIdentity?.trim()),
  );
  return match ?? null;
}

/**
 * 轮询到新机器的 workspace 出现为止（上限 `timeoutMs`）。
 *
 * 安装完成后 runner 的 hello 可能晚于向导里的安装成功回报，所以打开动作必须
 * 短暂等待而不是读一次旧快照就放弃（docs/next/runner-folders.md 决策 5）。
 * `load` 由调用方注入（Root 传 refreshed 目录读取），`now`/`sleep` 可注入以便单测
 * 不依赖真实计时器。
 */
export async function pollRunnerWorkspaceToOpen(params: {
  load: () => Promise<readonly ServerWorkspaceOption[] | null>;
  runnerId: string;
  timeoutMs: number;
  intervalMs: number;
  now?: () => number;
  sleep?: (ms: number) => Promise<void>;
}): Promise<ServerWorkspaceOption | null> {
  const now = params.now ?? Date.now;
  const sleep =
    params.sleep ?? ((ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms)));
  const deadline = now() + params.timeoutMs;

  for (;;) {
    const workspaces = await params.load();
    const found = findRunnerWorkspaceToOpen(workspaces, params.runnerId);
    if (found) {
      return found;
    }
    if (now() >= deadline) {
      return null;
    }
    await sleep(params.intervalMs);
  }
}
