/**
 * `runner-workspaces` (docs/next/runner-folders.md decision 3): the runner owns
 * its served folder list, so adding/removing a folder is a runner call made with
 * the machine target (`workspacePath: "~"`). The guard in
 * `channels/workspaceGuard.ts` admits exactly this channel under that target.
 *
 * `add` returns the workspace identity the UI should open
 * (`runner:<runnerId>:<path>`); `list` reports `removable` so env roots are not
 * offered for removal.
 */
import type { ChannelRegistration } from "../link/registry.js";
import type { Logger } from "../logger.js";
import type { WorkspaceRoots } from "../workspaces/workspaceRoots.js";
import { recordArg, asString } from "./workspaceToolsShared.js";

export interface RunnerWorkspacesOptions {
  roots: WorkspaceRoots;
  runnerId: string;
  logger: Logger;
}

export interface RunnerWorkspaceEntry {
  path: string;
  label: string;
  removable: boolean;
}

export interface AddedRunnerWorkspace {
  path: string;
  label: string;
  workspaceIdentity: string;
}

export function registerRunnerWorkspaces(
  registry: ChannelRegistration,
  options: RunnerWorkspacesOptions,
): void {
  const { roots, runnerId, logger } = options;

  registry.call("runner-workspaces", "list", async (): Promise<RunnerWorkspaceEntry[]> => {
    return roots.list();
  });

  registry.call("runner-workspaces", "add", async (arg): Promise<AddedRunnerWorkspace> => {
    const rawPath = asString(recordArg(arg)["path"]);
    if (!rawPath) throw new Error("path is required");
    // WorkspaceRoots.add 校验绝对路径/存在/目录/可读并 realpath 后原子持久化；
    // 重复添加是幂等 no-op，返回既有条目。
    const folder = await roots.add(rawPath);
    logger.info("runner workspace added", { path: folder.path, label: folder.label });
    return {
      path: folder.path,
      label: folder.label,
      workspaceIdentity: `runner:${runnerId}:${folder.path}`,
    };
  });

  registry.call("runner-workspaces", "remove", async (arg): Promise<undefined> => {
    const rawPath = asString(recordArg(arg)["path"]);
    if (!rawPath) throw new Error("path is required");
    await roots.remove(rawPath);
    logger.info("runner workspace removed", { path: rawPath });
    return undefined;
  });
}
