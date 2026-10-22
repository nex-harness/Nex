import type { RowActions } from "@nex/shared/nex-protocol-v4";
import type { ProjectedRow, ProjectionState } from "./state.js";

/**
 * Row action affordances, derived from the projected row set on every snapshot.
 *
 * Port of `materializeCommandRowActions`
 * (`apps/nex-cli/packages/bootstrap/src/nex-protocol-v4/product-projection.ts:1182-1300`),
 * reduced to this runner's row kinds. The old CLI also revokes `canRewindFiles` and tracks
 * message-id/edit-target tables; both belong to batches that add file checkpoints and the
 * rewind command, so they are absent here.
 *
 * Rules (decision 1):
 * - `canEdit` + `editDisposition:"rewind"` — newest `realUser` row, and only while no
 *   compact work is active. Every userInput row this runner produces is `realUser`.
 * - `canRetry` — the timeline's newest assistantText row, when it is `complete`,
 *   its turnHeader is not running, and the same turn has a realUser cause.
 * - `canFork` — `complete` assistantText row that is the last text segment of its turn,
 *   whose turnHeader ended `completedSuccess`, and while no compact work is active.
 *
 * The old CLI additionally required a resolvable message id; this runner's entityId *is*
 * the pi entry id, so `entityId !== undefined` is that check (an unresolved id could not be
 * sent back as a command target).
 */
export function deriveRowActions(state: ProjectionState): Map<number, RowActions> {
  const actions = new Map<number, RowActions>();
  const rows = state.rows;
  const headerByTurn = new Map<string, ProjectedRow>();
  const latestAssistantByTurn = new Map<string, ProjectedRow>();
  for (const row of rows) {
    if (row.kind === "turnHeader") headerByTurn.set(row.turnId, row);
    if (row.kind === "assistantText") {
      const current = latestAssistantByTurn.get(row.turnId);
      if (!current || row.rowId > current.rowId) latestAssistantByTurn.set(row.turnId, row);
    }
  }

  // `activeWorks.kind === "compact"` (batch 2d): a rewind would abort the compaction, and
  // a fork's `createBranchedSession` would copy a half-written boundary. Both entries are
  // withheld until it ends.
  const compactActive = Object.keys(state.compactOperations).length > 0;
  const completionBlockingActive = compactActive;

  // Newest editable user row, walking backwards (oldest wins only if newer ones are absent).
  let latestEditable: ProjectedRow | undefined;
  let latestAssistant: ProjectedRow | undefined;
  for (let index = rows.length - 1; index >= 0; index -= 1) {
    const row = rows[index];
    if (!row) continue;
    if (!latestEditable && !compactActive && row.kind === "userInput") latestEditable = row;
    if (!latestAssistant && row.kind === "assistantText") latestAssistant = row;
    if (latestEditable && latestAssistant) break;
  }

  // Retry targets the timeline's newest assistant only; the same turn must carry the
  // canonical real-user cause (a synthetic/background turn never exposes retry).
  const retryable = (() => {
    if (completionBlockingActive) return undefined;
    if (!latestAssistant || latestAssistant.kind !== "assistantText") return undefined;
    if (latestAssistant.state !== "complete" || !latestAssistant.entityId) return undefined;
    const header = headerByTurn.get(latestAssistant.turnId);
    if (!header || header.kind !== "turnHeader" || header.state === "running") return undefined;
    const hasUserCause = rows.some(
      (row) => row.turnId === latestAssistant?.turnId && row.kind === "userInput",
    );
    return hasUserCause ? latestAssistant : undefined;
  })();

  for (const row of rows) {
    const next: RowActions = {};
    if (row.kind === "userInput") {
      if (row === latestEditable && row.entityId) {
        next.canEdit = true;
        next.editDisposition = "rewind";
      }
      if (next.canEdit) actions.set(row.rowId, next);
      continue;
    }
    if (row.kind !== "assistantText") continue;
    if (row === retryable) next.canRetry = true;
    const header = headerByTurn.get(row.turnId);
    const canFork =
      !compactActive &&
      row.state === "complete" &&
      header?.kind === "turnHeader" &&
      header.state === "completedSuccess" &&
      latestAssistantByTurn.get(row.turnId)?.rowId === row.rowId &&
      row.entityId !== undefined;
    if (canFork) next.canFork = true;
    if (next.canRetry || next.canFork) actions.set(row.rowId, next);
  }
  return actions;
}
