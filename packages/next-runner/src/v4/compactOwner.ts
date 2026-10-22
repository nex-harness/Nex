import type { Engine, EngineCompactOutcome } from "../engine/types.js";
import type { Logger } from "../logger.js";
import {
  autoCompactOperationId,
  finishCompactMarker,
  openAutoOperationId,
  startCompactMarker,
  COMPACT_OPERATION_LOCK,
} from "../projection/compactEvents.js";
import { bump, type ProjectionState } from "../projection/state.js";

/**
 * The runner's compact owner for one session (docs/next/conversation-features.md decision 7).
 *
 * **Owner.** The projection's `compactOperations` + marker rows are written only here (via
 * the fold helpers in `compactEvents.ts`), together with the `engine.compact()` call and
 * the queue's `runQueuedCompact` port. Automatic compactions reach it as engine-observed
 * outcomes, so a pi threshold/overflow compaction produces the same `origin:"auto"` marker.
 *
 * **Ordering.**
 * ```
 * compact cmd ─▶ lock? ─▶ busy? → queue(compact) → ACK queue
 *                   └─ idle → startMarker(running) → engine.compact() → finishMarker
 * agent_settled ─▶ takeCompactOutcomes() → open auto marker? finish : start+finish
 * ```
 * A failed or cancelled manual compaction pauses the queue (`autoDrain=false`), exactly
 * like a failed turn; queued items are then held for an explicit Resume.
 *
 * The engine reports two streams for one automatic operation: the translated
 * `compact_start`/`compact_end` events (fold-owned marker, keyed `auto:<startAt>`) and the
 * observed outcome used as a safety net when an end arrives without its start.
 */
export interface CompactOwnerDeps {
  engine: Engine;
  sessionId: string;
  logger: Logger;
  projection: () => ProjectionState;
  /** Single write path into the projection state. */
  apply: (next: ProjectionState) => void;
  /** Asks the session store for a throttled publish. */
  requestPublish: () => void;
  /** Pauses a non-empty queue after a failed/cancelled compaction (old CLI parity). */
  pauseQueueAfterFailure: () => void;
}

export class CompactOwner {
  /** Manual compactions this owner is running (queued item promotion included). */
  private running = 0;

  constructor(private readonly deps: CompactOwnerDeps) {}

  /**
   * Runs one manual compaction and owns its marker. Never throws for a pi failure: the
   * outcome carries the terminal status.
   */
  async runManual(sourceCommandId: string): Promise<EngineCompactOutcome> {
    const operationId = `manual:${sourceCommandId}`;
    this.write(
      startCompactMarker(this.deps.projection(), {
        at: Date.now(),
        origin: "manual",
        operationId,
        sourceCommandId,
      }),
    );
    this.running += 1;
    try {
      const outcome = await this.deps.engine.compact(this.deps.sessionId, sourceCommandId);
      this.write(
        finishCompactMarker(this.deps.projection(), {
          at: Date.now(),
          operationId,
          sourceCommandId,
          outcome,
        }),
      );
      this.afterTerminal(outcome);
      return outcome;
    } finally {
      this.running -= 1;
    }
  }

  /**
   * Queue drain port: a promoted compact item runs exactly like a manual command. A
   * duplicate that would overlap another compaction is refused (the item is dropped by
   * the caller's rollback and its ACK was already `queue`).
   */
  async runQueued(sourceCommandId: string): Promise<void> {
    if (this.manualActive()) {
      throw Object.assign(new Error("a compaction is already running"), {
        reasonCode: COMPACT_OPERATION_LOCK,
      });
    }
    await this.runManual(sourceCommandId);
  }

  /**
   * Called after the fold applies each engine event. An automatic compaction's marker is
   * normally created by its translated start/end events; this only closes the gap for an
   * end that arrived without a start (pi replied "nothing to compact" and emitted neither
   * a start nor a result), so no operation is ever invisible.
   */
  afterEvent(projection: ProjectionState): void {
    const outcomes = this.deps.engine.takeCompactOutcomes(this.deps.sessionId);
    if (outcomes.length === 0) return;
    for (const outcome of outcomes) {
      if (outcome.origin === "manual") continue;
      if (openAutoOperationId(projection) !== undefined) continue;
      const operationId = autoCompactOperationId(Date.now());
      const started = startCompactMarker(projection, {
        at: Date.now(),
        origin: "auto",
        operationId,
      });
      this.write(finishCompactMarker(started, { at: Date.now(), operationId, outcome }));
      this.afterTerminal(outcome);
    }
  }

  /** True while a manual compact is running or its marker is open (admission lock). */
  manualActive(): boolean {
    if (this.running > 0) return true;
    return Object.keys(this.deps.projection().compactOperations).length > 0;
  }

  private afterTerminal(outcome: EngineCompactOutcome): void {
    if (outcome.status === "failed" || outcome.status === "cancelled") {
      this.deps.pauseQueueAfterFailure();
    }
    this.deps.logger.debug("compact finished", {
      sessionId: this.deps.sessionId,
      status: outcome.status,
      origin: outcome.origin,
    });
  }

  private write(next: ProjectionState): void {
    if (next === this.deps.projection()) return;
    this.deps.apply(bump(next));
    this.deps.requestPublish();
  }
}
