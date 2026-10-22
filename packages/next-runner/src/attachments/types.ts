/**
 * Attachment store types (docs/next/attachments-rewind.md section A). The store is the only
 * writer of blobs; callers see refs + metadata and never a path derived from user input.
 */

export interface AttachmentMeta {
  uploadId: string;
  sessionId: string;
  fileName: string;
  /** Sanitised display name (the ref's last segment). */
  safeName: string;
  mime: string;
  totalBytes: number;
  totalChunks: number;
  checksum: string;
  state: "staging" | "committed";
  /** Owning V4 connection; chunk/commit must present the same one. */
  connectionId: string;
  createdAt: number;
  updatedAt: number;
  /** Bytes written so far. */
  writtenBytes: number;
  writtenChunks: number;
  ref?: string;
  /** Effective media mime after commit (sniffed for media; declared otherwise). */
  mediaType?: string;
}

/** One upload's in-memory bookkeeping; `meta.json` is the durable copy. */
export interface StagedUpload {
  dir: string;
  meta: AttachmentMeta;
}

/** Display metadata of one committed blob (never the bytes). */
export interface AttachmentInfo {
  ref: string;
  fileName: string;
  mime: string;
  mediaType: string;
  bytes: number;
  /** Absolute on-disk path of the committed blob (what pi's `read` tool needs). */
  path: string;
  mtimeMs: number;
}

export interface BeginUploadInput {
  connectionId: string;
  uploadId: string;
  sessionId: string;
  fileName: string;
  mime: string;
  totalBytes: number;
  totalChunks: number;
  checksum: string;
}

export type BeginResult =
  | { uploadId: string; state: "staging"; nextChunkIndex: number }
  | { uploadId: string; state: "committed"; nextChunkIndex: number; ref: string };

export type AttachmentStoreError =
  | "invalidPayload"
  | "uploadNotFound"
  | "uploadForbidden"
  | "uploadConflict"
  | "chunkOutOfOrder"
  | "chunkMismatch"
  | "uploadLimit"
  | "checksumMismatch"
  | "mimeMismatch"
  | "notFound"
  | "notAuthorized"
  | "invalidRef"
  | "notMedia";

export class AttachmentError extends Error {
  constructor(
    readonly reasonCode: AttachmentStoreError,
    message: string,
  ) {
    super(message);
    this.name = "AttachmentError";
  }
}
