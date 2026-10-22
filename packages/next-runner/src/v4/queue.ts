import type {
  AttachmentRef,
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
  /** Present when the text landed in the runner queue (queue/guide). */
  queueItemId?: string;
}

export interface SendTextPayload {
  text: string;
  /** Admitted attachment refs (spec A). */
  attachments?: AttachmentRef[];
  requestedDelivery?: "startNow" | "queue" | "guide";
  heldQueueDisposition?: HeldQueueDisposition;
  expectedHeldQueueItemIds?: string[];
  modelSelection?: ModelSelection;
  mode?: string;
  planEnabled?: boolean;
}

export type CompactAdmission =
  | { delivery: "startNow"; sourceCommandId: string }
  | { delivery: "queue"; queueItemId: string };

export class QueueAdmissionError extends Error {
  constructor(
    readonly reasonCode: string,
    message: string,
  ) {
    super(message);
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
  /** Asks the session store for a throttled publish. */
  requestPublish: () => void;
  /** Runs one queued `compact` item (the compact owner runs it; the queue dispatches). */
  runQueuedCompact: (sourceCommandId: string) => Promise<void>;
  /** Attachments-only input: no pi user message, the runtime appends its row. */
  appendTextlessInput?: (text: string, attachments: AttachmentRef[]) => void;
}

/** Runner-owned queue (decision 4): single writer of queued items, `projection.queue` cache. */
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
    if (payload.text.trim().length === 0 && (payload.attachments?.length ?? 0) === 0) {
      throw new QueueAdmissionError("proto.invalidPayload", "input must not be empty");
    }
    const requested: RequestedDelivery = payload.requestedDelivery ?? "auto";
    const busy = this.isBusy();
    const routing = this.inputRoutingMode();

    // Held queue: clear/keep is required before anything starts, startNow included.
    if (routing === "choice") this.applyHeldDisposition(payload);

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
      const steered = await this.deps.engine.steer(
        this.deps.sessionId,
        payload.text,
        payload.attachments,
      );
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

  /** Decision 7: busy → a `kind:"compact"` FIFO item; idle → starts now. One at a time. */
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

  /** `beforeQueueItemId: null` = tail; unknown ids are a no-op. */
  reorder(queueItemId: string, beforeQueueItemId: string | null): boolean {
    const next = moveQueueItem(this.items, queueItemId, beforeQueueItemId);
    if (next === null) return false;
    this.setItems(next);
    return true;
  }

  /** Protocol-only (the UI restores the composer); compact items are not editable. */
  edit(queueItemId: string, newText: string): QueueEditResult {
    const item = findItem(this.items, queueItemId);
    if (!item) return "missing";
    if (item.kind !== "sendText") return "notEditable";
    const next = replaceQueueItemText(this.items, queueItemId, newText);
    if (next === null) return "missing";
    this.setItems(next);
    return "edited";
  }

  /** Busy: arms the flag. Idle: re-arms and drains immediately. */
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

  /** `sendQueuedNow`: abort, wait idle, reserve → promote. */
  async sendQueuedNow(queueItemId: string): Promise<"promoted" | "missing" | "busy"> {
    const item = findItem(this.items, queueItemId);
    if (!item) return "missing";
    if (item.dispatch.state !== "queued") return "busy";
    // A pending guide is already inside pi's queue; there is nothing to promote.
    if (item.delivery.admitted === "guide") return "busy";
    await this.preempt();
    const promoted = await this.promote(item.queueItemId);
    return promoted ? "promoted" : "missing";
  }

  // ── engine-driven transitions ──

  /** The settle event is the only drain trigger; a preemption owns it while waiting. */
  afterEvent(event: EngineEvent, projection: ProjectionState): void {
    if (event.type === "pi_queue_update") return this.reconcileGuides(event.steering);
    if (event.type !== "settled" || this.preempting > 0) return;
    // Stop or any abort holds the queue so items are not replayed into a run the user
    // just interrupted; pending guides count too (pi would inject them next run).
    if (event.aborted) {
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

  /** Failed/cancelled compaction holds the remaining items. */
  pauseAfterCompactFailure(): void {
    if (this.items.length > 0) this.pause("error");
  }

  // ── internals ──

  /** A promotion in flight and pi's streaming flag both count (double-submit guard). */
  private isBusy(): boolean {
    const { phase } = this.deps.projection();
    return (
      isRunningPhase(phase) ||
      this.draining ||
      this.items.some((item) => item.dispatch.state !== "queued") ||
      this.deps.engine.runState(this.deps.sessionId).streaming
    );
  }

  /** Same table the snapshot publishes (delegated so the two never disagree). */
  private inputRoutingMode() {
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

  /** Starts one input now; a failed submit is an admission error, not a queued retry. */
  private async startNow(envelope: CommandEnvelope, payload: SendTextPayload): Promise<void> {
    try {
      await this.submitNow(envelope.commandId, payload.text, payload);
    } catch (error) {
      throw new QueueAdmissionError("send_failed", messageOf(error));
    }
  }
  private submitNow(
    requestId: string,
    text: string,
    payload: Omit<
      SendTextPayload,
      "requestedDelivery" | "heldQueueDisposition" | "expectedHeldQueueItemIds"
    >,
  ): Promise<unknown> {
    const { attachments, modelSelection, mode, planEnabled } = payload;
    return this.deps.engine.submit(this.deps.sessionId, text, {
      requestId,
      ...attachmentFields(attachments),
      ...(modelSelection ? { modelSelection } : {}),
      ...(mode ? { mode: mode as QueueItem["mode"] } : {}),
      ...(planEnabled !== undefined ? { planEnabled } : {}),
    });
  }

  private appendItem(
    envelope: CommandEnvelope,
    payload: SendTextPayload,
    requested: RequestedDelivery,
    admitted: AdmittedDelivery,
    options: { fallbackReasonCode?: string; kind?: QueueItem["kind"] } = {},
  ): QueueItem {
    this.admissionSeq += 1;
    const { attachments, modelSelection, mode, planEnabled } = payload;
    const item = createQueueItem({
      sourceCommandId: envelope.commandId,
      queueItemId: randomId("qi"),
      clientId: envelope.clientId || "runner",
      ...(options.kind ? { kind: options.kind } : {}),
      text: payload.text,
      ...attachmentFields(attachments),
      ...(modelSelection ? { modelSelection } : {}),
      ...(mode ? { mode: mode as QueueItem["mode"] } : {}),
      ...(planEnabled !== undefined ? { planEnabled } : {}),
      requested,
      admitted,
      ...(options.fallbackReasonCode ? { fallbackReasonCode: options.fallbackReasonCode } : {}),
      admissionSeq: this.admissionSeq,
      admittedAt: Date.now(),
    });
    this.setItems([...this.items, item]);
    return item;
  }

  private hasCompactInFlight(): boolean {
    // pi's own state plus the projection's open marker both mean "compaction running".
    const state = this.deps.engine.compactState(this.deps.sessionId);
    const open = Object.keys(this.deps.projection().compactOperations).length > 0;
    return state.manualInFlight || state.compacting || open;
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
        await this.submitNow(item.sourceCommandId, item.text, item);
        // Attachments-only input persists no user message; the runtime appends the row.
        if (item.text.trim().length === 0) this.deps.appendTextlessInput?.("", item.attachments);
      }
    } catch (error) {
      this.warn("queue promotion failed", error);
      this.markDispatch(queueItemId, { state: "queued" });
      return false;
    }
    const next = removeQueueItem(this.items, queueItemId);
    if (next) this.setItems(next);
    return true;
  }

  private markDispatch(queueItemId: string, dispatch: QueueItem["dispatch"]): void {
    this.setItems(
      this.items.map((item) => (item.queueItemId === queueItemId ? { ...item, dispatch } : item)),
    );
  }

  /** Absence from pi's `queue_update` steering list is the guide-consumed signal. */
  private reconcileGuides(steering: readonly string[]): void {
    const consumed = consumedGuideIds(this.items, steering);
    if (consumed.length === 0) return;
    const drop = new Set(consumed);
    this.setItems(this.items.filter((item) => !drop.has(item.queueItemId)));
  }

  /** Demote, then clear pi's queue (clearing first would look like consumption). */
  private demoteUndeliveredGuides(): void {
    if (!this.items.some((item) => item.delivery.admitted === "guide")) return;
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

  /** Abort + bounded idle barrier (startNow-while-busy, sendQueuedNow). */
  private async preempt(): Promise<void> {
    this.preempting += 1;
    try {
      await this.deps.engine.abort(this.deps.sessionId);
      // Bounded: a run that never reports idle must not hold the queue forever.
      const idle = withTimeout(this.deps.engine.waitForIdle(this.deps.sessionId), IDLE_TIMEOUT_MS);
      await idle.catch((error: unknown) =>
        this.warn("queue preemption idle wait timed out", error),
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

  private warn(message: string, error: unknown): void {
    this.deps.logger.warn(message, { sessionId: this.deps.sessionId, message: messageOf(error) });
  }
}

// Attachments travel only when present (the protocol defaults the list to empty).
const attachmentFields = (attachments: readonly AttachmentRef[] | undefined) =>
  attachments?.length ? { attachments: [...attachments] } : {};

const isRunningPhase = (phase: ProjectionState["phase"]) =>
  phase === "running" || phase === "prewarming";
