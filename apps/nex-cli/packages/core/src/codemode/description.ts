// ============================================================
// Codemode tool description：声明预算 + cache 稳定
// ============================================================
// 描述只随 exposure 变化，不随 MCP 连断变化（否则每次 MCP 抖动都会让 prompt 前缀 cache 失效）。
// 所以这里列的是 direct 的工具；deferred 工具永远不列，靠脚本内 searchTools/ALL_TOOLS 发现。
// 列不下的工具（超出 inline 预算）与 deferred 同待遇。

import type { ToolCatalogEntry } from "../tool/registry.js";

/** 估算：约 4 字符一个 token。 */
export const CODEMODE_CHARS_PER_TOKEN = 4;
/** 描述里列出工具声明的 token 预算。 */
export const CODEMODE_INLINE_BUDGET_TOKENS = 3000;
const LISTED_DESCRIPTION_MAX_CHARS = 120;

export const CODEMODE_DESCRIPTION_INTRO = [
  "Runs JavaScript in an isolated sandbox whose only capability is calling tools.",
  "",
  "- The code is an async function body: `return` and top-level `await` work.",
  "- Call tools as `tools.<name>(args)`; they are async, so use `Promise.all` to run independent calls in parallel.",
  "- Only what the script returns, prints with `text(...)`, or shows with `image(...)` reaches you. Intermediate tool results stay in the sandbox, so filter and summarize inside the script.",
  "- There is no filesystem, network, `process`, or `require` in the sandbox.",
  '- Optional first line: `// @options: {"max_output_tokens": 2000, "timeout_ms": 30000}`.',
].join("\n");

export const CODEMODE_GLOBALS_LINES = [
  "Globals:",
  "- `tools`: callable tools. `ALL_TOOLS` lists every tool name and description.",
  "- `await searchTools(query, {limit, namespace})`: find tools by topic, including ones not listed below. Returns `[{name, description}]`.",
  "- `await describeTool(name)`: full input schema of one tool.",
  "- `text(value)`, `image(urlOrBlock)`, `exit()`; `store(key, value)` / `load(key)` keep small values between calls of one script.",
].join("\n");

export interface CodemodeDescriptionInput {
  /** 仅 direct 工具；deferred 与 hidden 由调用方在传入前剔除。 */
  listedCandidates: readonly ToolCatalogEntry[];
  inlineBudgetTokens?: number;
}

/**
 * 把工具描述压成一行摘要。完整描述可达数 KB（例如 EnterPlanMode），而 searchTools/ALL_TOOLS 的
 * 结果常被脚本整体 text() 出来——不压缩就会把省下的上下文又吐回去。完整文本走 describeTool。
 */
export function oneLine(text: string): string {
  const collapsed = text.replace(/\s+/g, " ").trim();
  return collapsed.length > LISTED_DESCRIPTION_MAX_CHARS
    ? `${collapsed.slice(0, LISTED_DESCRIPTION_MAX_CHARS - 1)}…`
    : collapsed;
}

function toolLine(entry: ToolCatalogEntry): string {
  return `- tools.${entry.name}(args): ${oneLine(entry.description)}`;
}

export function buildCodemodeDescription(input: CodemodeDescriptionInput): string {
  const budgetChars =
    (input.inlineBudgetTokens ?? CODEMODE_INLINE_BUDGET_TOKENS) * CODEMODE_CHARS_PER_TOKEN;
  const sorted = [...input.listedCandidates].sort((a, b) => a.name.localeCompare(b.name));
  const listed: string[] = [];
  let used = 0;
  let omitted = 0;
  for (const entry of sorted) {
    const line = toolLine(entry);
    if (used + line.length + 1 > budgetChars) {
      omitted += 1;
      continue;
    }
    used += line.length + 1;
    listed.push(line);
  }
  const sections = [CODEMODE_DESCRIPTION_INTRO, "", CODEMODE_GLOBALS_LINES];
  if (listed.length > 0) {
    sections.push("", "Listed tools:", ...listed);
  }
  sections.push(
    "",
    omitted > 0
      ? `${omitted} more tools are not listed here; find them with \`searchTools(query)\`.`
      : "Tools that are not listed here can be found with `searchTools(query)`.",
  );
  return sections.join("\n");
}
