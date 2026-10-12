package modelclient

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

type assemblerVector struct {
	Name     string         `json:"name"`
	Input    []decodedEvent `json:"input"`
	Output   []decodedEvent `json:"output"`
	Snapshot []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Input any    `json:"input"`
	} `json:"snapshot"`
}

// toAssemblerEvent builds a modelclient Event from a decoded vector event, the
// same way the Node test builds a ModelStreamEvent from a TextStreamPart.
func toAssemblerEvent(ev decodedEvent) Event {
	switch ev.Type {
	case "start":
		return Event{Kind: EventStart}
	case "text_start":
		return Event{Kind: EventTextStart, ID: ev.ID}
	case "text_delta":
		return Event{Kind: EventTextDelta, ID: ev.ID, Text: ev.Text}
	case "text_end":
		return Event{Kind: EventTextEnd, ID: ev.ID}
	case "reasoning_start":
		return Event{Kind: EventReasoningStart, ID: ev.ID}
	case "reasoning_delta":
		return Event{Kind: EventReasoningDelta, ID: ev.ID, Text: ev.Text}
	case "reasoning_end":
		return Event{Kind: EventReasoningEnd, ID: ev.ID}
	case "tool_input_start":
		return Event{Kind: EventToolInputStart, ID: ev.ID, ToolName: ev.ToolName}
	case "tool_input_delta":
		return Event{Kind: EventToolInputDelta, ID: ev.ID, Text: ev.Delta}
	case "tool_input_end":
		return Event{Kind: EventToolInputEnd, ID: ev.ID}
	case "tool_call":
		return Event{Kind: EventToolCall, ToolCall: ToolCall{ID: ev.ToolCallID, Name: ev.ToolName, HasInput: true, Input: ev.Input}}
	case "finish":
		return Event{Kind: EventFinish, FinishReason: ev.FinishReason, Usage: usageFromRaw(ev.Usage)}
	default:
		return Event{Kind: EventKind(ev.Type)}
	}
}

func TestAssemblerMatchesVectors(t *testing.T) {
	var vectors []assemblerVector
	readJSONFile(t, filepath.Join(vectorDir(t), "vectors", "model-assembler.json"), &vectors)
	if len(vectors) == 0 {
		t.Fatal("no vectors")
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			a := NewAssembler()
			var out []Event
			for _, in := range v.Input {
				events, err := a.Handle(toAssemblerEvent(in))
				if err != nil {
					t.Fatalf("handle %s: %v", in.Type, err)
				}
				out = append(out, events...)
			}
			got := serializeEvents(out)
			if len(got) != len(v.Output) {
				t.Fatalf("output count mismatch\n got %s\nwant %s", mustJSONStr(got), mustJSONStr(v.Output))
			}
			for i := range got {
				if !eventsEqual(got[i], v.Output[i]) {
					t.Fatalf("output %d mismatch\n got %s\nwant %s", i, mustJSONStr(got[i]), mustJSONStr(v.Output[i]))
				}
			}
			snapshot := a.SnapshotNormalizedToolCalls()
			if len(snapshot) != len(v.Snapshot) {
				t.Fatalf("snapshot count mismatch: got %d want %d", len(snapshot), len(v.Snapshot))
			}
			for i, tc := range snapshot {
				if tc.ID != v.Snapshot[i].ID || tc.Name != v.Snapshot[i].Name {
					t.Fatalf("snapshot %d mismatch: got %s/%s want %s/%s", i, tc.ID, tc.Name, v.Snapshot[i].ID, v.Snapshot[i].Name)
				}
				// The Node vector's input is the parsed value (the assembler parses
				// the raw string). Compare via canonical JSON.
				wantJSON, _ := json.Marshal(v.Snapshot[i].Input)
				gotJSON, err := MarshalCanonicalInput(tc.Input)
				if err != nil {
					t.Fatalf("marshal input: %v", err)
				}
				if !jsonEqualString(wantJSON, gotJSON) {
					t.Fatalf("snapshot %d input mismatch: got %s want %s", i, gotJSON, wantJSON)
				}
			}
		})
	}
}

func jsonEqualString(a []byte, b string) bool {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(b), &bv); err != nil {
		return false
	}
	ab, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return string(ab) == string(bb)
}

// usageFromRaw parses the vector's raw usage object into a Usage.
func usageFromRaw(raw json.RawMessage) *Usage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var m map[string]json.Number
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	u := &Usage{}
	if v, ok := m["prompt_tokens"]; ok {
		u.PromptTokens = int64Ptr(v)
	}
	if v, ok := m["completion_tokens"]; ok {
		u.CompletionTokens = int64Ptr(v)
	}
	if v, ok := m["total_tokens"]; ok {
		u.TotalTokens = int64Ptr(v)
	}
	return u
}

func int64Ptr(n json.Number) *int64 {
	v, err := n.Int64()
	if err != nil {
		return nil
	}
	return &v
}

// TestAssemblerDedupesByID exercises the dedupe paths that no SSE fixture
// reaches: a repeated tool_call, a repeat after a completed streaming input, and
// a tool_call that arrives while the streaming input is still open (which must be
// dropped so the missing tool-input-end is not silently replaced).
func TestAssemblerDedupesByID(t *testing.T) {
	a := NewAssembler()
	// A full streaming lifecycle, then the final call.
	mustHandle(t, a, Event{Kind: EventToolInputStart, ID: "c1", ToolName: "Read"})
	mustHandle(t, a, Event{Kind: EventToolInputDelta, ID: "c1", Text: `{"a":1}`})
	mustHandle(t, a, Event{Kind: EventToolInputEnd, ID: "c1"})
	out := mustHandle(t, a, Event{Kind: EventToolCall, ToolCall: ToolCall{ID: "c1", Name: "Read", Arguments: `{"a":1}`}})
	if len(out) != 1 || out[0].Kind != EventToolCall {
		t.Fatalf("expected one tool_call, got %v", out)
	}
	// A second call for the same id must be dropped.
	if got := mustHandle(t, a, Event{Kind: EventToolCall, ToolCall: ToolCall{ID: "c1", Name: "Read", Arguments: `{"a":1}`}}); len(got) != 0 {
		t.Fatalf("expected duplicate tool_call to be dropped, got %v", got)
	}
	// A tool_input_start for an already-normalized id must be dropped.
	if got := mustHandle(t, a, Event{Kind: EventToolInputStart, ID: "c1", ToolName: "Read"}); len(got) != 0 {
		t.Fatalf("expected tool_input_start for normalized id to be dropped, got %v", got)
	}

	// A final call while a streaming input is still open (no end) is dropped.
	b := NewAssembler()
	mustHandle(t, b, Event{Kind: EventToolInputStart, ID: "c2", ToolName: "Write"})
	if got := mustHandle(t, b, Event{Kind: EventToolCall, ToolCall: ToolCall{ID: "c2", Name: "Write", Arguments: "{}"}}); len(got) != 0 {
		t.Fatalf("expected open-streaming final call to be dropped, got %v", got)
	}
	// The same id after the input ends is accepted.
	mustHandle(t, b, Event{Kind: EventToolInputEnd, ID: "c2"})
	if got := mustHandle(t, b, Event{Kind: EventToolCall, ToolCall: ToolCall{ID: "c2", Name: "Write", Arguments: "{}"}}); len(got) != 1 {
		t.Fatalf("expected tool_call after input end, got %v", got)
	}
}

func mustHandle(t *testing.T, a *Assembler, ev Event) []Event {
	t.Helper()
	out, err := a.Handle(ev)
	if err != nil {
		t.Fatalf("handle %s: %v", ev.Kind, err)
	}
	return out
}
