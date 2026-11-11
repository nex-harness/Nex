// ============================================================
// Codemode Tool - 沙箱里写 JS 编排其它工具
// ============================================================
// 只有脚本显式输出（return / text() / image()）进入模型上下文；脚本里的嵌套工具调用及其
// 中间结果不进上下文（见 docs/specs/codemode.md 的 P2/P3）。

import { z } from "zod";
import { toToolJsonSchema } from "./json-schema.js";

export const CODEMODE_TOOL_NAME = "Codemode";

/** 脚本输出的默认 token 预算（≈4 字符/token）。可被脚本首行 `// @options` 下调或上调。 */
export const CODEMODE_DEFAULT_MAX_OUTPUT_TOKENS = 10_000;

export const CodemodeInputSchema = z
  .object({
    code: z
      .string()
      .min(1)
      .describe(
        "JavaScript run as an async function body (return and top-level await work). Optional first line: // @options: {\"max_output_tokens\": 2000, \"timeout_ms\": 30000}",
      ),
  })
  .strict();

export type CodemodeInput = z.infer<typeof CodemodeInputSchema>;

export const CodemodeInputJsonSchema = toToolJsonSchema(CodemodeInputSchema);

export const CodemodeCallRecordSchema = z.object({
  name: z.string(),
  status: z.enum(["ok", "error", "cancelled"]),
  durationMs: z.number(),
});

export const CodemodeOutputSchema = z.object({
  /** 脚本的 text()/console 输出与 return 值拼成的文本（可能已按预算截断）。 */
  text: z.string(),
  images: z.array(z.object({ mimeType: z.string(), data: z.string() })),
  calls: z.array(CodemodeCallRecordSchema),
  truncated: z.boolean(),
  /** 输出超预算时，完整文本落盘的位置。 */
  fullOutputPath: z.string().optional(),
});

export type CodemodeOutput = z.infer<typeof CodemodeOutputSchema>;

export const CodemodeOutputJsonSchema = toToolJsonSchema(CodemodeOutputSchema);
