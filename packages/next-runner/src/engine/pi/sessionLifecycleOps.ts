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
        .then(() => restoreEngineSession(deps.hooks.sessionCreateDeps(), workspacePath, sessionId));
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
      const open = deps.requireOpen(sessionId);
      open.listeners.add(listener);
      return { dispose: () => open.listeners.delete(listener) };
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
