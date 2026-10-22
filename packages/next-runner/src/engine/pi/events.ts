import type { AgentSessionEvent, SessionManager } from "@earendil-works/pi-coding-agent";
import type { EngineEvent, EngineStopReason } from "../types.js";

/**
 * Finds the persisted session entry id of a message pi just appended.
 *
 * pi persists `message_end` messages in `_handleAgentEvent` *after* emitting the event to
 * subscribers (`agent-session.ts:1148-1170`), and it emits `entry_appended` only for
 * custom/boundary/compaction entries — never for ordinary messages. The message object
 * identity is preserved by `SessionManager.appendMessage`, so a reverse scan of the branch
 * finds the entry pi just wrote.
 */
export function findMessageEntryId(
  sessionManager: SessionManager,
  message: unknown,
): string | undefined {
  const branch = sessionManager.getBranch();
  for (let index = branch.length - 1; index >= 0; index -= 1) {
    const entry = branch[index];
    if (entry?.type === "message" && entry.message === message) return entry.id;
  }
  return undefined;
}

/**
 * Facts only the engine knows when translating one pi event: which manual compact
 * operation is in flight (pi's events carry no command id).
 */
export interface PiTranslateContext {
  compactCommandId?: string;
  /**
   * Consumes the admitted text input that produced this user message: `inputText` is the
   * prompt text pi persisted (user text + attachment block); the return value is the user's
   * text plus the attachment refs. Spec A — the user row shows what the user typed, while
   * the model sees the block.
   */
  takeAttachments?: (
    promptText: string,
  ) =>
    | { text: string; attachments?: import("@nex/shared/nex-protocol-v4").AttachmentRef[] }
    | undefined;
}

/**
 * pi AgentSessionEvent → engine events. `message_update` carries a cumulative
 * `partial` snapshot; it is deliberately dropped here so projection only sees deltas.
 */
export function translatePiEvent(
  event: AgentSessionEvent,
  at = Date.now(),
  local?: PiTranslateContext,
): EngineEvent[] {
  switch (event.type) {
    case "message_start":
      return event.message.role === "assistant"
        ? [{ type: "assistant_start", at, model: event.message.model }]
        : [];
    case "message_update":
      return translateUpdate(event.assistantMessageEvent, at);
    case "tool_execution_start":
      return [
        {
          type: "tool_call_start",
          at,
          toolCallId: event.toolCallId,
          toolName: event.toolName,
          input: event.args,
        },
      ];
    case "tool_execution_update": {
      const text = toolResultText(event.partialResult);
      return [
        {
          type: "tool_call_update",
          at,
          toolCallId: event.toolCallId,
          toolName: event.toolName,
          ...(text !== undefined ? { text } : {}),
        },
      ];
    }
    case "tool_execution_end":
      return [
        {
          type: "tool_call_end",
          at,
          toolCallId: event.toolCallId,
          toolName: event.toolName,
          text: toolResultText(event.result) ?? "",
          isError: event.isError,
        },
      ];
    case "message_end":
      if (event.message.role === "user") {
        // Attachments of the prompt this user message belongs to (the runner queued them
        // at admission, spec A); a steer sent outside the runner has none.
        const claim = local?.takeAttachments?.(messageText(event.message.content));
        return [
          {
            type: "user_input",
            at,
            text: claim?.text ?? messageText(event.message.content),
            ...(claim?.attachments && claim.attachments.length > 0
              ? { attachments: claim.attachments }
              : {}),
          },
        ];
      }
      return event.message.role === "assistant"
        ? [
            {
              type: "assistant_end",
              at,
              stopReason: event.message.stopReason as EngineStopReason,
              ...(event.message.errorMessage ? { errorMessage: event.message.errorMessage } : {}),
              model: event.message.model,
              usage: {
                input: event.message.usage.input,
                output: event.message.usage.output,
                cacheRead: event.message.usage.cacheRead,
                cacheWrite: event.message.usage.cacheWrite,
              },
            },
          ]
        : [];
    case "agent_settled":
      return [{ type: "settled", at, aborted: event.aborted }];
    case "compaction_start":
      // `local.compactCommandId` is set only while the runner's own `compact()` runs; an
      // automatic compaction (threshold/overflow) has no command and the fold keys it by
      // prompt generation.
      return [
        {
          type: "compact_start",
          at,
          reason: event.reason,
          ...(local?.compactCommandId ? { sourceCommandId: local.compactCommandId } : {}),
        },
      ];
    case "compaction_end":
      return [
        {
          type: "compact_end",
          at,
          reason: event.reason,
          ...(local?.compactCommandId ? { sourceCommandId: local.compactCommandId } : {}),
          ...(event.result
            ? {
                result: {
                  tokensBefore: event.result.tokensBefore,
                  ...(event.result.estimatedTokensAfter !== undefined
                    ? { tokensAfter: event.result.estimatedTokensAfter }
                    : {}),
                },
              }
            : {}),
          aborted: event.aborted,
          ...(event.errorMessage ? { errorMessage: event.errorMessage } : {}),
        },
      ];
    case "auto_retry_start":
      return [
        {
          type: "api_retry_start",
          at,
          attempt: event.attempt,
          maxAttempts: event.maxAttempts,
          delayMs: event.delayMs,
        },
      ];
    case "auto_retry_end":
      return [{ type: "api_retry_end", at, success: event.success }];
    case "entry_appended":
      // Only the compaction boundary is a product fact here, and the fold already owns
      // that marker row from `compaction_start`/`compaction_end` (restore reads the entry
      // itself instead). Message entries never come through this event.
      return [];
    case "queue_update":
      // Guide items are visible as pending rows until pi consumes them; this is the only
      // signal that the steering queue changed (`agent-session.ts:1120-1140` removes the
      // text before emitting `message_start`).
      return [
        {
          type: "pi_queue_update",
          at,
          steering: [...event.steering],
          followUp: [...event.followUp],
        },
      ];
    default:
      return [];
  }
}

type AssistantUpdate = Extract<
  AgentSessionEvent,
  { type: "message_update" }
>["assistantMessageEvent"];

function translateUpdate(update: AssistantUpdate, at: number): EngineEvent[] {
  switch (update.type) {
    case "text_delta":
      return [{ type: "text_delta", at, delta: update.delta }];
    case "thinking_delta":
      return [{ type: "thinking_delta", at, delta: update.delta }];
    case "toolcall_start": {
      const part = update.partial.content[update.contentIndex];
      if (part?.type !== "toolCall") return [];
      return [
        {
          type: "tool_call_start",
          at,
          toolCallId: part.id,
          toolName: part.name,
          input: part.arguments,
        },
      ];
    }
    case "toolcall_delta": {
      const part = update.partial.content[update.contentIndex];
      if (part?.type !== "toolCall") return [];
      return [
        {
          type: "tool_input_delta",
          at,
          toolCallId: part.id,
          toolName: part.name,
          delta: update.delta,
        },
      ];
    }
    default:
      return [];
  }
}

function toolResultText(value: unknown): string | undefined {
  if (typeof value === "string") return value;
  if (typeof value !== "object" || value === null) return undefined;
  const content = (value as { content?: unknown }).content;
  return Array.isArray(content) ? messageText(content) : undefined;
}

function messageText(content: unknown): string {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .map((item) => {
      if (typeof item === "string") return item;
      if (
        typeof item === "object" &&
        item !== null &&
        (item as { type?: unknown }).type === "text"
      ) {
        const text = (item as { text?: unknown }).text;
        return typeof text === "string" ? text : "";
      }
      return "";
    })
    .filter((text) => text.length > 0)
    .join("\n");
}

/** Compatibility re-export kept for tests that build synthetic pi streams. */
export type PiSessionEvent = AgentSessionEvent;
