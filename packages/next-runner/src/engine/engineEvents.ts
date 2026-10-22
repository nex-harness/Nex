import type {
  EngineApprovalOutcome,
  EngineApprovalRequest,
  EngineFollowupMode,
  EngineModelState,
  EnginePhase,
  EngineStopReason,
  EngineTitleSource,
  EngineUsage,
  PermissionMode,
} from "./types.js";

/**
 * Live engine events (kept apart from the boundary declaration for the file budget).
 * Deliberately close to pi's AgentSessionEvent but stripped of cumulative `partial`
 * payloads, so projection stays pure and cheap.
 */
export type EngineEvent =
  | {
      type: "user_input";
      at: number;
      text: string;
      /** Admitted attachment refs of this prompt (spec A); the row renders them live. */
      attachments?: import("@nex/shared/nex-protocol-v4").AttachmentRef[];
    }
  /**
   * A message became a persisted pi session entry. pi emits `message_end` before
   * `appendMessage`, so the row already exists; this event carries the persisted entry id
   * that turns row identity into `pi:<entryId>` (docs/next/conversation-features.md
   * decision 1). Events arrive in persistence order, so the projection pairs each id with
   * the oldest row group of the same role.
   */
  | { type: "message_entry"; at: number; entryId: string; role: "user" | "assistant" }
  | { type: "assistant_start"; at: number; model?: string }
  | { type: "text_delta"; at: number; delta: string }
  | { type: "thinking_delta"; at: number; delta: string }
  | { type: "tool_call_start"; at: number; toolCallId: string; toolName: string; input?: unknown }
  | {
      type: "tool_input_delta";
      at: number;
      toolCallId: string;
      toolName: string;
      delta: string;
    }
  | {
      type: "tool_call_update";
      at: number;
      toolCallId: string;
      toolName: string;
      text?: string;
    }
  | {
      type: "tool_call_end";
      at: number;
      toolCallId: string;
      toolName: string;
      text: string;
      isError: boolean;
      input?: unknown;
      /**
       * File-diff display derived from the checkpoint record (spec B). Set by the engine
       * after `details.patch` (edit) or the synthesized write patch; the projection copies
       * it verbatim into `output.display`.
       */
      display?: import("./checkpoints/types.js").FileDiffDisplay;
    }
  | {
      type: "assistant_end";
      at: number;
      stopReason: EngineStopReason;
      errorMessage?: string;
      model?: string;
      usage?: EngineUsage;
    }
  | { type: "settled"; at: number; aborted: boolean }
  /**
   * Checkpoint facts for a finished turn, persisted as `nex.checkpoints` when the user
   * entry is known. `display` per tool arrives on `tool_call_end`; this event only aims the
   * summary at a turn (and carries the persisted data for the live projection).
   */
  | {
      type: "turn_checkpoint";
      at: number;
      turnId: string;
      userEntryId?: string;
      data: import("./checkpoints/types.js").NexCheckpointsRecord;
    }
  | {
      type: "turn_rewind";
      at: number;
      userEntryId: string;
      data: import("./checkpoints/types.js").NexRewindRecord;
    }
  /**
   * pi's own steer/follow-up queue contents (`AgentSessionEvent.queue_update`). The runner
   * never uses pi's follow-up queue for queued text (docs/next/conversation-features.md
   * decision 4), but guide messages live in pi's steering queue until the model consumes
   * them; this event is how a pending guide item learns it was consumed.
   */
  | { type: "pi_queue_update"; at: number; steering: string[]; followUp: string[] }
  | { type: "phase"; at: number; phase: EnginePhase }
  /** Generated/renamed title; projection updates `meta` without another snapshot source. */
  | { type: "title_changed"; at: number; title: string; titleSource: EngineTitleSource }
  | { type: "model_changed"; at: number; model: EngineModelState }
  /** Mid-session switch marker; `from` is absent for the explicit ∅→X boundary. */
  | {
      type: "model_change_marker";
      at: number;
      from?: { provider: string; model: string };
      to: { provider: string; model: string; thought: string };
      sourceCommandId?: string;
    }
  | {
      type: "followup_mode_changed";
      at: number;
      mode: EngineFollowupMode;
      sourceCommandId?: string;
    }
  /**
   * pi began a compaction. `reason:"manual"` is either the runner's own `compact()` call
   * (linked by `sourceCommandId`, set while a manual operation is in flight) or another
   * manual trigger; `threshold`/`overflow` are pi's automatic pre-run compaction.
   */
  | {
      type: "compact_start";
      at: number;
      reason: "manual" | "threshold" | "overflow";
      sourceCommandId?: string;
    }
  | {
      type: "compact_end";
      at: number;
      reason: "manual" | "threshold" | "overflow";
      sourceCommandId?: string;
      /** Present when the compaction completed; absent on failure/cancel. */
      result?: { tokensBefore: number; tokensAfter?: number };
      aborted: boolean;
      errorMessage?: string;
    }
  /** Transient API retry (pi `auto_retry_*`); never a row, surfaced for the UI's retry chip. */
  | { type: "api_retry_start"; at: number; attempt: number; maxAttempts: number; delayMs: number }
  | { type: "api_retry_end"; at: number; success: boolean }
  | { type: "mode_changed"; at: number; mode: PermissionMode; planEnabled: boolean }
  | { type: "approval_request"; at: number; approval: EngineApprovalRequest }
  | {
      type: "approval_settled";
      at: number;
      interactionId: string;
      toolCallId: string;
      outcome: EngineApprovalOutcome;
    }
  | { type: "error"; at: number; message: string };
