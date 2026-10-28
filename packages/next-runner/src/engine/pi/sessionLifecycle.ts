import {
  SettingsManager,
  getAgentDir,
  type AgentSession,
  type AgentSessionEvent,
  type ModelRuntime,
  type SessionManager,
} from "@earendil-works/pi-coding-agent";
import type { Model } from "@earendil-works/pi-ai";
import type { PermissionGate } from "../permissions/gate.js";
import type { EngineEvent } from "../types.js";
import { createPiSessionWithGate } from "./piSessionFactory.js";
import type { PiCheckpointHook } from "./piCheckpointHook.js";
import type { PiTranslateContext } from "./events.js";
import type { McpStatusTracker } from "./mcpStatusExtension.js";
import type { McpWiring } from "../../config/mcpConfig.js";
import type { McpServerStatus } from "../../config/applyRuntimeConfig.js";

/** One open pi session plus its approval gate and subscribers. */
export interface OpenSession {
  workspacePath: string;
  session: AgentSession;
  /**
   * The id this session is registered under (the metadata row's id). pi can give a
   * session file header its own id while the engine has already registered the draft
   * under the requested id, so `session.sessionId` is not always the engine's id.
   */
  sessionId?: string;
  /** pi's `session_start` ran: pi's own runtime does its first run in the background. */
  sessionStarted?: boolean;
  gate: PermissionGate;
  unsubscribe: () => void;
  /** Subscribers of translated pi events plus gate events (approval/mode). */
  listeners: Set<(event: EngineEvent) => void>;
  /** prompt() awaits the whole run; link calls must never await this. */
  inflight: Promise<void> | null;
  /** File-checkpoint hooks of this session (spec B); absent when git is unavailable. */
  checkpoints?: PiCheckpointHook;
  /** Writes the `nex.checkpoints` / `nex.rewind` custom entries through this file's manager. */
  sessionManager: SessionManager;
  /**
   * Per-run system-prompt note (spec A). Set right before `prompt()`, read and cleared by
   * the session's inline extension on `before_agent_start`; never persisted.
   */
  systemNote?: string;
  /**
   * Input of the user message the session is delivering right now (spec A): set at its
   * `message_start`, read by the `message_end` translate context, cleared by the engine.
   */
  /** MCP status sampler of a server-owned session (P1). */
  mcpTracker?: McpStatusTracker;
  /** The MCP list the session was built with (pi reads it at `session_start`). */
  mcpStartedWith?: McpWiring;
  claimedUserInput?: {
    text: string;
    attachments?: import("@nex/shared/nex-protocol-v4").AttachmentRef[];
  };
  /** automation 派发会话：审批一律 deny（见 `PermissionGateOptions.unattended`）。 */
  unattended?: boolean;
}

export interface SessionFactoryDeps {
  /** Test seam: in-memory settings instead of the user's settings.json. */
  createSettingsManager?: (workspacePath: string) => SettingsManager;
  /** Fallback settings manager created at startup. */
  fallbackSettings: () => SettingsManager;
  modelRuntime: () => ModelRuntime;
  /**
   * Facts only the engine knows when translating (the manual compact command in flight);
   * pi's compaction events carry no command id.
   */
  translateContext?: (sessionId: string) => PiTranslateContext;
  /** Metadata transitions (phase/title) stay owned by the engine. */
  onPiEvent: (workspacePath: string, sessionId: string, event: AgentSessionEvent) => void;
  /** MCP wiring of the session's config generation (P1). */
  mcpSession?: McpWiring;
  /** Receives MCP status changes with the session id. */
  onMcpStatus?: (workspacePath: string, sessionId: string, statuses: McpServerStatus[]) => void;
  /** Materialized bundle directories of the generation (P2). */
  additionalSkillPaths?: ReadonlyArray<string>;
  additionalPromptTemplatePaths?: ReadonlyArray<string>;
  /**
   * File checkpoints for a session about to open (spec B). `undefined` disables the hooks for
   * this session (e.g. git missing → Undo is hidden because no turn has fileChanges).
   */
  checkpointsFor?: (
    workspacePath: string,
    sessionIdRef: { readonly current: string },
  ) => PiCheckpointHook | undefined;
  /** Reads and clears the pending per-run system-prompt note (spec A). */
  takeSystemNote?: () => string | undefined;
  /**
   * 会话是否为 automation 无人值守会话。按会话 id 查询（draft 原地重建会重新走
   * `openPiSession`），元数据里的 automationId 是唯一事实来源。
   */
  isUnattended?: (sessionId: string) => boolean;
  /** Pre-translation pi event hook, addressed by the session it belongs to (spec A). */
  onPiEventBeforeTranslate?: (open: OpenSession, event: AgentSessionEvent) => void;
  /**
   * `message_end` claim of the session's admitted prompts (spec A): prompt text → the
   * text/refs the userInput row must show. The session id is implicit (one table per
   * session, captured here).
   */
  attachmentInputs?: {
    claim: (
      promptText: string,
      sessionId: string,
    ) =>
      | { text: string; attachments?: import("@nex/shared/nex-protocol-v4").AttachmentRef[] }
      | undefined;
  };
}

/**
 * Builds the pi session with the inline permission-gate extension. pi's SDK does not
 * reload a supplied resource loader, so the loader is reloaded inside the factory; inline
 * extensions load after disk extensions and therefore see every `tool_call` last.
 *
 * `expectedSessionId` is the engine's id for this session (the metadata row's): pi's
 * background first write can stamp the session file with another id, and every event
 * the engine records must be addressed by the id it registered.
 */
export async function openPiSession(
  deps: SessionFactoryDeps,
  workspacePath: string,
  sessionManager: SessionManager,
  model: Model<never> | undefined,
  expectedSessionId?: string,
  sessionStartReason?: "startup" | "reload" | "new" | "resume" | "fork",
  /**
   * createSession 时的 automation 身份。创建路径的元数据尚未落盘（稍后由
   * `createEngineSession` 写入），这里作为一次性的显式标记；恢复/重建路径读元数据。
   */
  automationId?: string,
): Promise<{ session: AgentSession; gate: PermissionGate; open: OpenSession }> {
  const settingsManager =
    deps.createSettingsManager?.(workspacePath) ??
    deps.fallbackSettings() ??
    SettingsManager.create(workspacePath, getAgentDir());
  const sessionIdRef = { current: "" };
  const attachmentInputs = deps.attachmentInputs
    ? {
        claim: (promptText: string) =>
          deps.attachmentInputs?.claim(promptText, sessionIdRef.current),
      }
    : undefined;
  /** The open session as soon as it exists (note/attachment hooks run per event). */
  const openRef: { current: OpenSession | null } = { current: null };
  /** The engine's id of this session (see `expectedSessionId`). */
  const engineId = (): string =>
    expectedSessionId ?? openRef.current?.sessionId ?? sessionIdRef.current;
  // Each open session builds its own hook; persistence and live events read `sessionIdRef`
  // so they address the real id once pi has created the session.
  const checkpoints = deps.checkpointsFor?.(workspacePath, sessionIdRef);
  // 无人值守标记属于会话而不是某一次打开：草稿重建/恢复后必须重新带上，否则 automation
  // 会话在重建后会退回“等待用户审批”并永久挂起。
  const unattended =
    automationId !== undefined ||
    (expectedSessionId ? deps.isUnattended?.(expectedSessionId) === true : false);
  const created = await createPiSessionWithGate({
    workspacePath,
    ...(sessionStartReason ? { sessionStartReason } : {}),
    ...(unattended ? { unattended: true } : {}),
    sessionManager,
    model,
    modelRuntime: deps.modelRuntime(),
    settingsManager,
    onPiEvent: (event) => {
      deps.onPiEvent(workspacePath, engineId(), event);
    },
    ...(deps.translateContext ? { translateContext: deps.translateContext } : {}),
    ...(checkpoints ? { checkpoints } : {}),
    // The note belongs to the open session, which exists only after the factory returned;
    // the extension reads it through this scope on every run start.
    takeSystemNote: () => {
      const note = openRef.current?.systemNote;
      if (openRef.current) openRef.current.systemNote = undefined;
      return note;
    },
    onPiEventBeforeTranslate: (event) => {
      if (openRef.current) deps.onPiEventBeforeTranslate?.(openRef.current, event);
    },
    ...(attachmentInputs ? { attachmentInputs } : {}),
    ...(deps.mcpSession
      ? {
          mcp: deps.mcpSession,
          ...(deps.onMcpStatus ? { onMcpStatus: deps.onMcpStatus } : {}),
        }
      : {}),
    ...(deps.additionalSkillPaths ? { additionalSkillPaths: deps.additionalSkillPaths } : {}),
    ...(deps.additionalPromptTemplatePaths
      ? { additionalPromptTemplatePaths: deps.additionalPromptTemplatePaths }
      : {}),
  });
  sessionIdRef.current = created.session.sessionId;
  const open: OpenSession = {
    workspacePath,
    session: created.session,
    sessionId: expectedSessionId ?? created.session.sessionId,
    // pi continues its own startup in the background after this call returns.
    sessionStarted: true,
    gate: created.gate,
    listeners: created.listeners,
    inflight: null,
    unsubscribe: created.unsubscribe,
    ...(checkpoints ? { checkpoints } : {}),
    ...(created.mcpTracker ? { mcpTracker: created.mcpTracker } : {}),
    ...(created.mcp ? { mcpStartedWith: created.mcp } : {}),
    ...(unattended ? { unattended: true } : {}),
    sessionManager,
  };
  openRef.current = open;
  return { session: created.session, gate: created.gate, open };
}
