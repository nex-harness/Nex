import type { AttachmentRegistry } from "../../attachments/registry.js";
import type { AttachmentPromptTable } from "./piAttachmentOps.js";
import type { OpenSession } from "./sessionLifecycle.js";
import type { EngineRewindOutcome, EngineRewindRequest } from "../types.js";
import { cutBranchToUserEntry } from "./piRewindOps.js";

/**
 * Rewind + re-prompt with the request's attachments mapped (spec A): images become prompt
 * images and other files an attachment block, exactly like a fresh send. The branch is cut
 * first (a rejected target never prompts), then the prompt carries the edited refs.
 */
export async function rewindWithAttachments(
  deps: {
    attachments: AttachmentRegistry;
    attachmentOps: AttachmentPromptTable;
    requireOpen: (sessionId: string) => OpenSession;
    /** Starts the prompt and runs `onStart` right after pi accepted it. */
    promptAgain: (
      open: OpenSession,
      text: string,
      payload: {
        promptText: string;
        images: import("@earendil-works/pi-ai").ImageContent[];
        streamingBehavior: "followUp";
      },
      onStart: (session: import("@earendil-works/pi-coding-agent").AgentSession) => void,
    ) => void;
  },
  sessionId: string,
  request: EngineRewindRequest,
): Promise<EngineRewindOutcome> {
  const open = deps.requireOpen(sessionId);
  const prepared = await deps.attachmentOps.prepare(
    sessionId,
    request.newText,
    request.attachments,
  );
  if (!(await cutBranchToUserEntry(open.session, request.target))) {
    throw new Error(`Rewind target ${request.target.userEntryId} is not on the active branch`);
  }
  if (request.newText.trim().length === 0) return { started: false };
  deps.promptAgain(open, request.newText, prepared.prompt, () =>
    deps.attachmentOps.adopt(open, request.newText, prepared),
  );
  return { started: true };
}
