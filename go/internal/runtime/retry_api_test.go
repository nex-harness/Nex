package runtime

// This file pins the apiRetry / model-retry behaviour against the Node agent.
//
// Oracle: apps/nex-cli/packages/adapters/src/model/runner-stream.ts emits one
// model_retry_scheduled per failed physical attempt, each carrying that
// attempt's own `attempt` (1-based) and `nextAttempt: attempt + 1`. The
// product projection (product-projection.ts onModelNetworkStatus) maps that
// event onto control.apiRetry = {attempt, maxAttempts, nextRetryAt,
// reasonCode}, so the projected `attempt` is the attempt that just failed, not
// the one about to run.
//
// The projection and the frame boundaries are NOT this package's business: what
// is pinned here is the runtime half, namely the rule recorded in
// appendNetworkStatus. The failure this test guards against is real: flushing
// per retry status split one retry lifecycle into N frames, so the N apiRetry
// patches each survived alone on their own frame (heldout 429-twice: Node emits
// one apiRetry delta carrying attempt 2, Go emitted attempt 1 then attempt 2).
// Because the frame boundary is what decides whether adjacent state.updated
// patches coalesce (v4pub coalesce rule 2), the assertion has to be made on
// the delivered frames.

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/tylinux/nex/go/internal/modelclient"
	"github.com/tylinux/nex/go/internal/v4pub"
)

// retryTransport answers the scripted statuses in order: a transport error
// counts as an attempt the model client retries.
type retryTransport struct {
	statuses []int // HTTP status per call; 200 means "stream the success body"
	calls    int
}

const retrySuccessBody = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: [DONE]\n\n"

func (t *retryTransport) Do(_ context.Context, _ modelclient.Request) (*modelclient.Response, error) {
	status := 200
	if t.calls < len(t.statuses) {
		status = t.statuses[t.calls]
	}
	t.calls++
	switch status {
	case 200:
		return &modelclient.Response{StatusCode: 200, Stream: io.NopCloser(strings.NewReader(retrySuccessBody))}, nil
	case 429:
		return &modelclient.Response{StatusCode: 429, Headers: map[string]string{"Retry-After": "0"}, BodyText: ""}, nil
	default:
		return &modelclient.Response{StatusCode: status, BodyText: ""}, nil
	}
}

// apiRetryFromFrame extracts every apiRetry the delivered frames carry, in order.
func apiRetryFromFrame(frame v4pub.Frame) []map[string]any {
	var out []map[string]any
	for _, d := range frame.Payload.Deltas {
		if d.Op != "state.updated" {
			continue
		}
		raw, err := d.Raw.MarshalJSON()
		if err != nil {
			continue
		}
		var delta struct {
			Patch struct {
				Control *struct {
					APIRetry *map[string]any `json:"apiRetry"`
				} `json:"control"`
			} `json:"patch"`
		}
		if json.Unmarshal(raw, &delta) != nil || delta.Patch.Control == nil {
			continue
		}
		if delta.Patch.Control.APIRetry == nil {
			continue // an explicit null clear, not a retry announcement
		}
		out = append(out, *delta.Patch.Control.APIRetry)
	}
	return out
}

// newRetryRuntime builds a runtime whose model client actually retries. The
// shared harness leaves Retry zero-valued (the production agent resolves it in
// cmd/nex-agent); with MaxAttempts 0 the retry loop never runs at all, so the
// rule under test could not be exercised.
func newRetryRuntime(t *testing.T, transport ModelTransport) (*Runtime, *fakeOutbox) {
	t.Helper()
	rt, ob, _ := newTestRuntime(t, transport)
	rt.deps.ModelConfig.Retry = modelclient.ResolveRetryOptions(nil, nil)
	return rt, ob
}

// newRetrySession builds a session with one continuous-profile subscriber.
func newRetrySession(t *testing.T, rt *Runtime) (*session, *fakeOutbox) {
	t.Helper()
	s := mustSession(t, rt)
	s.seedPublisher()
	s.setMemoryEnabled(false)
	if _, err := s.pub.SubscribeReserved(v4pub.SubscribeParams{
		ConnectionID:    "conn-1",
		DeliveryProfile: v4pub.ProfileContinuous,
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	// Take the initial snapshot frame so the test observes only retry deltas.
	rt.flushSubscribers(s)
	return s, rt.deps.Outbox.(*fakeOutbox)
}

// startRetryTurn emits the turn_started event the projection needs before it
// accepts model events: onModelNetworkStatus is gated on phase == running.
func startRetryTurn(s *session) {
	s.turnNumber = 1
	s.cmd = commandRef{ID: "cmd-1", ClientID: "rec-client"}
	s.appendEvent(sessionEvent{
		Type: evTurnStarted, TurnID: "turn_1", HasTurnID: true,
		Payload: turnStartedPayload(s, "do it", "msg_1", "cmd-1"),
	})
}

// runRetryStep drives one model step whose first statuses answers are failures.
func runRetryStep(t *testing.T, s *session) {
	t.Helper()
	step := s.runModelStep(context.Background(), "turn_1", "cmd-1", nil)
	if step == nil {
		t.Fatal("model step did not complete")
	}
}

// TestApiRetryAttemptCountsTheFailedAttempt pins the rule: for a retry
// lifecycle the delivered frames announce apiRetry exactly once per flush window
// and the announced attempt is the attempt that FAILED (not the upcoming one),
// with nextRetryAt set; the label is cleared when the turn succeeds.
//
// The statuses here all happen within one flush window (Retry-After: 0), so
// coalesce rule 2 keeps only the last apiRetry patch — attempt 2.
func TestApiRetryAttemptCountsTheFailedAttempt(t *testing.T) {
	rt, _ := newRetryRuntime(t, &retryTransport{statuses: []int{429, 429}})
	s, ob := newRetrySession(t, rt)
	startRetryTurn(s)
	runRetryStep(t, s)
	rt.flushSubscribers(s)

	frames := conversationDeltaFrames(ob)
	all := []map[string]any{}
	for _, f := range frames {
		all = append(all, apiRetryFromFrame(f)...)
	}
	if len(all) == 0 {
		t.Fatal("no apiRetry was ever delivered; the retry label would never show in the UI")
	}
	for i, r := range all {
		got := r["attempt"]
		if got != float64(2) {
			t.Errorf("apiRetry[%d].attempt = %v, want 2 (the attempt that failed, not the upcoming one)", i, got)
		}
		if r["reasonCode"] != "fault.provider.rateLimited" {
			t.Errorf("apiRetry[%d].reasonCode = %v, want fault.provider.rateLimited", i, r["reasonCode"])
		}
		if _, ok := r["nextRetryAt"]; !ok {
			t.Errorf("apiRetry[%d] has no nextRetryAt", i)
		}
		if r["maxAttempts"] != float64(11) {
			t.Errorf("apiRetry[%d].maxAttempts = %v, want 11", i, r["maxAttempts"])
		}
	}
}

// TestRetryStatusesShareOneFlushWindow is the regression this rule protects: two
// consecutive 429s must NOT each open their own frame. Before the fix this saw
// attempt 1 and attempt 2 as two separate apiRetry deltas.
func TestRetryStatusesShareOneFlushWindow(t *testing.T) {
	rt, _ := newRetryRuntime(t, &retryTransport{statuses: []int{429, 429}})
	s, ob := newRetrySession(t, rt)
	startRetryTurn(s)
	runRetryStep(t, s)
	rt.flushSubscribers(s)

	announcements := 0
	for _, f := range conversationDeltaFrames(ob) {
		if n := len(apiRetryFromFrame(f)); n > 0 {
			announcements += n
		}
	}
	if announcements != 1 {
		t.Fatalf("saw %d apiRetry deltas, want exactly 1: a retry lifecycle must land in one flush window", announcements)
	}
}

// TestApiRetryClearedOnSuccess pins the other half of the rule: the retry label
// does not survive a successful attempt.
func TestApiRetryClearedOnSuccess(t *testing.T) {
	rt, _ := newRetryRuntime(t, &retryTransport{statuses: []int{429}})
	s, ob := newRetrySession(t, rt)
	startRetryTurn(s)
	runRetryStep(t, s)
	rt.flushSubscribers(s)

	var sawRetry, sawClear bool
	for _, f := range conversationDeltaFrames(ob) {
		for _, d := range f.Payload.Deltas {
			if d.Op != "state.updated" {
				continue
			}
			raw, err := d.Raw.MarshalJSON()
			if err != nil {
				continue
			}
			var delta struct {
				Patch struct {
					Control *struct {
						APIRetry json.RawMessage `json:"apiRetry"`
					} `json:"control"`
				} `json:"patch"`
			}
			if json.Unmarshal(raw, &delta) != nil || delta.Patch.Control == nil {
				continue
			}
			switch string(delta.Patch.Control.APIRetry) {
			case "null":
				sawClear = true
			case "":
			default:
				sawRetry = true
			}
		}
	}
	if !sawRetry {
		t.Error("no apiRetry announced for the 429; the UI never shows the retrying state")
	}
	if !sawClear {
		t.Error("apiRetry was never cleared after the successful attempt")
	}
}

// TestFirstAttemptStartedFlushes pins the positive half: the flush that closes
// the frame is still driven by the first started status, so the recorded frame
// boundary (the turn's first deltas end at the started status) is preserved.
func TestFirstAttemptStartedFlushes(t *testing.T) {
	rt, _ := newRetryRuntime(t, &retryTransport{statuses: []int{429}})
	s, ob := newRetrySession(t, rt)
	s.appendNetworkStatus("turn_1", map[string]any{
		"type": "model_request_started", "attempt": 1,
	})
	if n := len(conversationDeltaFrames(ob)); n == 0 {
		t.Fatal("the first model_request_started did not flush; recorded frames close there")
	}

	// A retry attempt's started status must not open a frame of its own.
	rt2, _ := newRetryRuntime(t, &retryTransport{statuses: []int{429}})
	s2, ob2 := newRetrySession(t, rt2)
	s2.appendNetworkStatus("turn_1", map[string]any{
		"type": "model_request_started", "attempt": 2,
	})
	if n := len(conversationDeltaFrames(ob2)); n != 0 {
		t.Fatalf("a retry attempt's started status flushed %d frames, want 0", n)
	}
}

// conversationDeltaFrames pulls the delivered delta frames out of the outbox,
// keeping each frame's delta list so coalesce boundaries stay observable.
func conversationDeltaFrames(ob *fakeOutbox) []v4pub.Frame {
	var out []v4pub.Frame
	for _, msg := range ob.snapshot() {
		if msg.Method != "v4/conversation/frame" {
			continue
		}
		var wire struct {
			Frame struct {
				Payload struct {
					Kind   string            `json:"kind"`
					Deltas []json.RawMessage `json:"deltas"`
				} `json:"payload"`
			} `json:"frame"`
		}
		if json.Unmarshal(msg.Params, &wire) != nil {
			continue
		}
		if wire.Frame.Payload.Kind != "deltas" {
			continue
		}
		var deltas []v4pub.Delta
		for _, raw := range wire.Frame.Payload.Deltas {
			d, err := v4pub.ParseDelta(raw)
			if err != nil {
				continue
			}
			deltas = append(deltas, d)
		}
		out = append(out, v4pub.Frame{Payload: v4pub.FramePayload{Kind: "deltas", Deltas: deltas}})
	}
	return out
}
