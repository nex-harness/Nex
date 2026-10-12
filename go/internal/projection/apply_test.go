package projection

import (
	"encoding/json"
	"testing"
)

// These tests exercise the apply.ts port against the real TS semantics captured
// in the vectors, and check the coalesce invariants (semantics preserving).

// TestApplyMatchesOracleOnVectors replays every scenario's deltas through the
// immutable apply and asserts the final snapshot equals the TS final snapshot.
// This makes apply.ts — verified by the TS round-trip — the oracle for the Go
// snapshot reducer independently of the projection.
func TestApplyMatchesOracleOnVectors(t *testing.T) {
	vf := loadVectors(t)
	for _, scenario := range vf.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			// Decode the TS snapshot literal into our ordered representation so the
			// comparison is structural, then apply the deltas.
			snapshot := mustObj(t, scenario.SnapshotBeforeLive)
			var all []Delta
			for _, ec := range scenario.Events {
				for _, raw := range ec.Deltas {
					all = append(all, mustObj(t, raw))
				}
			}
			got := applyConversationDeltas(snapshot, all)
			// seq is not part of apply.ts: the projection records the event's
			// sequence number separately, so adopt it from the oracle snapshot.
			final := mustObj(t, scenario.FinalSnapshot)
			got.put("seq", final.get("seq"))
			gotJSON, err := got.MarshalJSON()
			if err != nil {
				t.Fatalf("marshal snapshot: %v", err)
			}
			assertJSONEqual(t, "apply("+scenario.ID+")", gotJSON, scenario.FinalSnapshot)
		})
	}
}

// TestCoalescePreservesApplySemantics checks apply(o, coalesce(ds)) ==
// apply(o, ds) per scenario (coalesce.ts's stated invariant), using the real TS
// delta streams.
func TestCoalescePreservesApplySemantics(t *testing.T) {
	vf := loadVectors(t)
	for _, scenario := range vf.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			snapshot := mustObj(t, scenario.SnapshotBeforeLive)
			var all []Delta
			for _, ec := range scenario.Events {
				for _, raw := range ec.Deltas {
					all = append(all, mustObj(t, raw))
				}
			}
			direct := applyConversationDeltas(shallowClone(snapshot), all)
			coalesced := coalesceConversationDeltas(all)
			viaCoalesce := applyConversationDeltas(shallowClone(snapshot), coalesced)
			directJSON, _ := direct.MarshalJSON()
			coalescedJSON, _ := viaCoalesce.MarshalJSON()
			if string(directJSON) != string(coalescedJSON) {
				t.Fatalf("coalesce changed apply result for %s\n direct=%s\ncoalesce=%s",
					scenario.ID, directJSON, coalescedJSON)
			}
		})
	}
}

// TestCoalesceMatchesTypeScript compares the Go coalesce output to the TS
// coalesced stream in the vectors (order and bytes).
func TestCoalesceMatchesTypeScript(t *testing.T) {
	vf := loadVectors(t)
	for _, scenario := range vf.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			var all []Delta
			for _, ec := range scenario.Events {
				for _, raw := range ec.Deltas {
					all = append(all, mustObj(t, raw))
				}
			}
			coalesced := coalesceConversationDeltas(all)
			got := make([]json.RawMessage, 0, len(coalesced))
			for _, d := range coalesced {
				data, _ := d.MarshalJSON()
				got = append(got, data)
			}
			assertJSONList(t, "coalesced", scenario.ID, 0, got, scenario.Coalesced)
		})
	}
}

// mustObj decodes JSON into an ordered object. Value kinds are preserved: numbers
// become json.Number so the ordered marshal re-emits them verbatim.
func mustObj(t *testing.T, raw json.RawMessage) *obj {
	t.Helper()
	value, err := decodeOrdered(raw)
	if err != nil {
		t.Fatalf("decode ordered: %v", err)
	}
	return value
}

// decodeOrdered parses JSON into obj/[]any preserving key order and number text.
func decodeOrdered(raw json.RawMessage) (*obj, error) {
	dec := json.NewDecoder(bytesReader(raw))
	dec.UseNumber()
	value, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	result, _ := value.(*obj)
	return result, nil
}
