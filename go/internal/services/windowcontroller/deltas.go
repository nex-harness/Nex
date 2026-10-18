package windowcontroller

// deltas.go computes the delta batches. It is separate from projection_state.go
// because the ORDER of the deltas is the contract:
//
//   - a replaced source's rows are removed FIRST, then the current source's
//     previous rows that are gone are removed, then the new rows are upserted.
//     Doing it in this order means a consumer applying deltas in sequence never
//     holds a duplicate (same task key present twice) even for an instant, and
//     never drops a row that the new snapshot re-adds under the same key.
//   - computeUpsertOnlyDeltas (the overlay path) emits ONLY upserts for rows that
//     actually changed, because an overlay update never removes a row.

import "sort"

// computeSnapshotTaskDeltas computes the tasks-index delta batch for one snapshot
// replacement. replaced may be nil (a first snapshot, not a reconnect).
func computeSnapshotTaskDeltas(
	replaced *sourceState,
	previousRows map[string]TaskRow,
	nextRows map[string]TaskRow,
) []TaskDelta {
	deltas := make([]TaskDelta, 0, len(nextRows)+len(previousRows))

	// 1. every row of a replaced source is removed.
	if replaced != nil {
		for _, key := range sortedRowKeys(replaced.rows) {
			addr := replaced.rows[key].Address
			deltas = append(deltas, TaskDelta{Op: OpTaskRemoved, Address: &addr})
		}
	}
	// 2. rows that were in the current source and are not in the new snapshot are
	//    removed (they were deleted or archived out of the index).
	for _, key := range sortedRowKeys(previousRows) {
		if _, ok := nextRows[key]; !ok {
			addr := previousRows[key].Address
			deltas = append(deltas, TaskDelta{Op: OpTaskRemoved, Address: &addr})
		}
	}
	// 3. every new row, or a changed previous row, is upserted.
	for _, key := range sortedRowKeys(nextRows) {
		prev, hadPrev := previousRows[key]
		if hadPrev && taskRowEqual(prev, nextRows[key]) {
			continue
		}
		row := nextRows[key]
		deltas = append(deltas, TaskDelta{Op: OpTaskUpserted, Task: &row})
	}
	return deltas
}

// computeUpsertOnlyDeltas computes the batch for an overlay-only update: upserts
// for rows that changed, no removals.
func computeUpsertOnlyDeltas(previousRows map[string]TaskRow, nextRows map[string]TaskRow) []TaskDelta {
	deltas := make([]TaskDelta, 0, len(nextRows))
	for _, key := range sortedRowKeys(nextRows) {
		prev, hadPrev := previousRows[key]
		if hadPrev && taskRowEqual(prev, nextRows[key]) {
			continue
		}
		row := nextRows[key]
		deltas = append(deltas, TaskDelta{Op: OpTaskUpserted, Task: &row})
	}
	return deltas
}

// sortedRowKeys returns a source's row keys in a deterministic order so the delta
// sequence is reproducible (map iteration order must never leak into the wire).
func sortedRowKeys(rows map[string]TaskRow) []string {
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
