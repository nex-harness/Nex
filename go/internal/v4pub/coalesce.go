package v4pub

// Coalescing of one flush window's delta sequence. This ports coalesce.ts; the
// semantics requirement is that coalesce(deltas) is equivalent to delivering
// them one by one: two delivery profiles that process the same event sequence
// must end byte-identical, so no merge may change the state apply() produces.
//
// Rules (closed set), copied from the source:
//  1. adjacent row.delta with the same (rowId, path) -> append the strings;
//  2. adjacent state.updated -> shallow-merge the patches (keys replaced whole);
//  3. a row.delta followed by a row.upserted for the same rowId -> drop the
//     former (the whole-row replacement carries every append);
//  4. row.removed is a barrier; no rule may cross it;
//  5. frame splitting is not done here (the channel layer frames);
//  6. workflowRun.updated for the same runId merges into the *latest* one (see
//     mergeWorkflowRunUpdate).

// CoalesceConversationDeltas merges a flush window's deltas while preserving
// semantics. Input and output are in authoritative log order; the function does
// not modify its argument.
func CoalesceConversationDeltas(deltas []Delta, bounds WorkflowRunWireBounds) []Delta {
	result := make([]Delta, 0, len(deltas))
	for _, delta := range deltas {
		// Rule 3: row.upserted swallows earlier row.delta for the same rowId.
		// Backtrack only to the nearest barrier (rule 4), and stop at the same
		// rowId's earlier upserted/appended - crossing it would swallow the
		// "previous generation" appends and change the terminal state.
		if delta.Op == OpRowUpserted {
			for i := len(result) - 1; i >= 0; i-- {
				prev := result[i]
				if isBarrier(prev) {
					break
				}
				if prev.Op == OpRowDelta && prev.RowID == delta.RowID {
					result = append(result[:i], result[i+1:]...)
					continue
				}
				if (prev.Op == OpRowUpserted || prev.Op == OpRowAppended) && prev.RowID == delta.RowID {
					break
				}
			}
		}

		// Rule 6 (eviction): a workflowRun.removed swallows all earlier
		// increments for that run (birth + eviction in one window = the client
		// never saw the run, equivalent to one-by-one delivery).
		if delta.Op == OpWorkflowRunRemoved {
			for i := len(result) - 1; i >= 0; i-- {
				prev := result[i]
				if isWorkflowRunBarrier(prev, delta.RunID) {
					break
				}
				if prev.Op == OpWorkflowRunUpdated && prev.RunID == delta.RunID {
					result = append(result[:i], result[i+1:]...)
				}
			}
		}

		if delta.Op == OpWorkflowRunUpdated {
			if merged, ok := mergeWorkflowRunUpdate(result, delta, bounds); ok {
				result = merged
				continue
			}
		}

		var last *Delta
		if len(result) > 0 {
			last = &result[len(result)-1]
		}

		// Rule 1: adjacent row.delta with the same (rowId, path) concatenate.
		if delta.Op == OpRowDelta && last != nil &&
			last.Op == OpRowDelta && last.RowID == delta.RowID && last.Path == delta.Path {
			merged := *last
			merged.Append = last.Append + delta.Append
			merged.Raw = rebuildRowDelta(merged)
			result[len(result)-1] = merged
			continue
		}

		// Rule 2: adjacent state.updated shallow-merge (later keys win).
		if delta.Op == OpStateUpdated && last != nil && last.Op == OpStateUpdated {
			merged := mergeStatePatch(*last, delta)
			result[len(result)-1] = merged
			continue
		}

		// Adjacent row.upserted for the same rowId: keep the last one.
		if delta.Op == OpRowUpserted && last != nil &&
			last.Op == OpRowUpserted && last.RowID == delta.RowID {
			result[len(result)-1] = delta
			continue
		}

		result = append(result, delta)
	}
	return result
}

func isBarrier(delta Delta) bool { return delta.Op == OpRowRemoved }

// isWorkflowRunBarrier is the rule-6 barrier: a state.updated carrying the
// workflowRuns key, and a workflowRun.removed for the same run.
func isWorkflowRunBarrier(delta Delta, runID string) bool {
	if delta.Op == OpStateUpdated {
		return delta.HasWorkflowRuns
	}
	return delta.Op == OpWorkflowRunRemoved && delta.RunID == runID
}

func isWorkflowRunDelta(delta Delta) bool {
	return delta.Op == OpWorkflowRunUpdated || delta.Op == OpWorkflowRunRemoved
}

// mergeWorkflowRunUpdate merges a workflowRun.updated into the latest same-run
// increment in the window. It returns (newResult, true) when merged; the caller
// then does not push.
func mergeWorkflowRunUpdate(result []Delta, delta Delta, bounds WorkflowRunWireBounds) ([]Delta, bool) {
	target := -1
	for i := len(result) - 1; i >= 0; i-- {
		prev := result[i]
		if isWorkflowRunBarrier(prev, delta.RunID) {
			break
		}
		if prev.Op == OpWorkflowRunUpdated && prev.RunID == delta.RunID {
			target = i
			break
		}
	}
	if target < 0 {
		return result, false
	}
	merged, err := mergeWorkflowRunUpdates(result[target], delta)
	if err != nil {
		return result, false
	}
	// A rejected merge does not try an earlier entry: that would cross the
	// intermediate removal described in the source.
	if !workflowRunUpdateWithinWireBounds(merged, bounds) {
		return result, false
	}
	out := append([]Delta{}, result...)
	out[target] = merged
	return out, true
}
