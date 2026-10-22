import { SettingsManager, type ModelRuntime } from "@earendil-works/pi-coding-agent";
import type { PiEngineOptions } from "./piEngine.js";
import type { PiCompactOps } from "./piCompactOps.js";
import type { PiFileOps } from "./piFileOps.js";
import type { AttachmentPromptTable } from "./piAttachmentOps.js";
import type { OpenSession, SessionFactoryDeps } from "./sessionLifecycle.js";
import type { PiTranslateContext } from "./events.js";

/**
 * `SessionFactoryDeps` of one engine (kept out of `piEngine.ts`): the attachment input table,
 * checkpoint hooks, settings/runtime seams and the event translator's context.
 */
export function createSessionFactoryDeps(deps: {
  options: PiEngineOptions;
  settingsManager: () => SettingsManager | null;
  runtime: () => ModelRuntime;
  compactOps: PiCompactOps;
  fileOps: PiFileOps;
  attachmentOps: AttachmentPromptTable;
  sessions: Map<string, OpenSession>;
  onPiEvent: (
    workspacePath: string,
    sessionId: string,
    event: Parameters<SessionFactoryDeps["onPiEvent"]>[2],
  ) => void;
}): SessionFactoryDeps {
  return {
    checkpointsFor: (workspacePath, sessionIdRef) =>
      deps.fileOps.checkpointHookFor(workspacePath, sessionIdRef),
    onPiEventBeforeTranslate: (session, event) => deps.attachmentOps.handlePiEvent(session, event),
    attachmentInputs: {
      // The translate context carries the session id (see `translateContextFor`).
      claim: (_promptText, sessionId) => {
        const open = deps.sessions.get(sessionId);
        const claimed = open?.claimedUserInput;
        if (open) open.claimedUserInput = undefined;
        return claimed;
      },
    },
    fallbackSettings: () => deps.settingsManager() ?? SettingsManager.create(process.cwd()),
    ...(deps.options.createSettingsManager
      ? { createSettingsManager: deps.options.createSettingsManager }
      : {}),
    modelRuntime: () => deps.runtime(),
    translateContext: (sessionId): PiTranslateContext => {
      const compactCommandId = deps.compactOps.activeCommandId(sessionId);
      return compactCommandId ? { compactCommandId } : {};
    },
    onPiEvent: deps.onPiEvent,
  };
}
