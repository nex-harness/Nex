import { PROTOCOL_V4_LIMITS, type ConversationRowTarget } from "@nex/shared/nex-protocol-v4";
import { attachmentKind } from "../attachments/mime.js";
import type { AttachmentInfo } from "../attachments/types.js";
import type { AttachmentRegistry } from "../attachments/registry.js";
import type { ProjectionState } from "../projection/state.js";
import { entryIdFromEntityId } from "../projection/entityIds.js";
import { userEntityId } from "../projection/entityIds.js";

/**
 * `nex-agent` attachment methods (docs/next/attachments-rewind.md section A): the V4 upload
 * transaction, media preview reads and the share read/stat pair.
 *
 * ```
 * attachmentBeginV4 ─▶ registry.store.begin      (staging cursor | committed ref)
 * attachmentChunkV4 ─▶ appendChunk               (contiguous, replay-equal)
 * attachmentCommitV4 ─▶ commit                   (sha256 + magic bytes → blob + ref)
 * attachmentAbortV4 ─▶ abort                     (staging only)
 * attachmentReadV4  ─▶ authorized? ─▶ ranged read (image/video/pdf only)
 * attachmentPreviewSourceV4 ─▶ { kind: "chunked" } (web always; runner has no media URL)
 * conversationAttachmentRead/StatV4 ─▶ any mime, row-target authorization
 * ```
 *
 * Authorization is row/queue/`nex.attachments` membership of the session (`AttachmentRegistry`)
 * plus session-id equality in the ref. A ref is never read because it *looks* valid.
 */
export interface AttachmentCommandContext {
  registry: AttachmentRegistry;
  /** Live projection of a session (rows feed the authorization listing). */
  session: (sessionId: string) => { ready: Promise<void>; projection: ProjectionState } | undefined;
  /** Opens a session addressed by an upload begin (so its listing exists before reads). */
  openSession?: (sessionId: string) => { ready: Promise<void> } | undefined;
}

export type AttachmentCommandResult =
  | { ok: true; value: unknown }
  | { ok: false; reasonCode: string; message: string };

export async function handleAttachmentBegin(
  ctx: AttachmentCommandContext,
  arg: Record<string, unknown>,
): Promise<AttachmentCommandResult> {
  // The V4 gateway injects `connectionId` from the trusted connection; the runner sees it
  // on the same wire object as every other field.
  const connectionId = requireString(arg["connectionId"]) ?? "runner";
  const sessionId = requireString(arg["sessionId"]);
  const uploadId = requireString(arg["uploadId"]);
  const fileName = requireString(arg["fileName"]);
  const mime = requireString(arg["mime"]);
  const totalBytes = requireNumber(arg["totalBytes"]);
  const totalChunks = requireNumber(arg["totalChunks"]);
  const checksum = requireString(arg["checksum"]);
  // A draft composer has no session yet; uploads are session-scoped (`nex-runner://<sid>/…`)
  // and the UI retries once the first send created the session.
  if (sessionId === null || uploadId === null) {
    return failure("invalidPayload", "sessionId and uploadId are required");
  }
  try {
    const value = await ctx.registry.store.begin({
      connectionId,
      uploadId,
      sessionId,
      fileName: fileName ?? "attachment",
      mime: mime ?? "application/octet-stream",
      totalBytes: totalBytes ?? 0,
      totalChunks: totalChunks ?? 0,
      checksum: checksum ?? "",
    });
    // The upload belongs to a session the caller addressed; the upload itself needs no
    // projection, reads later authorize against the session's listing.
    void rowTargetOf(arg["target"]);
    return { ok: true, value };
  } catch (error) {
    return failureOf(error);
  }
}

export async function handleAttachmentChunk(
  ctx: AttachmentCommandContext,
  arg: Record<string, unknown>,
): Promise<AttachmentCommandResult> {
  try {
    return {
      ok: true,
      value: await ctx.registry.store.appendChunk({
        connectionId: requireString(arg["connectionId"]) ?? "runner",
        sessionId: requireString(arg["sessionId"]) ?? "",
        uploadId: requireString(arg["uploadId"]) ?? "",
        chunkIndex: requireNumber(arg["chunkIndex"]) ?? -1,
        dataBase64: typeof arg["dataBase64"] === "string" ? arg["dataBase64"] : "",
      }),
    };
  } catch (error) {
    return failureOf(error);
  }
}

export async function handleAttachmentCommit(
  ctx: AttachmentCommandContext,
  arg: Record<string, unknown>,
): Promise<AttachmentCommandResult> {
  const sessionId = requireString(arg["sessionId"]);
  const connectionId = requireString(arg["connectionId"]) ?? "runner";
  const uploadId = requireString(arg["uploadId"]);
  if (!sessionId || !uploadId) {
    return failure("invalidPayload", "sessionId and uploadId are required");
  }
  try {
    // The committed blob becomes readable only when the admission that follows lists it in
    // a row or queue item; commit grants nothing by itself (spec A).
    const value = await ctx.registry.store.commit({ connectionId, sessionId, uploadId });
    return { ok: true, value };
  } catch (error) {
    return failureOf(error);
  }
}

export async function handleAttachmentAbort(
  ctx: AttachmentCommandContext,
  arg: Record<string, unknown>,
): Promise<AttachmentCommandResult> {
  const sessionId = requireString(arg["sessionId"]);
  const connectionId = requireString(arg["connectionId"]) ?? "runner";
  const uploadId = requireString(arg["uploadId"]);
  if (!sessionId || !uploadId) {
    return failure("invalidPayload", "sessionId and uploadId are required");
  }
  try {
    await ctx.registry.store.abort({ connectionId, sessionId, uploadId });
    return { ok: true, value: {} };
  } catch (error) {
    return failureOf(error);
  }
}

/** Media preview read (`attachmentReadV4`). Only image/video/pdf may be served here. */
export async function handleAttachmentRead(
  ctx: AttachmentCommandContext,
  arg: Record<string, unknown>,
): Promise<AttachmentCommandResult> {
  const sessionId = requireString(arg["sessionId"]);
  const ref = requireString(arg["ref"]);
  if (!sessionId || !ref) return failure("invalidPayload", "sessionId and ref are required");
  const offset = Math.max(0, requireNumber(arg["offset"]) ?? 0);
  const limit = clampLimit(requireNumber(arg["limit"]));
  if (!(await ctx.registry.authorize(sessionId, ref))) {
    return failure("notAuthorized", "attachment is not listed by this session");
  }
  const info = await ctx.registry.store.describe(ref);
  if (!info) return failure("notFound", "attachment not found");
  const mediaType = previewMediaType(info);
  if (!mediaType) return failure("notMedia", "attachment is not previewable media");
  if (mediaType.startsWith("image/") && info.bytes > PROTOCOL_V4_LIMITS.attachmentMaxBytes) {
    return failure("notMedia", "image exceeds the preview limit");
  }
  const range = await ctx.registry.store.readRange(ref, offset, limit);
  if (!range) return failure("notFound", "attachment not found");
  return {
    ok: true,
    value: {
      dataBase64: Buffer.from(range.bytes).toString("base64"),
      mediaType,
      totalBytes: range.info.bytes,
      nextOffset: range.nextOffset,
    },
  };
}

/** `attachmentPreviewSourceV4`: the runner never serves a local media URL (web is chunked). */
export async function handleAttachmentPreviewSource(
  ctx: AttachmentCommandContext,
  arg: Record<string, unknown>,
): Promise<AttachmentCommandResult> {
  const sessionId = requireString(arg["sessionId"]);
  const ref = requireString(arg["ref"]);
  if (!sessionId || !ref) return failure("invalidPayload", "sessionId and ref are required");
  if (!(await ctx.registry.authorize(sessionId, ref))) {
    return failure("notAuthorized", "attachment is not listed by this session");
  }
  return { ok: true, value: { kind: "chunked" } };
}

/** Share read: any MIME, authorization by row target + ref (spec A). */
export async function handleConversationAttachmentRead(
  ctx: AttachmentCommandContext,
  arg: Record<string, unknown>,
): Promise<AttachmentCommandResult> {
  const sessionId = requireString(arg["sessionId"]);
  const ref = requireString(arg["ref"]);
  const target = rowTargetOf(arg["target"]);
  const attachmentIndex = requireNumber(arg["attachmentIndex"]);
  if (!sessionId || !ref) return failure("invalidPayload", "sessionId and ref are required");
  if (!target || attachmentIndex === undefined || attachmentIndex === null) {
    return failure("invalidPayload", "target and attachmentIndex are required");
  }
  const authorized = await authorizeForRow(ctx, sessionId, ref, target, attachmentIndex);
  if (!authorized) return failure("notAuthorized", "attachment is not listed by that row");
  const range = await ctx.registry.store.readRange(
    ref,
    Math.max(0, requireNumber(arg["offset"]) ?? 0),
    clampLimit(requireNumber(arg["limit"])),
  );
  if (!range) return failure("notFound", "attachment not found");
  return {
    ok: true,
    value: {
      dataBase64: Buffer.from(range.bytes).toString("base64"),
      mediaType: range.info.mime || range.info.mediaType,
      totalBytes: range.info.bytes,
      nextOffset: range.nextOffset,
    },
  };
}

/** Share stat: metadata only, up to the protocol's 2 GiB stat bound. */
export async function handleConversationAttachmentStat(
  ctx: AttachmentCommandContext,
  arg: Record<string, unknown>,
): Promise<AttachmentCommandResult> {
  const sessionId = requireString(arg["sessionId"]);
  const ref = requireString(arg["ref"]);
  const target = rowTargetOf(arg["target"]);
  const attachmentIndex = requireNumber(arg["attachmentIndex"]);
  if (!sessionId || !ref) return failure("invalidPayload", "sessionId and ref are required");
  if (!target || attachmentIndex === undefined || attachmentIndex === null) {
    return failure("invalidPayload", "target and attachmentIndex are required");
  }
  const authorized = await authorizeForRow(ctx, sessionId, ref, target, attachmentIndex);
  if (!authorized) return failure("notAuthorized", "attachment is not listed by that row");
  const info = await ctx.registry.store.describe(ref);
  if (!info) return failure("notFound", "attachment not found");
  return {
    ok: true,
    value: {
      mediaType: info.mime || info.mediaType,
      totalBytes: info.bytes,
      mtimeMs: info.mtimeMs,
    },
  };
}

// ── helpers ──

/** The ref must be listed by the row named by `target` at `attachmentIndex`. */
async function authorizeForRow(
  ctx: AttachmentCommandContext,
  sessionId: string,
  ref: string,
  target: ConversationRowTarget,
  attachmentIndex: number,
): Promise<boolean> {
  const runtime = ctx.session(sessionId);
  if (!runtime) return false;
  await runtime.ready;
  const userRow = runtime.projection.rows.find(
    (row) =>
      row.kind === "userInput" &&
      (row.entityId === target.entityId ||
        (() => {
          const entryId = entryIdFromEntityId(target.entityId);
          return entryId ? row.entityId === userEntityId(entryId) : row.rowId === target.rowId;
        })()),
  );
  if (!userRow || userRow.kind !== "userInput") return false;
  const listed = userRow.attachments?.[attachmentIndex];
  if (!listed || listed.ref !== ref) return false;
  return (await ctx.registry.store.describe(ref)) !== null;
}

function previewMediaType(info: AttachmentInfo): string | null {
  const kind = attachmentKind(info.mediaType);
  if (kind === "image" || kind === "video" || kind === "pdf") return info.mediaType;
  return null;
}

function clampLimit(value: number | null): number {
  if (value === null || value <= 0) return PROTOCOL_V4_LIMITS.attachmentChunkMaxBytes;
  return Math.min(value, PROTOCOL_V4_LIMITS.attachmentChunkMaxBytes);
}

function rowTargetOf(value: unknown): ConversationRowTarget | null {
  if (typeof value !== "object" || value === null) return null;
  const record = value as Record<string, unknown>;
  const rowId = record["rowId"];
  const entityId = record["entityId"];
  if (typeof rowId !== "number" || typeof entityId !== "string" || entityId.length === 0) {
    return null;
  }
  return { rowId, entityId };
}

function requireString(value: unknown): string | null {
  return typeof value === "string" && value.length > 0 ? value : null;
}

function requireNumber(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function failure(reasonCode: string, message: string): AttachmentCommandResult {
  return { ok: false, reasonCode, message };
}

function failureOf(error: unknown): AttachmentCommandResult {
  if (typeof error === "object" && error !== null && "reasonCode" in error) {
    const reason = (error as { reasonCode: unknown }).reasonCode;
    if (typeof reason === "string") {
      return failure(
        `attachment.${reason}`,
        error instanceof Error ? error.message : String(error),
      );
    }
  }
  return failure("attachment.failed", error instanceof Error ? error.message : String(error));
}
