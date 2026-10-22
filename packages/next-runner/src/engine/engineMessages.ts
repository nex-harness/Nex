import type { EngineAssistantPart, EngineStopReason, EngineUsage } from "./types.js";

/** `nex.fork` custom entry: the fork notice at a child session's head (decision 6). */
export interface EngineForkMarker {
  role: "runnerMarker";
  kind: "forkNotice";
  at: number;
  entryId: string;
  parentSessionId: string;
  parentRowId: number;
}
/**
 * Normalized transcript used for restore; mirrors the pi message shapes we consume.
 *
 * `entryId` is pi's session entry id (`SessionEntryBase.id`). Restore derives the row
 * `entityId` from it (`pi:<entryId>`), so it must match the id the live projection learned
 * through `message_entry` — otherwise action targets change identity across a restart.
 */
export type EngineMessage =
  | {
      role: "user";
      at: number;
      text: string;
      entryId?: string;
      /**
       * Admitted attachment refs when the runner persisted a `nex.attachments` entry for
       * this user message (spec A). Restore re-renders them on the userInput row.
       */
      attachments?: import("@nex/shared/nex-protocol-v4").AttachmentRef[];
    }
  | {
      role: "assistant";
      at: number;
      model?: string;
      usage?: EngineUsage;
      stopReason: EngineStopReason;
      errorMessage?: string;
      entryId?: string;
      parts: EngineAssistantPart[];
    }
  | {
      role: "toolResult";
      at: number;
      toolCallId: string;
      toolName: string;
      text: string;
      isError: boolean;
      durationMs?: number;
      entryId?: string;
    }
  /**
   * A pi compaction boundary. Restore projects it as a `timelineMarker` row so the
   * pre-compaction transcript stays visible (research §0-b): `session.messages` is the
   * projected model context and drops exactly those rows.
   */
  | {
      role: "compaction";
      at: number;
      entryId: string;
      summary: string;
      tokensBefore: number;
      firstKeptEntryId: string;
      /**
       * `manual` when the entry belongs to a manual operation (pi `fromHook`, or the entry
       * linked to a live manual `compaction_start`); `auto` for threshold/overflow.
       */
      origin?: "manual" | "auto";
      /** Manual operation that produced this entry (the v4 commandId); keeps lineage on restore. */
      sourceCommandId?: string;
    }
  /** A branch summary left by pi's `navigateTree`; restore skips it (old CLI parity). */
  | { role: "branchSummary"; at: number; entryId: string; summary: string }
  /** Runner-only product markers stored as pi custom entries; restore projects them. */
  | EngineForkMarker
  | import("./checkpointMessages.js").EngineCheckpointsRecord
  | import("./checkpointMessages.js").EngineRewindRecord;

/**
 * `nex.fork` custom entry: the fork notice at a child session's head
 * (docs/next/conversation-features.md decision 6).
 */
export interface EngineForkMarker {
  role: "runnerMarker";
  kind: "forkNotice";
  at: number;
  entryId: string;
  parentSessionId: string;
  parentRowId: number;
}
