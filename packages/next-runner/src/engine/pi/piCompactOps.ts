import type { AgentSession, AgentSessionEvent } from "@earendil-works/pi-coding-agent";
import type { Logger } from "../../logger.js";
import type { EngineCompactOutcome, EngineCompactState } from "../types.js";
import type { OpenSession } from "./sessionLifecycle.js";
import { outcomeFromError } from "../../projection/compactEvents.js";

/**
 * Manual compaction around pi's `AgentSession.compact()` (docs/next/conversation-features.md
 * decision 7, research §1).
 *
 * pi reports start/end as events, but the engine still owns three facts the projection and
 * the queue need:
 * - the in-flight operation (its command id is attached to pi's events, which carry none);
 * - the terminal outcome (pi's `compact()` resolves a result or throws with a message);
 * - automatic compactions that settle outside a prompt call (pi's threshold/overflow path),
 *   which the engine observes and hands to the projection owner as outcomes.
 *
 * `compact()` never awaits usage of the session elsewhere: pi aborts the active run itself
 * first (`agent-session.ts:2794`). The runner only calls it when the queue says the session
 * is idle, so the abort is a no-op in the normal path.
 */
export class PiCompactOps {
  /** sessionId → command id of the manual compact currently running. */
  private readonly manual = new Map<string, string>();
  /** sessionId → outcomes pi reported that no caller awaited (automatic + lost manual). */
  private readonly outcomes = new Map<string, EngineCompactOutcome[]>();
  /** sessionId → auto compaction keyed by `compaction_start` timestamp, awaiting its end. */
  private readonly autoStarts = new Map<string, number[]>();

  constructor(
    private readonly deps: {
      logger: Logger;
      sessionFor: (sessionId: string) => AgentSession | undefined;
    },
  ) {}

  /**
   * pi-event hook, called before translation. Remembers the entry a compaction wrote
   * (`entry_appended` reports it, but its ordering with `compaction_end` is not
   * contractual — the branch after the end is authoritative) and, for an automatic
   * compaction, records the outcome the projection owner folds.
   */
  observe(
    sessionId: string,
    event: AgentSessionEvent,
    rememberEntry: (entryId: string) => void,
  ): void {
    if (event.type === "compaction_start") {
      this.onCompactionStart(sessionId, event.reason, Date.now());
      return;
    }
    if (event.type === "entry_appended" && event.entry.type === "compaction") {
      rememberEntry(event.entry.id);
      return;
    }
    if (event.type !== "compaction_end") return;
    this.onCompactionEnd(sessionId, event);
    const session = this.deps.sessionFor(sessionId);
    if (!session) return;
    const branch = session.sessionManager.getBranch();
    for (let index = branch.length - 1; index >= 0; index -= 1) {
      const entry = branch[index];
      if (entry?.type === "compaction") {
        rememberEntry(entry.id);
        return;
      }
    }
  }

  state(sessionId: string, queued: boolean): EngineCompactState {
    const session = this.deps.sessionFor(sessionId);
    return {
      manualInFlight: this.manual.has(sessionId),
      queued,
      compacting: session?.isCompacting ?? false,
      pendingOutcomes: [...(this.outcomes.get(sessionId) ?? [])],
    };
  }

  takeOutcomes(sessionId: string): EngineCompactOutcome[] {
    const pending = this.outcomes.get(sessionId) ?? [];
    this.outcomes.delete(sessionId);
    return pending;
  }

  /** The command id pi's compaction events should be attributed to (manual only). */
  activeCommandId(sessionId: string): string | undefined {
    return this.manual.get(sessionId);
  }

  /**
   * Runs one manual compaction and resolves with its terminal outcome. Rejects only when
   * the session is not open; every pi failure becomes `failed`/`noop`/`cancelled`.
   */
  async compact(sessionId: string, sourceCommandId: string): Promise<EngineCompactOutcome> {
    const session = this.deps.sessionFor(sessionId);
    if (!session) {
      return { status: "failed", origin: "manual", reasonCode: "session.notOpen" };
    }
    if (this.manual.has(sessionId)) {
      // The command layer already refuses a second compact; keep this as the engine-level
      // invariant so two `session.compact()` calls can never interleave.
      return { status: "failed", origin: "manual", reasonCode: "compactOperationLock" };
    }
    this.manual.set(sessionId, sourceCommandId);
    try {
      const result = await session.compact();
      return {
        status: "success",
        origin: "manual",
        tokensBefore: result.tokensBefore,
        ...(result.estimatedTokensAfter !== undefined
          ? { tokensAfter: result.estimatedTokensAfter }
          : {}),
      };
    } catch (error) {
      // pi rethrows its own message ("Compaction cancelled") after an abort, so the signal
      // is whether the session is still compacting — an un-aborted session was not cancelled.
      const stillCompacting = session.isCompacting;
      const mapped = outcomeFromError(error, !stillCompacting, messageOf);
      this.deps.logger.warn("manual compaction ended without a result", {
        sessionId,
        status: mapped.status,
        message: mapped.message,
      });
      return { status: mapped.status, origin: "manual", message: mapped.message };
    } finally {
      this.manual.delete(sessionId);
    }
  }

  /**
   * Observes pi's compaction events before translation. A manual `compaction_start` belongs
   * to the caller awaiting `compact()`; an automatic one is tracked so its `compaction_end`
   * (or a cancellation without a matching start) becomes an outcome the projection folds.
   */
  onCompactionStart(sessionId: string, reason: "manual" | "threshold" | "overflow", at: number) {
    if (reason === "manual") return;
    const starts = this.autoStarts.get(sessionId) ?? [];
    this.autoStarts.set(sessionId, [...starts, at]);
  }

  onCompactionEnd(
    sessionId: string,
    event: {
      reason: "manual" | "threshold" | "overflow";
      result?: { tokensBefore: number; estimatedTokensAfter?: number };
      aborted: boolean;
      errorMessage?: string;
    },
  ): void {
    if (event.reason === "manual") return;
    const starts = this.autoStarts.get(sessionId) ?? [];
    // A start immediately followed by an end is pi declining to compact (`result` absent):
    // no state changed, so it reports `noop`, not a failed marker.
    if (starts.length > 0) starts.pop();
    this.autoStarts.set(sessionId, starts);
    const outcome: EngineCompactOutcome = event.result
      ? {
          status: "success",
          origin: "auto",
          tokensBefore: event.result.tokensBefore,
          ...(event.result.estimatedTokensAfter !== undefined
            ? { tokensAfter: event.result.estimatedTokensAfter }
            : {}),
        }
      : { status: event.aborted ? "cancelled" : "failed", origin: "auto" };
    if (event.errorMessage) outcome.message = event.errorMessage;
    this.push(sessionId, outcome);
  }

  /** Drops tracking for a closed/deleted session (no snapshot can consume its outcomes). */
  forget(sessionId: string): void {
    this.manual.delete(sessionId);
    this.outcomes.delete(sessionId);
    this.autoStarts.delete(sessionId);
  }

  private push(sessionId: string, outcome: EngineCompactOutcome): void {
    const list = this.outcomes.get(sessionId) ?? [];
    this.outcomes.set(sessionId, [...list, outcome]);
  }
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/** Wires the engine's open-session map into the ops' read interface. */
export function compactOpsFor(logger: Logger, sessions: Map<string, OpenSession>): PiCompactOps {
  return new PiCompactOps({
    logger,
    sessionFor: (sessionId) => sessions.get(sessionId)?.session,
  });
}
