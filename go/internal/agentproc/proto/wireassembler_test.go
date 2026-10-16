// wireassembler_test.go runs the 25 assembly vectors recorded from the TypeScript
// TopicWireFrameAssembler (go/testdata/vectors/wire.json, assemblyCases). Every
// event sequence and every final stat must match byte-for-byte.
//
// The vectors are the oracle, not this package's own expectations: they were
// produced by running the real TS assembler (scripts/go-rewrite/gen-wire-vectors.mts)
// over the same wire JSON. A drift in any rule — ordinals, supersede, budgets,
// checksum, UTF-8, JSON, envelope match, payload schema, timeout — changes an
// event or a stat and fails here.
package proto

import (
	"encoding/json"
	"os"
	"testing"
)

// vectorFrameSchema is the frame schema the vector generator used: a minimal
// passthrough object requiring string topic/subscriptionId and a numeric seq. It
// is intentionally weak so the "envelope matches but schema fails" path (the
// InvalidPayload fault) is reachable.
func vectorFrameSchema(frame []byte) ([]byte, bool) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(frame, &value); err != nil {
		return nil, false
	}
	var topic, subscriptionID string
	if err := json.Unmarshal(value["topic"], &topic); err != nil || topic == "" {
		return nil, false
	}
	if err := json.Unmarshal(value["subscriptionId"], &subscriptionID); err != nil || subscriptionID == "" {
		return nil, false
	}
	var seq float64
	if err := json.Unmarshal(value["seq"], &seq); err != nil {
		return nil, false
	}
	return frame, true
}

type assemblyVector struct {
	Name    string             `json:"name"`
	Options map[string]float64 `json:"options"`
	Times   []float64          `json:"times"`
	Wires   []string           `json:"wires"`
	Events  [][]vectorEvent    `json:"events"`
	Stats   struct {
		Assemblies         int `json:"assemblies"`
		StagedDecodedBytes int `json:"stagedDecodedBytes"`
	} `json:"stats"`
}

type vectorEvent struct {
	Kind         string `json:"kind"`
	DeliveryKind string `json:"deliveryKind"`
	FrameJSON    string `json:"frameJson"`
	Reason       string `json:"reason"`
	Ordinal      int64  `json:"ordinal"`
	ID           string `json:"id"`
}

type wireVectors struct {
	AssemblyCases []assemblyVector `json:"assemblyCases"`
}

func loadAssemblyVectors(t testing.TB) wireVectors {
	t.Helper()
	raw, err := os.ReadFile("../../../testdata/vectors/wire.json")
	if err != nil {
		t.Fatal(err)
	}
	var v wireVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.AssemblyCases) != 25 {
		t.Fatalf("expected 25 assembly vectors, got %d", len(v.AssemblyCases))
	}
	return v
}

func optionsFromVector(o map[string]float64) AssemblerOptions {
	return AssemblerOptions{
		MaxAssemblyBytes:        int(o["maxAssemblyBytes"]),
		MaxFragments:            int(o["maxFragments"]),
		MaxConcurrentAssemblies: int(o["maxConcurrentAssemblies"]),
		MaxStagedDecodedBytes:   int(o["maxStagedDecodedBytes"]),
	}
}

func TestAssemblerMatchesTypeScriptVectors(t *testing.T) {
	for _, tc := range loadAssemblyVectors(t).AssemblyCases {
		t.Run(tc.Name, func(t *testing.T) {
			asm := NewAssembler(vectorFrameSchema, optionsFromVector(tc.Options))
			for i, wireJSON := range tc.Wires {
				wire, err := ParseCandidate([]byte(wireJSON))
				if err != nil {
					t.Fatalf("wire %d: parse: %v", i, err)
				}
				now := int64(1000)
				if tc.Times != nil {
					now = int64(tc.Times[i])
				}
				got := asm.Accept(wire, now)
				assertEvents(t, tc.Name, i, got, tc.Events[i])
			}
			stats := asm.Stats()
			if stats.Assemblies != tc.Stats.Assemblies || stats.StagedDecodedBytes != tc.Stats.StagedDecodedBytes {
				t.Errorf("final stats = %+v, ts recorded %+v", stats, tc.Stats)
			}
		})
	}
}

func assertEvents(t *testing.T, name string, index int, got []Event, want []vectorEvent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s[%d]: got %d events %s, ts recorded %d %s",
			name, index, len(got), describe(got), len(want), describeVector(want))
	}
	for i := range got {
		w := want[i]
		switch w.Kind {
		case "complete":
			if got[i].IsFault {
				t.Fatalf("%s[%d] event %d: got fault %s, ts recorded complete", name, index, i, got[i].Fault.ReasonCode)
			}
			if string(got[i].Frame) != w.FrameJSON {
				t.Errorf("%s[%d] event %d: frame\n go: %s\n ts: %s", name, index, i, got[i].Frame, w.FrameJSON)
			}
			if string(got[i].DeliveryKind) != w.DeliveryKind {
				t.Errorf("%s[%d] event %d: deliveryKind %q, ts %q", name, index, i, got[i].DeliveryKind, w.DeliveryKind)
			}
		case "fault":
			if !got[i].IsFault {
				t.Fatalf("%s[%d] event %d: got complete, ts recorded fault %s", name, index, i, w.Reason)
			}
			f := got[i].Fault
			if f.ReasonCode != w.Reason || f.LogicalFrameID != w.ID || f.LogicalFrameOrdinal != w.Ordinal {
				t.Errorf("%s[%d] event %d: fault %+v, ts recorded reason=%s id=%s ordinal=%d",
					name, index, i, f, w.Reason, w.ID, w.Ordinal)
			}
			// The vector records deliveryKind: null when the TS reported the
			// field as absent (invalid/garbled deliveryKind).
			switch w.DeliveryKind {
			case "":
				if f.HasDeliveryKind {
					t.Errorf("%s[%d] event %d: reported deliveryKind %q where ts reported none", name, index, i, f.DeliveryKind)
				}
			default:
				if !f.HasDeliveryKind || string(f.DeliveryKind) != w.DeliveryKind {
					t.Errorf("%s[%d] event %d: deliveryKind %q (present=%v), ts %q",
						name, index, i, f.DeliveryKind, f.HasDeliveryKind, w.DeliveryKind)
				}
			}
		default:
			t.Fatalf("%s[%d]: unknown vector event kind %q", name, index, w.Kind)
		}
	}
}

func describe(events []Event) string {
	out := ""
	for _, e := range events {
		if e.IsFault {
			out += "[fault " + e.Fault.ReasonCode + "]"
		} else {
			out += "[complete " + string(e.DeliveryKind) + " " + string(e.Frame) + "]"
		}
	}
	return out
}

func describeVector(events []vectorEvent) string {
	out := ""
	for _, e := range events {
		if e.Kind == "fault" {
			out += "[fault " + e.Reason + "]"
		} else {
			out += "[complete " + e.DeliveryKind + " " + e.FrameJSON + "]"
		}
	}
	return out
}
