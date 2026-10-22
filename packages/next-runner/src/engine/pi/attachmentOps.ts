import { randomUUID } from "node:crypto";
import type { AgentSession } from "@earendil-works/pi-coding-agent";
import type { AttachmentRef } from "@nex/shared/nex-protocol-v4";
import type { Logger } from "../../logger.js";
import { attachmentsEntryData } from "./attachmentEntry.js";

/**
 * Prompt / steer attachment plumbing for one open pi session (spec A).
 *
 * Ownership: the engine is the single writer of `nex.attachments` custom entries and of the
 * pending input table. pi hands the prompt's user message to listeners at `message_start`
 * and persists that same object at `message_end`, so the run's starting `message_start`
 * rewrites its text to what the user typed and drops the marker right behind it:
 *
 * ```
 * submit: enqueue{requestId, promptText = userText + block, userText, refs}
 *   ─▶ attach the "use read" hint to the system prompt (one-shot, never persisted)
 *   ─▶ prompt(promptText, {images}); user message = [text(promptText), image…]
 * message_start (user): claim the head input → rewrite the handed-over message, append
 *   `nex.attachments` (id-less marker, joined by branch order on restore)
 *   ⇢ message_end: user_input{text: userText, attachments}
 * steer: counted the same way (steered text is exactly what was steered: no rewrite).
 * ```
 */

export interface PendingPromptInput {
  requestId: string;
  /** Exact text handed to `session.prompt` (user text + attachment block). */
  promptText: string;
  /** What the user typed; the userInput row renders this. */
  userText: string;
  attachments: AttachmentRef[];
}

interface SessionAttachmentState {
  /** Prompts handed to pi and not yet started, oldest first. */
  pending: PendingPromptInput[];
  steersOutstanding: number;
  pendingSteer: PendingPromptInput[];
  /** Cursor into `pending` for `message_end` text claims (a prompt is claimed once). */
  claimed: number;
}

export { ATTACHMENTS_CUSTOM_TYPE } from "./attachmentEntry.js";
import { ATTACHMENTS_CUSTOM_TYPE as CUSTOM_TYPE } from "./attachmentEntry.js";

/** True when the prompt text differs from what the user typed (an attachment block attached). */
export function hasBlock(input: PendingPromptInput): boolean {
  return input.promptText !== input.userText;
}

export class AttachmentPromptOps {
  private readonly states = new Map<string, SessionAttachmentState>();

  constructor(private readonly logger: Logger) {}

  private state(sessionId: string): SessionAttachmentState {
    let state = this.states.get(sessionId);
    if (!state) {
      state = { pending: [], steersOutstanding: 0, pendingSteer: [], claimed: 0 };
      this.states.set(sessionId, state);
    }
    return state;
  }

  runSettled(sessionId: string): void {
    const state = this.state(sessionId);
    if (state.pending.length > 0) {
      this.logger.debug("attachment input table drained without delivery", {
        sessionId,
        dropped: state.pending.length,
      });
    }
    state.pending = [];
    state.steersOutstanding = 0;
    state.pendingSteer = [];
    state.claimed = 0;
  }

  /** Queues the admitted input (before `prompt`/`steer` is issued). */
  enqueue(sessionId: string, input: PendingPromptInput, kind: "prompt" | "steer"): void {
    const state = this.state(sessionId);
    if (kind === "steer") {
      state.steersOutstanding += 1;
      state.pendingSteer.push(input);
      return;
    }
    state.pending.push(input);
  }

  /** The prompt/steer call failed before pi accepted it. */
  dequeue(sessionId: string, requestId: string): void {
    const state = this.state(sessionId);
    const index = state.pending.findIndex((item) => item.requestId === requestId);
    if (index >= 0) {
      state.pending.splice(index, 1);
      if (index < state.claimed) state.claimed -= 1;
    }
    state.pendingSteer = state.pendingSteer.filter((item) => item.requestId !== requestId);
  }

  /**
   * `message_start` of a user message: the admitted input it delivers, in admission order.
   * pi emits it for the run's starting prompt and for every steer, and text/refs are fixed
   * at admission, so a counter is exact — it runs before pi flips its streaming flag, which
   * makes a flag check unreliable here.
   *
   * An attachment prompt is rewritten here (block text → the user's text): the message
   * object is the one pi persists next, so the session file never shows the block. The
   * marker entry follows it (id-less; restore joins by branch order).
   */
  onUserMessageStart(sessionId: string, session: AgentSession): { input?: PendingPromptInput } {
    const state = this.state(sessionId);
    const head = state.pending[state.claimed];
    if (!head) return {};
    state.claimed += 1;
    if (state.steersOutstanding > 0) state.steersOutstanding -= 1;
    if (!hasBlock(head) || head.attachments.length === 0) return { input: head };
    const handed = session.messages.find(
      (message): message is Extract<typeof message, { role: "user" }> =>
        message.role === "user" && textOf(message.content).startsWith(head.promptText),
    );
    if (handed) {
      handed.content = userVisibleContent(handed.content, head.userText) as typeof handed.content;
    }
    session.sessionManager.appendCustomEntry(
      CUSTOM_TYPE,
      attachmentsEntryData("pending", head.attachments),
    );
    return { input: head };
  }

  dispose(sessionId: string): void {
    this.states.delete(sessionId);
  }

  disposeAll(): void {
    this.states.clear();
  }
}

function textOf(content: unknown): string {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .filter(
      (block): block is { type: "text"; text: string } =>
        typeof block === "object" &&
        block !== null &&
        (block as { type?: unknown }).type === "text",
    )
    .map((block) => block.text)
    .join("\n");
}

/** User-visible content: the text the user typed, keeping any image blocks. */
function userVisibleContent(content: unknown, userText: string): unknown {
  if (typeof content === "string") return userText;
  const blocks = Array.isArray(content) ? content : [];
  const images = blocks.filter(
    (block) =>
      typeof block === "object" && block !== null && (block as { type?: unknown }).type === "image",
  );
  return [...(userText.length > 0 ? [{ type: "text" as const, text: userText }] : []), ...images];
}

/** Deterministic request ids when the caller has none (tests, direct engine use). */
export function newPromptRequestId(): string {
  return `prompt_${randomUUID()}`;
}
