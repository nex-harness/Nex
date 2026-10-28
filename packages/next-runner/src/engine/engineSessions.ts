import type {
  CreateEngineSessionOptions,
  EngineEvent,
  EngineEventSubscription,
  EngineMessage,
  EngineSessionState,
  SubmitOptions,
  SubmitOutcome,
} from "./types.js";

/**
 * The session/transcript half of the `Engine` boundary: create/open/delete/list, history
 * from the raw pi branch, event subscription and prompt submission. `types.ts` keeps the
 * remaining surfaces so each file stays within the budget.
 */
export interface EngineSessions {
  /** Creates a pi session immediately (draft prewarm relies on this). */
  createSession(
    workspacePath: string,
    options: CreateEngineSessionOptions,
  ): Promise<{ sessionId: string; inputId?: string }>;

  /** Lazy restore from disk. Idempotent for an already-open session. */
  openSession(workspacePath: string, sessionId: string): Promise<void>;

  deleteSession(workspacePath: string, sessionId: string): Promise<void>;

  /** Non-deleted sessions, newest activity first. */
  listSessions(workspacePath: string): Promise<EngineSessionState[]>;

  /** Tombstoned session ids (deleted, kept for the sidebar's negative membership join). */
  listDeletedSessionIds(workspacePath: string): Promise<string[]>;

  /**
   * 全文搜索的正文来源（docs/next/automations-views-search.md §C）：`sessionId → 可见
   * 消息文本`，即 pi 的 `SessionInfo.allMessagesText`。实现自行按会话截断到上限；
   * 是否参与搜索（draft/deleted 过滤）由调用方按行规则决定，引擎不做产品判断。
   */
  listSessionSearchText(workspacePath: string): Promise<Map<string, string>>;

  history(sessionId: string): Promise<EngineMessage[]>;

  subscribe(sessionId: string, listener: (event: EngineEvent) => void): EngineEventSubscription;

  submit(sessionId: string, text: string, options: SubmitOptions): Promise<SubmitOutcome>;
}
