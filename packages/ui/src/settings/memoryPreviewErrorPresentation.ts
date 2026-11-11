import { PROJECT_MEMORY_PREVIEW_LIMIT_EXCEEDED_ERROR_CODE } from "@nex/services";

export interface MemoryPreviewErrorPresentation {
  /** 超限错误没有可重试的服务端解法，UI 只保留关闭出口；其余错误允许重试。 */
  oversized: boolean;
  /** 直接展示的服务端错误文本（仅非超限错误使用）。 */
  rawMessage?: string;
  /** oversized 时的本地化文案 key。 */
  messageKey?: "settings.memory.preview.tooLarge";
}

/**
 * 归一 Memory 预览读取失败的 UI 表现。
 *
 * 5 MiB 超限由服务端按 `code` 标记（`PROJECT_MEMORY_PREVIEW_LIMIT_EXCEEDED`）；
 * 按 code 而不是 message 匹配，服务端文案变化不会破坏 UI 分支。
 */
export function resolveMemoryPreviewErrorPresentation(
  error: unknown,
): MemoryPreviewErrorPresentation {
  const isOversized =
    typeof error === "object" &&
    error !== null &&
    "code" in error &&
    (error as { code?: unknown }).code === PROJECT_MEMORY_PREVIEW_LIMIT_EXCEEDED_ERROR_CODE;
  if (isOversized) {
    return { oversized: true, messageKey: "settings.memory.preview.tooLarge" };
  }
  return {
    oversized: false,
    rawMessage: error instanceof Error ? error.message : String(error),
  };
}
