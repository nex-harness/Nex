import {
  DefaultResourceLoader,
  SettingsManager,
  createAgentSession,
  getAgentDir,
  type AgentSession,
  type AgentSessionEvent,
  type ModelRuntime,
  type SessionManager,
} from "@earendil-works/pi-coding-agent";
import type { Model } from "@earendil-works/pi-ai";
import { PermissionGate } from "../permissions/gate.js";
import type { EngineEvent } from "../types.js";
import { translatePiEvent, type PiTranslateContext } from "./events.js";
import { createPermissionGateExtension } from "./permissionExtension.js";

export interface PiSessionFactoryOptions {
  workspacePath: string;
  sessionManager: SessionManager;
  model: Model<never> | undefined;
  modelRuntime: ModelRuntime;
  settingsManager: SettingsManager;
  /** Metadata transitions (phase/title) stay owned by the engine. */
  onPiEvent: (event: AgentSessionEvent) => void;
  /**
   * Facts only the engine knows when translating: the manual compact command in flight
   * (pi's compaction events carry no command id).
   */
  translateContext?: (sessionId: string) => PiTranslateContext;
}

export interface CreatedPiSession {
  session: AgentSession;
  gate: PermissionGate;
  listeners: Set<(event: EngineEvent) => void>;
  unsubscribe: () => void;
}

/**
 * Builds a pi session with the inline permission-gate extension.
 *
 * pi's SDK does not reload a supplied resource loader, so the loader is reloaded here.
 * Inline extensions load after disk extensions, which is what puts the gate last in the
 * `tool_call` chain — it sees the final, mutated arguments of every call. The gate emits
 * approval/mode events into the same listener set as the translated pi events, so
 * subscribers see one ordered stream.
 */
export async function createPiSessionWithGate(
  options: PiSessionFactoryOptions,
): Promise<CreatedPiSession> {
  const listeners = new Set<(event: EngineEvent) => void>();
  const gate = new PermissionGate({
    emit: (event) => {
      for (const listener of listeners) listener(event);
    },
  });
  const resourceLoader = new DefaultResourceLoader({
    cwd: options.workspacePath,
    agentDir: getAgentDir(),
    settingsManager: options.settingsManager,
    extensionFactories: [createPermissionGateExtension(gate)],
  });
  await resourceLoader.reload();
  const { session } = await createAgentSession({
    cwd: options.workspacePath,
    ...(options.model ? { model: options.model } : {}),
    modelRuntime: options.modelRuntime,
    sessionManager: options.sessionManager,
    resourceLoader,
    settingsManager: options.settingsManager,
  });
  const unsubscribe = session.subscribe((event: AgentSessionEvent) => {
    // Metadata/compaction bookkeeping first: it decides which command a compaction event
    // belongs to, and translation must see that before projection folds the event.
    options.onPiEvent(event);
    const sessionId = session.sessionId;
    for (const translated of translatePiEvent(
      event,
      Date.now(),
      options.translateContext?.(sessionId),
    )) {
      for (const listener of listeners) listener(translated);
    }
  });
  return { session, gate, listeners, unsubscribe };
}
