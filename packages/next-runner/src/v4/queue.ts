import type {
  CommandEnvelope,
  ModelSelection,
  QueueItem,
  QueueState,
} from "@nex/shared/nex-protocol-v4";
import type { Engine, EngineEvent } from "../engine/types.js";
import type { Logger } from "../logger.js";
import type { ProjectionState } from "../projection/state.js";
import { randomId } from "../util/ids.js";
import { deriveInputRouting } from "../projection/inputRouting.js";
import {
  COMPACT_COMMAND_TEXT,
  consumedGuideIds,
  createQueueItem,
  findItem,
  messageOf,
  moveQueueItem,
  nextDrainableItem,
  removeQueueItem,
  replaceQueueItemText,
  withQueuePositions,
  withTimeout,
  type AdmittedDelivery,
  type RequestedDelivery,
} from "./queueItem.js";

/** Bounded preemption barrier: how long to wait for pi to release a run. */
const IDLE_TIMEOUT_MS = 10_000;

export type HeldQueueDisposition = "clearQueueAndSend" | "keepQueueAndSend";

/** Admission decision for one `sendText` (echoed into the ACK). */
export interface SendTextAdmission {
  delivery: AdmittedDelivery;
  /** Present only when the text landed in the runner queue (queue/guide). */
  queueItemId?: string;
}

export interface SendTextPayload {
  text: string;
  requestedDelivery?: "startNow" | "queue" | "guide";
  heldQueueDisposition?: HeldQueueDisposition;
  expectedHeldQueueItemIds?: string[];
  modelSelection?: ModelSelection;
  mode?: string;
  planEnabled?: boolean;
}

/** Outcome of a `compact` admission (decision 7); the handler maps it to an ACK. */
export type CompactAdmission =
  | { delivery: "startNow"; sourceCommandId: string }
  | { delivery: "queue"; queueItemId: string };

export class QueueAdmissionError extends Error {
  constructor(
    readonly reasonCode: string,
    message: string,
  ) {
    super(message);
    this.name = "QueueAdmissionError";
  }
}

export type QueueEditResult = "edited" | "missing" | "notEditable";

export interface SessionQueueDeps {
  engine: Engine;
  sessionId: string;
  logger: Logger;
  /** Current projection (read-only); the queue derives routing from it. */
  projection: () => ProjectionState;
  /** Single write path into the projection cache the snapshot reads. */
  commit: (queue: QueueState) => void;
  /** Asks the session store for a throttled snapshot publish. */
  requestPublish: () => void;
  /** Runs one queued `compact` item (wired to the compact owner; the queue only dispatches). */
  runQueuedCompact: (sourceCommandId: string) => Promise<void>;
}

/**
 * Runner-owned queue (docs/next/conversation-features.md decision 4, research §5).
 *
 * Single writer of queued items; `projection.queue` is only a cache written through
 * `commit`, and pi's queues are used for `guide` only. The drain promotes one item per
 * `agent_settled` (single-flight); Stop or a failed turn pauses (`autoDrain=false` +
 * `pauseReason`). Items are in-memory only (old CLI parity). A `kind:"compact"` item is a
 * typed `/compact` intent run by the compact owner, never a user row.
 */
export class SessionQueue {
  private items: QueueItem[] = [];
  private autoDrain = true;
  private pauseReason: QueueState["pauseReason"];
  private admissionSeq = 0;
  private reservationSeq = 0;
  private draining = false;
  /** >0 while a preemption (startNow / sendQueuedNow) owns the next `settled`. */
  private preempting = 0;

  constructor(private readonly deps: SessionQueueDeps) {}

  snapshot(): QueueState {
    return {
      items: this.items.map((item) => ({ ...item })),
      autoDrain: this.autoDrain,
      ...(this.pauseReason ? { pauseReason: this.pauseReason } : {}),
    };
  }

  item(queueItemId: string): QueueItem | undefined {
    return findItem(this.items, queueItemId);
  }

  // ── admission ──

  /** `sendText` admission (decision 4; full matrix in docs/next/conversation-features.md). */
  async admitSendText(
    envelope: CommandEnvelope,
    payload: SendTextPayload,
  ): Promise<SendTextAdmission> {
    if (payload.text.trim().length === 0) {
      throw new QueueAdmissionError("proto.invalidPayload", "input must not be empty");
    }
    const requested: RequestedDelivery = payload.requestedDelivery ?? "auto";
    const busy = this.isBusy();
    const routing = this.inputRoutingMode();

    if (routing === "choice") {
      // Held queue: clear/keep is required before anything starts, startNow included.
      this.applyHeldDisposition(payload);
    }

    if (requested === "startNow") {
      if (busy) await this.preempt();
      await this.startNow(envelope, payload);
      return { delivery: "startNow" };
    }

    if (!busy) {
      // queue/guide on an idle session still starts now: there is no run to join.
      await this.startNow(envelope, payload);
      return { delivery: "startNow" };
    }

    const delivery: AdmittedDelivery =
      requested === "queue"
        ? "queue"
        : requested === "guide"
          ? "guide"
          : routing === "guide"
            ? "guide"
            : "queue";

    if (delivery === "guide") {
      const steered = await this.deps.engine.steer(this.deps.sessionId, payload.text);
      if (steered) {
        const item = this.appendItem(envelope, payload, requested, "guide");
        return { delivery: "guide", queueItemId: item.queueItemId };
      }
      // pi could not take a steer (run ending): keep the text queued with the fallback.
      const item = this.appendItem(envelope, payload, requested, "queue", {
        fallbackReasonCode: "guide.notSteerable",
      });
      return { delivery: "queue", queueItemId: item.queueItemId };
    }

    const item = this.appendItem(envelope, payload, requested, "queue");
    return { delivery: "queue", queueItemId: item.queueItemId };
  }

  /**
   * Decision 7: busy → a `kind:"compact"` FIFO item; idle → the caller starts it now. A
   * running/queued compaction holds the lock (old CLI dedupe: one at a time).
   */
  admitCompact(envelope: CommandEnvelope): CompactAdmission {
    if (this.hasCompactInFlight() || this.compactQueued()) {
      throw new QueueAdmissionError("compactOperationLock", "a compaction is already active");
    }
    if (!this.isBusy()) return { delivery: "startNow", sourceCommandId: envelope.commandId };
    const item = this.appendItem(envelope, { text: COMPACT_COMMAND_TEXT }, "auto", "queue", {
      kind: "compact",
    });
    return { delivery: "queue", queueItemId: item.queueItemId };
  }

  /** True while a manual compact is queued (pi's own state is the engine's fact). */
  compactQueued(): boolean {
    return this.items.some((item) => item.kind === "compact");
  }

  // ── queue commands ──

  /** False when the id is already gone (concurrent drain / duplicate delete). */
  delete(queueItemId: string): boolean {
    const next = removeQueueItem(this.items, queueItemId);
    if (next === null) return false;
    this.setItems(next);
    return true;
  }

  /** `beforeQueueItemId: null` = tail; unknown ids are a no-op (false). */
  reorder(queueItemId: string, beforeQueueItemId: string | null): boolean {
    const next = moveQueueItem(this.items, queueItemId, beforeQueueItemId);
    if (next === null) return false;
    this.setItems(next);
    return true;
  }

  /** Protocol-only (the UI restores the composer instead); compact items are not editable. */
  edit(queueItemId: string, newText: string): QueueEditResult {
    const item = findItem(this.items, queueItemId);
    if (!item) return "missing";
    if (item.kind !== "sendText") return "notEditable";
    const next = replaceQueueItemText(this.items, queueItemId, newText);
    if (next === null) return "missing";
    this.setItems(next);
    return "edited";
  }

  /** Busy: arms the flag. Idle: re-arms and drains immediately (old CLI parity). */
  async setAutoDrain(autoDrain: boolean): Promise<void> {
    if (!autoDrain) {
      this.pause("manual");
      return;
    }
    if (this.autoDrain && this.pauseReason === undefined) return;
    this.autoDrain = true;
    this.pauseReason = undefined;
    this.commit();
    await this.drain();
  }

  /** `sendQueuedNow`: abort, wait idle, reserve → promote. "missing"/"busy" outcomes. */
  async sendQueuedNow(queueItemId: string): Promise<"promoted" | "missing" | "busy"> {
    const item = findItem(this.items, queueItemId);
    if (!item) return "missing";
    if (item.dispatch.state !== "queued") return "busy";
    if (item.delivery.admitted === "guide") {
      // A pending guide is already inside pi's queue; there is nothing to promote.
      return "busy";
    }
    await this.preempt();
    const promoted = await this.promote(item.queueItemId);
    return promoted ? "promoted" : "missing";
  }

  // ── engine-driven transitions ──

  /** The settle event is the only drain trigger; a preemption owns it while waiting. */
  afterEvent(event: EngineEvent, projection: ProjectionState): void {
    if (event.type === "pi_queue_update") {
      this.reconcileGuides(event.steering);
      return;
    }
    if (event.type !== "settled") return;
    if (this.preempting > 0) return;
    if (event.aborted) {
      // Stop or any abort holds the queue so items are not replayed into a run the user
      // just interrupted; pending guides count too (pi would inject them next run).
      if (this.items.length > 0) this.pause("stopped");
      return;
    }
    if (projection.phase === "error") {
      if (this.items.length > 0) this.pause("error");
      return;
    }
    this.demoteUndeliveredGuides();
    void this.drain();
  }

  /** `stop` pauses the queue without dropping items (spec decision 4). */
  pauseForStop(): void {
    if (this.items.length > 0) this.pause("stopped");
  }

  /** Failed/cancelled compaction holds the remaining items (old CLI `goal-compact.ts`). */
  pauseAfterCompactFailure(): void {
    if (this.items.length > 0) this.pause("error");
  }

  // ── internals ──

  private isBusy(): boolean {
    // A promotion on its way to pi and pi's streaming flag (before the fold sees
    // `user_input`) both count: starting another run would double-submit.
    return (
      isRunningPhase(this.deps.projection().phase) ||
      this.draining ||
      this.items.some((item) => item.dispatch.state !== "queued") ||
      this.deps.engine.runState(this.deps.sessionId).streaming
    );
  }

  /** Same table the snapshot publishes (delegated so the two can never disagree). */
  private inputRoutingMode(): "startNow" | "enqueue" | "guide" | "choice" {
    const { phase, followupMode } = this.deps.projection();
    const route = deriveInputRouting({ phase, followupMode, queue: this.snapshot() }).mode;
    return route === "reject" ? "enqueue" : route;
  }

  private applyHeldDisposition(payload: SendTextPayload): void {
    const disposition = payload.heldQueueDisposition;
    if (!disposition) {
      throw new QueueAdmissionError(
        "heldQueueDispositionRequired",
        "held queue requires clearQueueAndSend or keepQueueAndSend",
      );
    }
    // A concurrent add/delete invalidates what the confirmation dialog showed.
    const expected = payload.expectedHeldQueueItemIds;
    const current = new Set(this.items.map((item) => item.queueItemId));
    if (
      expected &&
      (expected.length !== current.size || !expected.every((id) => current.has(id)))
    ) {
      throw new QueueAdmissionError(
        "guard.heldQueueConfirmationStale",
        "held queue changed after the confirmation was shown",
      );
    }
    if (disposition === "clearQueueAndSend") this.items = [];
    this.autoDrain = true;
    this.pauseReason = undefined;
    this.commit();
  }

  private async startNow(envelope: CommandEnvelope, payload: SendTextPayload): Promise<void> {
    try {
      await this.deps.engine.submit(this.deps.sessionId, payload.text, {
        requestId: envelope.commandId,
        ...(payload.modelSelection ? { modelSelection: payload.modelSelection } : {}),
        ...(payload.mode ? { mode: payload.mode } : {}),
        ...(payload.planEnabled !== undefined ? { planEnabled: payload.planEnabled } : {}),
      });
    } catch (error) {
      throw new QueueAdmissionError("send_failed", messageOf(error));
    }
  }

  private appendItem(
    envelope: CommandEnvelope,
    payload: SendTextPayload,
    requested: RequestedDelivery,
    admitted: AdmittedDelivery,
    options: { fallbackReasonCode?: string; kind?: QueueItem["kind"] } = {},
  ): QueueItem {
    const { fallbackReasonCode, kind } = options;
    this.admissionSeq += 1;
    const item = createQueueItem({
      sourceCommandId: envelope.commandId,
      queueItemId: randomId("qi"),
      clientId: envelope.clientId || "runner",
      ...(kind ? { kind } : {}),
      text: payload.text,
      ...(payload.modelSelection ? { modelSelection: payload.modelSelection } : {}),
      ...(payload.mode ? { mode: payload.mode as QueueItem["mode"] } : {}),
      ...(payload.planEnabled !== undefined ? { planEnabled: payload.planEnabled } : {}),
      requested,
      admitted,
      ...(fallbackReasonCode ? { fallbackReasonCode } : {}),
      admissionSeq: this.admissionSeq,
      admittedAt: Date.now(),
    });
    this.setItems([...this.items, item]);
    return item;
  }

  private hasDrainable(): boolean {
    return this.items.some((item) => item.delivery.admitted !== "guide");
  }

  private hasCompactInFlight(): boolean {
    // pi's own state plus the projection's open marker both mean "compaction running".
    const state = this.deps.engine.compactState(this.deps.sessionId);
    return (
      state.manualInFlight ||
      state.compacting ||
      Object.keys(this.deps.projection().compactOperations).length > 0
    );
  }

  private pause(reason: NonNullable<QueueState["pauseReason"]>): void {
    if (!this.autoDrain && this.pauseReason === reason) return;
    this.autoDrain = false;
    this.pauseReason = reason;
    this.commit();
  }

  /** One promotion per drain (single-flight): reserved → promoting → run → remove. */
  private async drain(): Promise<void> {
    if (this.draining) return;
    const head = this.autoDrain ? nextDrainableItem(this.items) : undefined;
    if (!head || !this.deps.engine.runState(this.deps.sessionId).idle) return;
    this.draining = true;
    try {
      await this.promote(head.queueItemId);
    } finally {
      this.draining = false;
    }
  }

  /** Any failure before the run starts rolls the item back to `queued` in place. */
  private async promote(queueItemId: string): Promise<boolean> {
    const item = findItem(this.items, queueItemId);
    if (!item || item.dispatch.state !== "queued") return false;
    this.reservationSeq += 1;
    const reservationId = `reservation-${this.reservationSeq}`;
    this.markDispatch(queueItemId, { state: "reserved", reservationId });
    this.markDispatch(queueItemId, { state: "promoting", reservationId });
    try {
      if (item.kind === "compact") {
        // The compact owner runs it and owns the marker; the queue only dispatches.
        await this.deps.runQueuedCompact(item.sourceCommandId);
      } else {
        await this.deps.engine.submit(this.deps.sessionId, item.text, {
          requestId: item.sourceCommandId,
          ...(item.modelSelection ? { modelSelection: item.modelSelection } : {}),
          ...(item.mode ? { mode: item.mode } : {}),
          ...(item.planEnabled !== undefined ? { planEnabled: item.planEnabled } : {}),
        });
      }
    } catch (error) {
      this.deps.logger.warn("queue promotion failed", {
        sessionId: this.deps.sessionId,
        queueItemId,
        message: messageOf(error),
      });
      this.markDispatch(queueItemId, { state: "queued" });
      return false;
    }
    const next = removeQueueItem(this.items, queueItemId);
    if (next) this.setItems(next);
    return true;
  }

  private markDispatch(queueItemId: string, dispatch: QueueItem["dispatch"]): void {
    const next = this.items.map((item) =>
      item.queueItemId === queueItemId ? { ...item, dispatch } : item,
    );
    this.setItems(next);
  }

  /** Absence from pi's `queue_update` steering list is the guide-consumed signal. */
  private reconcileGuides(steering: readonly string[]): void {
    const consumed = consumedGuideIds(this.items, steering);
    if (consumed.length === 0) return;
    const drop = new Set(consumed);
    this.setItems(this.items.filter((item) => !drop.has(item.queueItemId)));
  }

  /** Demote first, then clear pi's queue (clearing first would look like consumption). */
  private demoteUndeliveredGuides(): void {
    const pending = this.items.filter((item) => item.delivery.admitted === "guide");
    if (pending.length === 0) return;
    const demoted = this.items.map((item) =>
      item.delivery.admitted === "guide"
        ? {
            ...item,
            delivery: {
              requested: item.delivery.requested,
              admitted: "queue" as const,
              fallbackReasonCode: "guide.notDelivered",
            },
            steer: { state: "fellBack" as const, reasonCode: "guide.notDelivered" },
          }
        : item,
    );
    this.setItems(demoted);
    // Nothing is admitted as a guide now, so `queue_update` cannot consume an item.
    this.deps.engine.clearPiQueue(this.deps.sessionId);
  }

  /** Abort + bounded idle barrier shared by startNow-while-busy and sendQueuedNow. */
  private async preempt(): Promise<void> {
    this.preempting += 1;
    try {
      await this.deps.engine.abort(this.deps.sessionId);
      await withTimeout(this.deps.engine.waitForIdle(this.deps.sessionId), IDLE_TIMEOUT_MS).catch(
        (error: unknown) =>
          this.deps.logger.warn("queue preemption idle wait timed out", {
            sessionId: this.deps.sessionId,
            message: messageOf(error),
          }),
      );
    } finally {
      this.preempting -= 1;
    }
  }

  private setItems(items: QueueItem[]): void {
    this.items = withQueuePositions(items);
    this.commit();
  }

  private commit(): void {
    this.deps.commit(this.snapshot());
    this.deps.requestPublish();
  }
}

function isRunningPhase(phase: ProjectionState["phase"]): boolean {
  return phase === "running" || phase === "prewarming";
}
