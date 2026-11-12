import type { ModelSelectionView } from "@nex/services";
import { readComposerRecent, resolveDraftInitialModelSelection } from "@/lib/composerRecent.js";
import {
  type V4ComposerDraft,
  type ComposerScope,
} from "@/v4/composer/composerDraftStore.js";

/** 普通新任务初始化；保留 Recent 原意图，由公共 View 解析有效选择。 */
export function initializeNewTaskDraft(
  draft: V4ComposerDraft,
  workspacePath: string,
  workspaceIdentity: string | undefined,
  view: ModelSelectionView,
  scope?: ComposerScope,
): V4ComposerDraft {
  const recent = readComposerRecent(workspacePath, workspaceIdentity, undefined, scope);
  return {
    ...draft,
    mode: recent?.mode === "plan" ? "build" : (recent?.mode ?? "build"),
    planEnabled: false,
    modelSelection:
      recent?.modelSelection ??
      resolveDraftInitialModelSelection(view, null).selection ??
      undefined,
  };
}
