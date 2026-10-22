import {
  DefaultResourceLoader,
  SettingsManager,
  createAgentSession,
  getAgentDir,
  type AgentSession,
  type AgentSessionEvent,
  type BeforeAgentStartEvent,
  type ExtensionFactory,
  type InlineExtension,
  type ModelRuntime,
  type SessionManager,
} from "@earendil-works/pi-coding-agent";
import type { Model } from "@earendil-works/pi-ai";
import { PermissionGate } from "../permissions/gate.js";
import type { EngineEvent } from "../types.js";
import { translatePiEvent, type PiTranslateContext } from "./events.js";
import { createPermissionGateExtension } from "./permissionExtension.js";
import type { PiCheckpointHook } from "./piCheckpointHook.js";
import { createCheckpointExtension } from "./checkpointExtension.js";

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
  /**
   * File checkpoints (spec B). Optional: a session created before the service resolves (or
   * with git missing) simply has no checkpoint hooks.
   */
  checkpoints?: PiCheckpointHook;
  /**
   * Reads and clears the per-run system-prompt note (spec A: the "attached files are
   * readable with read" hint). The note is never persisted.
   */
  takeSystemNote?: () => string | undefined;
  /**
   * Pre-translation hook (spec A): `message_start` of a user message executes its prompt
   * here, before translation, so `user_input` can carry the row text + attachment refs.
   */
  onPiEventBeforeTranslate?: (event: AgentSessionEvent) => void;
  /** The session whose input table a user `message_end` claims (spec A). */
  attachmentInputs?: {
    claim: (
      promptText: string,
      sessionId: string,
    ) =>
      | { text: string; attachments?: import("@nex/shared/nex-protocol-v4").AttachmentRef[] }
      | undefined;
  };
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
    extensionFactories: [
      createPermissionGateExtension(gate),
      ...(options.takeSystemNote ? [createSystemNoteExtension(options.takeSystemNote)] : []),
      ...(options.checkpoints ? [createCheckpointExtension(options.checkpoints)] : []),
    ],
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
    // Attachment prompt bookkeeping must run before translation: an attachment prompt's
    // persisted user text is rewritten at its `message_start`, and its `message_entry`
    // is emitted there (spec A).
    options.onPiEventBeforeTranslate?.(event);
    // Metadata/compaction bookkeeping first: it decides which command a compaction event
    // belongs to, and translation must see that before projection folds the event.
    options.onPiEvent(event);
    // Checkpoint facts ride the same ordered stream: a user message opens a product turn,
    // its persisted entry id becomes the rewind anchor, and a settle flushes the record.
    const checkpoints = options.checkpoints;
    if (checkpoints) {
      if (event.type === "message_end" && event.message.role === "user") {
        checkpoints.startTurn();
      } else if (event.type === "agent_settled") {
        void checkpoints.settle();
      }
    }
    for (const translated of translatePiEvent(
      event,
      Date.now(),
      translateContextFor(options, session.sessionId),
    )) {
      for (const listener of listeners) listener(translated);
    }
  });
  return { session, gate, listeners, unsubscribe };
}

/** Translate context: manual compact + the session's attachment input table (spec A). */
function translateContextFor(
  options: PiSessionFactoryOptions,
  sessionId: string,
): PiTranslateContext | undefined {
  const compact = options.translateContext?.(sessionId);
  const attachmentInputs = options.attachmentInputs;
  const takeAttachments = attachmentInputs
    ? (promptText: string) => attachmentInputs.claim(promptText, sessionId)
    : undefined;
  if (!compact && !takeAttachments) return undefined;
  return { ...compact, ...(takeAttachments ? { takeAttachments } : {}) };
}

/**
 * Inline pi extension appending the run's one-shot system-prompt note (spec A). The engine
 * sets it right before `prompt()`; reading it on `before_agent_start` and returning
 * `systemPrompt` keeps it in this request only — nothing about attachments is written into
 * the session file, so a later `refreshContext()` never resurrects a stale hint.
 */
function createSystemNoteExtension(take: () => string | undefined): InlineExtension {
  const factory: ExtensionFactory = (pi) => {
    pi.on("before_agent_start", async (event: BeforeAgentStartEvent) => {
      const note = take();
      if (!note) return undefined;
      if (event.messageType !== "user") return undefined;
      return { systemPrompt: `${event.systemPrompt}${note}` };
    });
  };
  return { name: "nex-attachment-note", factory };
}
