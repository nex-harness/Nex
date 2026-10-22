import { mkdir } from "node:fs/promises";
import { SessionManager, type ModelRuntime } from "@earendil-works/pi-coding-agent";
import type { Model } from "@earendil-works/pi-ai";
import type { CreateEngineSessionOptions } from "../types.js";
import type { SessionMetaStore } from "./metaStore.js";
import { resolveModel } from "./modelCatalog.js";
import { openPiSession, type OpenSession, type SessionFactoryDeps } from "./sessionLifecycle.js";
import { selectionFor } from "./sessionHelpers.js";

export interface SessionCreateDeps {
  metadata: SessionMetaStore;
  runtime: () => ModelRuntime;
  sessionDir: (workspacePath: string) => string;
  findSessionFile: (workspacePath: string, sessionId: string) => Promise<string | undefined>;
  factoryDeps: () => SessionFactoryDeps;
  attach: (open: OpenSession) => void;
  sessionsChanged: (workspacePath: string) => void;
}

/**
 * `createSession` + `openSession` bodies. Kept out of `PiEngine` (file budget); the engine
 * remains the owner of the session map, this only builds and hands over the pi session.
 */
export async function createEngineSession(
  deps: SessionCreateDeps,
  workspacePath: string,
  options: CreateEngineSessionOptions,
): Promise<{ sessionId: string }> {
  const model = await resolveModel(options.modelSelection, deps.runtime());
  const sessionDir = deps.sessionDir(workspacePath);
  await mkdir(sessionDir, { recursive: true });
  const sessionManager = SessionManager.create(workspacePath, sessionDir);
  const { session, open } = await openPiSession(
    deps.factoryDeps(),
    workspacePath,
    sessionManager,
    model,
  );
  const sessionId = session.sessionId;
  const now = Date.now();
  const selection = model ? selectionFor(session) : undefined;
  await deps.metadata.upsert(workspacePath, sessionId, {
    createdAt: now,
    lastActivityAt: now,
    phase: "draft",
    title: "",
    titleSource: "default",
    deleted: false,
    mode: "build",
    planEnabled: false,
    ...(selection ? { model: selection } : {}),
  });
  deps.attach(open);
  deps.sessionsChanged(workspacePath);
  return { sessionId };
}

export async function restoreEngineSession(
  deps: SessionCreateDeps,
  workspacePath: string,
  sessionId: string,
): Promise<void> {
  const meta = await deps.metadata.get(workspacePath, sessionId);
  if (meta?.deleted) throw new Error(`Session ${sessionId} is deleted`);
  const sessionFile = await deps.findSessionFile(workspacePath, sessionId);
  if (!sessionFile) throw new Error(`Session ${sessionId} not found`);
  const sessionManager = SessionManager.open(sessionFile, deps.sessionDir(workspacePath));
  const runtime = deps.runtime();
  const restoredModel = meta?.model
    ? (runtime.getModel(meta.model.providerId, meta.model.modelId) as Model<never> | undefined)
    : undefined;
  const { session, gate, open } = await openPiSession(
    deps.factoryDeps(),
    workspacePath,
    sessionManager,
    restoredModel,
  );
  if (meta?.model?.thought) session.setThinkingLevel(meta.model.thought as never);
  gate.setMode(meta?.mode ?? "build", meta?.planEnabled ?? false);
  deps.attach(open);
  await deps.metadata.upsert(workspacePath, sessionId, {
    createdAt: meta?.createdAt ?? Date.now(),
    lastActivityAt: meta?.lastActivityAt ?? Date.now(),
    title: meta?.title ?? "",
    titleSource: meta?.titleSource ?? "default",
    model: selectionFor(session) ?? meta?.model,
    phase: session.isStreaming ? "running" : (meta?.phase ?? "completedSuccess"),
    ...(meta?.followupMode ? { followupMode: meta.followupMode } : {}),
  });
}
