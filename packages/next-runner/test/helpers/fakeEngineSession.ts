import type * as T from "../../src/engine/types.js";
import { modelState } from "./fakeEngineData.js";
import type { FakeEngine } from "./fakeEngine.js";

/** Session lifecycle half of the `FakeEngine` double (kept apart for the file budget). */
export class SessionLifecycle {
  constructor(private readonly host: FakeEngine) {}

  init = async (): Promise<void> => {};

  /** Test hook: adds a catalog model so a switch can change the model identity. */
  registerModel = (info: T.EngineModelInfo): void => {
    this.host.catalogValue = {
      ...this.host.catalogValue,
      models: [...this.host.catalogValue.models, info],
    };
  };

  createSession = async (
    workspacePath: string,
    options: T.CreateEngineSessionOptions,
  ): Promise<{ sessionId: string }> => {
    this.host.sequence += 1;
    const sessionId = `sess-${this.host.sequence}`;
    const now = 1_700_000_000_000 + this.host.sequence;
    this.host.sessions.set(sessionId, {
      sessionId,
      workspacePath,
      title: "",
      titleSource: "default",
      createdAt: now,
      lastActivityAt: now,
      phase: "draft",
      model: modelState(options.modelSelection),
      usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      isRunning: false,
    });
    this.host.histories.set(sessionId, []);
    this.host.emitSessionsChanged(workspacePath);
    return { sessionId };
  };

  openSession = async (_workspacePath: string, sessionId: string): Promise<void> => {
    if (this.host.tombstones.has(sessionId) || !this.host.sessions.has(sessionId)) {
      throw new Error(`session ${sessionId} not found`);
    }
  };

  deleteSession = async (workspacePath: string, sessionId: string): Promise<void> => {
    this.host.deleted.push({ workspacePath, sessionId });
    this.host.sessions.delete(sessionId);
    this.host.tombstones.add(sessionId);
    this.host.emitSessionsChanged(workspacePath);
  };

  listSessions = async (workspacePath: string): Promise<T.EngineSessionState[]> =>
    [...this.host.sessions.values()].filter((item) => item.workspacePath === workspacePath);

  listDeletedSessionIds = async (_workspacePath?: string): Promise<string[]> => [
    ...this.host.tombstones,
  ];

  history = async (sessionId: string): Promise<T.EngineMessage[]> =>
    this.host.histories.get(sessionId) ?? [];

  subscribe = (
    sessionId: string,
    listener: (e: T.EngineEvent) => void,
  ): T.EngineEventSubscription => {
    const set = this.host.listeners.get(sessionId) ?? new Set();
    set.add(listener);
    this.host.listeners.set(sessionId, set);
    return { dispose: () => set.delete(listener) };
  };
}
