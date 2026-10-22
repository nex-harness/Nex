import type { ImageContent } from "@earendil-works/pi-ai";
import type { AttachmentRef } from "@nex/shared/nex-protocol-v4";
import type { AttachmentRegistry } from "../../attachments/registry.js";
import { attachmentKind, baseMime, isPiImageMime } from "../../attachments/mime.js";
import { isRunnerRef } from "../../attachments/refs.js";

/**
 * pi prompt feeding for admitted attachments (docs/next/attachments-rewind.md section A).
 *
 * pi is the authority on prompt content: `AgentSession.prompt(text, {images})` is the only
 * content channel, and pi's own `read` tool is how text/PDF/binary files reach the model.
 * So:
 *
 * ```
 * image/*           → read the blob → { type:"image", data, mimeType } in PromptOptions.images
 * everything else   → append a deterministic attachment block naming the on-disk path
 *                     (never the bytes): "[Attachment 1: name (mime, N bytes)] path=…"
 * ```
 *
 * The block is appended to the prompt text the model sees (and pi persists), not to the
 * user row: the row's text stays what the user typed and the refs ride the `user_input`
 * event. Contents are never inlined at admission; a file the model wants to see it reads
 * with `read`, which already truncates sensibly. Refs the runner cannot resolve (a foreign
 * machine's path) are rejected, never fabricated into a path.
 */

export interface PromptAttachments {
  /** Text handed to `session.prompt` (user text + attachment block). */
  text: string;
  /** pi prompt images. */
  images: ImageContent[];
  /** Refs that could not be resolved (logged, not fabricated). */
  rejected: Array<{ ref: AttachmentRef; reason: string }>;
}

/**
 * Where the "use read on the attached paths" hint goes. It is appended to pi's system
 * prompt for prompts with non-image attachments (not to the user text: the row shows what
 * the user typed) and is not persisted anywhere, so no marker entry is needed.
 */
export const ATTACHMENT_SYSTEM_HINT =
  "The user attached file(s) to this message. Their paths are listed at the end of the user message; use the read tool on a path when you need the file contents.";

/** What the model was handed: prompt text + images, and the user-visible row text. */
export interface MappedPrompt extends PromptAttachments {
  /** Text appended to the prompt (empty without attachments). */
  block: string;
  /** Refs that made it into the prompt (images and block entries). */
  accepted: AttachmentRef[];
}

export async function mapPromptAttachments(
  host: AttachmentRegistry,
  sessionId: string,
  refs: readonly AttachmentRef[] | undefined,
): Promise<MappedPrompt> {
  const images: ImageContent[] = [];
  const blocks: string[] = [];
  const rejected: PromptAttachments["rejected"] = [];
  if (!refs || refs.length === 0) return { text: "", images, rejected, block: "", accepted: [] };
  const accepted: AttachmentRef[] = [];
  let pathIndex = 0;
  let index = 0;
  for (const ref of refs) {
    index += 1;
    const kind = attachmentKind(ref.mime);
    if (isRunnerRef(ref.ref)) {
      // Authorization: the ref must be listed by a write of the session carrying this
      // prompt; the engine passes that session through `host`'s listing table.
      if (!host.isListed(sessionId, ref.ref)) {
        rejected.push({ ref, reason: "attachment.notAuthorized" });
        continue;
      }
    }
    if (kind === "image" && isRunnerRef(ref.ref) && isPiImageMime(ref.mime)) {
      const blob = await host.store.read(ref.ref);
      if (!blob) {
        rejected.push({ ref, reason: "attachment.readFailed" });
        continue;
      }
      if (attachmentKind(blob.info.mediaType) !== "image") {
        // Magic-byte check failed at commit; never claim a non-image is an image.
        rejected.push({ ref, reason: "attachment.mimeMismatch" });
        continue;
      }
      images.push({
        type: "image",
        data: Buffer.from(blob.bytes).toString("base64"),
        mimeType: baseMime(blob.info.mediaType),
      });
      accepted.push(ref);
      continue;
    }
    const location = await resolveLocalPath(host, ref);
    if (!location) {
      // Local-path refs must resolve inside the runner data dir; a desktop workspace path
      // is not readable on this machine.
      rejected.push({ ref, reason: "attachment.unresolvable" });
      continue;
    }
    blocks.push(attachmentBlockLine(pathIndex + 1, ref, location));
    pathIndex += 1;
    accepted.push(ref);
  }
  const block = blocks.length === 0 ? "" : `\n\n${blocks.join("\n")}`;
  return { text: block, images, rejected, block, accepted };
}

/**
 * Absolute path the model's `read` tool can open: the committed blob's path inside the
 * runner data dir. A ref the store cannot resolve (foreign path, deleted blob) is rejected
 * rather than turned into a fake path.
 */
async function resolveLocalPath(
  host: AttachmentRegistry,
  ref: AttachmentRef,
): Promise<string | null> {
  const info = await host.store.describe(ref.ref);
  return info?.path ?? null;
}

/**
 * One deterministic line per attached file: the model sees a stable name, type, size and
 * the on-disk path its `read` tool accepts. Contents are never inlined at admission.
 */
export function attachmentBlockLine(index: number, ref: AttachmentRef, path: string): string {
  return `[Attachment ${index}: ${ref.fileName} (${baseMime(ref.mime)}, ${ref.bytes} bytes)] path=${path}`;
}
