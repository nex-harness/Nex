import type { AttachmentRef } from "@nex/shared/nex-protocol-v4";
import { baseMime } from "./mime.js";

/**
 * Normalizes the refs an admitted command carried (`sendText`, `createSession.firstInput`,
 * `editUserQuery`): display metadata is the composer's (fileName/mime/bytes), and a missing
 * or odd mime degrades to `application/octet-stream` instead of rejecting the input.
 */
export function normalizeAdmittedRefs(refs: readonly AttachmentRef[] | undefined): AttachmentRef[] {
  if (!refs || refs.length === 0) return [];
  return refs.map((ref) => ({
    ref: String(ref.ref),
    fileName: String(ref.fileName || "attachment"),
    mime: baseMime(String(ref.mime)) || "application/octet-stream",
    bytes: Number.isFinite(ref.bytes) ? Math.max(0, ref.bytes) : 0,
    ...(typeof ref.previewRef === "string" ? { previewRef: ref.previewRef } : {}),
  }));
}
