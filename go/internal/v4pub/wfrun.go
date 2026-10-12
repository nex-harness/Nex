package v4pub

import (
	"encoding/json"
)

// This file ports the subset of workflow-runs-delta.ts that coalesce rule 6
// needs: the entry key, the wire bounds, and mergeWorkflowRunUpdates. The rest of
// that module (diff, apply, canonical ordering) belongs to the reducer, which is
// out of this package's scope.

// WorkflowRunWireBounds are the per-table capacity limits used by the merge
// bounds check. They mirror WORKFLOW_RUNS_LIMITS.maxActors/maxNodes.
type WorkflowRunWireBounds struct {
	MaxActors int
	MaxNodes  int
}

// DefaultWorkflowRunWireBounds returns the protocol defaults (1024 / 1024).
func DefaultWorkflowRunWireBounds() WorkflowRunWireBounds {
	return WorkflowRunWireBounds{MaxActors: 1024, MaxNodes: 1024}
}

// workflowRunUpdateWithinWireBounds is the port of the function of the same name:
// it rejects a merge whose payload would exceed the wire bounds, because an
// over-limit patch fails to parse downstream and the whole frame is dropped.
// Merging is an optimisation, so refusing is always safe.
func workflowRunUpdateWithinWireBounds(d Delta, limits WorkflowRunWireBounds) bool {
	return len(d.Actors) <= limits.MaxActors &&
		len(d.RemovedActors) <= limits.MaxActors &&
		len(d.Nodes) <= limits.MaxNodes &&
		len(d.RemovedNodes) <= limits.MaxNodes
}

// mergeWorkflowRunUpdates ports mergeWorkflowRunUpdates from workflow-runs-delta.ts.
// It produces a delta whose Raw bytes are JSON.stringify-compatible with the TS
// result: keys are emitted in the schema-declared order the TS object literal
// uses (op, runId, revision, run?, cleared?, removedActors?, removedNodes?,
// actors?, nodes?).
func mergeWorkflowRunUpdates(earlier, later Delta) (Delta, error) {
	run, err := mergeHeaderRun(earlier.Run, later.Run, later.Cleared)
	if err != nil {
		return Delta{}, err
	}
	cleared := mergeCleared(earlier.Cleared, later.Cleared, run)
	removedActors := mergeRemovedRefs(earlier.RemovedActors, later.RemovedActors)
	removedNodes := mergeRemovedRefs(earlier.RemovedNodes, later.RemovedNodes)
	actors := mergeEntryLists(earlier.Actors, later.Actors, later.RemovedActors)
	nodes := mergeEntryLists(earlier.Nodes, later.Nodes, later.RemovedNodes)

	revision := max(earlier.Revision, later.Revision)
	members := []orderedMember{
		Member("op", Str(string(OpWorkflowRunUpdated))),
		Member("runId", Str(later.RunID)),
		Member("revision", Int(revision)),
	}
	if len(run) > 0 {
		members = append(members, Member("run", RawMessageOf(run)))
	}
	if len(cleared) > 0 {
		items := make([]JSONValue, len(cleared))
		for i, k := range cleared {
			items[i] = Str(k)
		}
		members = append(members, Member("cleared", Arr(items...)))
	}
	if removedActors != nil {
		members = append(members, Member("removedActors", entryRefArray(removedActors)))
	}
	if removedNodes != nil {
		members = append(members, Member("removedNodes", entryRefArray(removedNodes)))
	}
	if actors != nil {
		members = append(members, Member("actors", RawArray(actors)))
	}
	if nodes != nil {
		members = append(members, Member("nodes", RawArray(nodes)))
	}
	raw := Obj(members...).appendJSON(nil)
	merged := later.Clone()
	merged.RunID = later.RunID
	merged.Revision = revision
	merged.Run = run
	merged.Cleared = cleared
	merged.RemovedActors = removedActors
	merged.RemovedNodes = removedNodes
	merged.Actors = actors
	merged.Nodes = nodes
	merged.Raw = raw
	return merged, nil
}

// entryRefArray renders refs as [{siteId, ordinal}, ...] verbatim.
func entryRefArray(refs []RunEntryRef) JSONValue {
	items := make([]JSONValue, len(refs))
	for i, r := range refs {
		items[i] = Obj(Member("siteId", Str(r.SiteID)), Member("ordinal", Int(r.Ordinal)))
	}
	return Arr(items...)
}

// RawArray renders a list of raw JSON values as a JSON array.
func RawArray(raws []json.RawMessage) JSONValue {
	items := make([]JSONValue, len(raws))
	for i, r := range raws {
		items[i] = RawMessageOf(r)
	}
	return Arr(items...)
}

// mergeHeaderRun spreads earlier.run then later.run, then deletes every key in
// later.cleared, preserving insertion order as the TS object spread does.
func mergeHeaderRun(earlier, later json.RawMessage, laterCleared []string) (json.RawMessage, error) {
	run := newOrderedRawObject()
	if err := run.merge(earlier); err != nil {
		return nil, err
	}
	if err := run.merge(later); err != nil {
		return nil, err
	}
	run.delete(laterCleared)
	if run.len() == 0 {
		return nil, nil
	}
	return run.marshal()
}

func mergeCleared(earlierCleared, laterCleared []string, run json.RawMessage) []string {
	var runKeys map[string]json.RawMessage
	_ = json.Unmarshal(run, &runKeys)
	if runKeys == nil {
		runKeys = map[string]json.RawMessage{}
	}
	out := []string{}
	seen := map[string]bool{}
	for _, key := range earlierCleared {
		if _, present := runKeys[key]; present {
			continue
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	for _, key := range laterCleared {
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

// mergeRemovedRefs unions two removal lists, de-duplicating by entry key while
// preserving first-seen order; both empty yields nil (no key).
func mergeRemovedRefs(earlier, later []RunEntryRef) []RunEntryRef {
	if earlier == nil && later == nil {
		return nil
	}
	out := []RunEntryRef{}
	seen := map[string]bool{}
	for _, ref := range append(append([]RunEntryRef{}, earlier...), later...) {
		if seen[ref.Key()] {
			continue
		}
		seen[ref.Key()] = true
		out = append(out, ref)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mergeEntryLists upserts later's entries over earlier's, dropping any earlier
// entry that later removed. Both empty yields nil.
func mergeEntryLists(earlier, later []json.RawMessage, laterRemoved []RunEntryRef) []json.RawMessage {
	if earlier == nil && later == nil {
		return nil
	}
	dropped := map[string]bool{}
	for _, ref := range laterRemoved {
		dropped[ref.Key()] = true
	}
	kept := earlier
	if len(dropped) > 0 {
		kept = nil
		for _, e := range earlier {
			if ref, ok := entryKeyOf(e); ok && dropped[ref] {
				continue
			}
			kept = append(kept, e)
		}
	}
	merged := upsertEntries(kept, later)
	if len(merged) == 0 {
		return nil
	}
	return merged
}

// upsertEntries ports upsertWorkflowRunEntries: later entries replace earlier
// entries with the same key, new keys land in first-seen order.
func upsertEntries(earlier, later []json.RawMessage) []json.RawMessage {
	out := append([]json.RawMessage{}, earlier...)
	index := map[string]int{}
	for i, e := range out {
		if key, ok := entryKeyOf(e); ok {
			index[key] = i
		}
	}
	for _, e := range later {
		key, ok := entryKeyOf(e)
		if !ok {
			out = append(out, e)
			continue
		}
		if i, exists := index[key]; exists {
			out[i] = e
			continue
		}
		index[key] = len(out)
		out = append(out, e)
	}
	return out
}

func entryKeyOf(raw json.RawMessage) (string, bool) {
	var e struct {
		SiteID  string `json:"siteId"`
		Ordinal int64  `json:"ordinal"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return "", false
	}
	return RunEntryRef{SiteID: e.SiteID, Ordinal: e.Ordinal}.Key(), true
}

// orderedRawObject is a JSON object whose members keep insertion order, used to
// reproduce the TS object-spread merge.
type orderedRawObject struct {
	keys   []string
	values map[string]json.RawMessage
}

func newOrderedRawObject() *orderedRawObject {
	return &orderedRawObject{values: map[string]json.RawMessage{}}
}

func (o *orderedRawObject) merge(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	obj, err := decodeRawObject(raw)
	if err != nil {
		return err
	}
	for _, k := range obj.keys {
		if _, ok := o.values[k]; !ok {
			o.keys = append(o.keys, k)
		}
		o.values[k] = obj.values[k]
	}
	return nil
}

func (o *orderedRawObject) delete(keys []string) {
	for _, k := range keys {
		if _, ok := o.values[k]; !ok {
			continue
		}
		delete(o.values, k)
		for i, existing := range o.keys {
			if existing == k {
				o.keys = append(o.keys[:i], o.keys[i+1:]...)
				break
			}
		}
	}
}

func (o *orderedRawObject) len() int { return len(o.keys) }

func (o *orderedRawObject) marshal() (json.RawMessage, error) {
	members := make([]orderedMember, 0, len(o.keys))
	for _, k := range o.keys {
		members = append(members, Member(k, RawMessageOf(o.values[k])))
	}
	return Obj(members...).appendJSON(nil), nil
}
