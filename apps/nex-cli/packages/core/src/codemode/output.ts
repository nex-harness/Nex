// ============================================================
// Codemode 输出预算：return/text/console 拼成文本，超预算落盘
// ============================================================

import { CODEMODE_DEFAULT_MAX_OUTPUT_TOKENS } from "@nex/contracts";
import { CODEMODE_CHARS_PER_TOKEN } from "./description.js";
import type { CodemodeOutputItem, CodemodeResult } from "./types.js";

export interface RenderedCodemodeOutput {
  /** 完整文本（未截断）。 */
  fullText: string;
  /** 进入模型的文本（已按预算截断）。 */
  text: string;
  truncated: boolean;
  images: { mimeType: string; data: string }[];
}

function formatValue(value: unknown): string {
  if (value === undefined) return "";
  if (typeof value === "string") return value;
  return JSON.stringify(value, null, 2) ?? "";
}

export function renderCodemodeOutput(
  result: CodemodeResult,
  maxOutputTokens: number = CODEMODE_DEFAULT_MAX_OUTPUT_TOKENS,
): RenderedCodemodeOutput {
  const parts: string[] = [];
  const images: RenderedCodemodeOutput["images"] = [];
  for (const item of result.output as CodemodeOutputItem[]) {
    if (item.type === "text") parts.push(item.text);
    else images.push({ mimeType: item.mimeType, data: item.data });
  }
  if (result.ok) {
    const value = formatValue(result.value);
    if (value !== "") parts.push(value);
  }
  const fullText = parts.join("\n");
  const maxChars = maxOutputTokens * CODEMODE_CHARS_PER_TOKEN;
  if (fullText.length <= maxChars) {
    return { fullText, text: fullText, truncated: false, images };
  }
  return { fullText, text: fullText.slice(0, maxChars), truncated: true, images };
}
