package v4pub

import "encoding/json"

// Legacy-consumer encoding of a delta batch: the port of
// encodeConversationDeltasForLegacy. Earlier deltas that only carry key-level
// `workflowRun.*` are dropped and replaced, where the last such delta sat, by a
// single `state.updated { workflowRuns: <current> }` clamped to the legacy
// bounds. A batch with no workflowRun delta is returned unchanged.
func encodeDeltasForLegacy(deltas []Delta, workflowRuns json.RawMessage) []Delta {
	lastIndex := -1
	for i := len(deltas) - 1; i >= 0; i-- {
		if isWorkflowRunDelta(deltas[i]) {
			lastIndex = i
			break
		}
	}
	if lastIndex < 0 {
		return deltas
	}
	var replacement *Delta
	if workflowRuns != nil {
		clamped := clampWorkflowRunsForLegacy(workflowRuns)
		raw := Obj(
			Member("op", Str(string(OpStateUpdated))),
			Member("patch", Obj(Member("workflowRuns", RawMessageOf(clamped)))),
		).appendJSON(nil)
		d := Delta{Op: OpStateUpdated, HasWorkflowRuns: true, WorkflowRuns: clamped, Raw: raw}
		// A merged state.updated carrying workflowRuns must survive coalesce rule
		// 2 (adjacent state.updated shallow merges) with its workflowRuns intact.
		replacement = &d
	}
	out := make([]Delta, 0, len(deltas))
	for i, delta := range deltas {
		if isWorkflowRunDelta(delta) {
			if i == lastIndex && replacement != nil {
				out = append(out, *replacement)
			}
			continue
		}
		out = append(out, delta)
	}
	return out
}

// clampWorkflowRunsForLegacy is the port of clampWorkflowRunsForLegacy: keep at
// most the legacy per-table limits, preferring entries that are still live, and
// mark a clipped run `truncated: true`.
func clampWorkflowRunsForLegacy(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	obj, err := decodeRawObject(raw)
	if err != nil {
		return raw
	}
	runsRaw, ok := obj.values["runs"]
	if !ok || len(runsRaw) == 0 {
		return raw
	}
	var runs []json.RawMessage
	if err := json.Unmarshal(runsRaw, &runs); err != nil {
		return raw
	}
	clamped := false
	outRuns := make([]json.RawMessage, 0, len(runs))
	for _, runRaw := range runs {
		newRun, changed := clampRunForLegacy(runRaw)
		if changed {
			clamped = true
		}
		outRuns = append(outRuns, newRun)
	}
	if !clamped {
		return raw
	}
	items := make([]JSONValue, len(outRuns))
	for i, r := range outRuns {
		items[i] = RawMessageOf(r)
	}
	// Preserve the state key order (revision, runs) as the schema declares.
	members := []orderedMember{}
	for _, k := range obj.keys {
		switch k {
		case "revision":
			members = append(members, Member("revision", RawMessageOf(obj.values["revision"])))
		case "runs":
			members = append(members, Member("runs", Arr(items...)))
		default:
			members = append(members, Member(k, RawMessageOf(obj.values[k])))
		}
	}
	return Obj(members...).appendJSON(nil)
}

func clampRunForLegacy(runRaw json.RawMessage) (json.RawMessage, bool) {
	run, err := decodeRawObject(runRaw)
	if err != nil {
		return runRaw, false
	}
	actors, actorsChanged := clampKeepingLive(run, "actors", WorkflowRunsLegacyMaxActors, "status", "completed")
	nodes, nodesChanged := clampKeepingLive(run, "nodes", WorkflowRunsLegacyMaxNodes, "phase", "settled")
	if !actorsChanged && !nodesChanged {
		return runRaw, false
	}
	members := make([]orderedMember, 0, len(run.keys))
	for _, k := range run.keys {
		switch k {
		case "actors":
			members = append(members, Member("actors", RawArray(actors)))
		case "nodes":
			members = append(members, Member("nodes", RawArray(nodes)))
		case "truncated":
			members = append(members, Member("truncated", Bool(true)))
		default:
			members = append(members, Member(k, RawMessageOf(run.values[k])))
		}
	}
	if _, ok := run.values["truncated"]; !ok {
		members = append(members, Member("truncated", Bool(true)))
	}
	return Obj(members...).appendJSON(nil), true
}

// clampKeepingLive keeps at most limit entries: live entries first (in table
// order), then the earliest entries to fill the quota, output in original table
// order.
func clampKeepingLive(obj *orderedRawObject, key string, limit int, liveKey, liveValue string) ([]json.RawMessage, bool) {
	raw, ok := obj.values[key]
	if !ok || len(raw) == 0 {
		return nil, false
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, false
	}
	if len(list) <= limit {
		return list, false
	}
	kept := map[int]bool{}
	count := 0
	for i, entry := range list {
		if count >= limit {
			break
		}
		if entryHasField(entry, liveKey, liveValue) {
			kept[i] = true
			count++
		}
	}
	for i := 0; i < len(list) && count < limit; i++ {
		if !kept[i] {
			kept[i] = true
			count++
		}
	}
	out := make([]json.RawMessage, 0, count)
	for i, entry := range list {
		if kept[i] {
			out = append(out, entry)
		}
	}
	return out, true
}

func entryHasField(raw json.RawMessage, key, value string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	v, ok := m[key]
	if !ok {
		return false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return false
	}
	return s != value
}
