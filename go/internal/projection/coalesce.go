package projection

// coalesceConversationDeltas ports coalesceConversationDeltas (coalesce.ts): the
// flush-window merge rules that must preserve apply semantics. Rule 6
// (workflowRun.updated) is a no-op here because the projection of the ported
// scenarios emits no workflowRun ops; the rule is kept as a barrier for the
// state.updated-workflowRuns case.
//
// Rules:
//  1. adjacent row.delta with the same (rowId, path) → concatenate append;
//  2. adjacent state.updated → shallow key merge;
//  3. a row.upserted drops earlier row.delta of the same rowId back to the last
//     barrier (or the previous appended/upserted of that rowId);
//  4. row.removed is a barrier no rule crosses.
func coalesceConversationDeltas(deltas []Delta) []Delta {
	result := []Delta{}
	for _, delta := range deltas {
		if opOf(delta) == "row.upserted" {
			row := rowOf(delta)
			for i := len(result) - 1; i >= 0; i-- {
				prev := result[i]
				if isBarrier(prev) {
					break
				}
				if opOf(prev) == "row.delta" && int64Of(prev.get("rowId")) == rowIDOf(row) {
					result = append(result[:i], result[i+1:]...)
					continue
				}
				if (opOf(prev) == "row.upserted" || opOf(prev) == "row.appended") &&
					rowIDOf(rowOf(prev)) == rowIDOf(row) {
					break
				}
			}
		}

		last := Delta(nil)
		if len(result) > 0 {
			last = result[len(result)-1]
		}

		if opOf(delta) == "row.delta" && last != nil && opOf(last) == "row.delta" &&
			int64Of(last.get("rowId")) == int64Of(delta.get("rowId")) &&
			strOf(last.get("path")) == strOf(delta.get("path")) {
			last.put("append", strOf(last.get("append"))+strOf(delta.get("append")))
			continue
		}

		if opOf(delta) == "state.updated" && last != nil && opOf(last) == "state.updated" {
			merged := shallowClone(patchOf(last))
			patch := patchOf(delta)
			for i, key := range patch.keys {
				merged.put(key, patch.vals[i])
			}
			last.put("patch", merged)
			continue
		}

		if opOf(delta) == "row.upserted" && last != nil && opOf(last) == "row.upserted" &&
			rowIDOf(rowOf(last)) == rowIDOf(rowOf(delta)) {
			result[len(result)-1] = delta
			continue
		}

		result = append(result, delta)
	}
	return result
}

func isBarrier(delta Delta) bool {
	return opOf(delta) == "row.removed"
}
