import type { Logger } from "../../logger.js";
import type { CheckpointService } from "../checkpoints/checkpointService.js";
import type { NexCheckpointsRecord } from "../checkpoints/types.js";
import type { EngineEvent } from "../types.js";

/** Dependencies / live state of the checkpoint hook (kept apart for the file budget). */
export interface CheckpointHookDeps {
  service: CheckpointService;
  logger: Logger;
  /** Persists the `nex.checkpoints` custom entry (idempotent per turn id). */
  persistCheckpoints: (data: NexCheckpointsRecord) => Promise<void>;
  /** Live projection event (tool display / turn summary). */
  emit: (event: EngineEvent) => void;
}

export interface PendingTurn {
  turnId: string;
  /** pi entry id of the user message; unknown until `message_entry` reports it. */
  userEntryId?: string;
  /** Paths a shell tool may have touched (rewind preview reports them as ignored). */
  shellTouched: Set<string>;
  /** True once a mutating call recorded something (the settle then persists a record). */
  touched: boolean;
}
