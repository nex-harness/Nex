/**
 * `nex-task.searchTasks` 的摘要片段构造（从 `@nex/services` 的 `taskIndexRepo.ts` 移植，
 * 见 docs/next/automations-views-search.md §C）。runner 不依赖 services，所以这里保留
 * 同一组常量与窗口算法：以命中点为中心截窗、相邻窗口去重、最多 4 条。
 *
 * 与原实现的唯一差别：不提供“仅标题命中”时的整段兜底摘要 —— 命令中心对仅标题命中
 * 的行不展示摘要，契约要求这类匹配不带 snippet 字段。
 */

export const SEARCH_SNIPPET_PREFIX_RADIUS = 20;
export const SEARCH_SNIPPET_SUFFIX_RADIUS = 72;
export const SEARCH_SNIPPET_MAX_CHARS = 140;
export const SEARCH_SNIPPET_LIMIT = 4;

function normalizeSnippetText(text: string): string {
  return text.replace(/\s+/g, " ").trim().slice(0, SEARCH_SNIPPET_MAX_CHARS);
}

/**
 * 返回 `searchableText` 中命中 `search` 的摘要窗口（最多 4 条）。
 *
 * 大小写统一用 `toLowerCase`：索引里缓存的也是它，两边必须一致，否则会出现
 * “索引判定命中、摘要却找不到匹配”的窗口。
 */
export function buildSearchSnippets(searchableText: string, search: string): string[] {
  const needle = search.trim();
  if (!needle || !searchableText.trim()) return [];

  const normalizedSearch = needle.toLowerCase();
  const normalizedText = searchableText.toLowerCase();
  const snippets: string[] = [];
  const snippetRanges: Array<{ start: number; end: number }> = [];
  let searchStart = 0;

  while (snippets.length < SEARCH_SNIPPET_LIMIT && searchStart < normalizedText.length) {
    const matchIndex = normalizedText.indexOf(normalizedSearch, searchStart);
    if (matchIndex < 0) break;

    const start = Math.max(0, matchIndex - SEARCH_SNIPPET_PREFIX_RADIUS);
    const end = Math.min(
      searchableText.length,
      matchIndex + normalizedSearch.length + SEARCH_SNIPPET_SUFFIX_RADIUS,
    );
    const prefix = start > 0 ? "..." : "";
    const suffix = end < searchableText.length ? "..." : "";
    const snippet = normalizeSnippetText(`${prefix}${searchableText.slice(start, end)}${suffix}`);
    // 同一关键词在很近的位置重复出现时窗口高度重叠，只保留第一条。
    const overlapsExistingSnippet = snippetRanges.some(
      (range) => Math.min(range.end, end) - Math.max(range.start, start) > 0,
    );
    if (snippet && !overlapsExistingSnippet) {
      snippets.push(snippet);
      snippetRanges.push({ start, end });
    }
    searchStart = matchIndex + normalizedSearch.length;
  }

  return snippets;
}
