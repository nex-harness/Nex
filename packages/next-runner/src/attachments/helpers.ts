import { createHash } from "node:crypto";
import { open as openFile, readFile, realpath, rm, stat } from "node:fs/promises";
import { join, resolve } from "node:path";
import { attachmentKind, effectiveMediaMime, requiresMagicCheck, sniffMediaMime } from "./mime.js";
import { attachmentsRoot, runnerRef, sessionAttachmentDir, shortHash } from "./refs.js";
import { AttachmentError, type AttachmentMeta, type AttachmentInfo } from "./types.js";

/**
 * Blob-level filesystem helpers of `AttachmentStore` (kept apart for the file budget;
 * the store is their only caller).
 */

export const META_FILE = "meta.json";
export const PART_FILE = "blob.part";
export const BLOB_FILE = "blob";
/** Magic-byte window read on commit. */
export const MAGIC_BYTES = 16;

/** Verifies media magic bytes; returns the effective media mime (undefined for files). */
export function verifyMagic(meta: AttachmentMeta, head: Buffer): string | undefined {
  if (attachmentKind(meta.mime) === "file") return undefined;
  const declared = meta.mime.toLowerCase();
  if (!requiresMagicCheck(declared)) return declared;
  const sniffed = sniffMediaMime(new Uint8Array(head));
  if (sniffed !== declared) {
    throw new AttachmentError(
      "mimeMismatch",
      `declared ${declared} does not match the file contents`,
    );
  }
  return effectiveMediaMime(declared, new Uint8Array(head));
}

export async function statBlob(dir: string, meta: AttachmentMeta): Promise<AttachmentInfo> {
  const path = join(dir, BLOB_FILE);
  const stats = await stat(path);
  if (!stats.isFile()) throw new AttachmentError("notFound", "blob is not a file");
  const mediaType =
    meta.mediaType ??
    (attachmentKind(meta.mime) === "file"
      ? meta.mime.toLowerCase()
      : effectiveMediaMime(meta.mime, new Uint8Array(await readAt(path, 0, MAGIC_BYTES))));
  return {
    ref: meta.ref ?? runnerRef(meta.sessionId, meta.uploadId, meta.safeName),
    fileName: meta.safeName,
    mime: meta.mime,
    mediaType,
    bytes: stats.size,
    path,
    mtimeMs: stats.mtimeMs,
  };
}

export async function readAt(path: string, offset: number, length: number): Promise<Buffer> {
  if (length <= 0) return Buffer.alloc(0);
  const handle = await openFile(path, "r");
  try {
    const buffer = Buffer.alloc(length);
    const { bytesRead } = await handle.read(buffer, 0, length, offset);
    return buffer.subarray(0, bytesRead);
  } finally {
    await handle.close();
  }
}

export async function appendChunkBytes(dir: string, chunk: Buffer): Promise<void> {
  const handle = await openFile(join(dir, PART_FILE), "r+");
  try {
    const { size } = await handle.stat();
    await handle.write(chunk, 0, chunk.byteLength, size);
    await handle.sync();
  } finally {
    await handle.close();
  }
}

export async function exists(path: string): Promise<boolean> {
  return stat(path).then(
    () => true,
    () => false,
  );
}

/**
 * Deletes a deleted session's attachment directory. Guarded by `realpath` so a symlinked
 * session directory can never make the runner delete something outside the store.
 */
export async function removeSessionAttachments(dataDir: string, sessionId: string): Promise<void> {
  const dir = sessionAttachmentDir(dataDir, sessionId);
  const root = resolve(attachmentsRoot(dataDir));
  const realRoot = await realpath(root).catch(() => root);
  const realDir = await realpath(dir).catch(() => null);
  if (!realDir || !realDir.startsWith(`${realRoot}/`)) return;
  await rm(realDir, { recursive: true, force: true });
}

/** Test helper: short hash of a file's bytes. */
export async function hashFileBytes(path: string): Promise<string> {
  return shortHash(
    createHash("sha256")
      .update(await readFile(path))
      .digest("hex"),
  );
}

/** Strict base64 (padded, no whitespace); throws on anything else. */
export function decodeBase64Strict(value: string): Buffer {
  if (value.length === 0) return Buffer.alloc(0);
  if (value.length % 4 !== 0) throw new Error("invalid base64 length");
  if (!/^[A-Za-z0-9+/]*={0,2}$/.test(value)) throw new Error("invalid base64");
  return Buffer.from(value, "base64");
}

/** Registry identity: a replay is only accepted from the same connection/session/upload. */
export function uploadKey(connectionId: string, sessionId: string, uploadId: string): string {
  return `${connectionId}\0${sessionId}\0${uploadId}`;
}
