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
    const message = toEngineMessage(raw);
    if (message) result.push(message);
  }
  return result;
}

/**
 * Rebuild history from pi's raw branch (`SessionManager.getBranch()`), not
 * `session.messages`.
 *
 * 修复依据（research §0-b）：`session.messages` 是喂给模型的投影上下文——压缩后旧消息被
 * summary 取代、`branch()` 后废弃分支消失——用它重建会丢行、也看不到压缩边界。
 * `getBranch()` 返回当前 leaf 的完整路径（含 message/compaction/branch_summary），
 * 每条 message 都带稳定的 `entryId`，用它派生行 `entityId`（`pi:<entryId>`），
 * 与 live 投影从 `message_end` 学到的 id 一致。
 */
export function toEngineBranchHistory(
  entries: readonly unknown[],
  /**
   * Manual operations that ended in a compaction entry. pi's file records no command id,
   * so restore matches the entry to the finished operation through the runner metadata's
   * `lastCompact` (see `SessionMetaStore` and `piEngine.history`).
   */
  lastManualCompact?: { entryId: string; sourceCommandId: string },
): EngineMessage[] {
  const result: EngineMessage[] = [];
  for (const raw of entries) {
    if (typeof raw !== "object" || raw === null) continue;
    const entry = raw as Record<string, unknown>;
    const entryId = typeof entry["id"] === "string" ? entry["id"] : undefined;
    const at = entryTimestamp(entry);
    const type = entry["type"];
    if (type === "message") {
      const message = toEngineMessage(entry["message"], entryId);
      if (message) result.push(message);
      continue;
    }
    if (type === "compaction") {
      // pi 不在 compaction entry 上记录 manual/auto；`fromHook` 只表示由扩展生成。
      // runner 的 manual compact 在完成后把新 entry id + 命令写进 metadata（
      // `lastCompact`），restore 用它把同一条 entry 还原成 manual；否则按 auto。
      const manual = lastManualCompact !== undefined && lastManualCompact.entryId === entryId;
      result.push({
        role: "compaction",
        at,
        entryId: entryId ?? "",
        summary: typeof entry["summary"] === "string" ? entry["summary"] : "",
        tokensBefore: num(entry["tokensBefore"]),
        firstKeptEntryId:
          typeof entry["firstKeptEntryId"] === "string" ? entry["firstKeptEntryId"] : "",
        origin: manual || entry["fromHook"] === true ? "manual" : "auto",
        ...(manual ? { sourceCommandId: lastManualCompact.sourceCommandId } : {}),
      });
      continue;
    }
    if (type === "custom") {
      // File-feature entries (spec B): restore projects them onto rows after the walk.
      const fileRecord = fileRecordFromCustom(entry, entryId, at);
      if (fileRecord) {
        result.push(fileRecord);
        continue;
      }
      // Runner-written entries: `nex.fork` becomes the child's fork notice (decision 6).
      const fork = forkMarkerFromCustom(entry, entryId, at);
      if (fork) result.push(fork);
      continue;
    }
    if (type === "branch_summary") {
      // 旧 CLI 的 branch summary 不产生 v4 行；restore 跳过以保持行集合一致。
      result.push({
        role: "branchSummary",
        at,
        entryId: entryId ?? "",
        summary: typeof entry["summary"] === "string" ? entry["summary"] : "",
      });
    }
  }
  return result;
}

/**
 * `nex.checkpoints` / `nex.rewind` custom entries → restore facts (spec B). The payload is
 * validated loosely here; `piFileFactsOps` re-validates it before use.
 */
function fileRecordFromCustom(
  entry: Record<string, unknown>,
  entryId: string | undefined,
  at: number,
): Extract<EngineMessage, { role: "checkpoints" | "rewind" }> | undefined {
  const customType = entry["customType"];
  const data = entry["data"];
  if (!entryId || typeof data !== "object" || data === null) return undefined;
  const record = data as Record<string, unknown>;
  const userEntryId = record["userEntryId"];
  if (typeof userEntryId !== "string") return undefined;
  if (customType === "nex.checkpoints") {
    return {
      role: "checkpoints",
      at,
      entryId,
      userEntryId,
      turnId: typeof record["turnId"] === "string" ? record["turnId"] : "",
      data: record as never,
    };
  }
  if (customType === "nex.rewind") {
    return { role: "rewind", at, entryId, userEntryId, data: record as never };
  }
  return undefined;
}

/** `nex.fork` custom entry data → a restore-visible fork marker. */
function forkMarkerFromCustom(
  entry: Record<string, unknown>,
  entryId: string | undefined,
  at: number,
): Extract<EngineMessage, { role: "runnerMarker" }> | undefined {
  if (!entryId || entry["customType"] !== FORK_CUSTOM_TYPE) return undefined;
  const data = entry["data"];
  if (typeof data !== "object" || data === null) return undefined;
  const parentSessionId = (data as Record<string, unknown>)["parentSessionId"];
  const parentRowId = (data as Record<string, unknown>)["parentRowId"];
  if (typeof parentSessionId !== "string" || typeof parentRowId !== "number") return undefined;
  return {
    role: "runnerMarker",
    kind: "forkNotice",
    at,
    entryId,
    parentSessionId,
    parentRowId,
  };
}

/** pi custom-entity type the fork command writes (`SessionManager.appendCustomEntry`). */
export const FORK_CUSTOM_TYPE = "nex.fork";

function entryTimestamp(entry: Record<string, unknown>): number {
  const raw = entry["timestamp"];
  if (typeof raw === "number" && Number.isFinite(raw)) return raw;
  if (typeof raw === "string") {
    const parsed = Date.parse(raw);
    if (Number.isFinite(parsed)) return parsed;
  }
  return Date.now();
}

/** One pi message → one engine message; shared by the branch and legacy paths. */
function toEngineMessage(raw: unknown, entryId?: string): EngineMessage | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const message = raw as Record<string, unknown>;
  const role = message["role"];
  const id = entryId ? { entryId } : {};
  if (role === "user") {
    return {
      role: "user",
      at: typeof message["timestamp"] === "number" ? message["timestamp"] : Date.now(),
      text: contentText(message["content"]),
      ...id,
    };
  }
  if (role === "assistant") {
    return {
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
      ...id,
    };
  }
  if (role === "toolResult") {
    return {
      role: "toolResult",
      at: typeof message["timestamp"] === "number" ? message["timestamp"] : Date.now(),
      toolCallId: String(message["toolCallId"] ?? ""),
      toolName: String(message["toolName"] ?? ""),
      text: contentText(message["content"]),
      isError: message["isError"] === true,
      durationMs: typeof message["durationMs"] === "number" ? message["durationMs"] : undefined,
      ...id,
    };
  }
  return undefined;
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
