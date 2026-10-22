/**
 * MIME classification for attachments (docs/next/attachments-rewind.md section A).
 *
 * The composer declares the mime; media types are verified against magic bytes on commit
 * so a renamed/mislabelled file can never be handed to pi as an image. SVG is deliberately
 * not an image here: pi's `read` tool has no SVG decoder and serving SVG-RDF would be an
 * XSS vector on a preview surface, so it is a plain file.
 */

export type AttachmentKind = "image" | "video" | "pdf" | "file";

const MEDIA_MIMES = new Set([
  "image/png",
  "image/jpeg",
  "image/gif",
  "image/webp",
  "image/bmp",
  "video/mp4",
  "application/pdf",
]);

/** Lowercases and strips parameters (`image/png; charset=binary` → `image/png`). */
export function baseMime(mime: string): string {
  return (mime.split(";", 1)[0] ?? "").trim().toLowerCase();
}

export function attachmentKind(mime: string): AttachmentKind {
  const base = baseMime(mime);
  if (base.startsWith("image/")) return "image";
  if (base.startsWith("video/")) return "video";
  if (base === "application/pdf") return "pdf";
  return "file";
}

/** Types whose bytes must match a signature on commit. */
export function requiresMagicCheck(mime: string): boolean {
  return MEDIA_MIMES.has(baseMime(mime));
}

const HINTS: ReadonlyArray<{ mime: string; test: (head: Uint8Array) => boolean }> = [
  {
    mime: "image/png",
    test: (b) => startsWith(b, [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
  },
  { mime: "image/jpeg", test: (b) => startsWith(b, [0xff, 0xd8, 0xff]) },
  { mime: "image/gif", test: (b) => ascii(b, 0, "GIF87a") || ascii(b, 0, "GIF89a") },
  { mime: "image/webp", test: (b) => ascii(b, 0, "RIFF") && ascii(b, 8, "WEBP") },
  { mime: "image/bmp", test: (b) => ascii(b, 0, "BM") },
  { mime: "application/pdf", test: (b) => ascii(b, 0, "%PDF-") },
  { mime: "video/mp4", test: (b) => ascii(b, 4, "ftyp") },
];

/**
 * Sniffs the leading bytes. Returns the detected media mime or `null` when unknown. The
 * caller compares it with the declared type when `requiresMagicCheck` is true.
 */
export function sniffMediaMime(head: Uint8Array): string | null {
  for (const hint of HINTS) {
    if (hint.test(head)) return hint.mime;
  }
  return null;
}

function startsWith(bytes: Uint8Array, prefix: readonly number[]): boolean {
  if (bytes.length < prefix.length) return false;
  return prefix.every((value, index) => bytes[index] === value);
}

function ascii(bytes: Uint8Array, offset: number, text: string): boolean {
  if (bytes.length < offset + text.length) return false;
  for (let index = 0; index < text.length; index += 1) {
    if (bytes[offset + index] !== text.charCodeAt(index)) return false;
  }
  return true;
}

/**
 * Effective media type of a committed blob for preview/read: the sniffed type when it is
 * known (so a mislabelled-but-valid file previews correctly), otherwise the declared type.
 * Non-media declared types never get promoted to media.
 */
export function effectiveMediaMime(declared: string, head: Uint8Array): string {
  if (!requiresMagicCheck(declared)) return baseMime(declared);
  return sniffMediaMime(head) ?? baseMime(declared);
}

/** `image/png` etc. that `attachmentReadV4`'s result schema would accept. */
export function isPreviewableMedia(mime: string): boolean {
  const base = baseMime(mime);
  return base.startsWith("image/") || base.startsWith("video/") || base === "application/pdf";
}

/** The four image types pi's `read` tool and prompts accept (SVG is a plain file). */
const PI_IMAGE_MIMES = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);

/**
 * True when the file may be handed to pi as an image. BMP is a legal declared image for
 * previews but pi cannot decode it, so it stays a block ref (`read` degrades gracefully).
 */
export function isPiImageMime(mime: string): boolean {
  return PI_IMAGE_MIMES.has(baseMime(mime));
}
