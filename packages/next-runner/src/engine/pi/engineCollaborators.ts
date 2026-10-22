import type { PiEngine } from "./piEngine.js";
import { PiCompactOps } from "./piCompactOps.js";
import { PiFileOps } from "./piFileOps.js";
import { createSessionControls } from "./sessionControls.js";
import { createHookFactories, sessionDirFactory } from "./engineHooks.js";
import { createSessionLifecycleOps } from "./sessionLifecycleOps.js";
import { createTaskStateApi } from "./taskStateOps.js";
import { createEngineOps } from "./engineOps.js";
import { createConversationCommands } from "./piConversationCommands.js";
import { rewindWithAttachments } from "./piRewindWithAttachments.js";
import { teardownSession } from "./sessionViews.js";

/**
 * Builds every collaborator `PiEngine` delegates to (checkpoints, run controls, hooks, task
 * state, compact/model ops, session lifecycle, conversation commands). Kept out of the
 * engine constructor for the file budget; ownership stays on the engine's session map.
 */
export function createEngineCollaborators(engine: PiEngine) {
  const options = engine.options;
  const fileOps = new PiFileOps({
    dataDir: options.dataDir,
    logger: options.logger,
    ...(options.gitExecutable ? { gitExecutable: options.gitExecutable } : {}),
    ...(options.maxCheckpointFiles !== undefined
      ? { maxSnapshotFiles: options.maxCheckpointFiles }
      : {}),
    requireOpen: (id) => engine.requireOpen(id),
    openFor: (id) => engine.sessions.get(id),
    emit: (id, event) => engine.emitEngineEvent(id, event),
  });
  const sessionControls = createSessionControls({
    sessions: engine.sessions,
    openOf: (id) => engine.sessions.get(id),
    metadata: engine.metadata,
    sessionsChanged: (path) => engine.emitSessionsChanged(path),
  });
  const hooks = createHookFactories({
    dataDir: options.dataDir,
    metadata: engine.metadata,
    sessions: engine.sessions,
    sessionDir: sessionDirFactory(options.dataDir),
    attach: (open) => engine.attach(open),
    teardown: teardownSession,
    sessionsChanged: (path) => engine.emitSessionsChanged(path),
    factoryDeps: () => engine.sessionFactoryDeps(),
    runtime: () => engine.requireRuntime(),
  });
  const taskApi = createTaskStateApi(hooks.taskStateHooks());
  const sessionFor = (id: string) => engine.sessions.get(id)?.session;
  const compactOps = new PiCompactOps({ logger: options.logger, sessionFor });
  const ops = createEngineOps({
    ...options,
    metadata: engine.metadata,
    runtime: () => engine.requireRuntime(),
    sessionFor,
    emit: (id, event) => engine.emitEngineEvent(id, event),
    sessionsChanged: (path) => engine.emitSessionsChanged(path),
  });
  const sessionLifecycle = createSessionLifecycleOps({
    init: () => engine.init(),
    sessions: engine.sessions,
    metadata: engine.metadata,
    hooks,
    titles: ops.titles,
    attachments: engine.attachments,
    host: engine.attachments,
    logger: options.logger,
    attachmentOps: engine.attachmentOps,
    runtime: () => engine.requireRuntime(),
    emitSessionsChanged: (path) => engine.emitSessionsChanged(path),
    onPiEvent: (path, id, event) => void engine.onPiEventHook(path, id, event),
    requireOpen: (id) => engine.requireOpen(id),
    startPrompt: (w, id, text, requestId, payload, onStart) =>
      engine.startPrompt(w, id, text, requestId, payload, onStart),
    applySelection: (id, selection) => engine.applySelection(id, selection),
  });
  const conversation = createConversationCommands({
    ops: () => engine.conversationOps(),
    compactionEntries: engine.compactionEntries,
    compactOps,
    rewindPrompt: (id, request) =>
      rewindWithAttachments(
        {
          attachments: engine.attachments,
          attachmentOps: engine.attachmentOps,
          requireOpen: (sessionId) => engine.requireOpen(sessionId),
          promptAgain: (open, text, payload, onStart) => {
            void engine
              .startPrompt(
                open.workspacePath,
                open.session.sessionId,
                text,
                `rewind_${Date.now()}`,
                payload,
                onStart,
              )
              .catch(() => undefined);
          },
        },
        id,
        request,
      ),
  });
  return {
    fileOps,
    sessionControls,
    hooks,
    taskApi,
    compactOps,
    configOps: ops.configOps,
    titles: ops.titles,
    titleSettings: ops.titleSettings,
    sessionLifecycle,
    conversation,
  };
}
