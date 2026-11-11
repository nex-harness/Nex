// ============================================================
// ToolSearch 的 BM25 检索
// ============================================================
// 文档 = 工具的 name + description；纯内存、无向量库。工具名是关键词密集的短文本，
// BM25 足够且可解释。

export interface ToolSearchDocument {
  name: string;
  description: string;
  /** MCP server 名片段；非 MCP 工具缺席。 */
  namespace?: string;
}

export interface ToolSearchQuery {
  query: string;
  limit: number;
  namespace?: string;
}

const BM25_K1 = 1.5;
const BM25_B = 0.75;
/** 工具名里的词权重：搜 `read file` 时名字命中应压过描述里的偶然出现。 */
const NAME_TOKEN_WEIGHT = 3;
const MCP_NAME_SEPARATOR = "__";

export function tokenizeToolText(text: string): string[] {
  return text
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .toLowerCase()
    .split(/[^a-z0-9]+/)
    .filter((token) => token.length > 0);
}

/** `mcp__server__tool` → `server`；其余工具无 namespace。 */
export function mcpNamespaceOf(toolName: string): string | undefined {
  if (!toolName.startsWith(`mcp${MCP_NAME_SEPARATOR}`)) return undefined;
  const segments = toolName.split(MCP_NAME_SEPARATOR);
  return segments.length >= 3 ? segments[1] : undefined;
}

function termFrequencies(document: ToolSearchDocument): Map<string, number> {
  const frequencies = new Map<string, number>();
  for (const token of tokenizeToolText(document.name)) {
    frequencies.set(token, (frequencies.get(token) ?? 0) + NAME_TOKEN_WEIGHT);
  }
  for (const token of tokenizeToolText(document.description)) {
    frequencies.set(token, (frequencies.get(token) ?? 0) + 1);
  }
  return frequencies;
}

export function searchToolDocuments(
  documents: readonly ToolSearchDocument[],
  request: ToolSearchQuery,
): ToolSearchDocument[] {
  const namespace = request.namespace?.toLowerCase();
  const candidates = documents.filter(
    (document) => namespace === undefined || document.namespace?.toLowerCase() === namespace,
  );
  const queryTokens = [...new Set(tokenizeToolText(request.query))];
  if (candidates.length === 0 || queryTokens.length === 0) return [];

  const profiles = candidates.map((document) => {
    const frequencies = termFrequencies(document);
    let length = 0;
    for (const count of frequencies.values()) length += count;
    return { document, frequencies, length };
  });
  const averageLength = profiles.reduce((sum, profile) => sum + profile.length, 0) / profiles.length;

  const scored = profiles.map((profile) => {
    let score = 0;
    for (const token of queryTokens) {
      const termCount = profile.frequencies.get(token) ?? 0;
      if (termCount === 0) continue;
      const containing = profiles.filter((other) => other.frequencies.has(token)).length;
      const idf = Math.log(1 + (profiles.length - containing + 0.5) / (containing + 0.5));
      const normalizer = 1 - BM25_B + BM25_B * (profile.length / (averageLength || 1));
      score += idf * ((termCount * (BM25_K1 + 1)) / (termCount + BM25_K1 * normalizer));
    }
    return { document: profile.document, score };
  });

  return scored
    .filter((item) => item.score > 0)
    .sort((a, b) => b.score - a.score || a.document.name.localeCompare(b.document.name))
    .slice(0, request.limit)
    .map((item) => item.document);
}
