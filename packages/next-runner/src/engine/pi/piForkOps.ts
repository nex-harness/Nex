import { SessionManager, type ModelRuntime } from "@earendil-works/pi-coding-agent";
import type { Model } from "@earendil-works/pi-ai";
import type { EngineForkOutcome, EngineForkRequest, EngineTitleSource } from "../types.js";
import type { SessionMetaStore } from "./metaStore.js";
import { openPiSession, type OpenSession, type SessionFactoryDeps } from "./sessionLifecycle.js";
import { FORK_CUSTOM_TYPE } from "./translate.js";

/**
 * `forkAssistant` (docs/next/conversation-features.md decision 6, research §2).
 *
 * pi has no `AgentSession.fork()`; the primitive is a *second* `SessionManager` on the
 * parent's file calling `createBranchedSession(leafId)`. That writes a new session file
 * containing exactly the root→leaf path (new id, `parentSession` header, re-chained
 * labels, fixed `firstKeptEntryId`) and returns its path; the parent manager keeps its own
 * leaf and file untouched. The child then opens with its own `AgentSession`, gets the
 * `nex.fork` custom entry (the child restore synthesizes the `forkNotice` marker from it)
 * and the `Fork of <parent>` title.
 *
 * Caveat kept deliberately: `createBranchedSession` deep-copies entries, so the child is a
 * true copy — later parent appends never leak in, matching the old CLI's fork semantics.
 */
export interface ForkOpsDeps {
  metadata: SessionMetaStore;
  runtime: () => ModelRuntime;
  factoryDeps: () => SessionFactoryDeps;
  attach: (open: OpenSession) => void;
  sessionsChanged: (workspacePath: string) => void;
}

export async function forkAssistantSession(
  deps: ForkOpsDeps,
  workspacePath: string,
  parent: OpenSession,
  request: EngineForkRequest,
): Promise<EngineForkOutcome> {
  const parentManager = parent.session.sessionManager;
  const parentFile = parentManager.getSessionFile();
  if (!parentFile) throw forkError("fork.parentNotPersisted", "The parent session has no file");
  const onBranch = parentManager.getBranch().some((entry) => entry.id === request.targetEntryId);
  if (!onBranch)
    throw forkError("fork.targetNotOnBranch", "The fork target left the active branch");

  // A second manager reads the parent file; `createBranchedSession` writes the child under
  // the same session directory (the manager's dir stays the parent's dir).
  const forkManager = SessionManager.open(parentFile, sessionDirOf(parentManager));
  const childFile = forkManager.createBranchedSession(request.targetEntryId);
  if (childFile) {
    // Written through the fork manager's own handle: the entry lands in the child file as a
    // direct child of the copied leaf, never in the parent file.
    forkManager.appendCustomEntry(FORK_CUSTOM_TYPE, {
      parentSessionId: parent.session.sessionId,
      parentRowId: request.parentRowId,
    });
  }
  const childManager = childFile
    ? SessionManager.open(childFile, sessionDirOf(parentManager))
    : forkManager;

  const parentModel = parent.session.model as Model<never> | undefined;
  const restoredModel = parentModel
    ? ((deps.runtime().getModel(parentModel.provider, parentModel.id) as
        | Model<never>
        | undefined) ?? parentModel)
    : undefined;
  const { session, gate, open } = await openPiSession(
    deps.factoryDeps(),
    workspacePath,
    childManager,
    restoredModel,
  );
  const sessionId = session.sessionId;
  // The child inherits the parent's live composer/permission settings.
  const parentMode = parent.gate.getMode();
  gate.setMode(parentMode.mode, parentMode.planEnabled);
  if (parent.session.thinkingLevel) {
    session.setThinkingLevel(parent.session.thinkingLevel as never);
  }
  session.setSessionName(request.title);

  const titleSource: EngineTitleSource = "generated";
  await deps.metadata.upsert(workspacePath, sessionId, {
    createdAt: Date.now(),
    lastActivityAt: Date.now(),
    phase: "completedSuccess",
    title: request.title,
    titleSource,
    deleted: false,
    mode: parentMode.mode,
    planEnabled: parentMode.planEnabled,
  });
  deps.attach(open);
  deps.sessionsChanged(workspacePath);
  return { sessionId, title: request.title, titleSource };
}

/** pi keeps the session dir on the manager; `open(file, dir)` must be given the same one. */
function sessionDirOf(manager: SessionManager): string | undefined {
  return manager.getSessionDir() || undefined;
}

export function forkError(reasonCode: string, message: string): Error & { reasonCode: string } {
  return Object.assign(new Error(message), { reasonCode });
}
