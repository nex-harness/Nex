package anthropic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tylinux/nex/go/internal/modelclient"
)

// sseEventBody is one framed SSE event's data payload. The tests parse the
// fixture bytes here rather than reusing modelclient's parser, because the
// decoder under test is fed bodies, not bytes: framing belongs to
// internal/modelclient and is covered by its own split/fuzz tests.
type sseEventBody struct {
	data  string
	event string
}

// splitFixture frames a raw SSE fixture into event bodies. It understands only
// what the fixtures use (data:/event:/comment lines, LF and CRLF), which is
// enough to hand the decoder exactly what the real parser would.
func splitFixture(text string) []sseEventBody {
	var out []sseEventBody
	var data []string
	var event string
	flush := func() {
		if len(data) > 0 {
			out = append(out, sseEventBody{data: joinLines(data), event: event})
		}
		data = nil
		event = ""
	}
	for _, line := range splitLines(text) {
		switch {
		case line == "":
			flush()
		case line[0] == ':':
			// comment
		case hasPrefix(line, "data:"):
			value := line[5:]
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
			data = append(data, value)
		case hasPrefix(line, "event:"):
			value := line[6:]
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
			event = value
		}
	}
	flush()
	return out
}

// splitLines splits on LF, CRLF or a lone CR, mirroring what the SSE parser
// accepts. The CRLF case consumes both bytes before computing the next offset.
func splitLines(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] != '\n' && text[i] != '\r' {
			continue
		}
		out = append(out, text[start:i])
		if text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n' {
			i++
		}
		start = i + 1
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// decodeFixture runs the decoder over one fixture's event bodies.
func decodeFixture(t *testing.T, bodies []sseEventBody) []modelclient.Event {
	t.Helper()
	d := NewDecoder()
	var out []modelclient.Event
	out = append(out, d.Start())
	for _, body := range bodies {
		var err error
		out, err = d.Consume(body.data, out)
		if err != nil {
			t.Fatalf("Consume(%s): %v", body.data, err)
		}
	}
	out, err := d.Finish(out)
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return out
}

// TestDecoderFixturesAtEveryChunkSplit is the core property: the byte stream may
// arrive in any chunking, and the decoded event sequence must not change. The
// fixture splitter is fed here in whole-event units; the byte-level split
// guarantee lives in vectors_test.go, which re-frames the same bytes through
// modelclient's real SSE parser at 1/2/3-byte chunk sizes.
func TestDecoderFixturesAtEveryChunkSplit(t *testing.T) {
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			bodies := splitFixture(readFixture(t, name))
			want := decodeFixture(t, bodies)
			if len(want) == 0 {
				t.Fatalf("fixture %s produced no events", name)
			}
			// Replaying the same bodies in the same order through a fresh decoder
			// must produce an identical sequence; the split dimension that matters
			// is the framing, covered by vectors_test.go.
			got := decodeFixture(t, bodies)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("non-deterministic decode:\ngot  %s\nwant %s", formatEvents(got), formatEvents(want))
			}
		})
	}
}

// TestDecoderEventNames pins the visible event kinds per fixture, so a mapping
// change (a delta dropped, a block emitting an extra start) fails loudly.
func TestDecoderEventNames(t *testing.T) {
	want := map[string][]modelclient.EventKind{
		"text-only": {
			modelclient.EventStart,
			modelclient.EventTextDelta, modelclient.EventTextDelta, modelclient.EventTextDelta,
			modelclient.EventTextDelta, modelclient.EventTextDelta, modelclient.EventTextDelta,
			modelclient.EventTextDelta, modelclient.EventTextDelta,
			modelclient.EventUsage,
			modelclient.EventFinish,
		},
		"thinking-then-text": {
			modelclient.EventStart,
			modelclient.EventReasoningDelta, modelclient.EventReasoningDelta,
			modelclient.EventTextDelta,
			modelclient.EventUsage,
			modelclient.EventFinish,
		},
		"tool-call-multiple": {
			modelclient.EventStart,
			modelclient.EventToolInputStart,
			modelclient.EventToolInputDelta,
			modelclient.EventToolInputEnd,
			modelclient.EventTextDelta,
			modelclient.EventToolInputStart,
			modelclient.EventToolInputDelta, modelclient.EventToolInputDelta, modelclient.EventToolInputDelta,
			modelclient.EventToolInputEnd,
			modelclient.EventUsage,
			modelclient.EventFinish,
		},
		"ping-and-comments": {
			modelclient.EventStart,
			modelclient.EventTextDelta, modelclient.EventTextDelta,
			modelclient.EventUsage,
			modelclient.EventFinish,
		},
		"missing-event-header": {
			modelclient.EventStart,
			modelclient.EventTextDelta,
			modelclient.EventUsage,
			modelclient.EventFinish,
		},
		"error-event": {
			modelclient.EventStart,
			modelclient.EventError,
		},
	}
	for name, wantKinds := range want {
		t.Run(name, func(t *testing.T) {
			got := decodeFixture(t, splitFixture(readFixture(t, name)))
			gotKinds := make([]modelclient.EventKind, 0, len(got))
			for _, ev := range got {
				gotKinds = append(gotKinds, ev.Kind)
			}
			if !reflect.DeepEqual(gotKinds, wantKinds) {
				t.Fatalf("event kinds:\ngot  %v\nwant %v", gotKinds, wantKinds)
			}
		})
	}
}

// TestDecoderIgnoresPingAndEmptyDeltas proves the two no-op branches: a ping event
// and an empty input_json_delta produce nothing.
func TestDecoderIgnoresPingAndEmptyDeltas(t *testing.T) {
	d := NewDecoder()
	var out []modelclient.Event
	out, err := d.Consume(`{"type":"ping"}`, out)
	if err != nil {
		t.Fatal(err)
	}
	out, err = d.Consume(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"Read","input":{}}}`, out)
	if err != nil {
		t.Fatal(err)
	}
	// An empty partial_json must not emit a tool-input-delta (the AI SDK provider
	// skips these so a first-character replacement stays possible).
	out, err = d.Consume(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`, out)
	if err != nil {
		t.Fatal(err)
	}
	out, err = d.Finish(out)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []modelclient.EventKind{}
	for _, ev := range out {
		kinds = append(kinds, ev.Kind)
	}
	want := []modelclient.EventKind{
		modelclient.EventToolInputStart,
		modelclient.EventToolInputEnd,
		modelclient.EventToolCall,
		modelclient.EventFinish,
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	// The tool call with no arguments still carries an empty argument string; the
	// assembler normalizes that to {} downstream.
	for _, ev := range out {
		if ev.Kind == modelclient.EventToolCall && ev.ToolCall.Arguments != "" {
			t.Fatalf("expected empty arguments, got %q", ev.ToolCall.Arguments)
		}
	}
}

// TestDecoderIgnoresUnknownType proves an unrecognised type is skipped instead of
// failing the turn: a proxy may add vendor events between ours.
func TestDecoderIgnoresUnknownType(t *testing.T) {
	d := NewDecoder()
	var out []modelclient.Event
	out, err := d.Consume(`{"type":"some_vendor_event","payload":{"x":1}}`, out)
	if err != nil {
		t.Fatal(err)
	}
	out, err = d.Consume("not json at all", out)
	if err != nil {
		t.Fatal(err)
	}
	// "not json" is undecodable and becomes an error event.
	kinds := []modelclient.EventKind{}
	for _, ev := range out {
		kinds = append(kinds, ev.Kind)
	}
	if len(kinds) != 1 || kinds[0] != modelclient.EventError {
		t.Fatalf("kinds = %v, want one error", kinds)
	}
}

// TestDecoderMergesUsage pins the message_start + message_delta merge: input from
// the start event survives, output comes from the delta, and the finish event
// carries the same cumulative numbers.
func TestDecoderMergesUsage(t *testing.T) {
	out := decodeFixture(t, splitFixture(readFixture(t, "usage-in-message-delta")))
	var finish *modelclient.Event
	var usageEvents []modelclient.Event
	for i := range out {
		if out[i].Kind == modelclient.EventFinish {
			finish = &out[i]
		}
		if out[i].Kind == modelclient.EventUsage {
			usageEvents = append(usageEvents, out[i])
		}
	}
	if finish == nil {
		t.Fatal("no finish event")
	}
	if len(usageEvents) != 1 {
		t.Fatalf("want 1 usage event, got %d", len(usageEvents))
	}
	got := finish.Usage
	if got.PromptTokens == nil || *got.PromptTokens != 111 {
		t.Fatalf("prompt tokens = %v, want 111", got.PromptTokens)
	}
	if got.CompletionTokens == nil || *got.CompletionTokens != 7 {
		t.Fatalf("completion tokens = %v, want 7", got.CompletionTokens)
	}
	if got.TotalTokens == nil || *got.TotalTokens != 118 {
		t.Fatalf("total tokens = %v, want 118", got.TotalTokens)
	}
	// The usage event must not alias the decoder's running total.
	usageEvents[0].Usage.PromptTokens = nil
	if finish.Usage.PromptTokens == nil {
		t.Fatal("usage event aliased the decoder's running usage")
	}
}

// TestMapStopReason pins the normalization table, including the context-window
// reason the failure classifier depends on.
func TestMapStopReason(t *testing.T) {
	cases := map[string]string{
		"end_turn":                       modelclient.FinishStop,
		"stop_sequence":                  modelclient.FinishStop,
		"pause_turn":                     modelclient.FinishStop,
		"refusal":                        modelclient.FinishContentFilter,
		"tool_use":                       modelclient.FinishToolCalls,
		"max_tokens":                     modelclient.FinishLength,
		"model_context_window_exceeded":  modelclient.FinishLength,
		"compaction":                     modelclient.FinishOther,
		"something_the_provider_invents": modelclient.FinishOther,
	}
	for stop, want := range cases {
		if got := MapStopReason(stop); got != want {
			t.Errorf("MapStopReason(%q) = %q, want %q", stop, got, want)
		}
	}
}

func fixtureDir(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testdata", "sse", "anthropic")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func readFixture(t testing.TB, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir(t), name+".txt"))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return string(data)
}

func fixtureNames(t testing.TB) []string {
	t.Helper()
	entries, err := os.ReadDir(fixtureDir(t))
	if err != nil {
		t.Fatalf("fixture dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".txt" {
			out = append(out, e.Name()[:len(e.Name())-4])
		}
	}
	if len(out) == 0 {
		t.Fatal("no fixtures; run scripts/go-rewrite/gen-anthropic-vectors.mts")
	}
	return out
}

func formatEvents(events []modelclient.Event) string {
	b, _ := json.Marshal(eventSummary(events))
	return string(b)
}

func eventSummary(events []modelclient.Event) []map[string]any {
	out := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		row := map[string]any{"kind": string(ev.Kind)}
		if ev.ID != "" {
			row["id"] = ev.ID
		}
		if ev.Text != "" {
			row["text"] = ev.Text
		}
		if ev.ToolName != "" {
			row["tool"] = ev.ToolName
		}
		if ev.FinishReason != "" {
			row["finish"] = ev.FinishReason
		}
		if ev.Err != nil {
			row["err"] = ev.Err.Error()
		}
		out = append(out, row)
	}
	return out
}
