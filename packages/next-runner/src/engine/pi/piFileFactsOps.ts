import type { SessionManager } from "@earendil-works/pi-coding-agent";
import type { CheckpointService } from "../checkpoints/checkpointService.js";
import type { RewindPreview, RewindStore } from "../checkpoints/rewindStore.js";
import type { NexCheckpointsRecord, NexRewindRecord } from "../checkpoints/types.js";
import { CHECKPOINTS_CUSTOM_TYPE, REWIND_CUSTOM_TYPE } from "../checkpoints/types.js";
import { parseUnifiedPatch } from "../checkpoints/patch.js";
import type { EngineApplyFileRewindOutcome, EngineFileChanges, EngineFileFacts } from "../types.js";
import type { OpenSession } from "./sessionLifecycle.js";

/**
 * File facts read from a pi session (spec B): the `nex.checkpoints` / `nex.rewind` custom
 * entries of a turn's branch, aggregated for the changed-files panel and the rewind path.
 * Nothing here writes the session except `apply`'s trailing `nex.rewind` entry.
 */
export interface FileFactsDeps {
  checkpoints: CheckpointService;
  rewind: RewindStore;
}

interface TurnFacts {
  record?: NexCheckpointsRecord;
  reverted: boolean;
  /** Paths a shell tool touched (from the record's bash operations or the live entry). */
  shellTouched: string[];
}

export async function readFileFacts(
  deps: FileFactsDeps,
  open: OpenSession,
  userEntryId: string,
): Promise<TurnFacts & { rewindable: boolean }> {
  const manager = open.sessionManager;
  const branch = manager.getBranch();
  const record = findCheckpointRecord(branch, userEntryId);
  const reverted = findRewindRecord(branch, userEntryId) !== undefined;
  const shellTouched = record
    ? record.operations.filter((operation) => operation.toolName === "bash").map((o) => o.path)
    : [];
  const rewindable =
    (await deps.checkpoints.available()) && record !== undefined && !record.partial;
  return { record, reverted, shellTouched, rewindable };
}

export async function factsOf(
  deps: FileFactsDeps,
  open: OpenSession,
  userEntryId: string,
): Promise<EngineFileFacts> {
  const facts = await readFileFacts(deps, open, userEntryId);
  return {
    found: facts.record !== undefined,
    ...(facts.record ? { record: facts.record } : {}),
    reverted: facts.reverted,
    ...(facts.record ? { turnId: facts.record.turnId } : {}),
    rewindable: facts.rewindable,
  };
}

export async function fileChangesOf(
  deps: FileFactsDeps,
  open: OpenSession,
  userEntryId: string,
): Promise<EngineFileChanges> {
  const facts = await readFileFacts(deps, open, userEntryId);
  const changes = await deps.rewind.fileChanges(facts.record, facts.reverted);
  return {
    files: changes.files,
    additions: changes.additions,
    deletions: changes.deletions,
    state: changes.state,
    items: changes.items.map((item) => ({
      ...item,
      // The stored patch is the authoritative hunk source (git may be gone by now).
      ...(item.patches.length === 0 ? { patches: patchesOf(facts.record, item.path) } : {}),
    })),
  };
}

export async function previewOf(
  deps: FileFactsDeps,
  open: OpenSession,
  userEntryId: string,
): Promise<RewindPreview> {
  const facts = await readFileFacts(deps, open, userEntryId);
  if (facts.reverted) return { canApply: false, ignoredFiles: [], safeFiles: [], unsafeFiles: [] };
  const { preview } = await deps.rewind.preview(facts.record, facts.shellTouched);
  return { ...preview, canApply: gateCanApply(preview.safeFiles, preview.unsafeFiles) };
}

export async function applyOf(
  deps: FileFactsDeps,
  open: OpenSession,
  userEntryId: string,
  options: { commitAfterApply?: () => Promise<void>; sourceCommandId?: string },
): Promise<EngineApplyFileRewindOutcome> {
  const facts = await readFileFacts(deps, open, userEntryId);
  if (facts.reverted) {
    return {
      applied: false,
      preview: { canApply: false, ignoredFiles: [], safeFiles: [], unsafeFiles: [] },
      response: "This turn's file changes were already undone.",
    };
  }
  const { preview: raw, plan } = await deps.rewind.preview(facts.record, facts.shellTouched);
  const preview = { ...raw, canApply: gateCanApply(raw.safeFiles, raw.unsafeFiles) };
  if (!preview.canApply) {
    return {
      applied: false,
      preview,
      response: preview.unsafeFiles.some((file) => file.reason === "external_modified")
        ? "Files changed outside the runner; nothing was restored."
        : preview.safeFiles.length === 0
          ? "No files can be safely restored for this turn."
          : "Some files cannot be safely restored; nothing was restored.",
    };
  }
  const result = await deps.rewind.apply(plan, options);
  if (!result.applied) {
    return {
      applied: false,
      preview,
      response: `File restore failed and was rolled back: ${result.failures[0] ?? "unknown error"}`,
    };
  }
  const marker: NexRewindRecord = {
    version: 1,
    userEntryId,
    turnId: facts.record?.turnId ?? "",
    revertedAt: Date.now(),
    sourceCommandId: options.sourceCommandId ?? "",
    files: plan.length,
  };
  open.sessionManager.appendCustomEntry(REWIND_CUSTOM_TYPE, marker);
  return {
    applied: true,
    preview,
    response: `Restored ${plan.length} file${plan.length === 1 ? "" : "s"}.`,
  };
}

/**
 * An ignored (bash-touched) path does not have to be present: deleting a shell-created
 * scratch file changes nothing for the turn, so `ignoredFiles` alone must not block the
 * rewind (a real shell modification is still reported, just non-blocking). The gate is the
 * old CLI's safe-set rule: some safe files, no unsafe file.
 */
function gateCanApply(safeFiles: unknown[], unsafeFiles: unknown[]): boolean {
  return safeFiles.length > 0 && unsafeFiles.length === 0;
}

function findCheckpointRecord(
  branch: ReturnType<SessionManager["getBranch"]>,
  userEntryId: string,
): NexCheckpointsRecord | undefined {
  let found: NexCheckpointsRecord | undefined;
  for (const entry of branch) {
    if (entry.type !== "custom" || entry.customType !== CHECKPOINTS_CUSTOM_TYPE) continue;
    const data = asRecord(entry.data);
    if (data?.["userEntryId"] !== userEntryId) continue;
    const parsed = parseCheckpoints(data);
    if (parsed) found = parsed;
  }
  return found;
}

function findRewindRecord(
  branch: ReturnType<SessionManager["getBranch"]>,
  userEntryId: string,
): NexRewindRecord | undefined {
  for (let index = branch.length - 1; index >= 0; index -= 1) {
    const entry = branch[index];
    if (!entry || entry.type !== "custom" || entry.customType !== REWIND_CUSTOM_TYPE) continue;
    const data = asRecord(entry.data);
    if (data?.["userEntryId"] !== userEntryId) continue;
    const files = data["files"];
    return {
      version: 1,
      userEntryId,
      turnId: typeof data["turnId"] === "string" ? data["turnId"] : "",
      revertedAt: typeof data["revertedAt"] === "number" ? data["revertedAt"] : 0,
      sourceCommandId: typeof data["sourceCommandId"] === "string" ? data["sourceCommandId"] : "",
      files: typeof files === "number" ? files : 0,
    };
  }
  return undefined;
}

/** Validates a persisted record (a hand-edited session file must not crash restore). */
export function parseCheckpoints(
  data: Record<string, unknown> | null,
): NexCheckpointsRecord | undefined {
  if (!data || data["version"] !== 1) return undefined;
  const userEntryId = data["userEntryId"];
  const turnId = data["turnId"];
  const files = asRecord(data["files"]);
  const operations = data["operations"];
  if (typeof userEntryId !== "string" || typeof turnId !== "string") return undefined;
  if (!files || !Array.isArray(operations)) return undefined;
  const parsedFiles: NexCheckpointsRecord["files"] = {};
  for (const [path, value] of Object.entries(files)) {
    const blob = asRecord(value)?.["blob"];
    parsedFiles[path] = { blob: typeof blob === "string" ? blob : null };
  }
  const parsedOperations: NexCheckpointsRecord["operations"] = [];
  for (const raw of operations) {
    const operation = asRecord(raw);
    const path = operation?.["path"];
    const toolName = operation?.["toolName"];
    if (!operation || typeof path !== "string" || typeof toolName !== "string") continue;
    parsedOperations.push({
      path,
      toolName,
      toolCallId: typeof operation["toolCallId"] === "string" ? operation["toolCallId"] : "",
      postBlob: typeof operation["postBlob"] === "string" ? operation["postBlob"] : null,
      additions: numberOr(operation["additions"]),
      deletions: numberOr(operation["deletions"]),
      ...(typeof operation["patch"] === "string" ? { patch: operation["patch"] } : {}),
    });
  }
  return {
    version: 1,
    userEntryId,
    turnId,
    sequence: numberOr(data["sequence"]),
    files: parsedFiles,
    operations: parsedOperations,
    ...(data["partial"] === true ? { partial: true } : {}),
  };
}

function patchesOf(
  record: NexCheckpointsRecord | undefined,
  path: string,
): Array<{
  oldStart: number;
  oldLines: number;
  newStart: number;
  newLines: number;
  lines: string[];
}> {
  const operation = record?.operations.filter((item) => item.path === path).at(-1);
  if (!operation?.patch) return [];
  return parseUnifiedPatch(operation.patch).hunks;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function numberOr(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}
