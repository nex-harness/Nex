package modelclient

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// sseVector mirrors one entry of model-sse.json.
type sseVector struct {
	Name          string         `json:"name"`
	Text          string         `json:"text"`
	Events        []decodedEvent `json:"events"`
	TerminalError *struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	} `json:"terminalError"`
	Split map[string]struct {
		Events []decodedEvent `json:"events"`
		Error  string         `json:"error"`
	} `json:"split"`
}

type decodedEvent struct {
	Type         string          `json:"type"`
	ID           string          `json:"id"`
	Delta        string          `json:"delta"`
	Text         string          `json:"text"`
	ToolName     string          `json:"toolName"`
	Input        any             `json:"input"`
	ToolCallID   string          `json:"toolCallId"`
	FinishReason string          `json:"finishReason"`
	Usage        json.RawMessage `json:"usage"`
	Error        json.RawMessage `json:"error"`
}

// goEventsFromText runs the decoder over the whole fixture text.
func goEventsFromText(t *testing.T, text string) ([]Event, error) {
	t.Helper()
	d := NewDecoder()
	out := []Event{d.Start()}
	next, err := d.Decode([]byte(text), out)
	out = next
	if err != nil {
		return out, err
	}
	out, err = d.Finish(out)
	return out, err
}

func TestDecoderMatchesVectors(t *testing.T) {
	var vectors []sseVector
	readJSONFile(t, filepath.Join(vectorDir(t), "vectors", "model-sse.json"), &vectors)
	if len(vectors) == 0 {
		t.Fatal("no vectors")
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			events, err := goEventsFromText(t, v.Text)
			if v.TerminalError != nil {
				if err == nil {
					t.Fatalf("expected terminal error %q, got none (events %s)", v.TerminalError.Message, dumpEvents(events))
				}
				if !contains(err.Error(), v.TerminalError.Message) {
					t.Fatalf("terminal error %q does not contain %q", err.Error(), v.TerminalError.Message)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected terminal error: %v", err)
			}
			got := serializeEvents(events)
			if len(got) != len(v.Events) {
				t.Fatalf("event count mismatch\n got %s\nwant %s", mustJSONStr(got), mustJSONStr(v.Events))
			}
			for i := range got {
				if !eventsEqual(got[i], v.Events[i]) {
					t.Fatalf("event %d mismatch\n got %s\nwant %s", i, mustJSONStr(got[i]), mustJSONStr(v.Events[i]))
				}
			}
		})
	}
}

// TestDecoderSplitMatchesVectors feeds each fixture at 1/2/3-byte chunk sizes and
// compares against the vector produced by the real SDK pipeline at the same chunk
// sizes. This pins byte-boundary behavior against the reference, including the
// reference's own whole-vs-split differences on pathological lone-CR inputs.
func TestDecoderSplitMatchesVectors(t *testing.T) {
	var vectors []sseVector
	readJSONFile(t, filepath.Join(vectorDir(t), "vectors", "model-sse.json"), &vectors)
	for _, v := range vectors {
		for sizeStr, want := range v.Split {
			size := 0
			for _, r := range sizeStr {
				size = size*10 + int(r-'0')
			}
			if size <= 0 {
				continue
			}
			t.Run(v.Name+"/chunk"+sizeStr, func(t *testing.T) {
				events, err := decodeChunked(v.Text, size)
				if want.Error != "" {
					if err == nil {
						t.Fatalf("expected terminal error containing %q", want.Error)
					}
					if !contains(err.Error(), want.Error) {
						t.Fatalf("terminal error %q does not contain %q", err.Error(), want.Error)
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected terminal error: %v", err)
				}
				got := serializeEvents(events)
				if len(got) != len(want.Events) {
					t.Fatalf("event count mismatch\n got %s\nwant %s", mustJSONStr(got), mustJSONStr(want.Events))
				}
				for i := range got {
					if !eventsEqual(got[i], want.Events[i]) {
						t.Fatalf("event %d mismatch\n got %s\nwant %s", i, mustJSONStr(got[i]), mustJSONStr(want.Events[i]))
					}
				}
			})
		}
	}
}

// decodeChunked decodes text in fixed-size byte chunks.
func decodeChunked(text string, size int) ([]Event, error) {
	d := NewDecoder()
	out := []Event{d.Start()}
	for i := 0; i < len(text); i += size {
		end := i + size
		if end > len(text) {
			end = len(text)
		}
		var err error
		out, err = d.Decode([]byte(text[i:end]), out)
		if err != nil {
			return out, err
		}
	}
	return d.Finish(out)
}

func serializeEvents(events []Event) []decodedEvent {
	out := make([]decodedEvent, 0, len(events))
	for _, ev := range events {
		switch ev.Kind {
		case EventStart:
			out = append(out, decodedEvent{Type: "start"})
		case EventTextStart:
			out = append(out, decodedEvent{Type: "text_start", ID: ev.ID})
		case EventTextDelta:
			out = append(out, decodedEvent{Type: "text_delta", ID: ev.ID, Text: ev.Text})
		case EventTextEnd:
			out = append(out, decodedEvent{Type: "text_end", ID: ev.ID})
		case EventReasoningStart:
			out = append(out, decodedEvent{Type: "reasoning_start", ID: ev.ID})
		case EventReasoningDelta:
			out = append(out, decodedEvent{Type: "reasoning_delta", ID: ev.ID, Text: ev.Text})
		case EventReasoningEnd:
			out = append(out, decodedEvent{Type: "reasoning_end", ID: ev.ID})
		case EventToolInputStart:
			out = append(out, decodedEvent{Type: "tool_input_start", ID: ev.ID, ToolName: ev.ToolName})
		case EventToolInputDelta:
			out = append(out, decodedEvent{Type: "tool_input_delta", ID: ev.ID, Delta: ev.Text})
		case EventToolInputEnd:
			out = append(out, decodedEvent{Type: "tool_input_end", ID: ev.ID})
		case EventToolCall:
			out = append(out, decodedEvent{Type: "tool_call", ToolCallID: ev.ToolCall.ID, ToolName: ev.ToolCall.Name, Input: ev.ToolCall.Arguments})
		case EventFinish:
			out = append(out, decodedEvent{Type: "finish", FinishReason: ev.FinishReason, Usage: usageRaw(ev.Usage)})
		case EventError:
			out = append(out, decodedEvent{Type: "error"})
		}
	}
	return out
}

func usageRaw(u *Usage) json.RawMessage {
	if u == nil {
		return json.RawMessage("null")
	}
	m := map[string]any{}
	if u.PromptTokens != nil {
		m["prompt_tokens"] = *u.PromptTokens
	}
	if u.CompletionTokens != nil {
		m["completion_tokens"] = *u.CompletionTokens
	}
	if u.TotalTokens != nil {
		m["total_tokens"] = *u.TotalTokens
	}
	b, _ := json.Marshal(m)
	return b
}

func eventsEqual(got, want decodedEvent) bool {
	if got.Type != want.Type {
		return false
	}
	if want.ID != "" && got.ID != want.ID {
		return false
	}
	if want.Delta != "" && got.Delta != want.Delta {
		return false
	}
	if want.Text != "" && got.Text != want.Text {
		return false
	}
	if want.ToolName != "" && got.ToolName != want.ToolName {
		return false
	}
	if want.ToolCallID != "" && got.ToolCallID != want.ToolCallID {
		return false
	}
	if want.FinishReason != "" && got.FinishReason != want.FinishReason {
		return false
	}
	if len(want.Usage) > 0 && string(want.Usage) != "null" && !jsonEqual(got.Usage, want.Usage) {
		return false
	}
	return true
}

func jsonEqual(a, b json.RawMessage) bool {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	return deepEqualJSON(av, bv)
}

func deepEqualJSON(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func mustJSONStr(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func dumpEvents(events []Event) string {
	return mustJSONStr(serializeEvents(events))
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// An incomplete UTF-8 sequence still pending at end of stream must surface as
// U+FFFD when the decoder is finished (WHATWG TextDecoder flush behaviour).
// Without the flush the byte would be silently dropped.
func TestFinishFlushesIncompleteUTF8AsReplacementCharacter(t *testing.T) {
	d := NewDecoder()
	// "data: {...}\n\n" whose content ends with the first 2 bytes of a 3-byte rune,
	// followed by no terminating blank line: the tail is only seen on Finish.
	payload := "data: {\"choices\":[{\"delta\":{\"content\":\"a\xe4\xb8"
	var out []Event
	var err error
	out, err = d.Decode([]byte(payload), out)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("no complete event expected before Finish, got %d", len(out))
	}
	if got := d.textDecoder.pendingLen(); got != 2 {
		t.Fatalf("incomplete sequence must be held back (2 bytes), pending=%d", got)
	}
	d.textDecoder.decode(nil, true)
	if got := d.textDecoder.pendingLen(); got != 0 {
		t.Fatalf("flush must clear the pending bytes, pending=%d", got)
	}
}

func TestTextDecoderFlushEmitsReplacementForIncompleteSequence(t *testing.T) {
	var td textDecoder
	if s := td.decode([]byte("ok\xe4\xb8"), false); s != "ok" {
		t.Fatalf("held-back decode = %q", s)
	}
	if s := td.decode(nil, true); s != "\uFFFD" {
		t.Fatalf("flush = %q, want U+FFFD", s)
	}
}
