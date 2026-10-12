package projection

import "strconv"

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// appendToRow ports appendToRow (apply.ts): row.delta only lands on a streaming
// row of the matching kind; a path/row mismatch is a no-op.
func appendToRow(row *obj, path, append string) *obj {
	switch path {
	case "text":
		if kindOf(row) == "assistantText" || kindOf(row) == "reasoning" {
			copy := shallowClone(row)
			copy.put("text", strOf(row.get("text"))+append)
			return copy
		}
		return row
	case "inputText":
		if kindOf(row) == "toolCall" {
			copy := shallowClone(row)
			copy.put("inputText", strOf(row.get("inputText"))+append)
			return copy
		}
		return row
	case "output.text":
		if kindOf(row) == "toolCall" {
			if output, ok := row.get("output").(*obj); ok && output != nil {
				copy := shallowClone(row)
				nextOutput := shallowClone(output)
				nextOutput.put("text", strOf(output.get("text"))+append)
				copy.put("output", nextOutput)
				return copy
			}
		}
		return row
	case "summaryText":
		if kindOf(row) == "subagent" {
			copy := shallowClone(row)
			copy.put("summaryText", strOf(row.get("summaryText"))+append)
			return copy
		}
		return row
	}
	return row
}

// shallowClone clones an obj (keys and values copied; nested values shared,
// matching the TS spread `{...row}`).
func shallowClone(o *obj) *obj {
	clone := &obj{keys: make([]string, len(o.keys)), vals: make([]any, len(o.vals))}
	copy(clone.keys, o.keys)
	copy(clone.vals, o.vals)
	return clone
}

// applyConversationDelta ports applyConversationDelta (apply.ts). It returns a
// new snapshot; the input is not modified.
func applyConversationDelta(snapshot *obj, delta Delta) *obj {
	switch opOf(delta) {
	case "row.appended":
		row := rowOf(delta)
		rows := snapshot.get("rows").(*obj)
		window := appendWindow(rows)
		next := shallowClone(snapshot)
		nextRows := shallowClone(rows)
		nextRows.put("window", append(window, row))
		nextRows.put("totalCount", int64Of(rows.get("totalCount"))+1)
		if rows.get("firstRowId") == nil {
			nextRows.put("firstRowId", rowIDOf(row))
		}
		next.put("rows", nextRows)
		return next
	case "row.upserted":
		row := rowOf(delta)
		rows := snapshot.get("rows").(*obj)
		window := rows.get("window").([]any)
		index := -1
		for i, r := range window {
			if rr, ok := r.(*obj); ok && rowIDOf(rr) == rowIDOf(row) {
				index = i
				break
			}
		}
		if index == -1 {
			return snapshot
		}
		nextWindow := append([]any{}, window...)
		nextWindow[index] = row
		next := shallowClone(snapshot)
		nextRows := shallowClone(rows)
		nextRows.put("window", nextWindow)
		next.put("rows", nextRows)
		return next
	case "row.removed":
		from := int64Of(delta.get("fromRowId"))
		rows := snapshot.get("rows").(*obj)
		window := rows.get("window").([]any)
		kept := make([]any, 0, len(window))
		for _, r := range window {
			if rr, ok := r.(*obj); ok && rowIDOf(rr) < from {
				kept = append(kept, r)
			}
		}
		removed := len(window) - len(kept)
		firstRowID := rows.get("firstRowId")
		removesEntireActiveBranch := firstRowID != nil && from <= int64Of(firstRowID)
		totalCount := int64Of(rows.get("totalCount")) - int64(removed)
		if totalCount < 0 {
			totalCount = 0
		}
		next := shallowClone(snapshot)
		nextRows := shallowClone(rows)
		nextRows.put("window", kept)
		if removesEntireActiveBranch {
			nextRows.put("totalCount", int64(0))
			nextRows.put("firstRowId", nil)
		} else {
			nextRows.put("totalCount", totalCount)
		}
		next.put("rows", nextRows)
		return next
	case "row.delta":
		rowID := int64Of(delta.get("rowId"))
		path, _ := delta.get("path").(string)
		appendStr, _ := delta.get("append").(string)
		rows := snapshot.get("rows").(*obj)
		window := rows.get("window").([]any)
		index := -1
		for i, r := range window {
			if rr, ok := r.(*obj); ok && rowIDOf(rr) == rowID {
				index = i
				break
			}
		}
		if index == -1 {
			return snapshot
		}
		target, _ := window[index].(*obj)
		if target == nil {
			return snapshot
		}
		nextWindow := append([]any{}, window...)
		nextWindow[index] = appendToRow(target, path, appendStr)
		next := shallowClone(snapshot)
		nextRows := shallowClone(rows)
		nextRows.put("window", nextWindow)
		next.put("rows", nextRows)
		return next
	case "state.updated":
		patch := patchOf(delta)
		next := shallowClone(snapshot)
		for i, key := range patch.keys {
			next.put(key, patch.vals[i])
		}
		return next
	default:
		return snapshot
	}
}

// applyConversationDeltas ports applyConversationDeltas.
func applyConversationDeltas(snapshot *obj, deltas []Delta) *obj {
	current := snapshot
	for _, delta := range deltas {
		current = applyConversationDelta(current, delta)
	}
	return current
}

func appendWindow(rows *obj) []any {
	window, _ := rows.get("window").([]any)
	return window
}
