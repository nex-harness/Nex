import { readdir, readFile, rm } from "node:fs/promises";
import { join } from "node:path";
import { PROTOCOL_V4_LIMITS } from "@nex/shared/nex-protocol-v4";
import { META_FILE, statBlob } from "./helpers.js";
import type { AttachmentStore } from "./store.js";
import type { AttachmentInfo, AttachmentMeta } from "./types.js";

/**
 * Cleanup rules of the store (spec A: abort, staging TTL, unreferenced sweep). Kept apart
 * from `store.ts` for the file budget; the store is the only caller and remains the only
 * writer (these functions only delete files the store listed).
 */

/** Upload directories on disk with their parsed meta (`meta.json` is the durable copy). */
export async function listUploadDirectories(
  dataDir: string,
): Promise<Array<{ dir: string; meta: AttachmentMeta }>> {
  const result: Array<{ dir: string; meta: AttachmentMeta }> = [];
  const root = join(dataDir, "attachments");
  for (const sessionRow of await readdir(root, { withFileTypes: true }).catch(() => [])) {
    if (!sessionRow.isDirectory()) continue;
    const sessionDir = join(root, sessionRow.name);
    for (const uploadRow of await readdir(sessionDir, { withFileTypes: true }).catch(() => [])) {
      if (!uploadRow.isDirectory()) continue;
      const dir = join(sessionDir, uploadRow.name);
      const meta = await readMeta(dir);
      if (meta) result.push({ dir, meta });
    }
  }
  return result;
}

/** Staging directories with no in-memory entry (left by an earlier process), past the TTL. */
export async function pruneStagingDirs(store: AttachmentStore, cutoff: number): Promise<number> {
  let removed = 0;
  for (const { dir, meta } of await listUploadDirectories(store.dataDir)) {
    if (meta.state !== "staging" || meta.updatedAt > cutoff) continue;
    if (store.isRegistered(meta.connectionId, meta.sessionId, meta.uploadId)) continue;
    await rm(dir, { recursive: true, force: true });
    removed += 1;
  }
  return removed;
}

interface BlobRow {
  dir: string;
  ref: string;
  bytes: number;
  mtimeMs: number;
}

/**
 * Deletes committed blobs older than `unreferencedTtlMs` that no caller lists, then evicts
 * the oldest unreferenced blobs of each session until its directory fits the quota.
 */
export async function sweepBlobs(
  store: AttachmentStore,
  options: { isReferenced: (ref: string, mtimeMs: number) => boolean; unreferencedTtlMs?: number },
): Promise<{ removed: number; bytes: number }> {
  const ttl = options.unreferencedTtlMs ?? PROTOCOL_V4_LIMITS.attachmentUnreferencedTtlMs;
  const cutoff = store.now() - ttl;
  let removed = 0;
  let bytes = 0;
  const perSession = new Map<string, BlobRow[]>();
  for (const { dir, meta } of await listUploadDirectories(store.dataDir)) {
    if (meta.state !== "committed" || !meta.ref) continue;
    let info: AttachmentInfo;
    try {
      info = await statBlob(dir, meta);
    } catch {
      continue;
    }
    const listed = perSession.get(meta.sessionId) ?? [];
    listed.push({ dir, ref: info.ref, bytes: info.bytes, mtimeMs: info.mtimeMs });
    perSession.set(meta.sessionId, listed);
    if (info.mtimeMs > cutoff || options.isReferenced(info.ref, info.mtimeMs)) continue;
    await rm(dir, { recursive: true, force: true });
    store.forgetRef(info.ref);
    removed += 1;
    bytes += info.bytes;
  }
  for (const listed of perSession.values()) {
    let total = listed.reduce((sum, item) => sum + item.bytes, 0);
    const oldestFirst = [...listed].sort((left, right) => left.mtimeMs - right.mtimeMs);
    for (const item of oldestFirst) {
      if (total <= store.quota()) break;
      if (options.isReferenced(item.ref, item.mtimeMs)) continue;
      await rm(item.dir, { recursive: true, force: true });
      store.forgetRef(item.ref);
      total -= item.bytes;
      removed += 1;
      bytes += item.bytes;
    }
  }
  return { removed, bytes };
}

async function readMeta(dir: string): Promise<AttachmentMeta | null> {
  try {
    const parsed = JSON.parse(await readFile(join(dir, META_FILE), "utf8")) as AttachmentMeta;
    if (typeof parsed.uploadId !== "string" || typeof parsed.sessionId !== "string") return null;
    return parsed;
  } catch {
    return null;
  }
}
