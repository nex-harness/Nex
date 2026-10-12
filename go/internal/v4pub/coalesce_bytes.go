package v4pub

import "encoding/json"

// This file holds the byte-level rebuilds of the deltas coalescing mutates: a
// merged row.delta's append string and a merged state.updated's patch. Both must
// reproduce JSON.stringify's output because frames carry these bytes verbatim.

// rebuildRowDelta re-renders a row.delta with a concatenated append, keeping the
// TS object-literal key order (op, rowId, path, append).
func rebuildRowDelta(d Delta) json.RawMessage {
	return Obj(
		Member("op", Str(string(OpRowDelta))),
		Member("rowId", Int(d.RowID)),
		Member("path", Str(d.Path)),
		Member("append", Str(d.Append)),
	).appendJSON(nil)
}

// mergeStatePatch pours later's patch keys over earlier's (key-level whole
// replacement), keeping earlier's insertion order for shared keys and appending
// later-only keys in order - exactly object spread in TS.
func mergeStatePatch(earlier, later Delta) Delta {
	earlierPatch := rawObjectFromStatePatch(earlier)
	laterPatch := rawObjectFromStatePatch(later)
	for _, k := range laterPatch.keys {
		earlierPatch.set(k, laterPatch.values[k])
	}
	merged := later.Clone()
	var patchRevision *int64
	if v, ok := earlierPatch.get("revision"); ok {
		var n int64
		if err := json.Unmarshal(v, &n); err == nil {
			patchRevision = &n
		}
	}
	merged.PatchRevision = patchRevision
	if v, ok := earlierPatch.get("workflowRuns"); ok && len(v) > 0 && string(v) != "null" {
		merged.HasWorkflowRuns = true
		merged.WorkflowRuns = v
	} else {
		merged.HasWorkflowRuns = false
		merged.WorkflowRuns = nil
	}
	members := make([]orderedMember, 0, len(earlierPatch.keys))
	for _, k := range earlierPatch.keys {
		members = append(members, Member(k, RawMessageOf(earlierPatch.values[k])))
	}
	merged.Raw = Obj(
		Member("op", Str(string(OpStateUpdated))),
		Member("patch", Obj(members...)),
	).appendJSON(nil)
	return merged
}

// rawObjectFromStatePatch extracts patch as an ordered object. A missing patch
// is an empty object.
func rawObjectFromStatePatch(d Delta) *orderedRawObject {
	o := newOrderedRawObject()
	raw := statePatchRaw(d.Raw)
	if len(raw) == 0 {
		return o
	}
	obj, err := decodeRawObject(raw)
	if err != nil {
		return o
	}
	for _, k := range obj.keys {
		o.set(k, obj.values[k])
	}
	return o
}

// statePatchRaw returns the "patch" member of a state.updated delta's raw JSON.
func statePatchRaw(raw json.RawMessage) json.RawMessage {
	var envelope struct {
		Patch json.RawMessage `json:"patch"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil
	}
	return envelope.Patch
}

func (o *orderedRawObject) get(key string) (json.RawMessage, bool) {
	v, ok := o.values[key]
	return v, ok
}

func (o *orderedRawObject) set(key string, value json.RawMessage) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}
