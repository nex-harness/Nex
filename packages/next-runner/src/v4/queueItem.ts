import type { QueueItem } from "@nex/shared/nex-protocol-v4";

/**
 * Pure queue-item construction and list edits (docs/next/conversation-features.md
 * decision 4, research §5 item schema). Everything here is a pure function of its inputs so
 * the queue controller stays the only state owner and every list rewrite is testable
 * without an engine.
 */

/** `auto` = the composer did not override the session routing. */
export type RequestedDelivery = "auto" | "startNow" | "queue" | "guide";
export type AdmittedDelivery = "startNow" | "queue" | "guide";

export interface NewQueueItemInput {
  sourceCommandId: string;
  queueItemId: string;
  clientId: string;
  text: string;
  attachments?: QueueItem["attachments"];
  modelSelection?: QueueItem["modelSelection"];
  mode?: QueueItem["mode"];
  planEnabled?: boolean;
  requested: RequestedDelivery;
  admitted: AdmittedDelivery;
  fallbackReasonCode?: string;
  admissionSeq: number;
  admittedAt: number;
  toolDisallowlist?: string[];
}

/**
 * Guide items stay in the runner queue as pending rows until pi consumes them, so they use
 * `steer.state:"guided"` and the same `dispatch.state:"queued"` as any other parked item
 * (`queueItemSchema` narrows dispatch to queued|reserved|promoting — there is no "admitted").
 */
export function createQueueItem(input: NewQueueItemInput): QueueItem {
  return {
    sourceCommandId: input.sourceCommandId,
    queueItemId: input.queueItemId,
    clientId: input.clientId,
    kind: "sendText",
    text: input.text,
    attachments: input.attachments ? [...input.attachments] : [],
    ...(input.modelSelection ? { modelSelection: input.modelSelection } : {}),
    ...(input.mode ? { mode: input.mode } : {}),
    ...(input.planEnabled !== undefined ? { planEnabled: input.planEnabled } : {}),
    delivery: {
      requested: input.requested,
      admitted: input.admitted,
      ...(input.fallbackReasonCode ? { fallbackReasonCode: input.fallbackReasonCode } : {}),
    },
    order: { admissionSeq: input.admissionSeq },
    steer: input.admitted === "guide" ? { state: "guided" } : { state: "notRequested" },
    dispatch: { state: "queued" },
    admittedAt: input.admittedAt,
    // Lineage: a promoted item keeps the command that originally produced it, so the UI can
    // settle its pending command and a retried sendText is still recognisable.
    provenance: { sourceCommandId: input.sourceCommandId, queueItemId: input.queueItemId },
    ...(input.toolDisallowlist && input.toolDisallowlist.length > 0
      ? { toolDisallowlist: [...input.toolDisallowlist] }
      : {}),
  };
}

/** Rewrites `order.queuePosition` for the visible order (guide items included). */
export function withQueuePositions(items: readonly QueueItem[]): QueueItem[] {
  return items.map((item, index) => ({
    ...item,
    order: { ...item.order, queuePosition: index },
  }));
}

export function findItem(items: readonly QueueItem[], queueItemId: string): QueueItem | undefined {
  return items.find((item) => item.queueItemId === queueItemId);
}

/** Returns the new list, or null when the id is absent (caller answers `queue.itemMissing`). */
export function removeQueueItem(
  items: readonly QueueItem[],
  queueItemId: string,
): QueueItem[] | null {
  if (!items.some((item) => item.queueItemId === queueItemId)) return null;
  return items.filter((item) => item.queueItemId !== queueItemId);
}

export function replaceQueueItemText(
  items: readonly QueueItem[],
  queueItemId: string,
  text: string,
): QueueItem[] | null {
  if (!items.some((item) => item.queueItemId === queueItemId)) return null;
  return items.map((item) => (item.queueItemId === queueItemId ? { ...item, text } : item));
}

/**
 * Moves `queueItemId` so it sits directly before `beforeQueueItemId`; a null anchor means the
 * tail (protocol `reorderQueueItem`). Returns null when either id is unknown or the item is
 * already at that position, so the caller can answer a no-op instead of bumping revision.
 */
export function moveQueueItem(
  items: readonly QueueItem[],
  queueItemId: string,
  beforeQueueItemId: string | null,
): QueueItem[] | null {
  const from = items.findIndex((item) => item.queueItemId === queueItemId);
  if (from < 0) return null;
  if (beforeQueueItemId === queueItemId) return null;
  const rest = items.filter((item) => item.queueItemId !== queueItemId);
  let target = rest.length;
  if (beforeQueueItemId !== null) {
    const index = rest.findIndex((item) => item.queueItemId === beforeQueueItemId);
    if (index < 0) return null;
    target = index;
  }
  const next = [...rest.slice(0, target), items[from] as QueueItem, ...rest.slice(target)];
  if (next.every((item, index) => item.queueItemId === items[index]?.queueItemId)) return null;
  return next;
}

/** First item the drain may promote: guide items are pi-owned pending rows, never queued text. */
export function nextDrainableItem(items: readonly QueueItem[]): QueueItem | undefined {
  return items.find((item) => item.delivery.admitted !== "guide");
}

/**
 * Consumes one occurrence of each pending guide text from pi's steering queue, in admission
 * order. pi removes a steering message right before emitting `message_start` for it, so a
 * text that is no longer in the queue has been delivered. Returns the ids to drop.
 */
export function consumedGuideIds(
  items: readonly QueueItem[],
  steering: readonly string[],
): string[] {
  const remaining = new Map<string, number>();
  for (const text of steering) remaining.set(text, (remaining.get(text) ?? 0) + 1);
  const consumed: string[] = [];
  for (const item of items) {
    if (item.delivery.admitted !== "guide") continue;
    const count = remaining.get(item.text) ?? 0;
    if (count > 0) remaining.set(item.text, count - 1);
    else consumed.push(item.queueItemId);
  }
  return consumed;
}
