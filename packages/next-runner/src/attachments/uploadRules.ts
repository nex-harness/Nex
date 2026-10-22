import { PROTOCOL_V4_LIMITS } from "@nex/shared/nex-protocol-v4";
import {
  AttachmentError,
  type AttachmentMeta,
  type BeginResult,
  type BeginUploadInput,
} from "./types.js";

/**
 * Upload-transaction invariants (spec A): idempotent begin results, the protocol limits on
 * the declared metadata, and replay-metadata equality.
 */

export function beginResult(meta: AttachmentMeta): BeginResult {
  if (meta.state === "committed" && meta.ref) {
    return {
      uploadId: meta.uploadId,
      state: "committed",
      nextChunkIndex: meta.totalChunks,
      ref: meta.ref,
    };
  }
  return { uploadId: meta.uploadId, state: "staging", nextChunkIndex: meta.writtenChunks };
}

export function assertBeginInput(input: BeginUploadInput): void {
  const { attachmentMaxBytes, attachmentUploadMaxChunks } = PROTOCOL_V4_LIMITS;
  if (!Number.isInteger(input.totalBytes) || input.totalBytes < 0) {
    throw new AttachmentError("invalidPayload", "invalid totalBytes");
  }
  if (input.totalBytes > attachmentMaxBytes) {
    throw new AttachmentError("uploadLimit", "totalBytes exceeds the protocol limit");
  }
  if (!Number.isInteger(input.totalChunks) || input.totalChunks < 0) {
    throw new AttachmentError("invalidPayload", "invalid totalChunks");
  }
  if (input.totalChunks > attachmentUploadMaxChunks) {
    throw new AttachmentError("uploadLimit", "totalChunks exceeds the protocol limit");
  }
  if ((input.totalBytes === 0) !== (input.totalChunks === 0)) {
    throw new AttachmentError("invalidPayload", "zero-byte upload must declare zero chunks");
  }
  if (!/^sha256:[0-9a-f]{64}$/i.test(input.checksum)) {
    throw new AttachmentError("invalidPayload", "invalid checksum");
  }
  if (!/^[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*\/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*$/.test(input.mime)) {
    throw new AttachmentError("invalidPayload", "invalid mime");
  }
}

export function sameUpload(meta: AttachmentMeta, input: BeginUploadInput): boolean {
  return (
    meta.totalBytes === input.totalBytes &&
    meta.totalChunks === input.totalChunks &&
    meta.checksum.toLowerCase() === input.checksum.toLowerCase() &&
    meta.mime.toLowerCase() === input.mime.toLowerCase() &&
    meta.sessionId === input.sessionId &&
    meta.uploadId === input.uploadId
  );
}
