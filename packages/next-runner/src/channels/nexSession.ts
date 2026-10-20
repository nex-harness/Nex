import type { NexWorkspacePresentation } from "@nex/shared";

/**
 * `nex-session.readWorkspacePresentation` minimal valid return: the UI turns `mode` into a
 * synthetic config option and passes `slashCommands` through. pi has no workspace slash
 * command catalog in this MVP, so the list is empty.
 */
export function readWorkspacePresentation(params: {
  workspacePath: string;
  workspaceIdentity?: string;
}): NexWorkspacePresentation {
  const { workspacePath } = params;
  // 多 runner：服务端按 `runner:<id>:<path>` 路由，UI 的 workspaceKey 统一取
  // `identity?.trim() || path`。这里必须回显请求里的身份，否则同路径的两台机器在
  // UI 侧会被归到同一个 workspaceKey，任务行/订阅无法区分机器。
  const workspaceIdentity = params.workspaceIdentity?.trim();
  return {
    workspace: {
      workspacePath,
      ...(workspaceIdentity ? { workspaceIdentity } : {}),
      workspaceKey: workspaceIdentity || workspacePath,
    },
    mode: "build",
    slashCommands: [],
  };
}

/** Accepts both the service form `{workspacePath}` and the schema form `{workspace:{...}}`. */
export function workspacePathFromParams(arg: unknown): string {
  if (typeof arg !== "object" || arg === null) {
    throw new Error("workspacePath is required");
  }
  const record = arg as Record<string, unknown>;
  if (typeof record["workspacePath"] === "string" && record["workspacePath"].length > 0) {
    return record["workspacePath"];
  }
  const workspace = record["workspace"];
  if (typeof workspace === "object" && workspace !== null) {
    const nested = (workspace as Record<string, unknown>)["workspacePath"];
    if (typeof nested === "string" && nested.length > 0) return nested;
  }
  throw new Error("workspacePath is required");
}
