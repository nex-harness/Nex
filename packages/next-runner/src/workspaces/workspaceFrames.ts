/**
 * Binds the served-folder owner to the link (docs/next/runner-folders.md
 * decision 3): every add/remove pushes the full list as `{t:"workspaces"}`.
 *
 * Extracted from `main.ts` so the wiring is exercised by tests instead of being
 * re-implemented there. A reconnect needs no special case: the next `hello`
 * already carries the live list, so a frame is only sent while connected.
 */
import type { WorkspaceRoots } from "./workspaceRoots.js";

export interface WorkspaceFrameSender {
  readonly connected: boolean;
  sendWorkspaces(workspaces: Array<{ path: string; label: string }>): void;
}

export function publishWorkspaceFrames(
  roots: WorkspaceRoots,
  link: WorkspaceFrameSender,
): () => void {
  return roots.onChange(() => {
    if (!link.connected) return;
    link.sendWorkspaces(roots.list().map((folder) => ({ path: folder.path, label: folder.label })));
  });
}
