import { CODEMODE_TOOL_NAME, TOOL_SEARCH_NAME } from "@nex/contracts";

// ============================================================
// 嵌套工具调用（codemode P2）的契约
// ============================================================
// 一次工具结果拆成三个通道，且必须先取未截断的原值再派生另外两个：
//   execution —— 给脚本：原始、未被模型预算截断（仍有上限，防止撑爆沙箱）
//   model     —— 给 LLM：预算化文本；只有它能进上下文
//   audit     —— 给存储/UI：完整记录，带 parent id

export interface NestedToolAuditRecord {
  callId: string;
  parentToolCallId: string;
  toolName: string;
  input: unknown;
  success: boolean;
  durationMs: number;
  /** 未截断的原始输出；失败时缺席。 */
  output?: unknown;
  errorMessage?: string;
  /** 模型预算化后的文本长度，供 UI 展示「若进上下文会多大」。 */
  modelContentChars: number;
}

export interface NestedToolCallResult {
  success: boolean;
  /** 执行通道：脚本拿到的原始值。 */
  data?: unknown;
  /** 模型通道：预算化文本。 */
  modelText: string;
  /** 失败原因，脚本据此决定是否继续。 */
  errorMessage?: string;
  audit: NestedToolAuditRecord;
}

export interface NestedToolRunnerLimits {
  /** 单个父调用内的子调用总数上限。 */
  maxCalls: number;
  /** 同一父调用内同时在飞的子调用数。 */
  maxConcurrency: number;
  /** 执行通道里单个原始结果的字节上限。 */
  maxResultBytes: number;
}

/** 脚本不得调用：codemode 自身（防自递归）与 ToolSearch（脚本内有 searchTools）。 */
export const NESTED_FORBIDDEN_TOOL_NAMES: ReadonlySet<string> = new Set([
  CODEMODE_TOOL_NAME,
  TOOL_SEARCH_NAME,
]);

export const DEFAULT_NESTED_TOOL_LIMITS: NestedToolRunnerLimits = {
  maxCalls: 200,
  maxConcurrency: 8,
  maxResultBytes: 5 * 1024 * 1024,
};

/** executor 交给 handler 的嵌套调用入口；handler 不接触 executor 或 registry 本身。 */
export interface NestedToolsPort {
  createRunner(input: { parentToolCallId: string; signal: AbortSignal }): NestedToolRunnerLike;
  catalog(): readonly NestedToolCatalogEntry[];
}

export interface NestedToolCatalogEntry {
  name: string;
  description: string;
  namespace?: string;
  inputSchema: Record<string, unknown>;
}

export interface NestedToolRunnerLike {
  call(toolName: string, input: unknown): Promise<NestedToolCallResult>;
  auditRecords(): readonly NestedToolAuditRecord[];
}
