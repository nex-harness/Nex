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

/** One open pi session plus its approval gate and subscribers. */
export interface OpenSession {
  workspacePath: string;
  session: AgentSession;
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
  claimedUserInput?: {
    text: string;
    attachments?: import("@nex/shared/nex-protocol-v4").AttachmentRef[];
  };
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
 */
export async function openPiSession(
  deps: SessionFactoryDeps,
  workspacePath: string,
  sessionManager: SessionManager,
  model: Model<never> | undefined,
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
  // Each open session builds its own hook; persistence and live events read `sessionIdRef`
  // so they address the real id once pi has created the session.
  const checkpoints = deps.checkpointsFor?.(workspacePath, sessionIdRef);
  const created = await createPiSessionWithGate({
    workspacePath,
    sessionManager,
    model,
    modelRuntime: deps.modelRuntime(),
    settingsManager,
    onPiEvent: (event) => {
      deps.onPiEvent(workspacePath, sessionIdRef.current, event);
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
  });
  sessionIdRef.current = created.session.sessionId;
  const open: OpenSession = {
    workspacePath,
    session: created.session,
    gate: created.gate,
    listeners: created.listeners,
    inflight: null,
    unsubscribe: created.unsubscribe,
    ...(checkpoints ? { checkpoints } : {}),
    sessionManager,
  };
  openRef.current = open;
  return { session: created.session, gate: created.gate, open };
}
