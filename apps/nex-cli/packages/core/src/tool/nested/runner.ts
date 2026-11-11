// ============================================================
// NestedToolRunner：脚本调用工具的唯一入口
// ============================================================
// 所有子调用都经统一 executor（权限、校验、abort、预算、审计），绝不直连 MCP adapter。
// 本类只加四件事：callable-scope 过滤、防自递归、次数/并发上限、abort 传播。

import { modelMessageContentToText } from "@nex/contracts";
import type { ToolExecutor } from "../executor/types.js";
import type { ToolRegistry } from "../registry.js";
import type { ToolExecutionResult } from "../types.js";
import {
  DEFAULT_NESTED_TOOL_LIMITS,
  type NestedToolAuditRecord,
  type NestedToolCallResult,
  type NestedToolRunnerLimits,
} from "./types.js";

export interface NestedToolRunnerOptions {
  executor: Pick<ToolExecutor, "execute">;
  registry: Pick<ToolRegistry, "get">;
  parentToolCallId: string;
  /** 被拒绝嵌套调用的工具名（至少含 codemode 自己，防自递归）。 */
  forbiddenToolNames: ReadonlySet<string>;
  signal?: AbortSignal;
  limits?: Partial<NestedToolRunnerLimits>;
}

export class NestedToolRunner {
  private readonly limits: NestedToolRunnerLimits;
  private readonly audit: NestedToolAuditRecord[] = [];
  private started = 0;
  private inFlight = 0;
  private readonly waiters: Array<() => void> = [];

  constructor(private readonly options: NestedToolRunnerOptions) {
    this.limits = { ...DEFAULT_NESTED_TOOL_LIMITS, ...options.limits };
  }

  /** 审计通道：本父调用发起的全部子调用，按发起顺序。 */
  auditRecords(): readonly NestedToolAuditRecord[] {
    return this.audit;
  }

  async call(toolName: string, input: unknown): Promise<NestedToolCallResult> {
    const callId = `${this.options.parentToolCallId}:${this.started + 1}`;
    const rejection = this.checkAdmission(toolName);
    if (rejection !== undefined) return this.rejected(callId, toolName, input, rejection);
    this.started += 1;

    await this.acquireSlot();
    const startedAt = Date.now();
    try {
      if (this.options.signal?.aborted) {
        return this.rejected(callId, toolName, input, "Nested call cancelled before start");
      }
      const result = await this.options.executor.execute(
        { id: callId, name: toolName, input },
        {
          parentToolCallId: this.options.parentToolCallId,
          ...(this.options.signal === undefined ? {} : { signal: this.options.signal }),
        },
      );
      return this.project(callId, toolName, input, result, Date.now() - startedAt);
    } finally {
      this.releaseSlot();
    }
  }

  private checkAdmission(toolName: string): string | undefined {
    if (this.options.forbiddenToolNames.has(toolName)) {
      return `Tool ${toolName} cannot be called from a script`;
    }
    // hidden 工具在 registry 里等同不存在。
    if (this.options.registry.get(toolName) === undefined) {
      return `Tool not found: ${toolName}`;
    }
    if (this.started >= this.limits.maxCalls) {
      return `Script exceeded the limit of ${this.limits.maxCalls} tool calls`;
    }
    return undefined;
  }

  private project(
    callId: string,
    toolName: string,
    input: unknown,
    result: ToolExecutionResult,
    durationMs: number,
  ): NestedToolCallResult {
    // 顺序不变量：先取 result.output（handler 原值，未经预算截断），再派生模型文本。
    const rawOutput = result.output;
    const modelText =
      result.modelContent === undefined ? "" : modelMessageContentToText(result.modelContent);
    const oversize = result.success ? this.oversizeReason(rawOutput) : undefined;
    const success = result.success && oversize === undefined;
    const errorMessage = oversize ?? (result.success ? undefined : result.error?.message);
    const audit: NestedToolAuditRecord = {
      callId,
      parentToolCallId: this.options.parentToolCallId,
      toolName,
      input,
      success,
      durationMs,
      ...(result.success ? { output: rawOutput } : {}),
      ...(errorMessage === undefined ? {} : { errorMessage }),
      modelContentChars: modelText.length,
    };
    this.audit.push(audit);
    return {
      success,
      ...(success ? { data: rawOutput } : {}),
      modelText,
      ...(errorMessage === undefined ? {} : { errorMessage }),
      audit,
    };
  }

  private oversizeReason(output: unknown): string | undefined {
    const bytes = Buffer.byteLength(JSON.stringify(output) ?? "", "utf8");
    return bytes > this.limits.maxResultBytes
      ? `Tool result is ${bytes} bytes, over the ${this.limits.maxResultBytes} byte script limit; narrow the call`
      : undefined;
  }

  private rejected(
    callId: string,
    toolName: string,
    input: unknown,
    errorMessage: string,
  ): NestedToolCallResult {
    const audit: NestedToolAuditRecord = {
      callId,
      parentToolCallId: this.options.parentToolCallId,
      toolName,
      input,
      success: false,
      durationMs: 0,
      errorMessage,
      modelContentChars: 0,
    };
    this.audit.push(audit);
    return { success: false, modelText: errorMessage, errorMessage, audit };
  }

  private async acquireSlot(): Promise<void> {
    if (this.inFlight < this.limits.maxConcurrency) {
      this.inFlight += 1;
      return;
    }
    await new Promise<void>((resolve) => this.waiters.push(resolve));
  }

  private releaseSlot(): void {
    const next = this.waiters.shift();
    if (next) next();
    else this.inFlight -= 1;
  }
}
