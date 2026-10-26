import type { ModelSelection } from "@nex/shared";
import type { Logger } from "../../logger.js";
import type { ModelRuntime } from "@earendil-works/pi-coding-agent";
import { applyEngineSelection, startEnginePrompt, type PromptOpsDeps } from "./promptOps.js";
import type { PromptAttachmentPayload } from "./piSessionOps.js";
import type { OpenSession } from "./sessionLifecycle.js";
import type { SessionConfigOps } from "./sessionConfigOps.js";
import type { SessionMetaStore } from "./metaStore.js";
import { mcpSessionWiring } from "../../config/mcpConfig.js";

/** Facts the prompt helpers read from the engine (kept apart from `piEngine.ts`). */
export interface PromptHookHost {
  metadata: SessionMetaStore;
  sessions: Map<string, OpenSession>;
  configOps: SessionConfigOps;
  logger: Logger;
  runtime: () => ModelRuntime;
  /** Current config generation (draft MCP refresh) and the rebuild seam. */
  currentMcp?: () => ReturnType<typeof mcpSessionWiring>;
  /**
   * Rebuilds an untouched draft with the current config. Called with the session id
   * of the input that is starting; it reads the open session at submission time.
   */
  reopenDraft?: (sessionId: string) => Promise<OpenSession>;
  sessionsChanged: (workspacePath: string) => void;
}

/** Prompt start / attachment payload / model selection helpers bound to one engine. */
export function createPromptHooks(host: PromptHookHost) {
  const requireOpen = (sessionId: string): OpenSession => {
    const open = host.sessions.get(sessionId);
    if (!open) throw new Error(`Session ${sessionId} is not open`);
    return open;
  };
  const promptOps = (): PromptOpsDeps => ({
    metadata: host.metadata,
    logger: host.logger,
    sessionsChanged: (p) => host.sessionsChanged(p),
    runtime: () => host.runtime(),
    configOps: host.configOps,
  });
  return {
    promptOps,
    /** Returns the input id (the link's ACK carries it as `inputId`). */
    startPrompt(
      workspacePath: string,
      sessionId: string,
      text: string,
      requestId: string | undefined,
      payload?: PromptAttachmentPayload,
      onStart?: (session: import("@earendil-works/pi-coding-agent").AgentSession) => void,
    ): Promise<string> {
      // A draft created before a config push was built with the old MCP list /
      // bundles (pi reads them once at `session_start`); rebuild it first when
      // nothing has run yet, so the first turn uses the current config.
      const open = requireOpen(sessionId);
      const startFor = (target: OpenSession) =>
        startEnginePrompt(
          promptOps(),
          target,
          workspacePath,
          sessionId,
          text,
          requestId,
          payload
            ? {
                images: payload.images,
                streamingBehavior: payload.streamingBehavior,
                promptText: payload.promptText,
                ...(onStart ? { onStart } : {}),
              }
            : onStart
              ? { images: [], streamingBehavior: "followUp" as const, onStart }
              : undefined,
        );
      const next = host.currentMcp?.();
      const rebuild = host.reopenDraft;
      // 修复依据：此前只比较 `owned`，于是 draft 随一次 push 重建后，同一推送再把配置
      // 改回来（owned 相同、列表不同）时不会重建，第一轮会用上一份列表。按生效列表的
      // 身份（`key`）比较，列表变了就重建。
      if (
        rebuild &&
        isUntouchedDraft(open) &&
        (open.mcpStartedWith?.key ?? "") !== (next?.key ?? "")
      ) {
        // 修复依据：重建是异步的，作废/删除可能在它之前并发发生。原实现把提交时捕获的
        // `open` 交给重建，即使会话已关闭也会继续，随后在已不在册的会话上起一轮 —— 表现为
        // `send_failed: "Session … is not open"`（E2E 里重建与会话作废交错）。改为按会话 id
        // 在重建时刻重新取当前会话，重建完成后核对它仍在册（不在册按未打开会话拒绝）。
        return rebuild(sessionId).then((rebuilt) => {
          if (host.sessions.get(sessionId) === rebuilt) return startFor(rebuilt);
          throw new Error(`Session ${sessionId} is not open`);
        });
      }
      return startFor(open);
    },
    applySelection(sessionId: string, selection: ModelSelection): Promise<void> {
      return applyEngineSelection(promptOps(), requireOpen(sessionId), sessionId, selection);
    },
  };
}

/**
 * A session that never produced a turn: `createAgentSession` already persists
 * session-start entries (model/thinking level), while anything the user sent
 * becomes a `message`.
 */
function isUntouchedDraft(open: OpenSession): boolean {
  if (open.inflight || open.claimedUserInput) return false;
  const branch = open.session.sessionManager?.getBranch?.() ?? [];
  // A draft's log holds only session-start bookkeeping (a model change); any
  // user input or turn leaves a `message` entry.
  return !branch.some((entry) => entry.type === "message");
}
