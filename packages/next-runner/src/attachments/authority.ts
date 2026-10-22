import type { AttachmentRef } from "@nex/shared/nex-protocol-v4";
import type { ProjectionState } from "../projection/state.js";

/**
 * Attachment ref → bytes authority (spec A).
 *
 * A ref becomes readable only when a **write of this session** lists it: a userInput row,
 * a queued item, the live turn's admitted input, or (restore) a `nex.attachments` entry on
 * the active branch. Nothing else grants access, so a guessed ref never reads a blob.
 *
 * Both sides use the same predicate:
 * - read/preview authorization (host → `isReferenced`)
 * - the sweep (a blob stays while any listed ref names it)
 */
export interface AttachmentAuthoritySource {
  /** Every write this session's projection currently shows. */
  listed: () => readonly AttachmentRef[][];
}

export function attachmentsOfProjection(state: ProjectionState): AttachmentRef[][] {
  const result: AttachmentRef[][] = [];
  for (const row of state.rows) {
    if (row.kind === "userInput" && row.attachments && row.attachments.length > 0) {
      result.push(row.attachments);
    }
  }
  for (const item of state.queue.items) {
    if (item.attachments && item.attachments.length > 0) result.push(item.attachments);
  }
  return result;
}

export function projectionListsRef(state: ProjectionState, ref: string): boolean {
  return attachmentsOfProjection(state).some((list) => list.some((item) => item.ref === ref));
}
