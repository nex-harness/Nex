/**
 * Availability of the two mid-session config commands.
 *
 * Kept in `projection/` (no pi imports) so the availability computation
 * (`availability.ts`) can merge these two flags without re-deriving them; both commands are
 * implemented by the engine. The old CLI always allows both
 * (`apps/nex-cli/packages/bootstrap/src/nex-protocol-v4/projection-state.ts:135-136`);
 * nothing about phase or compaction gates them.
 */
export function modelConfigAvailability(_state: { phase?: string; compacting?: boolean }): {
  switchModelConfig: { allowed: true };
  setFollowupMode: { allowed: true };
} {
  return { switchModelConfig: { allowed: true }, setFollowupMode: { allowed: true } };
}
