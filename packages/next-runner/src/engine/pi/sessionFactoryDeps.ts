import { SettingsManager, type ModelRuntime } from "@earendil-works/pi-coding-agent";
import { mcpSessionWiring } from "../../config/mcpConfig.js";
import type { McpServerStatus } from "../../config/applyRuntimeConfig.js";
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
  /** Receives MCP statuses of a session (facts + live status). */
  onMcpStatus?: (workspacePath: string, sessionId: string, statuses: McpServerStatus[]) => void;
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
    fallbackSettings: () =>
      deps.settingsManager() ??
      deps.options.runtimeConfig?.settingsManager() ??
      SettingsManager.create(process.cwd()),
    ...(deps.options.createSettingsManager
      ? { createSettingsManager: deps.options.createSettingsManager }
      : {}),
    modelRuntime: () => deps.runtime(),
    ...mcpAndResourceOptions(deps),
    translateContext: (sessionId): PiTranslateContext => {
      const compactCommandId = deps.compactOps.activeCommandId(sessionId);
      return compactCommandId ? { compactCommandId } : {};
    },
    onPiEvent: deps.onPiEvent,
  };
}

/**
 * MCP wiring and bundle paths of the generation the next session will use. The
 * generation is read here (not at engine construction) so a config swap reaches
 * new sessions; running sessions keep the options they were created with.
 */
function mcpAndResourceOptions(deps: {
  options: PiEngineOptions;
  sessions: Map<string, OpenSession>;
  onMcpStatus?: (workspacePath: string, sessionId: string, statuses: McpServerStatus[]) => void;
}): Partial<SessionFactoryDeps> {
  const manager = deps.options.runtimeConfig;
  if (!manager) return {};
  const config = manager.current.config;
  const resources = manager.resources();
  return {
    mcpSession: mcpSessionWiring(config.mcp),
    ...(deps.onMcpStatus ? { onMcpStatus: deps.onMcpStatus } : {}),
    ...(resources.skillPaths.length > 0 ? { additionalSkillPaths: resources.skillPaths } : {}),
    ...(resources.promptPaths.length > 0
      ? { additionalPromptTemplatePaths: resources.promptPaths }
      : {}),
  };
}
