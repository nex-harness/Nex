import { createHash } from "node:crypto";
import { mkdir, readdir, unlink } from "node:fs/promises";
import { join } from "node:path";
import {
  ModelRuntime,
  SessionManager,
  SettingsManager,
  createAgentSession,
  type AgentSession,
  type AgentSessionEvent,
} from "@earendil-works/pi-coding-agent";
import type { Model } from "@earendil-works/pi-ai";
import { clampThinkingLevel } from "@earendil-works/pi-ai";
import type { ModelSelection } from "@nex/shared";
import type { Logger } from "../../logger.js";
import type {
  CreateEngineSessionOptions,
  Engine,
  EngineCatalog,
  EngineEvent,
  EngineEventSubscription,
  EngineMessage,
  EngineSessionState,
  SubmitOptions,
  SubmitOutcome,
} from "../types.js";
import { translatePiEvent } from "./events.js";
import { SessionMetaStore } from "./metaStore.js";
import { EMPTY_USAGE, defaultTitle, selectionFor, usageFromSession } from "./sessionHelpers.js";
import { buildCatalog, resolveModel } from "./modelCatalog.js";
import { toEngineHistory } from "./translate.js";

interface OpenSession {
  workspacePath: string;
  session: AgentSession;
  unsubscribe: () => void;
  /** prompt() awaits the whole run; link calls must never await this. */
  inflight: Promise<void> | null;
}

export interface PiEngineOptions {
  dataDir: string;
  logger: Logger;
}

/**
 * pi-backed Engine. Owns all mutable session facts:
 * - pi session files under `<dataDir>/sessions/<sha1(workspacePath)>/`
 * - one runner metadata file per workspace (title/phase/tombstones)
 * - the in-process AgentSession map
 */
export class PiEngine implements Engine {
  private readonly sessions = new Map<string, OpenSession>();
  private readonly listeners = new Set<(workspacePath: string) => void>();
  private readonly metadata: SessionMetaStore;
  private modelRuntime: ModelRuntime | null = null;
  private settingsManager: SettingsManager | null = null;
  private catalogCache: EngineCatalog | null = null;
  private initialized = false;

  constructor(private readonly options: PiEngineOptions) {
    this.metadata = new SessionMetaStore(options.dataDir, options.logger);
  }

  async init(): Promise<void> {
    if (this.initialized) return;
    // Defaults read ~/.pi/agent/auth.json + models.json, so the user's existing pi
    // credentials are used without any server push.
    this.modelRuntime = await ModelRuntime.create();
    this.settingsManager = SettingsManager.create(process.cwd());
    await this.metadata.load();
    this.initialized = true;
  }

  async createSession(
    workspacePath: string,
    options: CreateEngineSessionOptions,
  ): Promise<{ sessionId: string; inputId?: string }> {
    await this.init();
    const runtime = this.requireRuntime();
    const model = await resolveModel(options.modelSelection, runtime);
    const sessionDir = this.sessionDir(workspacePath);
    await mkdir(sessionDir, { recursive: true });
    const sessionManager = SessionManager.create(workspacePath, sessionDir);
    const { session } = await createAgentSession({
      cwd: workspacePath,
      ...(model ? { model } : {}),
      modelRuntime: runtime,
      sessionManager,
    });
    const sessionId = session.sessionId;
    const now = Date.now();
    const selection = model ? selectionFor(session) : undefined;
    await this.metadata.upsert(workspacePath, sessionId, {
      createdAt: now,
      lastActivityAt: now,
      phase: "draft",
      title: "",
      titleSource: "default",
      deleted: false,
      ...(selection ? { model: selection } : {}),
    });
    this.attach(workspacePath, session);
    this.emitSessionsChanged(workspacePath);
    return { sessionId };
  }

  async openSession(workspacePath: string, sessionId: string): Promise<void> {
    await this.init();
    if (this.sessions.has(sessionId)) return;
    const meta = await this.metadata.get(workspacePath, sessionId);
    if (meta?.deleted) throw new Error(`Session ${sessionId} is deleted`);
    const sessionFile = await this.findSessionFile(workspacePath, sessionId);
    if (!sessionFile) throw new Error(`Session ${sessionId} not found`);
    const sessionManager = SessionManager.open(sessionFile, this.sessionDir(workspacePath));
    const runtime = this.requireRuntime();
    const restoredModel = meta?.model
      ? (runtime.getModel(meta.model.providerId, meta.model.modelId) as Model<never> | undefined)
      : undefined;
    const { session } = await createAgentSession({
      cwd: workspacePath,
      ...(restoredModel ? { model: restoredModel } : {}),
      modelRuntime: runtime,
      sessionManager,
    });
    if (meta?.model?.thought) {
      session.setThinkingLevel(meta.model.thought as never);
    }
    this.attach(workspacePath, session);
    await this.metadata.upsert(workspacePath, sessionId, {
      createdAt: meta?.createdAt ?? Date.now(),
      lastActivityAt: meta?.lastActivityAt ?? Date.now(),
      title: meta?.title ?? "",
      titleSource: meta?.titleSource ?? "default",
      model: selectionFor(session) ?? meta?.model,
      phase: session.isStreaming ? "running" : (meta?.phase ?? "completedSuccess"),
    });
  }

  async deleteSession(workspacePath: string, sessionId: string): Promise<void> {
    await this.init();
    const open = this.sessions.get(sessionId);
    if (open) {
      try {
        await open.session.abort();
      } catch {
        // Abort is best-effort; disposal below still ends the in-process session.
      }
      open.unsubscribe();
      open.session.dispose();
      this.sessions.delete(sessionId);
    }
    const file = await this.findSessionFile(workspacePath, sessionId);
    if (file) await unlink(file).catch(() => undefined);
    await this.metadata.markDeleted(workspacePath, sessionId);
    this.emitSessionsChanged(workspacePath);
  }

  async listSessions(workspacePath: string): Promise<EngineSessionState[]> {
    await this.init();
    const entries = await this.metadata.list(workspacePath);
    const states: EngineSessionState[] = [];
    for (const entry of entries) {
      if (entry.deleted) continue;
      const open = this.sessions.get(entry.sessionId);
      states.push({
        sessionId: entry.sessionId,
        workspacePath,
        title: entry.title,
        titleSource: entry.titleSource,
        createdAt: entry.createdAt,
        lastActivityAt: entry.lastActivityAt,
        phase: open?.session.isStreaming ? "running" : entry.phase,
        model: open ? selectionFor(open.session) : (entry.model ?? null),
        usage: open ? usageFromSession(open.session) : (entry.usage ?? EMPTY_USAGE),
        isRunning: open?.session.isStreaming ?? false,
      });
    }
    states.sort((a, b) => b.lastActivityAt - a.lastActivityAt);
    return states;
  }

  async listDeletedSessionIds(workspacePath: string): Promise<string[]> {
    await this.init();
    const entries = await this.metadata.list(workspacePath);
    return entries.filter((entry) => entry.deleted).map((entry) => entry.sessionId);
  }

  async history(sessionId: string): Promise<EngineMessage[]> {
    const open = this.requireOpen(sessionId);
    open.session.refreshContext();
    return toEngineHistory(open.session.messages);
  }

  subscribe(sessionId: string, listener: (event: EngineEvent) => void): EngineEventSubscription {
    const open = this.requireOpen(sessionId);
    const unsubscribe = open.session.subscribe((event: AgentSessionEvent) => {
      for (const translated of translatePiEvent(event)) listener(translated);
    });
    return { dispose: unsubscribe };
  }

  async submit(sessionId: string, text: string, options: SubmitOptions): Promise<SubmitOutcome> {
    const open = this.requireOpen(sessionId);
    const workspacePath = open.workspacePath;
    const meta = await this.metadata.get(workspacePath, sessionId);
    if (meta?.seenRequestIds?.includes(options.requestId)) {
      return { status: "duplicate", inputId: options.requestId };
    }
    if (options.modelSelection) await this.applySelection(sessionId, options.modelSelection);
    const now = Date.now();
    const title = meta?.title || defaultTitle(text);
    await this.metadata.upsert(workspacePath, sessionId, {
      lastActivityAt: now,
      phase: text.trim() ? "running" : (meta?.phase ?? "draft"),
      title,
      titleSource: meta?.titleSource ?? "default",
      seenRequestId: options.requestId,
      ...(selectionFor(open.session) ? { model: selectionFor(open.session) ?? undefined } : {}),
    });
    if (text.trim().length > 0) {
      const inputId = await this.startPrompt(workspacePath, sessionId, text, options.requestId);
      return { status: "accepted", inputId };
    }
    this.emitSessionsChanged(workspacePath);
    return { status: "accepted", inputId: options.requestId };
  }

  async abort(sessionId: string): Promise<void> {
    const open = this.sessions.get(sessionId);
    if (!open) return;
    await open.session.abort();
    await this.metadata.upsert(open.workspacePath, sessionId, {
      phase: "completedInterrupted",
      lastActivityAt: Date.now(),
    });
    this.emitSessionsChanged(open.workspacePath);
  }

  async rename(sessionId: string, title: string): Promise<void> {
    const open = this.requireOpen(sessionId);
    open.session.setSessionName(title);
    await this.metadata.upsert(open.workspacePath, sessionId, {
      title,
      titleSource: "custom",
      lastActivityAt: Date.now(),
    });
    this.emitSessionsChanged(open.workspacePath);
  }

  async setModel(sessionId: string, selection: ModelSelection | undefined): Promise<void> {
    if (!selection) return;
    await this.applySelection(sessionId, selection);
  }

  async catalog(): Promise<EngineCatalog> {
    await this.init();
    if (this.catalogCache) return this.catalogCache;
    this.catalogCache = await buildCatalog(this.requireRuntime(), this.settingsManager);
    return this.catalogCache;
  }

  onSessionsChanged(listener: (workspacePath: string) => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  async dispose(): Promise<void> {
    for (const open of this.sessions.values()) {
      open.unsubscribe();
      open.session.dispose();
    }
    this.sessions.clear();
    this.listeners.clear();
  }

  // ── internals ──

  private attach(workspacePath: string, session: AgentSession): void {
    const sessionId = session.sessionId;
    this.sessions.get(sessionId)?.unsubscribe();
    const unsubscribe = session.subscribe((event: AgentSessionEvent) => {
      void this.onPiEvent(workspacePath, sessionId, event);
    });
    this.sessions.set(sessionId, { workspacePath, session, unsubscribe, inflight: null });
  }

  private async onPiEvent(
    workspacePath: string,
    sessionId: string,
    event: AgentSessionEvent,
  ): Promise<void> {
    if (event.type === "message_end") {
      const message = event.message;
      if (message.role === "user") {
        await this.metadata.upsert(workspacePath, sessionId, { lastActivityAt: Date.now() });
        this.emitSessionsChanged(workspacePath);
      } else if (message.role === "assistant" && message.stopReason === "error") {
        await this.metadata.upsert(workspacePath, sessionId, {
          phase: "error",
          lastActivityAt: Date.now(),
        });
        this.emitSessionsChanged(workspacePath);
      }
      return;
    }
    if (event.type === "agent_settled") {
      const open = this.sessions.get(sessionId);
      await this.metadata.upsert(workspacePath, sessionId, {
        phase: event.aborted ? "completedInterrupted" : "completedSuccess",
        lastActivityAt: Date.now(),
        ...(open ? { usage: usageFromSession(open.session) } : {}),
        ...(open && selectionFor(open.session)
          ? { model: selectionFor(open.session) ?? undefined }
          : {}),
      });
      this.emitSessionsChanged(workspacePath);
    }
  }

  private async startPrompt(
    workspacePath: string,
    sessionId: string,
    text: string,
    requestId?: string,
  ): Promise<string> {
    const open = this.requireOpen(sessionId);
    const inputId = requestId ?? `input_${Date.now()}_${Math.random().toString(36).slice(2, 10)}`;
    await this.metadata.upsert(workspacePath, sessionId, {
      lastActivityAt: Date.now(),
      phase: "running",
    });
    // prompt() resolves when the whole run settles. The link's call/result must not wait
    // for it, so the promise is retained only to keep it alive and update metadata.
    open.inflight = open.session
      .prompt(text, { streamingBehavior: "followUp" })
      .catch(async (error: unknown) => {
        await this.metadata.upsert(workspacePath, sessionId, {
          phase: "error",
          lastActivityAt: Date.now(),
        });
        this.options.logger.warn("pi prompt failed", {
          sessionId,
          message: error instanceof Error ? error.message : String(error),
        });
        this.emitSessionsChanged(workspacePath);
      })
      .finally(() => {
        open.inflight = null;
      });
    this.emitSessionsChanged(workspacePath);
    return inputId;
  }

  private async applySelection(sessionId: string, selection: ModelSelection): Promise<void> {
    const open = this.requireOpen(sessionId);
    const runtime = this.requireRuntime();
    const model = runtime.getModel(selection.providerId, selection.modelId);
    if (!model) throw new Error(`Unknown model ${selection.providerId}/${selection.modelId}`);
    await open.session.setModel(model);
    const level = selection.options?.reasoningLevel;
    if (level) {
      // pi clamps to the model's supported set; ask for the requested level and let it
      // land on the nearest supported one (the UI only sends levels we advertised).
      open.session.setThinkingLevel(clampThinkingLevel(model, level as never) as never);
    }
  }

  private requireRuntime(): ModelRuntime {
    if (!this.modelRuntime) throw new Error("Engine is not initialized");
    return this.modelRuntime;
  }

  private requireOpen(sessionId: string): OpenSession {
    const open = this.sessions.get(sessionId);
    if (!open) throw new Error(`Session ${sessionId} is not open`);
    return open;
  }

  private sessionDir(workspacePath: string): string {
    const digest = createHash("sha1").update(workspacePath).digest("hex");
    return join(this.options.dataDir, "sessions", digest);
  }

  private async findSessionFile(
    workspacePath: string,
    sessionId: string,
  ): Promise<string | undefined> {
    const dir = this.sessionDir(workspacePath);
    const files = await readdir(dir).catch(() => [] as string[]);
    const match = files.find((name) => name.endsWith(`_${sessionId}.jsonl`));
    return match ? join(dir, match) : undefined;
  }

  private emitSessionsChanged(workspacePath: string): void {
    for (const listener of this.listeners) listener(workspacePath);
  }
}
