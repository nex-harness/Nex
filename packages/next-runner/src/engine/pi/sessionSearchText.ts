import { SessionManager } from "@earendil-works/pi-coding-agent";

/**
 * 每个会话参与搜索的正文上限（字符）。pi 的 `allMessagesText` 是整段会话的拼接文本，
 * 长任务可以到几 MB；索引只保留前 100k 字符，既覆盖绝大多数会话，也把每个 workspace
 * 的内存占用限制在可预期范围。
 */
export const SESSION_SEARCH_TEXT_MAX_CHARS = 100_000;

/**
 * 读取一个 workspace 的会话正文（`SessionManager.list` 的 `allMessagesText`）。
 *
 * 只读、不做产品过滤：deleted/draft 由调用方按行规则剔除。会话文件读失败时返回空表，
 * 搜索退化为“仅标题匹配”，而不是让整个搜索请求失败。
 */
export function createSessionSearchTextReader(deps: {
  init: () => Promise<void>;
  sessionDir: (workspacePath: string) => string;
}) {
  return async (workspacePath: string): Promise<Map<string, string>> => {
    await deps.init();
    let infos: Awaited<ReturnType<typeof SessionManager.list>>;
    try {
      // 显式传 sessionDir：runner 的会话不在 pi 默认的 ~/.pi/agent 目录下。
      infos = await SessionManager.list(workspacePath, deps.sessionDir(workspacePath));
    } catch {
      return new Map();
    }
    const result = new Map<string, string>();
    for (const info of infos) {
      result.set(info.id, info.allMessagesText.slice(0, SESSION_SEARCH_TEXT_MAX_CHARS));
    }
    return result;
  };
}
