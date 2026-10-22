import type { AgentSession } from "@earendil-works/pi-coding-agent";
import type { EngineRewindOutcome, EngineRewindRequest, EngineRewindTarget } from "../types.js";
import type { PromptHooks } from "./piSessionOps.js";

/**
 * Rewind primitive shared by `editUserQuery` and `retryTurn`
 * (docs/next/conversation-features.md decision 5, research §3/§4).
 *
 * pi has no rewind API; the equivalent is exactly three steps:
 *
 * ```
 * abort() (if running) ─▶ waitForIdle()
 *   ─▶ sessionManager.branch(parentOf(userEntry))    // leaf moves; entries are not deleted
 *   ─▶ session.refreshContext()                      // agent state rebuilds from the branch
 *   ─▶ prompt(newText)                               // same session, same pi file
 * ```
 *
 * The abandoned branch stays in the pi file (append-only) but is invisible: `getBranch()`
 * walks from the leaf, so restore produces exactly the rows the live projection kept.
 * pi entry ids of retained entries are untouched, so surviving `entityId`s stay valid.
 *
 * Ordering with the projection: the caller truncates the live rows *before* calling this,
 * then prompts. The new prompt's events then append a fresh turn; row ids stay monotonic
 * because the projection never lowers `nextRowId`.
 */
export async function rewindSession(
  hooks: PromptHooks & { open: { session: AgentSession; inflight: Promise<void> | null } },
  workspacePath: string,
  sessionId: string,
  request: EngineRewindRequest,
): Promise<EngineRewindOutcome> {
  if (!(await cutBranchToUserEntry(hooks.open.session, request.target))) {
    throw new Error(`Rewind target ${request.target.userEntryId} is not on the active branch`);
  }

  void workspacePath;
  void sessionId;
  // The caller prompts after this returns (it may need to attach images/refs, spec A).
  return { started: true };
}

/**
 * Cuts the branch to the parent of the target user entry (aborting a live run first) and
 * refreshes pi's context. `false` = the target is not on the active branch.
 */
export async function cutBranchToUserEntry(
  session: AgentSession,
  target: EngineRewindTarget,
): Promise<boolean> {
  // Idempotent: after a cut the entry is no longer on the branch; the leaf already sits on
  // its parent, so a repeated call (files pre-applied, then the re-prompt) is a no-op.
  if (!parentOfUserEntry(session, target.userEntryId)) {
    return session.sessionManager.getLeafId() === parentIdOfEntry(session, target.userEntryId);
  }
  if (session.isStreaming || session.isCompacting) {
    // pi's abort also cancels a manual compaction and waits for idle; the command layer
    // refuses while a compact lock is held, so this normally only unwinds a live run.
    await session.abort();
  }
  await session.waitForIdle();
  if (session.isStreaming) {
    throw new Error("Session did not become idle; rewind not applied");
  }
  const parentId = parentOfUserEntry(session, target.userEntryId);
  if (parentId === undefined) {
    throw new Error(`Rewind target ${target.userEntryId} disappeared`);
  }
  if (parentId === null) session.sessionManager.resetLeaf();
  else session.sessionManager.branch(parentId);
  session.refreshContext();
  return true;
}

/** Parent id of an entry by id (also when it has dropped off the active branch). */
function parentIdOfEntry(session: AgentSession, entryId: string): string | null | undefined {
  const entry = session.sessionManager.getEntry(entryId);
  return entry ? (entry.parentId ?? null) : undefined;
}

/**
 * Resolves the canonical user message that caused an assistant message: the nearest user
 * entry at or before the target on the active branch. Retry re-sends its text.
 */
export function userMessageBefore(
  session: AgentSession,
  targetEntryId: string,
): EngineRewindTarget | null {
  const branch = session.sessionManager.getBranch();
  const index = branch.findIndex((entry) => entry.id === targetEntryId);
  if (index < 0) return null;
  const target = branch[index];
  if (!target || target.type !== "message" || target.message.role !== "assistant") return null;
  for (let cursor = index - 1; cursor >= 0; cursor -= 1) {
    const entry = branch[cursor];
    if (!entry || entry.type !== "message" || entry.message.role !== "user") continue;
    return { userEntryId: entry.id, text: messageText(entry.message.content) };
  }
  return null;
}

/**
 * `null` = the user entry is the first entry (rewind to an empty session, `resetLeaf`);
 * `undefined` = the entry is not on the active branch (caller rejects, never guesses).
 */
function parentOfUserEntry(session: AgentSession, userEntryId: string): string | null | undefined {
  const entry = session.sessionManager.getEntry(userEntryId);
  if (!entry || entry.type !== "message" || entry.message.role !== "user") return undefined;
  const onBranch = session.sessionManager.getBranch().some((item) => item.id === userEntryId);
  if (!onBranch) return undefined;
  return entry.parentId ?? null;
}

function messageText(content: unknown): string {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .map((block) => {
      if (typeof block === "object" && block !== null) {
        const typed = block as { type?: unknown; text?: unknown };
        if (typed.type === "text" && typeof typed.text === "string") return typed.text;
      }
      return "";
    })
    .filter((text) => text.length > 0)
    .join("\n");
}
