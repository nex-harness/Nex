package anthropic

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tylinux/nex/go/internal/modelclient"
)

// recordedVector mirrors one entry of go/testdata/vectors/model-anthropic.json,
// produced by scripts/go-rewrite/gen-anthropic-vectors.mts from the real
// @anthropic-ai/sdk.
type recordedVector struct {
	Name              string          `json:"name"`
	Notes             string          `json:"notes"`
	Text              string          `json:"text"`
	ModelClientEvents []recordedEvent `json:"modelClientEvents"`
	Usage             struct {
		InputTokens  *int64 `json:"inputTokens"`
		OutputTokens *int64 `json:"outputTokens"`
	} `json:"usage"`
	StopReason  *string `json:"stopReason"`
	StreamError *struct {
		Name    string `json:"name"`
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"streamError"`
	SDKDropsEvents bool `json:"sdkDroppedEvents"`
}

type recordedEvent struct {
	Type         string  `json:"type"`
	ID           *string `json:"id"`
	Text         *string `json:"text"`
	ToolName     *string `json:"toolName"`
	FinishReason *string `json:"finishReason"`
}

// recordedTypeToModelClient maps the generator's event vocabulary onto
// modelclient's EventKind.
func recordedTypeToModelClient(t string) (modelclient.EventKind, bool) {
	switch t {
	case "text_delta":
		return modelclient.EventTextDelta, true
	case "reasoning_delta":
		return modelclient.EventReasoningDelta, true
	case "tool_input_start":
		return modelclient.EventToolInputStart, true
	case "tool_input_delta":
		return modelclient.EventToolInputDelta, true
	case "tool_input_end":
		return modelclient.EventToolInputEnd, true
	case "error":
		return modelclient.EventError, true
	case "finish":
		return modelclient.EventFinish, true
	default:
		return "", false
	}
}

// TestAnthropicVectorsReplay drives each recorded fixture through the decoder and
// compares the produced event sequence against what the generator folded from the
// real SDK's per-type events. The `start` event the decoder prepends is dropped
// from both sides because the generator's fold starts at the first provider event.
func TestAnthropicVectorsReplay(t *testing.T) {
	vectors := readVectors(t)
	if len(vectors) == 0 {
		t.Fatal("no vectors; run scripts/go-rewrite/gen-anthropic-vectors.mts")
	}
	for _, vec := range vectors {
		t.Run(vec.Name, func(t *testing.T) {
			bodies := splitFixture(vec.Text)
			got := decodeFixture(t, bodies)
			// Drop the synthetic start event from the decoder output; the generator
			// folds only provider events.
			if len(got) > 0 && got[0].Kind == modelclient.EventStart {
				got = got[1:]
			}

			want := make([]modelclient.Event, 0, len(vec.ModelClientEvents))
			for _, ev := range vec.ModelClientEvents {
				kind, ok := recordedTypeToModelClient(ev.Type)
				if !ok {
					continue
				}
				out := modelclient.Event{Kind: kind}
				if ev.ID != nil {
					out.ID = *ev.ID
				}
				if ev.Text != nil {
					out.Text = *ev.Text
				}
				if ev.ToolName != nil {
					out.ToolName = *ev.ToolName
				}
				if ev.FinishReason != nil {
					out.FinishReason = *ev.FinishReason
				}
				want = append(want, out)
			}

			// The error fixture's error event carries an Err; the recorded side only
			// has a text marker, so compare kinds+order rather than full events for
			// that case. For every other fixture the fields must match exactly.
			if vec.StreamError != nil {
				if err := assertKindsMatch(got, want); err != nil {
					t.Fatalf("error fixture kinds: %v", err)
				}
				return
			}
			if !eventsEqual(got, want) {
				t.Fatalf("event mismatch:\ngot  %s\nwant %s", formatEvents(got), formatEvents(want))
			}
			// Usage and stop reason are recorded separately; assert them here.
			assertUsageAndStop(t, got, vec)
		})
	}
}

// TestMissingEventHeaderDecodesWhereSDKDropsIt is the R4 regression: for the
// data-only fixture the generator recorded sdkDroppedEvents=true because the TS
// SDK's Stream.Next silently discarded it. Our decoder, branching on JSON type,
// must still produce the full sequence.
func TestMissingEventHeaderDecodesWhereSDKDropsIt(t *testing.T) {
	vectors := readVectors(t)
	var found bool
	for _, vec := range vectors {
		if vec.Name != "missing-event-header" {
			continue
		}
		found = true
		if !vec.SDKDropsEvents {
			t.Fatal("fixture should record sdkDroppedEvents=true; the SDK behaviour changed")
		}
		bodies := splitFixture(vec.Text)
		got := decodeFixture(t, bodies)
		var textDeltas int
		var finish *modelclient.Event
		for i := range got {
			if got[i].Kind == modelclient.EventTextDelta {
				textDeltas++
			}
			if got[i].Kind == modelclient.EventFinish {
				finish = &got[i]
			}
		}
		if textDeltas == 0 {
			t.Fatal("no text deltas: a data-only stream must still decode")
		}
		if finish == nil || finish.FinishReason != modelclient.FinishStop {
			t.Fatalf("finish = %v, want stop", finish)
		}
	}
	if !found {
		t.Fatal("missing-event-header vector not found")
	}
}

func assertUsageAndStop(t *testing.T, got []modelclient.Event, vec recordedVector) {
	t.Helper()
	var finish *modelclient.Event
	for i := range got {
		if got[i].Kind == modelclient.EventFinish {
			finish = &got[i]
		}
	}
	if finish == nil {
		t.Fatalf("no finish event in %s", vec.Name)
	}
	if vec.StopReason != nil && finish.FinishReason != *vec.StopReason {
		t.Errorf("stop reason = %q, want %q", finish.FinishReason, *vec.StopReason)
	}
	if vec.Usage.InputTokens != nil {
		if finish.Usage.PromptTokens == nil || *finish.Usage.PromptTokens != *vec.Usage.InputTokens {
			t.Errorf("input tokens = %v, want %v", finish.Usage.PromptTokens, *vec.Usage.InputTokens)
		}
	}
	if vec.Usage.OutputTokens != nil {
		if finish.Usage.CompletionTokens == nil || *finish.Usage.CompletionTokens != *vec.Usage.OutputTokens {
			t.Errorf("output tokens = %v, want %v", finish.Usage.CompletionTokens, *vec.Usage.OutputTokens)
		}
	}
}

func assertKindsMatch(got, want []modelclient.Event) error {
	gotKinds := kindsOf(got)
	wantKinds := kindsOf(want)
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		return fmt.Errorf("kinds differ:\ngot  %v\nwant %v", gotKinds, wantKinds)
	}
	return nil
}

func kindsOf(events []modelclient.Event) []modelclient.EventKind {
	out := make([]modelclient.EventKind, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Kind)
	}
	return out
}

// eventsEqual compares two event sequences on the fields the generator records.
// Errors are compared on kind only (their text is a generator-side marker).
//
// Two known divergences are tolerated, both places where the decoder is more
// informative than the SDK's folded view and the extra information is not
// asserted by the vector file:
//
//   - id on text deltas: the SDK never surfaces the Anthropic block id to
//     callers, so the generator folds it away. The decoder keeps it (it is part
//     of the closed set so the projection can key text blocks). The comparison
//     ignores id on EventTextDelta.
//   - usage: the SDK folds usage into the finish event; the decoder emits a
//     separate EventUsage first so callers that track usage increments see it
//     as it arrives. The comparison drops EventUsage from both sides; usage
//     content is asserted separately in assertUsageAndStop.
func eventsEqual(got, want []modelclient.Event) bool {
	got = normalizeForCompare(got)
	want = normalizeForCompare(want)
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		g, w := got[i], want[i]
		if g.Kind != w.Kind {
			return false
		}
		if w.Kind == modelclient.EventError {
			if g.Err == nil {
				return false
			}
			continue
		}
		if g.ID != w.ID || g.Text != w.Text || g.ToolName != w.ToolName || g.FinishReason != w.FinishReason {
			return false
		}
	}
	return true
}

func normalizeForCompare(events []modelclient.Event) []modelclient.Event {
	out := make([]modelclient.Event, 0, len(events))
	for _, ev := range events {
		if ev.Kind == modelclient.EventUsage {
			continue
		}
		if ev.Kind == modelclient.EventTextDelta || ev.Kind == modelclient.EventReasoningDelta {
			ev.ID = ""
		}
		out = append(out, ev)
	}
	return out
}

func readVectors(t testing.TB) []recordedVector {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdataDir(t), "vectors", "model-anthropic.json"))
	if err != nil {
		t.Fatalf("vectors: %v", err)
	}
	var vectors []recordedVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	return vectors
}

func testdataDir(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testdata")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
