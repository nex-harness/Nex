import type { ImageContent } from "@earendil-works/pi-ai";
import type { AgentSessionEvent } from "@earendil-works/pi-coding-agent";
import type { AttachmentRef } from "@nex/shared/nex-protocol-v4";
import type { AttachmentRegistry } from "../../attachments/registry.js";
import type { Logger } from "../../logger.js";
import type { OpenSession } from "./sessionLifecycle.js";
import {
  AttachmentPromptOps,
  newPromptRequestId,
  type PendingPromptInput,
} from "./attachmentOps.js";
import { mapPromptAttachments } from "./piAttachments.js";

/**
 * Attachment bookkeeping on top of pi's prompt lifecycle (spec A), owned by `PiEngine`.
 *
 * ```
 * engine submit/rewind with refs
 *   mapPromptAttachments → images (pi prompt images) + block (paths, never contents)
 *   → prompt(text + block, {images})            (the engine's single prompt start)
 *   → adopt: input table entry + one-shot system note (inline extension, not persisted)
 *
 * pi event, before translation:
 *   user message_start (prompt or steer, in admission order)
 *     → claims the input (text + refs → `OpenSession.claimedUserInput`)
 *     → a block prompt: rewrites the persisted user text to what the user typed and writes
 *       `nex.attachments` behind it, then emits `message_entry` for the row identity
 *   agent_start / agent_settled → steer accounting
 * ```
 *
 * The refs a row shows come from the claim, never from parsing the prompt text.
 */

const ATTACHMENT_SYSTEM_NOTE =
  "\n\n# Attachments\nThe user attached file(s) to this message. Their paths are listed at the end of the user message; use the read tool on a path when you need the file contents.";

export type SubmitOutcome = { status: "ok"; input: PendingPromptInput };

/** Map + system note for one prompt (the engine passes it as `SubmitOptions.prompt`). */
export interface PreparedPrompt {
  prompt: { promptText: string; images: ImageContent[]; streamingBehavior: "followUp" };
  accepted: AttachmentRef[];
  block: boolean;
}

export interface AttachmentPromptTableDeps {
  host: AttachmentRegistry;
  logger: Logger;
}

export class AttachmentPromptTable {
  private readonly prompts: AttachmentPromptOps;

  constructor(private readonly deps: AttachmentPromptTableDeps) {
    this.prompts = new AttachmentPromptOps(deps.logger);
  }

  /** Maps one prompt's refs (images + block); nothing is started here. */
  async prepare(
    sessionId: string,
    text: string,
    attachments: readonly AttachmentRef[] | undefined,
  ): Promise<PreparedPrompt> {
    const mapped = await mapPromptAttachments(this.deps.host, sessionId, attachments);
    this.logRejected(sessionId, mapped.rejected);
    const accepted = this.acceptedRefs(attachments, mapped.rejected);
    return {
      prompt: {
        promptText: `${text}${mapped.block}`,
        images: mapped.images,
        streamingBehavior: "followUp",
      },
      accepted,
      block: mapped.block.length > 0,
    };
  }

  /**
   * Guide delivery. pi's steer has no image channel, so images are named in the block and
   * the model reads them with `read` (which returns image blocks for vision models).
   */
  async steer(
    open: OpenSession,
    text: string,
    attachments: readonly AttachmentRef[] | undefined,
  ): Promise<string> {
    const sessionId = open.sessionId ?? open.session.sessionId;
    const mapped = await mapPromptAttachments(this.deps.host, sessionId, attachments);
    const accepted = this.acceptedRefs(attachments, mapped.rejected);
    this.logRejected(sessionId, mapped.rejected);
    const input: PendingPromptInput = {
      requestId: newPromptRequestId(),
      promptText: `${text}${mapped.block}${imageReadHint(mapped.images, accepted)}`,
      userText: text,
      attachments: accepted,
    };
    this.prompts.enqueue(sessionId, input, "steer");
    open.systemNote = mapped.block.length > 0 ? ATTACHMENT_SYSTEM_NOTE : undefined;
    try {
      await open.session.steer(input.promptText);
      return input.promptText;
    } catch (error) {
      this.prompts.dequeue(sessionId, input.requestId);
      throw error;
    } finally {
      // A steer is delivered at the current run's next tool boundary — no new run starts
      // for it, so no `before_agent_start` consumes the note.
      open.systemNote = undefined;
    }
  }

  /**
   * Registers the admitted input of a run the engine just started with a mapped prompt:
   * the run's `message_start` claims text + refs, and the system note adds the read hint.
   */
  adopt(open: OpenSession, text: string, prepared: PreparedPrompt): void {
    const block = prepared.block ? prepared.prompt.promptText.slice(text.length) : "";
    const sessionId = open.sessionId ?? open.session.sessionId;
    this.prompts.enqueue(
      sessionId,
      {
        requestId: `adopt_${Date.now()}`,
        promptText: `${text}${block}`,
        userText: text,
        attachments: prepared.accepted,
      },
      "prompt",
    );
    open.systemNote = block.length > 0 ? ATTACHMENT_SYSTEM_NOTE : undefined;
  }

  /** pi event hook (before translation): claims, rewrite, marker, steer accounting. */
  handlePiEvent(open: OpenSession, event: AgentSessionEvent): void {
    const sessionId = open.sessionId ?? open.session.sessionId;
    if (event.type === "message_start" && event.message.role === "user") {
      const result = this.prompts.onUserMessageStart(sessionId, open.session);
      open.claimedUserInput = result.input
        ? {
            text: result.input.userText,
            ...(result.input.attachments.length > 0
              ? { attachments: result.input.attachments }
              : {}),
          }
        : undefined;
      return;
    }
    if (event.type === "agent_settled") this.prompts.runSettled(sessionId);
  }

  dispose(sessionId: string): void {
    this.prompts.dispose(sessionId);
  }

  disposeAll(): void {
    this.prompts.disposeAll();
  }

  private acceptedRefs(
    refs: readonly AttachmentRef[] | undefined,
    rejected: ReadonlyArray<{ ref: AttachmentRef }>,
  ): AttachmentRef[] {
    if (!refs) return [];
    return refs.filter((ref) => !rejected.some((item) => item.ref.ref === ref.ref));
  }

  private logRejected(
    sessionId: string,
    rejected: ReadonlyArray<{ ref: AttachmentRef; reason: string }>,
  ): void {
    for (const item of rejected) {
      this.deps.logger.warn("attachment not handed to the model", {
        sessionId,
        fileName: item.ref.fileName,
        reason: item.reason,
      });
    }
  }
}

/**
 * Steer fallback for images: a one-line note naming the runner path so `read` can return
 * the image block. Empty when nothing was accepted.
 */
function imageReadHint(images: readonly ImageContent[], refs: readonly AttachmentRef[]): string {
  if (images.length === 0 || refs.length === 0) return "";
  return "\n(The attached image(s) are listed above; read their paths to view them.)";
}
