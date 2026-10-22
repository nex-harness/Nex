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
 * pi AgentSessionEvent → engine events. `message_update` carries a cumulative
 * `partial` snapshot; it is deliberately dropped here so projection only sees deltas.
 */
export function translatePiEvent(event: AgentSessionEvent, at = Date.now()): EngineEvent[] {
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
        return [
          {
            type: "user_input",
            at,
            text: messageText(event.message.content),
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
