import type { NexWorkspacePresentation } from "@nex/shared";

/**
 * `nex-session.readWorkspacePresentation` minimal valid return: the UI turns `mode` into a
 * synthetic config option and passes `slashCommands` through. pi has no workspace slash
 * command catalog in this MVP, so the list is empty.
 */
export function readWorkspacePresentation(workspacePath: string): NexWorkspacePresentation {
  return {
    workspace: { workspacePath, workspaceKey: workspacePath },
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
