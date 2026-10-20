import type { Model } from "@earendil-works/pi-ai";
import type { ModelRuntime } from "@earendil-works/pi-coding-agent";
import { getSupportedThinkingLevels } from "@earendil-works/pi-ai";
import type {
  EngineAssistantPart,
  EngineMessage,
  EngineModelInfo,
  EngineStopReason,
} from "../types.js";

const PI_TO_ENGINE_API: Record<string, EngineModelInfo["apiType"]> = {
  "anthropic-messages": "anthropic-messages",
  "openai-completions": "openai-chat-completions",
  "openai-responses": "openai-responses",
  "azure-openai-responses": "openai-responses",
  "openai-codex-responses": "openai-responses",
};

export function toEngineModelInfo(
  model: Model<never>,
  _runtime: ModelRuntime,
): EngineModelInfo | null {
  // Image/classifier models are listed by pi but cannot run a chat turn; offering them
  // in the composer would produce a model that always fails.
  const modelType = (model as { type?: string }).type;
  if (modelType !== undefined && modelType !== "chat") return null;
  const apiType = PI_TO_ENGINE_API[model.api];
  if (!apiType) return null;
  const levels = getSupportedThinkingLevels(model);
  return {
    providerId: model.provider,
    providerName: model.provider,
    modelId: model.id,
    modelName: model.name || model.id,
    apiType,
    baseUrl: model.baseUrl,
    reasoningLevels: levels.length > 0 ? levels : ["off"],
    maxOutputTokens: model.maxTokens,
    contextWindow: model.contextWindow,
    supportsImage: model.input.includes("image"),
    supportsVideo: false,
    supportsAudio: false,
    supportsPdf: false,
  };
}

/**
 * Converts pi's AgentMessage transcript to the engine's narrow normalized form.
 * Bash-execution and custom messages have no direct v4 row, so `bashExecution` text
 * is surfaced as a user message only when it carries visible output.
 */
export function toEngineHistory(messages: readonly unknown[]): EngineMessage[] {
  const result: EngineMessage[] = [];
  for (const raw of messages) {
    if (typeof raw !== "object" || raw === null) continue;
    const message = raw as Record<string, unknown>;
    const role = message["role"];
    if (role === "user") {
      result.push({
        role: "user",
        at: typeof message["timestamp"] === "number" ? message["timestamp"] : Date.now(),
        text: contentText(message["content"]),
      });
      continue;
    }
    if (role === "assistant") {
      result.push({
        role: "assistant",
        at: typeof message["timestamp"] === "number" ? message["timestamp"] : Date.now(),
        model: typeof message["model"] === "string" ? message["model"] : undefined,
        usage:
          typeof message["usage"] === "object" && message["usage"] !== null
            ? {
                input: num((message["usage"] as Record<string, unknown>)["input"]),
                output: num((message["usage"] as Record<string, unknown>)["output"]),
                cacheRead: num((message["usage"] as Record<string, unknown>)["cacheRead"]),
                cacheWrite: num((message["usage"] as Record<string, unknown>)["cacheWrite"]),
              }
            : undefined,
        stopReason: (message["stopReason"] as EngineStopReason) ?? "stop",
        errorMessage:
          typeof message["errorMessage"] === "string" ? message["errorMessage"] : undefined,
        parts: assistantParts(message["content"]),
      });
      continue;
    }
    if (role === "toolResult") {
      result.push({
        role: "toolResult",
        at: typeof message["timestamp"] === "number" ? message["timestamp"] : Date.now(),
        toolCallId: String(message["toolCallId"] ?? ""),
        toolName: String(message["toolName"] ?? ""),
        text: contentText(message["content"]),
        isError: message["isError"] === true,
        durationMs: typeof message["durationMs"] === "number" ? message["durationMs"] : undefined,
      });
    }
  }
  return result;
}

function assistantParts(content: unknown): EngineAssistantPart[] {
  if (!Array.isArray(content)) return [];
  const parts: EngineAssistantPart[] = [];
  for (const item of content) {
    if (typeof item !== "object" || item === null) continue;
    const block = item as Record<string, unknown>;
    if (block["type"] === "text" && typeof block["text"] === "string") {
      parts.push({ type: "text", text: block["text"] });
    } else if (block["type"] === "thinking" && typeof block["thinking"] === "string") {
      parts.push({ type: "thinking", text: block["thinking"] });
    } else if (block["type"] === "toolCall") {
      parts.push({
        type: "toolCall",
        id: String(block["id"] ?? ""),
        name: String(block["name"] ?? ""),
        arguments: block["arguments"] ?? {},
      });
    }
  }
  return parts;
}

function contentText(content: unknown): string {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  const parts: string[] = [];
  for (const item of content) {
    if (typeof item === "string") {
      parts.push(item);
    } else if (
      typeof item === "object" &&
      item !== null &&
      (item as { type?: unknown }).type === "text" &&
      typeof (item as { text?: unknown }).text === "string"
    ) {
      parts.push((item as { text: string }).text);
    }
  }
  return parts.join("\n");
}

function num(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}
