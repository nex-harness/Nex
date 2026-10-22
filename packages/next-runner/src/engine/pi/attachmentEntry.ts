import type { AttachmentRef } from "@nex/shared/nex-protocol-v4";

/**
 * `nex.attachments` custom entry (spec A): the admitted refs of one user message, written
 * right behind the user entry it annotates. A custom entry (unlike a custom_message) never
 * participates in the model context; restore only reads it to re-render `userInput.attachments`.
 *
 * Data shape (versioned so a future shape can be rejected instead of mis-read):
 * ```
 * { version: 1, userEntryId: string, attachments: AttachmentRef[] }
 * ```
 */
export interface NexAttachmentsEntryData {
  version: 1;
  userEntryId: string;
  attachments: AttachmentRef[];
}

export const ATTACHMENTS_ENTRY_VERSION = 1;

export function attachmentsEntryData(
  userEntryId: string,
  attachments: readonly AttachmentRef[],
): NexAttachmentsEntryData {
  return { version: ATTACHMENTS_ENTRY_VERSION, userEntryId, attachments: [...attachments] };
}

function isAttachmentRef(value: unknown): value is AttachmentRef {
  if (typeof value !== "object" || value === null) return false;
  const record = value as Record<string, unknown>;
  return (
    typeof record["ref"] === "string" &&
    typeof record["fileName"] === "string" &&
    typeof record["mime"] === "string" &&
    typeof record["bytes"] === "number" &&
    (record["previewRef"] === undefined || typeof record["previewRef"] === "string")
  );
}

/**
 * Parses the `data` of a `nex.attachments` entry into `userEntryId → refs`. Malformed
 * entries are ignored (never throw during restore).
 */
export function parseAttachmentsEntry(data: unknown): Map<string, AttachmentRef[]> {
  const result = new Map<string, AttachmentRef[]>();
  if (typeof data !== "object" || data === null) return result;
  const record = data as Record<string, unknown>;
  const userEntryId = record["userEntryId"];
  const attachments = record["attachments"];
  if (record["version"] !== ATTACHMENTS_ENTRY_VERSION) return result;
  // `pending`: written at `message_start`, before pi assigned the user entry id. Restore
  // pairs such markers with user messages by branch order (`withAttachmentMarkers`).
  if (typeof userEntryId !== "string") return result;
  if (!Array.isArray(attachments)) return result;
  const refs = attachments.filter(isAttachmentRef);
  if (refs.length !== attachments.length) return result;
  result.set(userEntryId, refs);
  return result;
}

/**
 * Restore join: a user message gets the refs of the `nex.attachments` entry that follows it
 * on the active branch. Markers written at `message_start` carry the placeholder id
 * `pending` (pi assigns the user entry id later), so they pair with the nearest preceding
 * user message; a marker whose id is known pairs by id even across interleaved entries.
 */
export function withAttachmentMarkers<T extends EngineMessageLike>(
  messages: readonly T[],
  branch: readonly unknown[],
): T[] {
  const byEntry = new Map<string, AttachmentRef[]>();
  const pending: string[] = [];
  for (const entry of branch) {
    if (typeof entry !== "object" || entry === null) continue;
    const record = entry as Record<string, unknown>;
    if (record["type"] === "custom" && record["customType"] === ATTACHMENTS_CUSTOM_TYPE) {
      for (const [userEntryId, list] of parseAttachmentsEntry(record["data"])) {
        if (userEntryId === "pending") pending.push(record["id"] as string);
        else byEntry.set(userEntryId, list);
      }
      continue;
    }
    if (record["type"] !== "message") continue;
    const message = record["message"] as { role?: string } | undefined;
    if (message?.role !== "user" || pending.length === 0) continue;
    const markerId = pending.shift() as string;
    const refs = readMarkerRefs(branch, markerId);
    if (refs) byEntry.set(String(record["id"]), refs);
  }
  if (byEntry.size === 0) return [...messages];
  return messages.map((message) =>
    message.role === "user" && message.entryId && byEntry.has(message.entryId)
      ? ({ ...message, attachments: byEntry.get(message.entryId) } as T)
      : message,
  );
}

function readMarkerRefs(branch: readonly unknown[], markerId: string): AttachmentRef[] | undefined {
  for (const entry of branch) {
    if (typeof entry !== "object" || entry === null) continue;
    const record = entry as Record<string, unknown>;
    if (record["id"] !== markerId) continue;
    for (const [, list] of parseAttachmentsEntry(record["data"])) return list;
  }
  return undefined;
}

export const ATTACHMENTS_CUSTOM_TYPE = "nex.attachments";

/** Narrow view of `EngineMessage` this module needs (avoids an engine↔pi import cycle). */
export interface EngineMessageLike {
  role: string;
  entryId?: string;
  attachments?: AttachmentRef[];
}
