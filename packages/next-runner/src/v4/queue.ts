import type {
  CommandEnvelope,
  ModelSelection,
  QueueItem,
  QueueState,
} from "@nex/shared/nex-protocol-v4";
import type { Engine, EngineEvent, EngineFollowupMode } from "../engine/types.js";
import type { Logger } from "../logger.js";
import type { ProjectionState } from "../projection/state.js";
import { randomId } from "../util/ids.js";
import {
  consumedGuideIds,
  createQueueItem,
  findItem,
  moveQueueItem,
  nextDrainableItem,
  removeQueueItem,
  replaceQueueItemText,
  withQueuePositions,
  type AdmittedDelivery,
  type RequestedDelivery,
} from "./queueItem.js";

/** Bounded wait for pi to release a run after an abort (preemption barrier). */
const IDLE_TIMEOUT_MS = 10_000;

export type HeldQueueDisposition = "clearQueueAndSend" | "keepQueueAndSend";

/** Admission decision for one `sendText`, echoed into the ACK and the projection. */
export interface SendTextAdmission {
  delivery: AdmittedDelivery;
  /** Present only when the text actually landed in the runner queue (queue/guide). */
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
}

/**
 * Runner-owned queue (docs/next/conversation-features.md decision 4, research §5).
 *
 * **Owner.** This is the single writer of queued text for one session. `projection.queue` is
 * only a cache written through `commit`; nothing else mutates it. pi's own queues are never
 * used for queued text — only `guide`, which pi cannot address by id.
 *
 * **Ordering.** Admission appends in command order; the drain promotes at most one item per
 * `agent_settled` (single-flight), so a promoted prompt can never race a second one. Stop or
 * a failed turn pauses the queue (`autoDrain=false` + `pauseReason`); `setAutoDrain(true)`
 * re-arms it and drains immediately when idle.
 *
 * **Persistence.** Queued items are in-memory only, like the old CLI (`queue` is explicitly
 * not persisted: `snapshot.ts` "queue（不持久化）"). A runner restart drops them and the UI
 * sees them disappear; the user resends.
 */
export class SessionQueue {
  private items: QueueItem[] = [];
  private autoDrain = true;
  private pauseReason: QueueState["pauseReason"];
  private admissionSeq = 0;
  private reservationSeq = 0;
  private draining = false;
  /** >0 while an explicit preemption owns the next `settled` (startNow / sendQueuedNow). */
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

  /**
   * `sendText` admission matrix (spec decision 4):
   *
   * | state                          | requested      | result                       |
   * | ------------------------------ | -------------- | ---------------------------- |
   * | idle                           | auto           | start now                    |
   * | idle                           | queue / guide  | start now (nothing to join)  |
   * | idle (+held queue)             | auto           | choice confirmation, then start |
   * | running, followupMode=guide    | auto           | steer + pending guide item   |
   * | running, followupMode=queue    | auto           | enqueue                      |
   * | running                        | startNow       | abort, wait idle, start      |
   * | running                        | queue / guide  | that delivery                |
   */
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
      // Held queue: the user must pick clear/keep before anything starts, including an
      // explicit startNow (old CLI applies the disposition on both paths).
      this.applyHeldDisposition(payload);
    }

    if (requested === "startNow") {
      if (busy) await this.preempt();
      await this.startNow(envelope, payload);
      return { delivery: "startNow" };
    }

    if (!busy) {
      // A requested queue/guide delivery on an idle session still starts now: there is no
      // active run to join, and the old CLI's Core admission does the same.
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
      // pi could not take a steer (run already ending): keep the text in the runner queue
      // instead of dropping it, with the fallback recorded on the item.
      const item = this.appendItem(envelope, payload, requested, "queue", "guide.notSteerable");
      return { delivery: "queue", queueItemId: item.queueItemId };
    }

    const item = this.appendItem(envelope, payload, requested, "queue");
    return { delivery: "queue", queueItemId: item.queueItemId };
  }

  // ── queue commands ──

  /** Returns false when the id is already gone (concurrent drain / duplicate delete). */
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

  /**
   * Protocol-only (the UI restores the composer by deleting instead, research §5). Text
   * edits keep the position; a compact item is never editable.
   */
  edit(queueItemId: string, newText: string): QueueEditResult {
    const item = findItem(this.items, queueItemId);
    if (!item) return "missing";
    if (item.kind !== "sendText") return "notEditable";
    const next = replaceQueueItemText(this.items, queueItemId, newText);
    if (next === null) return "missing";
    this.setItems(next);
    return "edited";
  }

  /** Busy: only arms the flag. Idle: re-arms and drains immediately (old CLI parity). */
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

  /**
   * `sendQueuedNow`: abort the active run, wait for pi to be idle, then reserve → promote the
   * chosen item. autoDrain is preserved (the user is explicitly steering the queue, not
   * stopping work). Returns "missing" when the id is gone and "busy" when another writer
   * already reserved it.
   */
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

  /**
   * Called after the projection fold applied an engine event. Ordering guard: the settle
   * event is the only drain trigger, and a preemption owns it while it waits for idle.
   */
  afterEvent(event: EngineEvent, projection: ProjectionState): void {
    if (event.type === "pi_queue_update") {
      this.reconcileGuides(event.steering);
      return;
    }
    if (event.type !== "settled") return;
    if (this.preempting > 0) return;
    if (event.aborted) {
      // Stop, or an abort the user did not initiate (turn cancelled): hold the queue so the
      // remaining items are not silently replayed into a run the user just interrupted.
      // Pending guides count too — pi would otherwise inject them into the next run.
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

  // ── internals ──

  private isBusy(): boolean {
    const phase = this.deps.projection().phase;
    if (phase === "running" || phase === "prewarming") return true;
    // A promotion is already on its way to pi (`drain` set `draining` before awaiting, and
    // the item is reserved/promoting): starting another run here would double-submit.
    if (this.draining || this.items.some((item) => item.dispatch.state !== "queued")) return true;
    // Phase follows events; pi's streaming flag closes the gap right after a prompt starts
    // and before the fold sees `user_input` (starting twice would throw in pi).
    return this.deps.engine.runState(this.deps.sessionId).streaming;
  }

  private inputRoutingMode(): "startNow" | "enqueue" | "guide" | "choice" {
    const state = this.deps.projection();
    if (this.isBusy()) {
      return followedUpMode(state.followupMode) === "guide" ? "guide" : "enqueue";
    }
    const completed = state.phase === "completedSuccess" || state.phase === "completedInterrupted";
    if (completed && this.hasDrainable() && !this.autoDrain) return "choice";
    return "startNow";
  }

  private applyHeldDisposition(payload: SendTextPayload): void {
    const disposition = payload.heldQueueDisposition;
    if (!disposition) {
      throw new QueueAdmissionError(
        "heldQueueDispositionRequired",
        "held queue requires clearQueueAndSend or keepQueueAndSend",
      );
    }
    const expected = payload.expectedHeldQueueItemIds;
    if (expected) {
      // The confirmation dialog showed a specific set; a concurrent add/delete invalidates
      // it so the user never loses an item they did not see.
      const current = new Set(this.items.map((item) => item.queueItemId));
      const same = expected.length === current.size && expected.every((id) => current.has(id));
      if (!same) {
        throw new QueueAdmissionError(
          "guard.heldQueueConfirmationStale",
          "held queue changed after the confirmation was shown",
        );
      }
    }
    if (disposition === "clearQueueAndSend") {
      this.items = [];
      this.autoDrain = true;
      this.pauseReason = undefined;
      this.commit();
    } else {
      this.autoDrain = true;
      this.pauseReason = undefined;
      this.commit();
    }
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
    fallbackReasonCode?: string,
  ): QueueItem {
    this.admissionSeq += 1;
    const item = createQueueItem({
      sourceCommandId: envelope.commandId,
      queueItemId: this.nextItemId(),
      clientId: envelope.clientId || "runner",
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

  private nextItemId(): string {
    return randomId("qi");
  }

  private hasDrainable(): boolean {
    return this.items.some((item) => item.delivery.admitted !== "guide");
  }

  private pause(reason: NonNullable<QueueState["pauseReason"]>): void {
    if (!this.autoDrain && this.pauseReason === reason) return;
    this.autoDrain = false;
    this.pauseReason = reason;
    this.commit();
  }

  /**
   * One promotion per drain invocation (single-flight). Each item is reserved then promoted
   * before `engine.submit` starts the run, so a crash between the two leaves a visible
   * `promoting` row instead of a lost message.
   */
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

  /**
   * reserved → promoting → submit → remove. Any failure before the prompt starts rolls the
   * item back to `queued` in place, so nothing is lost and the UI unlocks the row.
   */
  private async promote(queueItemId: string): Promise<boolean> {
    const item = findItem(this.items, queueItemId);
    if (!item || item.dispatch.state !== "queued") return false;
    this.reservationSeq += 1;
    const reservationId = `reservation-${this.reservationSeq}`;
    this.markDispatch(queueItemId, { state: "reserved", reservationId });
    this.markDispatch(queueItemId, { state: "promoting", reservationId });
    try {
      await this.deps.engine.submit(this.deps.sessionId, item.text, {
        requestId: item.sourceCommandId,
        ...(item.modelSelection ? { modelSelection: item.modelSelection } : {}),
        ...(item.mode ? { mode: item.mode } : {}),
        ...(item.planEnabled !== undefined ? { planEnabled: item.planEnabled } : {}),
      });
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

  /**
   * A guide item is consumed when pi emits `queue_update` without its text. pi removes the
   * steering entry before `message_start`, so absence is the delivery signal.
   */
  private reconcileGuides(steering: readonly string[]): void {
    const consumed = consumedGuideIds(this.items, steering);
    if (consumed.length === 0) return;
    const drop = new Set(consumed);
    this.setItems(this.items.filter((item) => !drop.has(item.queueItemId)));
  }

  /**
   * Undelivered guides at settle: pi keeps unconsumed steering messages and would inject them
   * into the *next* run invisibly. Demote them to normal queued items first, then clear pi's
   * queue — clearing first would emit `queue_update` and make the still-pending items look
   * consumed (dropping the text entirely).
   */
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
    // Now nothing is admitted as a guide, so `queue_update` cannot consume an item.
    this.deps.engine.clearPiQueue(this.deps.sessionId);
  }

  /** Abort + bounded idle barrier shared by startNow-while-busy and sendQueuedNow. */
  private async preempt(): Promise<void> {
    this.preempting += 1;
    try {
      await this.deps.engine.abort(this.deps.sessionId);
      await withTimeout(this.deps.engine.waitForIdle(this.deps.sessionId), IDLE_TIMEOUT_MS).catch(
        (error: unknown) => {
          this.deps.logger.warn("queue preemption idle wait timed out", {
            sessionId: this.deps.sessionId,
            message: messageOf(error),
          });
        },
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

function followedUpMode(mode: EngineFollowupMode): EngineFollowupMode {
  return mode === "guide" ? "guide" : "queue";
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function withTimeout<T>(promise: Promise<T>, timeoutMs: number): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("timed out")), timeoutMs);
    promise.then(
      (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      (error: unknown) => {
        clearTimeout(timer);
        reject(error);
      },
    );
  });
}
