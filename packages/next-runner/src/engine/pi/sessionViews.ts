import type { AgentSession } from "@earendil-works/pi-coding-agent";
import type {
  EngineModelState,
  EngineSessionState,
  EngineUsage,
  PermissionMode,
} from "../types.js";
import type { SessionMetaEntry } from "./metaStore.js";

/** Live facts the engine reads from an open pi session (absent = restored-only row). */
export interface LiveSessionView {
  session: AgentSession;
  streaming: boolean;
  mode: PermissionMode;
  planEnabled: boolean;
}

/**
 * Tears down one in-process pi session. Abort is best-effort: disposal still ends the
 * session even when pi refuses to abort an already-settled run.
 */
export async function teardownSession(open: {
  session: AgentSession;
  gate: { dispose(): void };
  unsubscribe: () => void;
  /** File checkpoints (spec B): flush a finished turn's record before the session closes. */
  checkpoints?: { flushNow(): void };
}): Promise<void> {
  // Persist a settled turn before its session file loses the writer.
  open.checkpoints?.flushNow();
  try {
    await open.session.abort();
  } catch {
    // Best-effort.
  }
  open.gate.dispose();
  open.unsubscribe();
  open.session.dispose();
}

/**
 * Metadata entries → UI-facing session states (docs/next/task-state.md). Open sessions
 * contribute live phase/model/usage/mode; closed ones fall back to the persisted row.
 */
export function toEngineSessionStates(
  workspacePath: string,
  entries: readonly SessionMetaEntry[],
  live: (sessionId: string) => LiveSessionView | undefined,
  helpers: {
    selectionFor: (session: AgentSession) => EngineModelState | null;
    usageFromSession: (session: AgentSession) => EngineUsage;
    emptyUsage: EngineUsage;
  },
): EngineSessionState[] {
  const states: EngineSessionState[] = [];
  for (const entry of entries) {
    if (entry.deleted) continue;
    const open = live(entry.sessionId);
    states.push({
      sessionId: entry.sessionId,
      workspacePath,
      title: entry.title,
      titleSource: entry.titleSource,
      createdAt: entry.createdAt,
      lastActivityAt: entry.lastActivityAt,
      phase: open?.streaming ? "running" : entry.phase,
      model: open ? helpers.selectionFor(open.session) : (entry.model ?? null),
      usage: open ? helpers.usageFromSession(open.session) : (entry.usage ?? helpers.emptyUsage),
      isRunning: open?.streaming ?? false,
      mode: open ? open.mode : (entry.mode ?? "build"),
      planEnabled: open ? open.planEnabled : (entry.planEnabled ?? false),
      ...(entry.followupMode ? { followupMode: entry.followupMode } : {}),
      pinned: entry.pinned === true,
      archived: entry.archived === true,
      ...(typeof entry.unreadAt === "number" ? { unreadAt: entry.unreadAt } : {}),
    });
  }
  states.sort((a, b) => b.lastActivityAt - a.lastActivityAt);
  return states;
}
