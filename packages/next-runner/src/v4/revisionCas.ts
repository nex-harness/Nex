import {
  COMMANDS_REQUIRING_BASE_REVISION,
  ROW_TARGETING_COMMANDS,
  type CommandAck,
  type CommandEnvelope,
} from "@nex/shared/nex-protocol-v4";
import type { SessionRuntime } from "./sessionStore.js";
import { staleAck } from "./envelopes.js";

/**
 * Revision CAS (docs/next/conversation-features.md decision 3).
 *
 * Port of the old CLI's admission gate (`command-inbox.ts:326-364`): every command in
 * `COMMANDS_REQUIRING_BASE_REVISION` must carry the caller's `baseRevision`, commands that
 * target a row must also carry `baseLogEpoch`, and a mismatch answers `stale` with the
 * runner's *real* current revision so the client can retry against it
 * (`SessionPane.dispatchConfigCas` uses `revisionAtDecision` for exactly that).
 *
 * Deviation from the old CLI, deliberate: a missing `baseRevision`/`baseLogEpoch` is not
 * rejected here. The server validates envelopes with `parseCommandEnvelope` before they
 * reach a runner; the runner repeats the CAS comparison so a stale command cannot slip
 * through, but it does not duplicate payload validation.
 */
export function checkRevisionCas(
  runtime: SessionRuntime,
  envelope: CommandEnvelope,
): CommandAck | undefined {
  if (!COMMANDS_REQUIRING_BASE_REVISION.has(envelope.type)) return undefined;
  const revision = runtime.projection.revision;
  if (envelope.baseLogEpoch !== undefined && envelope.baseLogEpoch !== runtime.logEpoch) {
    // The log epoch identifies the row-id space. A command carrying a row target from a
    // previous runner process can never be resolved, even if the revision happens to match.
    return staleAck(envelope.commandId, revision, "proto.staleLogEpoch");
  }
  if (envelope.baseRevision !== undefined && envelope.baseRevision !== revision) {
    return staleAck(envelope.commandId, revision, "proto.staleRevision");
  }
  return undefined;
}

/** True when the command addresses rows and therefore needs the epoch check. */
export function targetsRows(envelope: CommandEnvelope): boolean {
  return ROW_TARGETING_COMMANDS.has(envelope.type);
}
