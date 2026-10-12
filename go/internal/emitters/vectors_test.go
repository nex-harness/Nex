package emitters

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tylinux/nex/go/internal/projection"
)

// compactJSON removes insignificant whitespace so a pretty-printed vector can be
// compared byte-for-byte with the emitter output (which is already compact).
func compactJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("compact: %v", err)
	}
	return buf.Bytes()
}

// VectorFile mirrors go/testdata/vectors/emitters.json, produced by
// scripts/go-rewrite/gen-emitter-vectors.mts from the REAL TypeScript
// ConversationTelemetryFactNormalizer and mapComputerUseOperationEvent over the
// recorded scenarios' ordered SessionEvent streams.
type VectorFile struct {
	GeneratedBy string           `json:"generatedBy"`
	Session     string           `json:"session"`
	Scenarios   []VectorScenario `json:"scenarios"`
}

type VectorScenario struct {
	ID         string            `json:"id"`
	FactCount  int               `json:"factCount"`
	Telemetry  []VectorTelemetry `json:"telemetry"`
	Operations []VectorOperation `json:"operations"`
}

type VectorTelemetry struct {
	Input json.RawMessage `json:"input"`
	Fact  json.RawMessage `json:"fact"`
}

type VectorOperation struct {
	Input json.RawMessage `json:"input"`
	Event json.RawMessage `json:"event"`
}

func loadEmitterVectors(t *testing.T) VectorFile {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "vectors", "emitters.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var vf VectorFile
	if err := json.Unmarshal(data, &vf); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	if len(vf.Scenarios) != 5 {
		t.Fatalf("want 5 scenarios, got %d", len(vf.Scenarios))
	}
	return vf
}

// eventFromVectorRaw decodes a vector input event into a projection.Event.
func eventFromVectorRaw(t *testing.T, raw json.RawMessage) projection.Event {
	t.Helper()
	var w struct {
		ID             string          `json:"id"`
		SessionID      string          `json:"sessionId"`
		TurnID         *string         `json:"turnId"`
		Type           string          `json:"type"`
		TimestampMs    int64           `json:"timestampMs"`
		TraceID        string          `json:"traceId"`
		SequenceNumber int64           `json:"sequenceNumber"`
		Payload        json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	ev := projection.Event{
		ID:             w.ID,
		SessionID:      w.SessionID,
		Type:           w.Type,
		TimestampMs:    w.TimestampMs,
		TraceID:        w.TraceID,
		SequenceNumber: w.SequenceNumber,
		Payload:        w.Payload,
	}
	if w.TurnID != nil {
		ev.TurnID = *w.TurnID
		ev.HasTurnID = true
	}
	return ev
}

// memFlag is the runtimeMetadata.memoryEnabled the vectors were built with.
func memFlag() *bool { v := false; return &v }

// TestVectorsReproduceTelemetryFacts replays every scenario's ordered event
// stream and requires the Go emitters to produce exactly the recorded telemetry
// facts, byte-for-byte (key order included), skipping ids the vector generator
// fills in.
func TestVectorsReproduceTelemetryFacts(t *testing.T) {
	vf := loadEmitterVectors(t)
	for _, sc := range vf.Scenarios {
		sc := sc
		t.Run(sc.ID, func(t *testing.T) {
			tel := New(vf.Session, Metadata{ModelName: "mock-model", ModelProvider: "mock", MemoryEnabled: memFlag()})
			idx := 0
			for _, tf := range sc.Telemetry {
				ev := eventFromVectorRaw(t, tf.Input)
				msg, ok, err := tel.OnSessionEventTelemetry(ev)
				if err != nil {
					t.Fatalf("event %s: %v", ev.Type, err)
				}
				if !ok {
					t.Fatalf("event seq %d (%s): no fact, want one", ev.SequenceNumber, ev.Type)
				}
				if idx >= len(sc.Telemetry) {
					t.Fatalf("more facts than vectors")
				}
				want := string(compactJSON(t, tf.Fact))
				if got := string(msg.Params); got != want {
					t.Fatalf("seq %d fact mismatch\n got: %s\nwant: %s", ev.SequenceNumber, got, want)
				}
				idx++
			}
			// Also assert no extra facts: replay all inputs and count.
			if idx != sc.FactCount {
				t.Fatalf("fact count = %d, want %d", idx, sc.FactCount)
			}
		})
	}
}

// TestVectorsReproduceOperationEvents replays every scenario and requires the Go
// operation-event mapper to produce exactly the recorded frames.
func TestVectorsReproduceOperationEvents(t *testing.T) {
	vf := loadEmitterVectors(t)
	for _, sc := range vf.Scenarios {
		sc := sc
		t.Run(sc.ID, func(t *testing.T) {
			for _, op := range sc.Operations {
				ev := eventFromVectorRaw(t, op.Input)
				got := mapOperationEvent(ev)
				if got == nil {
					t.Fatalf("seq %d (%s): no operation event, want one", ev.SequenceNumber, ev.Type)
				}
				if string(got) != string(compactJSON(t, op.Event)) {
					t.Fatalf("seq %d operation mismatch\n got: %s\nwant: %s", ev.SequenceNumber, got, op.Event)
				}
			}
		})
	}
}

// TestTelemetryFactsAppearInRecordedOrder replays each scenario's full ordered
// stream (telemetry facts interleaved with operation events) and requires both
// frame kinds in the order the Node agent emits them (telemetry then operation
// for the same event).
func TestTelemetryFactsAppearInRecordedOrder(t *testing.T) {
	vf := loadEmitterVectors(t)
	for _, sc := range vf.Scenarios {
		sc := sc
		t.Run(sc.ID, func(t *testing.T) {
			tel := New(vf.Session, Metadata{ModelName: "mock-model", ModelProvider: "mock", MemoryEnabled: memFlag()})
			// Merge both vector lists by their input sequenceNumber.
			factBySeq := map[int64][]byte{}
			for _, tf := range sc.Telemetry {
				ev := eventFromVectorRaw(t, tf.Input)
				factBySeq[ev.SequenceNumber] = tf.Fact
			}
			opBySeq := map[int64][]byte{}
			for _, op := range sc.Operations {
				ev := eventFromVectorRaw(t, op.Input)
				opBySeq[ev.SequenceNumber] = op.Event
			}
			all := append(append([]json.RawMessage{}, inputsOf(sc.Telemetry)...), opInputsOf(sc.Operations)...)
			// Re-derive an ordered event list from the two vector lists.
			seen := map[string]bool{}
			var events []projection.Event
			for _, raw := range all {
				ev := eventFromVectorRaw(t, raw)
				key := ev.ID
				if seen[key] {
					continue
				}
				seen[key] = true
				events = append(events, ev)
			}
			// Sort by sequenceNumber, then by whether a fact/op exists (stable enough
			// because a given seq has at most one event in these scenarios).
			sortEvents(events)
			for _, ev := range events {
				msgs, err := tel.OnSessionEvent(ev)
				if err != nil {
					t.Fatalf("event %s: %v", ev.Type, err)
				}
				var want []string
				if o, ok := opBySeq[ev.SequenceNumber]; ok {
					want = append(want, string(compactJSON(t, o)))
				}
				if f, ok := factBySeq[ev.SequenceNumber]; ok {
					want = append(want, string(compactJSON(t, f)))
				}
				if len(msgs) != len(want) {
					t.Fatalf("seq %d: %d frames, want %d", ev.SequenceNumber, len(msgs), len(want))
				}
				for i, m := range msgs {
					if string(m.Params) != want[i] {
						t.Fatalf("seq %d frame %d mismatch\n got: %s\nwant: %s", ev.SequenceNumber, i, m.Params, want[i])
					}
				}
			}
		})
	}
}

func inputsOf(v []VectorTelemetry) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(v))
	for _, x := range v {
		out = append(out, x.Input)
	}
	return out
}

func opInputsOf(v []VectorOperation) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(v))
	for _, x := range v {
		out = append(out, x.Input)
	}
	return out
}

func sortEvents(events []projection.Event) {
	// insertion sort (small n) by sequenceNumber
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && events[j-1].SequenceNumber > events[j].SequenceNumber; j-- {
			events[j-1], events[j] = events[j], events[j-1]
		}
	}
}
