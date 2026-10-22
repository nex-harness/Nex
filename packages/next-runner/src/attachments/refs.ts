import { createHash } from "node:crypto";
import { isAbsolute, join, relative, resolve, sep } from "node:path";

/**
 * Attachment reference grammar (docs/next/attachments-rewind.md section A).
 *
 * ```
 * nex-runner://<sessionId>/<uploadId>/<sanitized name>
 * ```
 *
 * The file name never becomes a path component: resolution maps a ref to
 * `<dataDir>/attachments/<sessionId>/<uploadId>/blob`, and both ids are checked against a
 * conservative grammar before they are joined. `resolveAttachmentDir` additionally proves
 * the resolved directory stays inside the attachments root (path-traversal defence).
 */

export const RUNNER_REF_SCHEME = "nex-runner://";

const UPLOAD_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const SESSION_ID_PATTERN = /^[A-Za-z0-9._:-]{1,256}$/;

export interface ParsedRunnerRef {
  sessionId: string;
  uploadId: string;
  /** Decoded display name (the sanitized form; never used as a path component). */
  fileName: string;
}

export function isRunnerRef(ref: string): boolean {
  return ref.startsWith(RUNNER_REF_SCHEME);
}

/**
 * Parses one runner ref. Returns null for anything malformed: unknown scheme, extra path
 * segments, `..`, backslashes, an invalid id, or a malformed percent escape.
 */
export function parseRunnerRef(ref: string): ParsedRunnerRef | null {
  if (!isRunnerRef(ref)) return null;
  const rest = ref.slice(RUNNER_REF_SCHEME.length);
  const segments = rest.split("/");
  if (segments.length !== 3) return null;
  const [sessionId, uploadId, rawName] = segments as [string, string, string];
  if (!SESSION_ID_PATTERN.test(sessionId) || !UPLOAD_ID_PATTERN.test(uploadId)) return null;
  if (!rawName || rawName.includes("\\")) return null;
  let fileName: string;
  try {
    fileName = decodeURIComponent(rawName);
  } catch {
    return null;
  }
  if (fileName.length === 0 || fileName === "." || fileName === "..") return null;
  // The display name never controls a path, but a ref that *decodes* to a traversal is
  // still a malformed ref (and must never be echoed back as a "valid" one).
  // eslint-disable-next-line no-control-regex -- NUL is a control char by definition.
  if (/[/\\\0\r\n]/.test(fileName)) return null;
  return { sessionId, uploadId, fileName };
}

/**
 * File-name sanitiser for the ref's display segment and for `meta.json`. `[^\w.-]` becomes
 * `_`; path separators and `..` are rejected (not rewritten) so a hostile name can never
 * look like a traversal.
 */
export function sanitizeFileName(raw: string): string | null {
  const name = raw.normalize("NFC");
  if (name.length === 0 || name.length > 255) return null;
  // eslint-disable-next-line no-control-regex -- NUL/CR/LF never belong in an attachment name.
  if (/[/\\\0\r\n]/.test(name)) return null;
  if (name === "." || name === "..") return null;
  return name.replace(/[^\w.-]/gu, "_").slice(0, 120) || null;
}

export function runnerRef(sessionId: string, uploadId: string, fileName: string): string {
  return `${RUNNER_REF_SCHEME}${sessionId}/${uploadId}/${encodeURIComponent(fileName)}`;
}

/** `<dataDir>/attachments` — the only root ref resolution may read from. */
export function attachmentsRoot(dataDir: string): string {
  return join(dataDir, "attachments");
}

/** `<dataDir>/attachments/<sessionId>`; the id is grammar-checked before joining. */
export function sessionAttachmentDir(dataDir: string, sessionId: string): string {
  if (!SESSION_ID_PATTERN.test(sessionId)) {
    throw new Error(`invalid attachment session id: ${sessionId}`);
  }
  return join(attachmentsRoot(dataDir), sessionId);
}

/**
 * Resolves the directory of one committed/staging upload. `null` when the ids are invalid
 * or the result (after `resolve`) escapes both the session directory and the attachments
 * root — the two checks the spec requires.
 */
export function resolveAttachmentDir(
  dataDir: string,
  sessionId: string,
  uploadId: string,
): string | null {
  if (!SESSION_ID_PATTERN.test(sessionId) || !UPLOAD_ID_PATTERN.test(uploadId)) return null;
  const root = resolve(attachmentsRoot(dataDir));
  const sessionDir = resolve(sessionAttachmentDir(dataDir, sessionId));
  const dir = resolve(join(sessionDir, uploadId));
  if (!isInside(root, dir) || !isInside(sessionDir, dir) || dir === sessionDir) return null;
  return dir;
}

/** True when `candidate` is strictly inside `parent` (same path or deeper is false for `===`). */
export function isInside(parent: string, candidate: string): boolean {
  const rel = relative(resolve(parent), resolve(candidate));
  if (rel.length === 0) return false;
  if (isAbsolute(rel)) return false;
  return rel !== ".." && !rel.startsWith(`..${sep}`);
}

/** Stable short hash used for content-addressed temp names and unsafe-ref logging. */
export function shortHash(value: string): string {
  return createHash("sha256").update(value).digest("hex").slice(0, 16);
}
