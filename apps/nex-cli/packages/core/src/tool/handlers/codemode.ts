// ============================================================
// Codemode Tool Handler
// ============================================================
// 一次执行 = 一个沙箱 worker。脚本里的 tools.<name>(args) 经 NestedToolRunner 回到统一
// executor；`searchTools`/`describeTool` 只读 registry 目录，不改变模型侧 declared set。

import {
  CODEMODE_DEFAULT_MAX_OUTPUT_TOKENS,
  CODEMODE_TOOL_NAME,
  CodemodeInputJsonSchema,
  CodemodeInputSchema,
  CodemodeOutputJsonSchema,
  CodemodeOutputSchema,
  type CodemodeOutput,
  type ModelMessageContent,
} from "@nex/contracts";
import { CodemodeSandbox } from "../../codemode/runtime/host.js";
import { toCodemodeIdentifier } from "../../codemode/identifier.js";
import { parseCodemodeSource, CodemodeSourceError } from "../../codemode/source.js";
import { buildCodemodeDescription, oneLine } from "../../codemode/description.js";
import { renderCodemodeOutput } from "../../codemode/output.js";
import {
  resolveCodemodeWorkerSource,
  resolveCodemodeWorkerUrl,
} from "../../codemode/worker-url.js";
import type { CodemodeTool } from "../../codemode/types.js";
import { NESTED_FORBIDDEN_TOOL_NAMES } from "../nested/types.js";
import { searchToolDocuments } from "../tool-search-index.js";
import type { ToolEntry, ToolHandler, ToolHandlerFailure } from "../types.js";

const CODEMODE_DEFAULT_TIMEOUT_MS = 120_000;
const CODEMODE_MAX_TIMEOUT_MS = 600_000;
const CODEMODE_MODEL_BYTES = 80_000;
const CODEMODE_SEARCH_DEFAULT_LIMIT = 8;
const CODEMODE_ERROR_CODE = { SOURCE: 41, UNAVAILABLE: 42, SCRIPT: 43 } as const;

function failure(code: number, message: string): ToolHandlerFailure {
  return { result: false, errorCode: code, message };
}

const codemodeHandler: ToolHandler = async (input, context) => {
  const { code: rawCode } = CodemodeInputSchema.parse(input);
  const nested = context.nestedTools;
  if (nested === undefined) {
    return failure(
      CODEMODE_ERROR_CODE.UNAVAILABLE,
      "codemode_unavailable: this session has no nested tool runner.",
    );
  }
  let parsed;
  try {
    parsed = parseCodemodeSource(rawCode);
  } catch (error) {
    if (error instanceof CodemodeSourceError)
      return failure(CODEMODE_ERROR_CODE.SOURCE, error.message);
    throw error;
  }

  const runner = nested.createRunner({
    parentToolCallId: context.toolCallId,
    signal: context.abortSignal,
  });
  const catalog = nested.catalog().filter((entry) => !NESTED_FORBIDDEN_TOOL_NAMES.has(entry.name));

  const tools: CodemodeTool[] = catalog.map((entry) => ({
    name: entry.name,
    description: oneLine(entry.description),
    execute: async (args) => {
      const result = await runner.call(entry.name, args ?? {});
      // 脚本拿未截断原值；失败以异常形式交给脚本，让它决定是否继续。
      if (!result.success) throw new Error(result.errorMessage ?? "Tool call failed");
      return result.data;
    },
  }));
  const globals: CodemodeTool[] = [
    {
      name: "searchTools",
      spread: true,
      execute: (args) => {
        const [query, options] = args as [string, { limit?: number; namespace?: string }?];
        return searchToolDocuments(catalog, {
          query: String(query ?? ""),
          limit: options?.limit ?? CODEMODE_SEARCH_DEFAULT_LIMIT,
          ...(options?.namespace === undefined ? {} : { namespace: options.namespace }),
        }).map((hit) => ({
          name: toCodemodeIdentifier(hit.name),
          description: oneLine(hit.description),
        }));
      },
    },
    {
      name: "describeTool",
      execute: (args) => {
        const wanted = String(args);
        const found = catalog.find(
          (entry) => entry.name === wanted || toCodemodeIdentifier(entry.name) === wanted,
        );
        if (found === undefined)
          throw new Error(`Unknown tool "${wanted}". Use searchTools(query) to find tools.`);
        return { name: found.name, description: found.description, inputSchema: found.inputSchema };
      },
    },
  ];

  const sandbox = new CodemodeSandbox({
    tools,
    globals,
    workerUrl: resolveCodemodeWorkerUrl(),
    workerSource: resolveCodemodeWorkerSource(),
    timeoutMs: CODEMODE_DEFAULT_TIMEOUT_MS,
  });
  try {
    const timeoutMs = Math.min(
      parsed.options.timeoutMs ?? CODEMODE_DEFAULT_TIMEOUT_MS,
      CODEMODE_MAX_TIMEOUT_MS,
    );
    const result = await sandbox.execute(parsed.code, { signal: context.abortSignal, timeoutMs });
    const rendered = renderCodemodeOutput(
      result,
      parsed.options.maxOutputTokens ?? CODEMODE_DEFAULT_MAX_OUTPUT_TOKENS,
    );
    const calls = result.calls.map((call) => ({
      name: call.name,
      status: call.status,
      durationMs: Math.round(call.durationMs),
    }));
    if (!result.ok) {
      // 超时/取消/脚本异常都以错误结果返回，不给模型半截成功。
      const trace = calls.map((call) => `${call.name}:${call.status}`).join(", ");
      throw Object.assign(
        new Error(
          `${result.error.kind}: ${result.error.stack ?? result.error.message}${trace === "" ? "" : `\nCalls: ${trace}`}${rendered.text === "" ? "" : `\nOutput before failure:\n${rendered.text}`}`,
        ),
        { codemodeErrorCode: CODEMODE_ERROR_CODE.SCRIPT },
      );
    }
    let fullOutputPath: string | undefined;
    if (rendered.truncated && context.artifactStore !== undefined) {
      const artifact = await context.artifactStore.writeToolResultArtifact(
        {
          sessionId: context.sessionId as never,
          toolCallId: context.toolCallId,
          toolName: CODEMODE_TOOL_NAME,
          content: rendered.fullText,
          contentType: "text/plain",
        },
        { signal: context.abortSignal },
      );
      fullOutputPath = artifact.path ?? artifact.uri;
    }
    return {
      text: rendered.text,
      images: rendered.images,
      calls,
      truncated: rendered.truncated,
      ...(fullOutputPath === undefined ? {} : { fullOutputPath }),
    } satisfies CodemodeOutput;
  } finally {
    await sandbox.close();
  }
};

function formatCodemodeModelContent(output: unknown): ModelMessageContent {
  const parsed = CodemodeOutputSchema.safeParse(output);
  if (!parsed.success) return "Codemode returned an invalid result.";
  const { text, images, truncated, fullOutputPath } = parsed.data;
  const lines: string[] = [];
  if (text !== "") lines.push(text);
  if (truncated) {
    lines.push(
      fullOutputPath === undefined
        ? "[output truncated to the token budget]"
        : `[output truncated to the token budget; full output saved to ${fullOutputPath}]`,
    );
  }
  if (lines.length === 0) lines.push("(no output)");
  const textBlock = lines.join("\n");
  if (images.length === 0) return textBlock;
  return [
    { type: "text", text: textBlock },
    ...images.map((image) => ({
      type: "image" as const,
      mediaType: image.mimeType,
      dataUrl: `data:${image.mimeType};base64,${image.data}`,
    })),
  ];
}

export function createCodemodeToolEntry(description: string): ToolEntry {
  return {
    capability: "Run sandboxed JavaScript that calls other tools without flooding context",
    metadata: {
      name: CODEMODE_TOOL_NAME,
      description,
      // 嵌套调用各自走权限；脚本本身不读写宿主，故自身是低风险、不二次确认。
      readOnly: false,
      destructive: false,
      concurrentSafe: false,
      timeoutMs: CODEMODE_MAX_TIMEOUT_MS,
      maxOutputBytes: CODEMODE_MODEL_BYTES,
      sideEffectScope: "none",
      riskLevel: "low",
      needsApproval: false,
    },
    handler: codemodeHandler,
    inputSchema: CodemodeInputJsonSchema,
    outputSchema: CodemodeOutputJsonSchema,
    runtimeInputSchema: CodemodeInputSchema,
    runtimeOutputSchema: CodemodeOutputSchema,
    formatModelContent: formatCodemodeModelContent,
    permission: {
      permission: "codemode",
      reason:
        "Codemode runs an isolated script; every tool it calls is permission-checked on its own",
      riskLevel: "low",
      sideEffectScope: "none",
      needsApproval: false,
      patternSources: ["toolName"],
      alwaysAllowPatternSources: ["toolName"],
      denyPriority: "beforeAsk",
    },
    resultBudget: {
      maxInlineBytes: CODEMODE_MODEL_BYTES,
      maxModelBytes: CODEMODE_MODEL_BYTES,
      strategy: "truncate",
      preview: { maxBytes: CODEMODE_MODEL_BYTES, direction: "head" },
    },
    timeout: {
      kind: "timed",
      defaultMs: CODEMODE_DEFAULT_TIMEOUT_MS,
      maxMs: CODEMODE_MAX_TIMEOUT_MS,
      allowCallOverride: false,
    },
    cancellation: {
      supported: true,
      cleanup: "bestEffort",
      userVisibleMessage: "Codemode script was cancelled and its in-flight tool calls were aborted",
    },
    trace: {
      required: true,
      propagateToAdapters: false,
      recordInput: "summary",
      recordOutput: "summary",
    },
  };
}

export const codemodeToolEntry: ToolEntry = createCodemodeToolEntry(
  buildCodemodeDescription({ listedCandidates: [] }),
);
