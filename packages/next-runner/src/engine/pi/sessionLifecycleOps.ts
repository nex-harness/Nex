import type { AgentSessionEvent, ModelRuntime } from "@earendil-works/pi-coding-agent";
import type { ModelSelection } from "@nex/shared";
import type { AttachmentRegistry } from "../../attachments/registry.js";
import type {
  CreateEngineSessionOptions,
  EngineEvent,
  EngineEventSubscription,
  EngineMessage,
  SubmitOptions,
  SubmitOutcome,
} from "../types.js";
import type { Logger } from "../../logger.js";
import type { SessionMetaStore } from "./metaStore.js";
import type { createHookFactories } from "./engineHooks.js";
import type { TitleCoordinator } from "./titleRunner.js";
import type { AttachmentPromptTable } from "./piAttachmentOps.js";
import type { OpenSession } from "./sessionLifecycle.js";
import type { PromptAttachmentPayload } from "./piSessionOps.js";
import { submitPrompt, type SubmitHooks } from "./piSessionOps.js";
import { withAttachmentMarkers } from "./attachmentEntry.js";
import { toEngineBranchHistory } from "./translate.js";
import { disposePiSession } from "./disposePiSession.js";
import { createEngineSession, restoreEngineSession } from "./sessionCreateOps.js";

import { deleteTaskImpl } from "./taskStateOps.js";
import { listDeletedSessions, listEngineSessions } from "./sessionListing.js";

/** Everything the session half of `Engine` needs from the owning engine. */
export interface SessionLifecycleDeps {
  init: () => Promise<void>;
  sessions: Map<string, OpenSession>;
  metadata: SessionMetaStore;
  hooks: ReturnType<typeof createHookFactories>;
  titles: TitleCoordinator;
  attachments: AttachmentRegistry;
  /** The attachment host (same registry; blob store + listing authority, spec A). */
  host: AttachmentRegistry;
  logger: Logger;
  attachmentOps: AttachmentPromptTable;
  runtime: () => ModelRuntime;
  emitSessionsChanged: (workspacePath: string) => void;
  onPiEvent: (workspacePath: string, sessionId: string, event: AgentSessionEvent) => void;
  requireOpen: (sessionId: string) => OpenSession;
  startPrompt: (
    workspacePath: string,
    sessionId: string,
    text: string,
    requestId: string | undefined,
    payload?: PromptAttachmentPayload,
    /** Runs right after pi accepted the prompt (before its first async hop). */
    onStart?: (session: import("@earendil-works/pi-coding-agent").AgentSession) => void,
  ) => Promise<string>;
  applySelection: (sessionId: string, selection: ModelSelection) => Promise<void>;
}

export type SessionLifecycleOps = ReturnType<typeof createSessionLifecycleOps>;

/**
 * Session/transcript half of the `Engine` surface (create/open/delete/list/history/
 * subscribe/submit), kept out of `piEngine.ts`.
 */
export function createSessionLifecycleOps(deps: SessionLifecycleDeps) {
  const listSessions = listEngineSessions({
    init: deps.init,
    metadata: deps.metadata,
    openOf: (sessionId) => deps.sessions.get(sessionId),
  });
  const listDeletedSessionIds = listDeletedSessions({ init: deps.init, metadata: deps.metadata });
  /** Serializes draft rebuilds per session (a rebuild must never interleave with itself). */
  const rebuildTails = new Map<string, Promise<void>>();

  /**
   * One rebuild: reads the session now, releases it, restores the same file under the
   * same id. A session pi already started keeps its (hidden, never prompted) runtime and
   * is reloaded in place, so its MCP extension and skills are what changes.
   */
  const rebuildDraftNow = async (sessionId: string): Promise<OpenSession> => {
    const open = deps.sessions.get(sessionId);
    if (!open) throw new Error(`Session ${sessionId} is not open`);
    const meta = await deps.metadata.get(open.workspacePath, sessionId);
    // 旧 draft 记下的 request id 没有产生任何一轮（触发重建的这次发送由客户端
    // 重发），标记为 stale，避免重发被当成 duplicate。
    if (meta?.seenRequestIds?.length) {
      await deps.metadata.upsert(open.workspacePath, sessionId, {
        staleRequestIds: [...meta.seenRequestIds],
      });
    }
    // 重建期间（await 之后）会话可能已被删除/作废：最新的在册会话不是这份 draft 时，
    // 不再重建，直接把"已不在册"交给调用方按未打开会话处理。
    if (deps.sessions.get(sessionId) !== open) throw new Error(`Session ${sessionId} is not open`);
    // 修复依据：draft 在 pi 自己的后台启动（`session_start` 及会话文件的首次写入）之后
    // 才会被重建，而此前总是释放并在新会话对象上恢复：
    //  - `SessionManager.open` 以文件头里的 id 为准，pi 的异步首写用的是另一个 id，于是
    //    重建出的会话不在引擎登记的 id 名下（提交立即 send_failed），作废/删除也管不到它；
    //  - 界面仍持有旧会话的事件订阅，看不见这一轮的输出（E2E 里 echo 调用后卡片不出现）。
    // 现在原地重建：同一个会话文件、同一个 id、同一组订阅者，按当前列表重新构建 pi 会话
    // （与新建会话同一路径）。pi 的 `session_start` 由会话文件 header 决定 reason（"resume"
    // 不关闭连接），所以这里用 "reload" 让 MCP 扩展先发 session_shutdown，再连接新列表。
    const sessionFile = open.session.sessionManager.getSessionFile?.();
    // 修复：重建出的会话带着一组全新的空 listeners，而 v4 运行时（UI 的会话订阅）早已
    // `subscribe` 在旧会话的集合上，这一轮的事件发给了没人听的新集合：模型收到了提示、
    // 侧栏出现任务行，但聊天视图停在欢迎页（MCP E2E 的 echo 卡片永远不出现）。
    // 释放旧会话前取走订阅者，重建后接到新会话上。
    const subscribers = [...open.listeners];
    await disposePiSession(open.session, "reload");
    if (deps.sessions.get(sessionId) === open) deps.sessions.delete(sessionId);
    const rebuilt = await restoreEngineSession(
      deps.hooks.sessionCreateDeps(),
      open.workspacePath,
      sessionId,
      {
        ...(sessionFile ? { sessionFile } : {}),
        sessionStartReason: "reload",
      },
    );
    for (const listener of subscribers) rebuilt.listeners.add(listener);
    return rebuilt;
  };

  return {
    createSession(workspacePath: string, options: CreateEngineSessionOptions) {
      return deps
        .init()
        .then(() => createEngineSession(deps.hooks.sessionCreateDeps(), workspacePath, options));
    },
    openSession(workspacePath: string, sessionId: string): Promise<void> {
      if (deps.sessions.has(sessionId)) return Promise.resolve();
      return deps
        .init()
        .then(() => restoreEngineSession(deps.hooks.sessionCreateDeps(), workspacePath, sessionId))
        .then(() => undefined);
    },
    /**
     * Rebuilds a not-yet-started draft with the current config (docs/next/
     * config-mcp-skills.md P1/P2).
     *
     * 修复依据：pi 在 `session_start` 时读取 MCP 列表（`loadConfig`）与技能目录，
     * 之后不再改变；UI 会先创建 draft（`createSession`），配置 push 到达后
     * draft 仍带着旧列表，新加的 MCP 服务器/技能要等下一次新开会话才生效。
     * 未产生任何一轮（无进行中 run、无排队输入、会话日志只有 header）的 draft
     * 在这里用同一个会话文件重建：id 不变，已提交的输入随后照常进入。
     *
     * 修复依据：重建与会话作废/删除是并发的异步操作。此前调用方把提交时的 `open`
     * 直接传进来，作废赢下后重建仍会跑完（会话已不在册时 restore 抛错被吞成
     * `send_failed`，或重建出一个不在册的会话再在它上面起一轮）。这里按会话 id 在
     * 重建时刻重新取会话，并把同一会话的重建串行化，等重建返回时再核对一次它在册。
     */
    async reopenDraft(sessionId: string): Promise<OpenSession> {
      await deps.init();
      const previous = rebuildTails.get(sessionId) ?? Promise.resolve();
      const current = previous.catch(() => undefined).then(() => rebuildDraftNow(sessionId));
      const tail = current.then(
        () => undefined,
        () => undefined,
      );
      rebuildTails.set(sessionId, tail);
      try {
        return await current;
      } finally {
        if (rebuildTails.get(sessionId) === tail) rebuildTails.delete(sessionId);
      }
    },
    deleteSession(workspacePath: string, sessionId: string): Promise<void> {
      return deps
        .init()
        .then(() => deleteTaskImpl(deps.hooks.taskStateHooks(), workspacePath, sessionId));
    },
    listSessions,
    listDeletedSessionIds,
    /** research §0-b: history reads pi's raw branch (not the compacted model context). */
    async history(sessionId: string): Promise<EngineMessage[]> {
      const open = deps.requireOpen(sessionId);
      const meta = await deps.metadata.get(open.workspacePath, sessionId);
      const branch = open.session.sessionManager.getBranch();
      return withAttachmentMarkers(toEngineBranchHistory(branch, meta?.lastCompact), branch);
    },
    subscribe(sessionId: string, listener: (event: EngineEvent) => void): EngineEventSubscription {
      deps.requireOpen(sessionId).listeners.add(listener);
      // The open session can be rebuilt under the same id (draft reopen); remove the
      // listener from whichever session currently holds it.
      return { dispose: () => deps.sessions.get(sessionId)?.listeners.delete(listener) };
    },
    /**
     * Submission. `submitPrompt` is the single start path (idempotency, model selection,
     * gate mode, metadata, then pi's prompt); attachment refs (spec A) are mapped just
     * before it and adopted right after pi accepted the prompt.
     */
    submit(sessionId: string, text: string, options: SubmitOptions): Promise<SubmitOutcome> {
      const open = deps.requireOpen(sessionId);
      const hooks: SubmitHooks = {
        metadata: deps.metadata,
        titles: deps.titles,
        sessionsChanged: (path) => deps.emitSessionsChanged(path),
        logger: deps.logger,
        startPrompt: (next, requestId, payload, onStart) =>
          deps.startPrompt(open.workspacePath, sessionId, next, requestId, payload, onStart),
        applySelection: (selection: ModelSelection) => deps.applySelection(sessionId, selection),
      };
      if (options.prompt || (options.attachments?.length ?? 0) === 0) {
        return submitPrompt(hooks, open, text, options);
      }
      // Ref authority (spec A): the prompt is about to materialize these refs; listing them
      // here makes a read of one of them legal before the row exists (the row replaces it).
      deps.attachments.addAdmitted(sessionId, options.attachments);
      // Attachment mapping happens before the run starts; `adopt` registers the refs the
      // run's `message_start` will claim (so the row shows text + refs, not the block).
      const attachments = options.attachments;
      return deps.attachmentOps.prepare(sessionId, text, attachments).then((prepared) => {
        hooks.onPromptStarted = () => deps.attachmentOps.adopt(open, text, prepared);
        return submitPrompt(hooks, open, text, { ...options, prompt: prepared.prompt });
      });
    },
  };
}
