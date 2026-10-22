import { createHash } from "node:crypto";
import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { PROTOCOL_V4_LIMITS } from "@nex/shared/nex-protocol-v4";
import { writeJsonAtomic } from "../util/fs.js";
import {
  BLOB_FILE,
  MAGIC_BYTES,
  META_FILE,
  PART_FILE,
  appendChunkBytes,
  decodeBase64Strict,
  readAt,
  statBlob,
  uploadKey,
  verifyMagic,
} from "./helpers.js";
import { parseRunnerRef, resolveAttachmentDir, runnerRef, sanitizeFileName } from "./refs.js";
import {
  AttachmentError,
  type AttachmentInfo,
  type AttachmentMeta,
  type BeginResult,
  type BeginUploadInput,
  type StagedUpload,
} from "./types.js";
import { assertBeginInput, beginResult, sameUpload } from "./uploadRules.js";
import { startAttachmentSweeper, type SweeperHandle } from "./sweeper.js";
import { listUploadDirectories, pruneStagingDirs, sweepBlobs } from "./cleanup.js";

/**
 * Runner-owned attachment blob store (docs/next/attachments-rewind.md section A).
 *
 * ```
 * <dataDir>/attachments/<sessionId>/<uploadId>/meta.json
 *                                           /blob.part   (staged bytes, then renamed)
 *                                           /blob        (committed)
 * ```
 *
 * The store is the only writer of blobs. Bytes arrive base64 in the V4 upload transaction;
 * `begin`/`appendChunk`/`commit` enforce exactly the protocol limits, and `commit` checks
 * the SHA-256 the client declared plus the media magic bytes. Refs are grammar-checked and
 * resolved inside `<dataDir>/attachments`; a ref is only readable through a host-supplied
 * membership predicate (row/queue item), never on its own.
 */
export interface AttachmentStoreOptions {
  dataDir: string;
  /** Clock seam for tests. */
  now?: () => number;
  /** Per-session disk quota (committed + staging). */
  sessionQuotaBytes?: number;
}

/** Per-session disk quota for committed + staging bytes (spec §A "storage + limits"). */
export const attachmentSessionQuotaBytes = 512 * 1024 * 1024;

export class AttachmentStore {
  readonly dataDir: string;
  private readonly staged = new Map<string, StagedUpload>();
  private readonly nowValue: () => number;
  private readonly quotaBytes: number;
  private sweeper: SweeperHandle | null = null;

  constructor(options: AttachmentStoreOptions) {
    this.dataDir = options.dataDir;
    this.nowValue = options.now ?? Date.now;
    this.quotaBytes = options.sessionQuotaBytes ?? attachmentSessionQuotaBytes;
  }

  /** A staging upload by its key (tests and diagnostics). */
  stagedUpload(
    connectionId: string,
    sessionId: string,
    uploadId: string,
  ): StagedUpload | undefined {
    return this.staged.get(uploadKey(connectionId, sessionId, uploadId));
  }

  // ── registry (begin → chunk* → commit / abort) ──

  /**
   * Idempotent on `(connectionId, sessionId, uploadId)` and on the metadata: a repeated
   * begin replays the staging cursor, an already-committed upload replays its ref, and a
   * different metadata for the same id is a conflict.
   */
  async begin(input: BeginUploadInput): Promise<BeginResult> {
    assertBeginInput(input);
    const key = uploadKey(input.connectionId, input.sessionId, input.uploadId);
    const existing = this.staged.get(key);
    if (existing) {
      if (!sameUpload(existing.meta, input)) {
        throw new AttachmentError("uploadConflict", "uploadId reused with different metadata");
      }
      return beginResult(existing.meta);
    }
    const dir = resolveAttachmentDir(this.dataDir, input.sessionId, input.uploadId);
    if (!dir) throw new AttachmentError("invalidPayload", "invalid attachment ids");
    const onDisk = await readMeta(dir);
    if (onDisk) {
      if (!sameUpload(onDisk, input)) {
        throw new AttachmentError("uploadConflict", "uploadId reused with different metadata");
      }
      // Runner restarted mid-upload: continue from the durable byte count; commit hashes
      // the whole `.part` file anyway.
      const entry: StagedUpload = { dir, meta: { ...onDisk, connectionId: input.connectionId } };
      this.register(entry);
      return beginResult(entry.meta);
    }
    if (this.stagingCount() >= PROTOCOL_V4_LIMITS.attachmentUploadMaxConcurrent) {
      throw new AttachmentError("uploadLimit", "too many concurrent uploads");
    }
    const staged = this.stagingBytes() + input.totalBytes;
    if (staged > PROTOCOL_V4_LIMITS.attachmentUploadMaxStagedBytes) {
      throw new AttachmentError("uploadLimit", "staged byte budget exhausted");
    }
    await this.assertSessionCapacity(input.sessionId, input.totalBytes);
    const safeName = sanitizeFileName(input.fileName);
    if (!safeName) throw new AttachmentError("invalidPayload", "invalid file name");
    const at = this.nowValue();
    const entry: StagedUpload = {
      dir,
      meta: {
        uploadId: input.uploadId,
        sessionId: input.sessionId,
        fileName: input.fileName,
        safeName,
        mime: input.mime.toLowerCase(),
        totalBytes: input.totalBytes,
        totalChunks: input.totalChunks,
        checksum: input.checksum.toLowerCase(),
        state: "staging",
        connectionId: input.connectionId,
        createdAt: at,
        updatedAt: at,
        writtenBytes: 0,
        writtenChunks: 0,
      },
    };
    await mkdir(dir, { recursive: true });
    await writeFile(join(dir, PART_FILE), new Uint8Array(0));
    this.register(entry);
    await writeMeta(entry);
    return { uploadId: entry.meta.uploadId, state: "staging", nextChunkIndex: 0 };
  }

  /**
   * Appends one chunk: contiguity is enforced, a payload must be non-empty (for a non-zero
   * upload), and a replay of an accepted chunk must match the stored bytes (the UI
   * retransmits the same chunk after a 500 ms retry).
   */
  async appendChunk(params: {
    connectionId: string;
    sessionId: string;
    uploadId: string;
    chunkIndex: number;
    dataBase64: string;
  }): Promise<{ uploadId: string; nextChunkIndex: number }> {
    const entry = this.requireUpload(params.connectionId, params.sessionId, params.uploadId);
    const meta = entry.meta;
    if (meta.state !== "staging") {
      throw new AttachmentError("uploadConflict", "upload is already committed");
    }
    let chunk: Buffer;
    try {
      chunk = decodeBase64Strict(params.dataBase64);
    } catch {
      throw new AttachmentError("invalidPayload", "invalid base64");
    }
    if (chunk.byteLength === 0) throw new AttachmentError("chunkMismatch", "empty chunk");
    if (chunk.byteLength > PROTOCOL_V4_LIMITS.attachmentChunkMaxBytes) {
      throw new AttachmentError("uploadLimit", "chunk exceeds the protocol limit");
    }
    if (meta.totalBytes === 0) {
      throw new AttachmentError("chunkMismatch", "zero-byte upload receives no chunks");
    }
    if (params.chunkIndex < meta.writtenChunks) {
      // Replay: the chunk's own bytes are either a full protocol chunk (its offset is
      // index × size) or the last one (its offset is the tail of the written bytes).
      const start =
        chunk.byteLength === PROTOCOL_V4_LIMITS.attachmentChunkMaxBytes
          ? params.chunkIndex * chunk.byteLength
          : meta.writtenBytes - chunk.byteLength;
      const stored =
        start >= 0 && start + chunk.byteLength <= meta.writtenBytes
          ? await readAt(join(entry.dir, PART_FILE), start, chunk.byteLength)
          : null;
      if (!stored || stored.length !== chunk.byteLength || !stored.equals(chunk)) {
        throw new AttachmentError("chunkMismatch", "chunk replay differs from the stored bytes");
      }
      return { uploadId: meta.uploadId, nextChunkIndex: meta.writtenChunks };
    }
    if (params.chunkIndex !== meta.writtenChunks) {
      throw new AttachmentError("chunkOutOfOrder", `expected chunk ${meta.writtenChunks}`);
    }
    if (meta.writtenBytes + chunk.byteLength > meta.totalBytes) {
      throw new AttachmentError("chunkMismatch", "chunk exceeds the declared total size");
    }
    await appendChunkBytes(entry.dir, chunk);
    entry.meta = {
      ...meta,
      writtenBytes: meta.writtenBytes + chunk.byteLength,
      writtenChunks: meta.writtenChunks + 1,
      updatedAt: this.nowValue(),
    };
    await writeMeta(entry);
    return { uploadId: meta.uploadId, nextChunkIndex: entry.meta.writtenChunks };
  }

  /**
   * Finalises an upload: verifies byte count, declared SHA-256 and media magic bytes,
   * renames `blob.part` → `blob`, and returns the ref. Committing twice replays the ref.
   */
  async commit(params: {
    connectionId: string;
    sessionId: string;
    uploadId: string;
  }): Promise<{ ref: string }> {
    const entry = this.requireUpload(params.connectionId, params.sessionId, params.uploadId);
    const meta = entry.meta;
    if (meta.state === "committed" && meta.ref) return { ref: meta.ref };
    if (meta.writtenChunks !== meta.totalChunks || meta.writtenBytes !== meta.totalBytes) {
      throw new AttachmentError("uploadLimit", "upload is incomplete");
    }
    const part = join(entry.dir, PART_FILE);
    const digest = createHash("sha256");
    digest.update(await readFile(part));
    if (`sha256:${digest.digest("hex")}` !== meta.checksum) {
      await this.discard(entry);
      throw new AttachmentError("checksumMismatch", "attachment checksum does not match");
    }
    const head = await readAt(part, 0, Math.min(MAGIC_BYTES, meta.totalBytes));
    const mediaType = verifyMagic(meta, head);
    await rename(part, join(entry.dir, BLOB_FILE));
    const ref = runnerRef(meta.sessionId, meta.uploadId, meta.safeName);
    entry.meta = {
      ...meta,
      state: "committed",
      updatedAt: this.nowValue(),
      ref,
      ...(mediaType ? { mediaType } : {}),
    };
    await writeMeta(entry);
    return { ref };
  }

  /** Idempotent abort: drops a staging upload; unknown ids and committed blobs are no-ops. */
  async abort(params: {
    connectionId: string;
    sessionId: string;
    uploadId: string;
  }): Promise<void> {
    const entry = this.staged.get(
      uploadKey(params.connectionId, params.sessionId, params.uploadId),
    );
    if (entry) {
      // A committed upload is referenced by rows; a late abort must not delete it.
      if (entry.meta.state === "staging") await this.discard(entry);
      return;
    }
    const dir = resolveAttachmentDir(this.dataDir, params.sessionId, params.uploadId);
    if (!dir) return;
    const meta = await readMeta(dir);
    if (!meta || meta.state !== "staging" || meta.connectionId !== params.connectionId) return;
    await rm(dir, { recursive: true, force: true });
  }

  // ── reads (preview / share) ──

  /** One committed blob by ref, re-reading `meta.json` (restart-safe, no membership). */
  async describe(ref: string): Promise<AttachmentInfo | null> {
    const parsed = parseRunnerRef(ref);
    if (!parsed) return null;
    const dir = resolveAttachmentDir(this.dataDir, parsed.sessionId, parsed.uploadId);
    if (!dir) return null;
    const meta = await readMeta(dir);
    if (!meta || meta.state !== "committed" || !meta.ref) return null;
    if (meta.sessionId !== parsed.sessionId || meta.uploadId !== parsed.uploadId) return null;
    try {
      return await statBlob(dir, meta);
    } catch {
      return null;
    }
  }

  /** Full blob bytes (images are handed to pi as base64). */
  async read(ref: string): Promise<{ info: AttachmentInfo; bytes: Uint8Array } | null> {
    const info = await this.describe(ref);
    if (!info) return null;
    try {
      return { info, bytes: new Uint8Array(await readFile(info.path)) };
    } catch {
      return null;
    }
  }

  /** Clamped byte range plus the blob's identity and the next offset (`null` = end). */
  async readRange(
    ref: string,
    offset: number,
    limit: number,
  ): Promise<{ bytes: Uint8Array; info: AttachmentInfo; nextOffset: number | null } | null> {
    const info = await this.describe(ref);
    if (!info) return null;
    const start = Math.min(Math.max(offset, 0), info.bytes);
    const end = Math.min(start + limit, info.bytes);
    const bytes = end > start ? await readAt(info.path, start, end - start) : Buffer.alloc(0);
    return { bytes: new Uint8Array(bytes), info, nextOffset: end < info.bytes ? end : null };
  }

  // ── cleanup ──

  /** Drops staging uploads idle for longer than the TTL (memory and disk). */
  async pruneStaging(ttlMs = PROTOCOL_V4_LIMITS.attachmentUploadTtlMs): Promise<number> {
    const cutoff = this.nowValue() - ttlMs;
    let removed = 0;
    for (const entry of this.staged.values()) {
      if (entry.meta.state !== "staging" || entry.meta.updatedAt > cutoff) continue;
      await this.discard(entry);
      removed += 1;
    }
    removed += await pruneStagingDirs(this, cutoff);
    return removed;
  }

  /**
   * Deletes committed blobs older than `unreferencedTtlMs` that no caller lists, then
   * enforces the per-session quota by evicting the oldest unreferenced blobs.
   */
  async sweep(options: {
    isReferenced: (ref: string, mtimeMs: number) => boolean;
    unreferencedTtlMs?: number;
  }): Promise<{ removed: number; bytes: number }> {
    await this.pruneStaging();
    return sweepBlobs(this, options);
  }

  /** Hourly sweep; `isReferenced` is the host's row/queue membership predicate. */
  startSweeper(isReferenced: (ref: string, mtimeMs: number) => boolean, intervalMs?: number): void {
    if (this.sweeper) return;
    this.sweeper = startAttachmentSweeper(this, isReferenced, intervalMs);
  }

  stopSweeper(): void {
    this.sweeper?.stop();
    this.sweeper = null;
  }

  // ── internals ──

  /** Every upload directory on disk with its parsed meta (sweep/quota source of truth). */
  async listUploads(): Promise<Array<{ dir: string; meta: AttachmentMeta }>> {
    return listUploadDirectories(this.dataDir);
  }

  /** Clock used by the cleanup rules (tests inject time). */
  now(): number {
    return this.nowValue();
  }

  quota(): number {
    return this.quotaBytes;
  }

  isRegistered(connectionId: string, sessionId: string, uploadId: string): boolean {
    return this.staged.has(uploadKey(connectionId, sessionId, uploadId));
  }

  private register(entry: StagedUpload): void {
    this.staged.set(
      uploadKey(entry.meta.connectionId, entry.meta.sessionId, entry.meta.uploadId),
      entry,
    );
  }

  private stagingCount(): number {
    let count = 0;
    for (const entry of this.staged.values()) if (entry.meta.state === "staging") count += 1;
    return count;
  }

  private stagingBytes(): number {
    let total = 0;
    for (const entry of this.staged.values()) {
      if (entry.meta.state === "staging") total += entry.meta.totalBytes;
    }
    return total;
  }

  forgetRef(ref: string): void {
    for (const [key, entry] of this.staged) if (entry.meta.ref === ref) this.staged.delete(key);
  }

  private async discard(entry: StagedUpload): Promise<void> {
    this.staged.delete(
      uploadKey(entry.meta.connectionId, entry.meta.sessionId, entry.meta.uploadId),
    );
    await rm(entry.dir, { recursive: true, force: true });
  }

  private requireUpload(connectionId: string, sessionId: string, uploadId: string): StagedUpload {
    const entry = this.staged.get(uploadKey(connectionId, sessionId, uploadId));
    if (!entry) throw new AttachmentError("uploadNotFound", "unknown upload");
    if (entry.meta.sessionId !== sessionId || entry.meta.uploadId !== uploadId) {
      throw new AttachmentError("uploadForbidden", "upload belongs to another target");
    }
    return entry;
  }

  private async assertSessionCapacity(sessionId: string, incoming: number): Promise<void> {
    let used = 0;
    for (const { meta } of await this.listUploads()) {
      if (meta.sessionId === sessionId) used += meta.totalBytes;
    }
    if (used + incoming > this.quotaBytes) {
      throw new AttachmentError("uploadLimit", "session attachment quota exceeded");
    }
  }
}

async function writeMeta(entry: StagedUpload): Promise<void> {
  await writeJsonAtomic(join(entry.dir, META_FILE), entry.meta);
}

async function readMeta(dir: string): Promise<AttachmentMeta | null> {
  try {
    const parsed = JSON.parse(await readFile(join(dir, META_FILE), "utf8")) as AttachmentMeta;
    if (typeof parsed.uploadId !== "string" || typeof parsed.sessionId !== "string") return null;
    if (typeof parsed.totalBytes !== "number" || typeof parsed.checksum !== "string") return null;
    return parsed;
  } catch {
    return null;
  }
}
