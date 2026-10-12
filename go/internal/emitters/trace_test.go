package emitters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// traceFrame is one line of go/testdata/traces/agent/<id>/out.normalized.json.
type traceFrame struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// TestAgainstRecordedTraces replays each scenario's vector event stream and
// requires the Go emitters to produce the same ordered sequence of telemetry
// facts and operation events the recorded Node agent emitted. Identity and time
// fields differ (the recorder normalizes them to placeholders), so the check
// compares the frame kind and the sequence numbers — every other field is
// already pinned byte-for-byte by the vector tests.
func TestAgainstRecordedTraces(t *testing.T) {
	vf := loadEmitterVectors(t)
	for _, sc := range vf.Scenarios {
		sc := sc
		t.Run(sc.ID, func(t *testing.T) {
			tracePath := filepath.Join("..", "..", "testdata", "traces", "agent", sc.ID, "out.normalized.json")
			raw, err := os.ReadFile(tracePath)
			if err != nil {
				t.Fatalf("read trace: %v", err)
			}
			var frames []traceFrame
			if err := json.Unmarshal(raw, &frames); err != nil {
				t.Fatalf("parse trace: %v", err)
			}
			var wantTelemetry, wantOps []string
			for _, f := range frames {
				switch f.Method {
				case MethodTelemetry:
					var p struct {
						Kind     string `json:"kind"`
						EventSeq int64  `json:"eventSeq"`
					}
					if err := json.Unmarshal(f.Params, &p); err != nil {
						t.Fatalf("parse telemetry: %v", err)
					}
					wantTelemetry = append(wantTelemetry, key(p.Kind, p.EventSeq))
				case MethodOperationEvent:
					var p struct {
						Kind  string `json:"kind"`
						SeqNo int64  `json:"sequenceNumber"`
					}
					if err := json.Unmarshal(f.Params, &p); err != nil {
						t.Fatalf("parse operation: %v", err)
					}
					wantOps = append(wantOps, key(p.Kind, p.SeqNo))
				}
			}

			// Replay the vector stream through the Go emitters.
			tel := New(vf.Session, Metadata{ModelName: "mock-model", ModelProvider: "mock", MemoryEnabled: memFlag()})
			var gotTelemetry, gotOps []string
			for _, tf := range sc.Telemetry {
				ev := eventFromVectorRaw(t, tf.Input)
				msg, ok, err := tel.OnSessionEventTelemetry(ev)
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					t.Fatalf("seq %d: expected a fact", ev.SequenceNumber)
				}
				var p struct {
					Kind     string `json:"kind"`
					EventSeq int64  `json:"eventSeq"`
				}
				_ = json.Unmarshal(msg.Params, &p)
				gotTelemetry = append(gotTelemetry, key(p.Kind, p.EventSeq))
			}
			for _, op := range sc.Operations {
				ev := eventFromVectorRaw(t, op.Input)
				got := mapOperationEvent(ev)
				if got == nil {
					t.Fatalf("seq %d: expected an operation event", ev.SequenceNumber)
				}
				var p struct {
					Kind  string `json:"kind"`
					SeqNo int64  `json:"sequenceNumber"`
				}
				_ = json.Unmarshal(got, &p)
				gotOps = append(gotOps, key(p.Kind, p.SeqNo))
			}
			compareKeys(t, "telemetry", gotTelemetry, wantTelemetry)
			compareKeys(t, "operation", gotOps, wantOps)
		})
	}
}

func key(kind string, seq int64) string {
	b, _ := json.Marshal(seq)
	return kind + "#" + string(b)
}

func compareKeys(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s count = %d, want %d\n got: %v\nwant: %v", label, len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s[%d] = %s, want %s", label, i, got[i], want[i])
		}
	}
}
