/**
 * Row `entityId` derivation (docs/next/conversation-features.md decision 1).
 *
 * The id is a pure function of pi's persisted session-entry ids, so the live projection and
 * the restore path (which reads `SessionManager.getBranch()`) produce the same identity for
 * the same row. That is what makes an action target (`{rowId, entityId}`) survive a runner
 * restart: rowIds are renumbered per `logEpoch`, entityIds are not.
 *
 * Scheme:
 * - `pi:<userEntryId>`                  userInput row
 * - `pi:turn:<userEntryId>`             its turnHeader (no user entry → `pi:turn:anon:<n>`)
 * - `pi:<assistantEntryId>:text:<n>`    nth non-empty text part of one assistant message
 * - `pi:<assistantEntryId>:thinking:<n>` nth thinking part
 * - `pi:tool:<toolCallId>`              toolCall row (toolCallId is stable and unique)
 * - `pi:<compactionEntryId>`            compaction timeline marker
 *
 * Tool rows deliberately do not embed the assistant entry id: live tool rows are created
 * from `toolcall_start` before the assistant message is persisted, and restore must not
 * change their identity once it becomes known.
 */

export const ENTITY_ID_PREFIX = "pi:";

export function userEntityId(entryId: string): string {
  return `${ENTITY_ID_PREFIX}${entryId}`;
}

export function turnEntityId(entryId: string | undefined, ordinal: number): string {
  return entryId ? `${ENTITY_ID_PREFIX}turn:${entryId}` : `${ENTITY_ID_PREFIX}turn:anon:${ordinal}`;
}

export function assistantPartEntityId(
  entryId: string,
  kind: "text" | "thinking",
  segmentIndex: number,
): string {
  return `${ENTITY_ID_PREFIX}${entryId}:${kind}:${segmentIndex}`;
}

export function toolEntityId(toolCallId: string): string {
  return `${ENTITY_ID_PREFIX}tool:${toolCallId}`;
}

export function compactionEntityId(entryId: string): string {
  return `${ENTITY_ID_PREFIX}${entryId}`;
}

/**
 * Runner-written custom entry (`nex.fork`): its id is not a message id, so the marker row
 * gets its own identity — stable across restore like every other pi entry.
 */
export function runnerMarkerEntityId(entryId: string): string {
  return `${ENTITY_ID_PREFIX}marker:${entryId}`;
}

/** Extracts the pi entry id from an entityId built by the helpers above. */
export function entryIdFromEntityId(entityId: string | undefined): string | undefined {
  if (!entityId?.startsWith(ENTITY_ID_PREFIX)) return undefined;
  const rest = entityId.slice(ENTITY_ID_PREFIX.length);
  if (rest.startsWith("turn:") || rest.startsWith("tool:") || rest.startsWith("marker:")) {
    return undefined;
  }
  const [entryId] = rest.split(":");
  return entryId || undefined;
}

/** Extracts the toolCallId from a `pi:tool:<id>` entityId. */
export function toolCallIdFromEntityId(entityId: string | undefined): string | undefined {
  if (!entityId?.startsWith(`${ENTITY_ID_PREFIX}tool:`)) return undefined;
  return entityId.slice(`${ENTITY_ID_PREFIX}tool:`.length) || undefined;
}
