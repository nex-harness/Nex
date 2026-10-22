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
}

export interface SessionFactoryDeps {
  /** Test seam: in-memory settings instead of the user's settings.json. */
  createSettingsManager?: (workspacePath: string) => SettingsManager;
  /** Fallback settings manager created at startup. */
  fallbackSettings: () => SettingsManager;
  modelRuntime: () => ModelRuntime;
  /** Metadata transitions (phase/title) stay owned by the engine. */
  onPiEvent: (workspacePath: string, sessionId: string, event: AgentSessionEvent) => void;
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
  const created = await createPiSessionWithGate({
    workspacePath,
    sessionManager,
    model,
    modelRuntime: deps.modelRuntime(),
    settingsManager,
    onPiEvent: (event) => {
      deps.onPiEvent(workspacePath, sessionIdRef.current, event);
    },
  });
  sessionIdRef.current = created.session.sessionId;
  const open: OpenSession = {
    workspacePath,
    session: created.session,
    gate: created.gate,
    listeners: created.listeners,
    inflight: null,
    unsubscribe: created.unsubscribe,
  };
  return { session: created.session, gate: created.gate, open };
}
