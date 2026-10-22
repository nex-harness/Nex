import type { ToolResultEvent } from "@earendil-works/pi-coding-agent";
import type { RecordedOperation } from "../checkpoints/checkpointService.js";
import { parseUnifiedPatch } from "../checkpoints/patch.js";
import type { FileDiffDisplay, NexCheckpointsRecord } from "../checkpoints/types.js";
import type { CheckpointHookDeps, PendingTurn } from "./checkpointHookTypes.js";

/**
 * pi ↔ checkpoint glue for one open session (spec B, "Runner design").
 *
 * ```
 * message_end(user)      ──► startTurn()                     new product turn (id turn-N)
 * message_entry(user)    ──► setUserEntry(entryId)           rewind anchor
 * tool_call(edit/write)  ──► prepareTurn(turn, path)         first call snapshots the turn
 * tool_result            ──► recordOperation(path)           post-state blob + patch
 * agent_settled          ──► settle() → persist nex.checkpoints (+ turn_checkpoint event)
 * abort/rewind           ──► discardTurn()                   nothing of the cut turn persists
 * ```
 *
 * Turn ownership follows the product turn, not pi's per-tool-round `turn_start`: a durable
 * user message opens it and the settle closes it. The turn id is the product row's turn id,
 * so a `tool_call_end` display lands on tool rows that are still "in flight" (they inherit
 * the current turn) and the header summary lines up with the same turn.
 */

export class PiCheckpointHook {
  private turn: PendingTurn | null = null;
  private seq = 0;
  private settleTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(private readonly deps: CheckpointHookDeps) {}

  /** The product turn that new tool rows inherit while this hook owns it. */
  get currentTurnId(): string | undefined {
    return this.turn?.turnId;
  }

  /** pi's `tool_call` hook: checkpoint before the tool mutates anything. */
  async beforeToolCall(toolName: string, input: unknown): Promise<void> {
    const service = this.deps.service;
    if (service.disabled) return;
    if (service.isShellTool(toolName)) {
      // A shell command can touch anything; its files show as "ignored (bash)" in previews.
      if (this.turn) {
        const path = shellPathOf(input);
        if (path) this.turn.shellTouched.add(path);
      }
      return;
    }
    if (!service.isMutatingTool(toolName)) return;
    const turn = this.turn;
    if (!turn) return;
    const path = filePathOf(input);
    await service.prepareTurn(turn.turnId, turn.userEntryId ?? "", path);
  }

  /** pi's `tool_result` hook: hash the post-state, build the diff, aim it at the tool row. */
  async afterToolResult(event: ToolResultEvent): Promise<void> {
    const service = this.deps.service;
    if (service.disabled) return;
    const turn = this.turn;
    if (!turn) return;
    if (service.isShellTool(event.toolName)) {
      const path = shellPathOf(event.input);
      if (path) turn.shellTouched.add(path);
      return;
    }
    if (!service.isMutatingTool(event.toolName)) return;
    const path = filePathOf(event.input);
    if (!path) return;
    const patch = editPatchOf(event.details);
    const operation = await service.recordOperation(turn.turnId, {
      toolCallId: event.toolCallId,
      toolName: event.toolName,
      path,
      ...(patch !== undefined ? { patch } : {}),
      isError: event.isError,
    });
    if (!operation) return;
    turn.touched = true;
    const display = displayOf(operation, path);
    if (!display) return;
    this.deps.emit({
      type: "tool_call_end",
      at: Date.now(),
      toolCallId: event.toolCallId,
      toolName: event.toolName,
      text: "",
      isError: false,
      display,
    });
  }

  /** A durable user message starts its product turn (queued prompts close the previous one). */
  startTurn(): void {
    this.flushNow();
    this.seq += 1;
    this.turn = {
      turnId: `turn-${this.seq}`,
      shellTouched: new Set(),
      touched: false,
    };
  }

  /** `message_entry(user)`: the turn's rewind anchor is now known. */
  setUserEntry(entryId: string): void {
    if (!this.turn) return;
    this.turn.userEntryId = entryId;
    // The entry can arrive after the settle started the flush; persist now.
    this.flushNow();
  }

  /**
   * `agent_settled`: persist after one macrotask, so pi's post-settle message persistence
   * has emitted `message_entry` (the entry id is the record's identity).
   */
  settle(): void {
    if (this.settleTimer) clearTimeout(this.settleTimer);
    this.settleTimer = setTimeout(() => {
      this.settleTimer = null;
      this.flushNow();
    }, 50);
  }

  /** A branch cut/rewind: nothing of the abandoned turn may be persisted or displayed. */
  discardTurn(): void {
    if (this.settleTimer) {
      clearTimeout(this.settleTimer);
      this.settleTimer = null;
    }
    this.turn = null;
    this.deps.service.resetSession();
  }

  /** Persists the current turn's record now (settle timer / session close). */
  flushNow(): void {
    const turn = this.turn;
    if (!turn || !turn.touched) return;
    const record = this.buildRecord(turn);
    if (!record) return;
    if (!turn.userEntryId) {
      // The anchor is a `message_entry` microtask behind the persist; retry once so the
      // record is never dropped for a missing id.
      if (!this.settleTimer) {
        this.settleTimer = setTimeout(() => {
          this.settleTimer = null;
          if (this.turn === turn) this.flushNow();
        }, 50);
      }
      return;
    }
    turn.touched = false;
    const { turnId, userEntryId } = turn;
    void this.deps.persistCheckpoints(record).then(
      () =>
        this.deps.emit({
          type: "turn_checkpoint",
          at: Date.now(),
          turnId,
          userEntryId,
          data: record,
        }),
      (error: unknown) =>
        this.deps.logger.warn("persisting nex.checkpoints failed", {
          message: error instanceof Error ? error.message : String(error),
        }),
    );
  }

  private buildRecord(turn: PendingTurn): NexCheckpointsRecord | null {
    const raw = this.deps.service.record(turn.turnId);
    if (!raw) return null;
    const shell = [...turn.shellTouched]
      .filter((path) => !raw.operations.some((operation) => operation.path === path))
      .map((path) => ({
        path,
        toolCallId: "bash",
        toolName: "bash",
        postBlob: null,
        additions: 0,
        deletions: 0,
      }));
    return {
      version: 1,
      ...raw,
      userEntryId: turn.userEntryId ?? raw.userEntryId,
      operations: [...raw.operations, ...shell],
    };
  }
}

/** `{path}` of an edit/write call (relative to the session cwd, i.e. the workspace). */
function filePathOf(input: unknown): string | undefined {
  if (typeof input !== "object" || input === null) return undefined;
  const record = input as Record<string, unknown>;
  const path = record["path"] ?? record["file_path"];
  return typeof path === "string" && path.length > 0 ? path : undefined;
}

/** A plain `touch/rm/mv/… <path>` command yields a path hint; anything else is opaque. */
function shellPathOf(input: unknown): string | undefined {
  if (typeof input !== "object" || input === null) return undefined;
  const command = (input as Record<string, unknown>)["command"];
  if (typeof command !== "string") return undefined;
  const match = /^(?:touch|rm|mv|cp|mkdir|tee|sed|echo)\s+(?:-[A-Za-z]+\s+)*([^\s|&<>;]+)/u.exec(
    command.trim(),
  );
  const candidate = match?.[1];
  if (!candidate || candidate.startsWith("-")) return undefined;
  return candidate.replace(/^["']|["']$/gu, "");
}

/** pi's edit result carries the standard unified patch in `details.patch`. */
function editPatchOf(details: unknown): string | undefined {
  if (typeof details !== "object" || details === null) return undefined;
  const patch = (details as Record<string, unknown>)["patch"];
  return typeof patch === "string" && patch.length > 0 ? patch : undefined;
}

/** `file_diff` display for a tool row (the path exactly as the tool received it). */
export function displayOf(
  operation: RecordedOperation,
  inputPath: string,
): FileDiffDisplay | undefined {
  if (operation.patch === undefined) return undefined;
  const { hunks } = parseUnifiedPatch(operation.patch);
  return {
    kind: "file_diff",
    filePath: inputPath,
    additions: operation.additions,
    deletions: operation.deletions,
    structuredPatch: hunks,
  };
}
