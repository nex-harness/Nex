import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readdir, rm, symlink, utimes } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  PROTOCOL_V4_LIMITS,
  v4AttachmentBeginResultSchema,
  v4AttachmentChunkResultSchema,
  v4AttachmentCommitResultSchema,
  v4AttachmentReadResultSchema,
} from "@nex/shared/nex-protocol-v4";
import { AttachmentStore, attachmentSessionQuotaBytes } from "../src/attachments/store.js";
import { AttachmentRegistry } from "../src/attachments/registry.js";
import { AttachmentError } from "../src/attachments/types.js";
import { sanitizeFileName, parseRunnerRef, isInside } from "../src/attachments/refs.js";
import {
  attachmentKind,
  effectiveMediaMime,
  isPiImageMime,
  sniffMediaMime,
} from "../src/attachments/mime.js";
import {
  handleAttachmentAbort,
  handleAttachmentBegin,
  handleAttachmentChunk,
  handleAttachmentCommit,
  handleAttachmentRead,
  type AttachmentCommandContext,
} from "../src/v4/attachmentCommands.js";

/**
 * Attachment store + V4 attachment methods (docs/next/attachments-rewind.md section A):
 * protocol limits, chunk order/replay, checksum + magic-byte rejection, ref safety
 * (traversal, session mismatch, listing authorization), staging TTL and the unreferenced
 * sweep. Every result is parsed with the real zod schemas.
 */

const CHUNK = 384 * 1024;

const PNG = Uint8Array.from([
  0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
]);

function pngBytes(size: number): Uint8Array {
  const bytes = new Uint8Array(size);
  bytes.set(PNG);
  for (let index = PNG.length; index < size; index += 1) bytes[index] = index % 251;
  return bytes;
}

function checksum(bytes: Uint8Array): string {
  return `sha256:${createHash("sha256").update(bytes).digest("hex")}`;
}

async function fixture(options: { now?: () => number; quota?: number } = {}) {
  const root = await mkdtemp(join(tmpdir(), "nex-attachments-"));
  const dataDir = join(root, "data");
  await mkdir(dataDir, { recursive: true });
  const store = new AttachmentStore({
    dataDir,
    ...(options.now ? { now: options.now } : {}),
    ...(options.quota !== undefined ? { sessionQuotaBytes: options.quota } : {}),
  });
  return { root, dataDir, store, cleanup: () => rm(root, { recursive: true, force: true }) };
}

/** `readRange` without the schema wrapper (raw bytes + next offset). */
async function rangeOf(
  store: AttachmentStore,
  ref: string,
  offset: number,
  limit: number,
): Promise<{ bytes: Uint8Array; nextOffset: number | null }> {
  const range = await store.readRange(ref, offset, limit);
  assert.ok(range);
  return range;
}

async function upload(
  store: AttachmentStore,
  params: {
    bytes: Uint8Array;
    sessionId?: string;
    uploadId?: string;
    fileName?: string;
    mime?: string;
    connectionId?: string;
    checksum?: string;
  },
): Promise<{ ref: string; uploadId: string }> {
  const sessionId = params.sessionId ?? "sess-1";
  const uploadId = params.uploadId ?? "upload-1";
  const connectionId = params.connectionId ?? "conn-1";
  const totalChunks = Math.ceil(params.bytes.byteLength / CHUNK);
  const begin = v4AttachmentBeginResultSchema.parse(
    await store.begin({
      connectionId,
      uploadId,
      sessionId,
      fileName: params.fileName ?? "photo.png",
      mime: params.mime ?? "image/png",
      totalBytes: params.bytes.byteLength,
      totalChunks,
      checksum: params.checksum ?? checksum(params.bytes),
    }),
  );
  assert.equal(begin.state, "staging");
  for (let index = 0; index < totalChunks; index += 1) {
    const chunk = params.bytes.subarray(
      index * CHUNK,
      Math.min((index + 1) * CHUNK, params.bytes.length),
    );
    const result = v4AttachmentChunkResultSchema.parse(
      await store.appendChunk({
        connectionId,
        sessionId,
        uploadId,
        chunkIndex: index,
        dataBase64: Buffer.from(chunk).toString("base64"),
      }),
    );
    assert.equal(result.nextChunkIndex, index + 1);
  }
  const commit = v4AttachmentCommitResultSchema.parse(
    await store.commit({ connectionId, sessionId, uploadId }),
  );
  return { ref: commit.ref, uploadId };
}

test("store: upload → commit → ranged read with the real schemas", async () => {
  const f = await fixture();
  try {
    const bytes = pngBytes(1000);
    const { ref } = await upload(f.store, { bytes });
    assert.equal(ref, "nex-runner://sess-1/upload-1/photo.png");
    const raw = await rangeOf(f.store, ref, 0, PROTOCOL_V4_LIMITS.attachmentChunkMaxBytes);
    const first = v4AttachmentReadResultSchema.parse({
      dataBase64: Buffer.from(raw.bytes).toString("base64"),
      mediaType: ((await f.store.describe(ref))?.mediaType ?? "image/png").toLowerCase(),
      totalBytes: raw.bytes.byteLength,
      nextOffset: raw.nextOffset,
    });
    assert.equal(first.totalBytes, 1000);
    assert.equal(first.dataBase64, Buffer.from(bytes).toString("base64"));
    assert.equal(first.mediaType, "image/png");
    assert.equal(first.nextOffset, null);
    const tail = await rangeOf(f.store, ref, 990, 512);
    assert.deepEqual(Array.from(tail.bytes), Array.from(bytes.slice(990)));
    assert.deepEqual(Array.from(raw.bytes), Array.from(bytes));
  } finally {
    await f.cleanup();
  }
});

test("store: chunk replay is byte-equal or rejected; out-of-order is rejected", async () => {
  const f = await fixture();
  try {
    const bytes = pngBytes(CHUNK * 2 + 10);
    const begin = await f.store.begin({
      connectionId: "c",
      uploadId: "u",
      sessionId: "s",
      fileName: "a.png",
      mime: "image/png",
      totalBytes: bytes.byteLength,
      totalChunks: 3,
      checksum: checksum(bytes),
    });
    assert.equal(begin.state, "staging");
    const chunk0 = Buffer.from(bytes.subarray(0, CHUNK)).toString("base64");
    await f.store.appendChunk({
      connectionId: "c",
      sessionId: "s",
      uploadId: "u",
      chunkIndex: 0,
      dataBase64: chunk0,
    });
    // Replay of chunk 0: same bytes, same cursor (the UI's 500 ms retry).
    const replay = await f.store.appendChunk({
      connectionId: "c",
      sessionId: "s",
      uploadId: "u",
      chunkIndex: 0,
      dataBase64: chunk0,
    });
    assert.equal(replay.nextChunkIndex, 1);
    // Replay with different bytes is a mismatch.
    const corrupt = Buffer.from(bytes.subarray(0, CHUNK));
    corrupt[0] = corrupt[0]! ^ 0xff;
    await assert.rejects(
      f.store.appendChunk({
        connectionId: "c",
        sessionId: "s",
        uploadId: "u",
        chunkIndex: 0,
        dataBase64: corrupt.toString("base64"),
      }),
      (error: unknown) => error instanceof AttachmentError && error.reasonCode === "chunkMismatch",
    );
    // Skipping ahead is out of order.
    await assert.rejects(
      f.store.appendChunk({
        connectionId: "c",
        sessionId: "s",
        uploadId: "u",
        chunkIndex: 2,
        dataBase64: chunk0,
      }),
      (error: unknown) =>
        error instanceof AttachmentError && error.reasonCode === "chunkOutOfOrder",
    );
    // A different connection cannot append to someone else's upload.
    await assert.rejects(
      f.store.appendChunk({
        connectionId: "other",
        sessionId: "s",
        uploadId: "u",
        chunkIndex: 1,
        dataBase64: chunk0,
      }),
      (error: unknown) => error instanceof AttachmentError && error.reasonCode === "uploadNotFound",
    );
  } finally {
    await f.cleanup();
  }
});

test("store: checksum mismatch discards the staging upload", async () => {
  const f = await fixture();
  try {
    const bytes = pngBytes(64);
    const begin = await f.store.begin({
      connectionId: "c",
      uploadId: "u",
      sessionId: "s",
      fileName: "a.png",
      mime: "image/png",
      totalBytes: bytes.byteLength,
      totalChunks: 1,
      checksum: `sha256:${"0".repeat(64)}`,
    });
    assert.equal(begin.state, "staging");
    await f.store.appendChunk({
      connectionId: "c",
      sessionId: "s",
      uploadId: "u",
      chunkIndex: 0,
      dataBase64: Buffer.from(bytes).toString("base64"),
    });
    await assert.rejects(
      f.store.commit({ connectionId: "c", sessionId: "s", uploadId: "u" }),
      (error: unknown) =>
        error instanceof AttachmentError && error.reasonCode === "checksumMismatch",
    );
    // The upload directory is gone and the id is unknown afterwards.
    await assert.rejects(f.store.commit({ connectionId: "c", sessionId: "s", uploadId: "u" }));
  } finally {
    await f.cleanup();
  }
});

test("store: media magic bytes must match the declared type", async () => {
  const f = await fixture();
  try {
    // Declared PNG, bytes are a PDF.
    await assert.rejects(
      upload(f.store, { bytes: Buffer.from("%PDF-1.7\nfake\n"), mime: "image/png" }),
      (error: unknown) => error instanceof AttachmentError && error.reasonCode === "mimeMismatch",
    );
    // A PDF declared as a PDF commits.
    const pdf = await upload(f.store, {
      bytes: Buffer.from("%PDF-1.7\nfake\n"),
      mime: "application/pdf",
      fileName: "doc.pdf",
      uploadId: "upload-pdf",
    });
    const info = await f.store.describe(pdf.ref);
    assert.equal(info?.mediaType, "application/pdf");
    // SVG is a plain file, not an image.
    const svg = await upload(f.store, {
      bytes: Buffer.from('<svg xmlns="http://www.w3.org/2000/svg"/>'),
      mime: "image/svg+xml",
      fileName: "x.svg",
      uploadId: "upload-svg",
    });
    // SVG is classified as a file for pi (no decoder), never a prompt image.
    assert.equal((await f.store.describe(svg.ref))?.mediaType, "image/svg+xml");
    assert.equal(isPiImageMime("image/svg+xml"), false);
  } finally {
    await f.cleanup();
  }
});

test("store: begin enforces the protocol limits and is idempotent", async () => {
  const f = await fixture();
  try {
    const base = {
      connectionId: "c",
      uploadId: "u",
      sessionId: "s",
      fileName: "a.png",
      mime: "image/png",
      checksum: checksum(PNG),
    };
    await assert.rejects(
      f.store.begin({
        ...base,
        totalBytes: PROTOCOL_V4_LIMITS.attachmentMaxBytes + 1,
        totalChunks: 64,
      }),
      (error: unknown) => error instanceof AttachmentError && error.reasonCode === "uploadLimit",
    );
    await assert.rejects(
      f.store.begin({
        ...base,
        totalBytes: 10,
        totalChunks: PROTOCOL_V4_LIMITS.attachmentUploadMaxChunks + 1,
      }),
      (error: unknown) => error instanceof AttachmentError && error.reasonCode === "uploadLimit",
    );
    await assert.rejects(
      f.store.begin({ ...base, totalBytes: 0, totalChunks: 1 }),
      (error: unknown) => error instanceof AttachmentError && error.reasonCode === "invalidPayload",
    );
    const first = await f.store.begin({ ...base, totalBytes: 10, totalChunks: 1 });
    assert.equal(first.state, "staging");
    // Same metadata replays the cursor; different metadata is a conflict.
    assert.equal(
      (await f.store.begin({ ...base, totalBytes: 10, totalChunks: 1 })).state,
      "staging",
    );
    await assert.rejects(
      f.store.begin({ ...base, totalBytes: 11, totalChunks: 1 }),
      (error: unknown) => error instanceof AttachmentError && error.reasonCode === "uploadConflict",
    );
  } finally {
    await f.cleanup();
  }
});

test("store: concurrent upload slots and the staged byte budget", async () => {
  const f = await fixture();
  try {
    for (let index = 0; index < PROTOCOL_V4_LIMITS.attachmentUploadMaxConcurrent; index += 1) {
      await f.store.begin({
        connectionId: "c",
        uploadId: `u-${index}`,
        sessionId: "s",
        fileName: "a.png",
        mime: "image/png",
        totalBytes: 16,
        totalChunks: 1,
        checksum: checksum(PNG),
      });
    }
    await assert.rejects(
      f.store.begin({
        connectionId: "c",
        uploadId: "u-overflow",
        sessionId: "s",
        fileName: "a.png",
        mime: "image/png",
        totalBytes: 16,
        totalChunks: 1,
        checksum: checksum(PNG),
      }),
      (error: unknown) => error instanceof AttachmentError && error.reasonCode === "uploadLimit",
    );
  } finally {
    await f.cleanup();
  }
});

test("store: per-session disk quota", async () => {
  const f = await fixture({ quota: 1000 });
  try {
    await upload(f.store, { bytes: pngBytes(600), uploadId: "q1" });
    await assert.rejects(
      upload(f.store, { bytes: pngBytes(600), uploadId: "q2", fileName: "b.png" }),
      (error: unknown) => error instanceof AttachmentError && error.reasonCode === "uploadLimit",
    );
    assert.equal(attachmentSessionQuotaBytes, 512 * 1024 * 1024);
  } finally {
    await f.cleanup();
  }
});

test("store: zero-byte upload commits an empty blob", async () => {
  const f = await fixture();
  try {
    const { ref } = await upload(f.store, {
      bytes: new Uint8Array(0),
      mime: "text/plain",
      fileName: "empty.txt",
    });
    const info = await f.store.describe(ref);
    assert.equal(info?.bytes, 0);
    const range = await rangeOf(f.store, ref, 0, 512);
    assert.equal(range.bytes.byteLength, 0);
  } finally {
    await f.cleanup();
  }
});

test("refs: traversal, wrong session and unknown uploads are never readable", async () => {
  const f = await fixture();
  try {
    const { ref } = await upload(f.store, { bytes: pngBytes(32) });
    // Traversal-looking refs never parse.
    for (const bad of [
      "nex-runner://sess-1/../blob",
      "nex-runner://sess-1/upload-1/..%2F..%2Fetc%2Fpasswd",
      "nex-runner://sess-1/upload-1/sub/extra",
      "nex-runner://sess-1/upload-1/",
      "nex-runner:///upload-1/x.png",
      "file:///etc/passwd",
    ]) {
      assert.equal(parseRunnerRef(bad), null, bad);
      assert.equal(await f.store.describe(bad), null, bad);
    }
    // A symlinked upload directory pointing outside the store resolves nothing.
    const outside = await mkdtemp(join(tmpdir(), "nex-attachments-outside-"));
    try {
      const dir = join(f.dataDir, "attachments", "sess-1");
      await rm(join(dir, "upload-1"), { recursive: true, force: true });
      await symlink(outside, join(dir, "upload-1"));
      assert.equal(await f.store.describe(ref), null);
    } finally {
      await rm(outside, { recursive: true, force: true });
    }
    assert.equal(sanitizeFileName("../../etc/passwd"), null);
    assert.equal(sanitizeFileName("a/b.png"), null);
    assert.equal(sanitizeFileName("my photo (1).png"), "my_photo__1_.png");
    assert.equal(isInside("/a/b", "/a/b/../c"), false);
    assert.equal(isInside("/a/b", "/a/b/c"), true);
  } finally {
    await f.cleanup();
  }
});

test("mime: magic-byte sniffing covers the media pi accepts", () => {
  assert.equal(sniffMediaMime(PNG), "image/png");
  assert.equal(sniffMediaMime(Uint8Array.from([0xff, 0xd8, 0xff, 0xe0])), "image/jpeg");
  assert.equal(sniffMediaMime(Buffer.from("GIF89a....")), "image/gif");
  assert.equal(sniffMediaMime(Buffer.from("RIFF0000WEBP")), "image/webp");
  assert.equal(sniffMediaMime(Buffer.from("%PDF-1.4")), "application/pdf");
  assert.equal(sniffMediaMime(Buffer.from("0000ftypisom")), "video/mp4");
  assert.equal(sniffMediaMime(Buffer.from("hello")), null);
  assert.equal(attachmentKind("image/svg+xml"), "image");
  assert.equal(attachmentKind("text/plain"), "file");
  assert.equal(effectiveMediaMime("image/png", PNG), "image/png");
});

test("cleanup: staging TTL and the unreferenced sweep", async () => {
  let now = 1_000_000;
  const f = await fixture({ now: () => now });
  try {
    const kept = await upload(f.store, { bytes: pngBytes(48), uploadId: "kept" });
    const dropped = await upload(f.store, {
      bytes: pngBytes(48),
      uploadId: "dropped",
      fileName: "b.png",
    });
    // The TTL clock jumps forward by more than the sweep window below; the blobs' mtimes
    // are real, so age them explicitly (the sweep compares mtime to `now - ttl`).
    const old = new Date(now - PROTOCOL_V4_LIMITS.attachmentUnreferencedTtlMs - 60_000);
    await utimes(join(f.dataDir, "attachments", "sess-1", "kept", "blob"), old, old);
    await utimes(join(f.dataDir, "attachments", "sess-1", "dropped", "blob"), old, old);
    // Unfinished staging upload goes away after the TTL.
    await f.store.begin({
      connectionId: "c",
      uploadId: "stale",
      sessionId: "sess-1",
      fileName: "c.png",
      mime: "image/png",
      totalBytes: 1024,
      totalChunks: 64,
      checksum: checksum(PNG),
    });
    now += PROTOCOL_V4_LIMITS.attachmentUploadTtlMs + 1;
    assert.equal(await f.store.pruneStaging(), 1);
    assert.equal(f.store.stagedUpload("c", "sess-1", "stale"), undefined);
    // Move past the unreferenced TTL; every ref referenced: nothing goes.
    now += PROTOCOL_V4_LIMITS.attachmentUnreferencedTtlMs + 1;
    assert.deepEqual(await f.store.sweep({ isReferenced: () => true }), { removed: 0, bytes: 0 });
    // Only the unlisted blob goes (the listed one survives by its ref).
    const swept = await f.store.sweep({ isReferenced: (ref) => ref === kept.ref });
    assert.equal(swept.removed, 1);
    assert.ok(await f.store.describe(kept.ref));
    assert.equal(await f.store.describe(dropped.ref), null);
    // The upload directories on disk agree.
    const sessions = await readdir(join(f.dataDir, "attachments"));
    assert.deepEqual(sessions, ["sess-1"]);
    assert.deepEqual((await readdir(join(f.dataDir, "attachments", "sess-1"))).sort(), ["kept"]);
  } finally {
    await f.cleanup();
  }
});

test("cleanup: abort removes a staging upload but never a committed blob", async () => {
  const f = await fixture();
  try {
    await f.store.begin({
      connectionId: "c",
      uploadId: "x",
      sessionId: "s",
      fileName: "x.png",
      mime: "image/png",
      totalBytes: 16,
      totalChunks: 1,
      checksum: checksum(PNG),
    });
    await f.store.abort({ connectionId: "c", sessionId: "s", uploadId: "x" });
    assert.equal(f.store.stagedUpload("c", "s", "x"), undefined);
    const committed = await upload(f.store, { bytes: pngBytes(40) });
    await f.store.abort({ connectionId: "conn-1", sessionId: "sess-1", uploadId: "upload-1" });
    assert.ok(await f.store.describe(committed.ref));
  } finally {
    await f.cleanup();
  }
});

test("commands: V4 methods authorize by session listing and serve chunked previews", async () => {
  const f = await fixture();
  const registry = new AttachmentRegistry({ dataDir: f.dataDir });
  try {
    const ctx: AttachmentCommandContext = {
      registry,
      session: () => ({ ready: Promise.resolve(), projection: emptyProjection() }),
    };
    const bytes = pngBytes(300);
    const begin = (await handleAttachmentBegin(ctx, {
      connectionId: "c",
      uploadId: "u",
      sessionId: "s",
      fileName: "pic.png",
      mime: "image/png",
      totalBytes: bytes.byteLength,
      totalChunks: 1,
      checksum: checksum(bytes),
    })) as { ok: true; value: unknown };
    assert.equal(begin.ok, true);
    const chunk = await handleAttachmentChunk(ctx, {
      connectionId: "c",
      uploadId: "u",
      sessionId: "s",
      chunkIndex: 0,
      dataBase64: Buffer.from(bytes).toString("base64"),
    });
    assert.equal(chunk.ok, true);
    const commit = await handleAttachmentCommit(ctx, {
      connectionId: "c",
      uploadId: "u",
      sessionId: "s",
    });
    assert.equal(commit.ok, true);
    const ref = (commit as { value: { ref: string } }).value.ref;

    // Not listed by any write yet: read is refused.
    const denied = await handleAttachmentRead(ctx, {
      sessionId: "s",
      ref,
      offset: 0,
      limit: 512,
    });
    assert.equal(denied.ok, false);
    // The committed name is sanitized in the ref.
    assert.equal(ref, "nex-runner://s/u/pic.png");

    registry.addAdmitted("s", [
      { ref, fileName: "pic.png", mime: "image/png", bytes: bytes.byteLength },
    ]);
    // Another session never reads it, even though the upload exists.
    assert.equal(await registry.authorize("other", ref), false);
    assert.equal(await registry.authorize("s", ref), true);
    const read = await handleAttachmentRead(ctx, { sessionId: "s", ref, offset: 0, limit: 512 });
    assert.equal(read.ok, true);
    const value = (read as { value: Record<string, unknown> }).value;
    assert.equal(value["mediaType"], "image/png");
    assert.equal(value["nextOffset"], null);
    assert.equal(
      Buffer.from(String(value["dataBase64"]), "base64").equals(Buffer.from(bytes)),
      true,
    );

    // Abort of the committed upload is a no-op.
    const abort = await handleAttachmentAbort(ctx, {
      connectionId: "c",
      uploadId: "u",
      sessionId: "s",
    });
    assert.equal(abort.ok, true);
    assert.ok(await f.store.describe(ref));
  } finally {
    registry.dispose();
    await f.cleanup();
  }
});

function emptyProjection() {
  return {
    sessionId: "s",
    logEpoch: "e",
    seq: 0,
    revision: 0,
    phase: "draft" as const,
    title: "",
    titleSource: "default" as const,
    createdAt: 0,
    lastActivityAt: 0,
    model: null,
    usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    mode: "build" as const,
    planEnabled: false,
    followupMode: "queue" as const,
    queue: { items: [], autoDrain: true },
    rows: [],
    nextRowId: 1,
    currentTurn: null,
    currentResponse: null,
    toolRows: {},
    pendingMessageGroups: [],
    pendingApprovals: {},
    lastError: null,
    compactOperations: {},
    apiRetry: null,
    promptLineage: { sourceCommandId: "", generation: 0 },
  };
}
