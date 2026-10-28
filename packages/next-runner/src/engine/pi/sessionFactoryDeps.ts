import { SettingsManager, type ModelRuntime } from "@earendil-works/pi-coding-agent";
import { mcpSessionWiring } from "../../config/mcpConfig.js";
import type { McpServerStatus } from "../../config/applyRuntimeConfig.js";
import type { PiEngineOptions } from "./piEngine.js";
import type { PiCompactOps } from "./piCompactOps.js";
import type { PiFileOps } from "./piFileOps.js";
import type { AttachmentPromptTable } from "./piAttachmentOps.js";
import type { SessionMetaStore } from "./metaStore.js";
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
  /** automation 身份查询（`SessionMetaEntry.automationId`）；与 sessions 同源，供无人值守判定。 */
  metadata: SessionMetaStore;
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
    ...unattendedOption(deps),
    ...mcpAndResourceOptions(deps),
    translateContext: (sessionId): PiTranslateContext => {
      const compactCommandId = deps.compactOps.activeCommandId(sessionId);
      return compactCommandId ? { compactCommandId } : {};
    },
    onPiEvent: deps.onPiEvent,
  };
}

/**
 * automation 会话的无人值守判定：已在册会话直接看标记（draft 重建复用同一个
 * `openPiSession`，标记会随新会话对象重建）；不在册（冷恢复）时按元数据缓存判断。
 */
function unattendedOption(deps: {
  sessions: Map<string, OpenSession>;
  /** automation 身份查询（`SessionMetaEntry.automationId`）；与 sessions 同源，供无人值守判定。 */
  metadata: SessionMetaStore;
}): Pick<SessionFactoryDeps, "isUnattended"> {
  return {
    isUnattended: (sessionId) =>
      [...deps.sessions.values()].some(
        (open) => open.sessionId === sessionId && open.unattended === true,
      ) || deps.metadata.hasAutomationId(sessionId),
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
