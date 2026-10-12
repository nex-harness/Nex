package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/rpc/rpctest"
)

// This file is the client-side mirror of replay_test.go. The server-side replay (step 2.3)
// feeds the RECORDED CLIENT frames to a Go server and compares what comes back; it bypasses
// the Client entirely, so nothing there proves the Go client produces the frames the
// TypeScript ChannelClient produced.
//
// This file closes that gap from the other end: each scenario is driven through the real Go
// Client against a real Go server, and the client's OWN frames are compared with the
// recording byte for byte. Byte equality is the point — the recording came from the real TS
// client over the real TS codec (scripts/go-rewrite/record/record-rpc.mjs), so a match means
// the Go client is a drop-in for the TS one on the wire, not merely self-consistent.
//
// R01 is covered here too: a Go client waiting for Initialize is the same handshake R01
// recorded, and asserting the client does not answer before it is what makes it a mirror.

type clientReplayCase struct {
	id      string
	channel string
	// body is the request body the recorded client sent, verbatim. The scenarios are driven
	// through the internal `call` with this value because the TS client sends `arg` as-is:
	// a bare object for a single-argument call, not the argument array `Call` builds.
	body Value
	// run drives the scenario and receives tc so it can send tc.body.
	run func(t *testing.T, tc clientReplayCase, pair *replayPair)
	// wantRequests is how many request frames the recording contains for the scenario.
	wantRequests int
}

// TestClientReplayTraces covers R01-R08 and R11 through the Go client.
func TestClientReplayTraces(t *testing.T) {
	for _, tc := range clientReplayCases() {
		t.Run(tc.id, func(t *testing.T) { runClientReplay(t, tc) })
	}
}

func clientReplayCases() []clientReplayCase {
	return []clientReplayCase{
		// R01 is the handshake alone. The recording has no client frames; the mirror is that
		// a Go client also emits none — it consumes Initialize and sends nothing until asked.
		{id: "R01", channel: "", body: Undefined, wantRequests: 0,
			run: func(t *testing.T, _ clientReplayCase, pair *replayPair) {
				// Give the read loop time to consume Initialize; nothing should follow.
				time.Sleep(50 * time.Millisecond)
			}},
		{id: "R02", channel: "demo", body: NewObject("value", int32(7)), wantRequests: 1,
			run: func(t *testing.T, tc clientReplayCase, pair *replayPair) {
				result, err := pair.client.call(t.Context(), "demo", "echo", tc.body)
				if err != nil {
					t.Fatalf("Call: %v", err)
				}
				if !equalValues(result, NewObject("value", int32(7))) {
					t.Errorf("echo returned %v", result)
				}
			}},
		{id: "R03", channel: "demo", body: Undefined, wantRequests: 2,
			run: func(t *testing.T, _ clientReplayCase, pair *replayPair) {
				events, dispose, err := pair.client.Listen(t.Context(), "demo", "tick", Undefined)
				if err != nil {
					t.Fatalf("Listen: %v", err)
				}
				select {
				case event := <-events:
					if event != "hello-event" {
						t.Errorf("event = %v, want hello-event", event)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("no event arrived")
				}
				dispose()
			}},
		{id: "R04", channel: "slow", body: Undefined, wantRequests: 2,
			run: func(t *testing.T, tc clientReplayCase, pair *replayPair) {
				// Cancel after the request is on the wire: the recording has exactly this
				// pair of frames (100 then 101) and no response at all.
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					_, err := pair.client.call(ctx, "slow", "wait", tc.body)
					done <- err
				}()
				select {
				case <-pair.served:
				case <-time.After(2 * time.Second):
					t.Fatal("the call never reached the handler")
				}
				cancel()
				if err := <-done; err == nil {
					t.Fatal("a cancelled call returned no error")
				}
			}},
		{id: "R05", channel: "unregistered", body: Undefined, wantRequests: 1,
			run: func(t *testing.T, tc clientReplayCase, pair *replayPair) {
				_, err := pair.client.call(t.Context(), "unregistered", "nope", tc.body)
				if err == nil || !strings.Contains(err.Error(), "timed out after 300ms") {
					t.Fatalf("err = %v, want the recorded pending-queue timeout", err)
				}
			}},
		{id: "R06", channel: "binary", body: []byte{1, 2, 3, 255}, wantRequests: 1,
			run: func(t *testing.T, tc clientReplayCase, pair *replayPair) {
				result, err := pair.client.call(t.Context(), "binary", "reverse", tc.body)
				if err != nil {
					t.Fatalf("Call: %v", err)
				}
				got, ok := result.([]byte)
				if !ok || !bytes.Equal(got, []byte{255, 3, 2, 1}) {
					t.Errorf("reverse = %v (%T), want [255 3 2 1]", result, result)
				}
			}},
		{id: "R07", channel: "nested", body: NewObject("payload", []byte{9, 8, 7}, "name", "x"), wantRequests: 1,
			run: func(t *testing.T, tc clientReplayCase, pair *replayPair) {
				result, err := pair.client.call(t.Context(), "nested", "wrap", tc.body)
				if err != nil {
					t.Fatalf("Call: %v", err)
				}
				obj, ok := result.(Object)
				if !ok {
					t.Fatalf("wrap returned %T, want Object", result)
				}
				payload, _ := obj.Get("payload")
				got, ok := payload.([]byte)
				if !ok || !bytes.Equal(got, []byte{9, 8, 7}) {
					t.Errorf("payload = %v (%T), want [9 8 7]", payload, payload)
				}
			}},
		{id: "R08", channel: "failing", body: Undefined, wantRequests: 1,
			run: func(t *testing.T, tc clientReplayCase, pair *replayPair) {
				_, err := pair.client.call(t.Context(), "failing", "boom", tc.body)
				rpcErr, ok := AsError(err)
				if !ok {
					t.Fatalf("err is %T (%v), want *rpc.Error", err, err)
				}
				// The recorded payload carries exactly these; `code` is the key the recorder's
				// own scenario asserts on. The stack is NOT asserted: the recording's frames
				// are Node stack traces, which no Go handler can reproduce — the same reason
				// the server-side replay drops stack CONTENTS.
				if rpcErr.Code != "E_EXPLODE" {
					t.Errorf("Code = %v, want E_EXPLODE", rpcErr.Code)
				}
				if !equalValues(rpcErr.RetryAfterMs, int32(42)) {
					t.Errorf("RetryAfterMs = %v, want 42", rpcErr.RetryAfterMs)
				}
				if rpcErr.Name != "ExplodedError" || rpcErr.Message != "channel exploded" {
					t.Errorf("name/message = %q/%q", rpcErr.Name, rpcErr.Message)
				}
			}},
		{id: "R11", channel: "subscribe-shaped", body: NewObject(
			"topic", "conversation/abc",
			"connectionId", "conn-1",
			"clientMode", "desktop-continuous",
			"workspace", NewObject("workspacePath", "/w", "workspaceKey", "/w"),
		), wantRequests: 1,
			run: func(t *testing.T, tc clientReplayCase, pair *replayPair) {
				result, err := pair.client.call(t.Context(), "subscribe-shaped", "subscribe", tc.body)
				if err != nil {
					t.Fatalf("Call: %v", err)
				}
				ack, ok := result.(Object)
				if !ok {
					t.Fatalf("subscribe returned %T, want Object", result)
				}
				inner, ok := ack.Get("ack")
				if !ok {
					t.Fatalf("no ack member in %v", ack)
				}
				if !equalValues(inner, NewObject("subscriptionId", "s1", "mode", "snapshot", "logEpoch", "e1")) {
					t.Errorf("ack = %v", inner)
				}
			}},
	}
}

// runClientReplay drives one scenario and compares the client's frames with the recording.
func runClientReplay(t *testing.T, tc clientReplayCase) {
	t.Helper()
	frames := readTrace(t, tc.id)

	var recorded []string
	for _, f := range frames {
		if f.Direction == "client-to-server" {
			recorded = append(recorded, f.Hex)
		}
	}
	if len(recorded) != tc.wantRequests {
		t.Fatalf("%s: recording has %d client frames, the case expects %d",
			tc.id, len(recorded), tc.wantRequests)
	}

	pair := newReplayClientPair(t, tc)
	defer pair.Close()
	tc.run(t, tc, pair)

	// The frames the client actually wrote, as payload hex — the recording stores payload
	// hex too, so this is a direct compare. The wait is for the EXPECTED count: R03's dispose
	// and R04's cancel are written after the scenario's own work returns, so a shorter wait
	// would race the client's writer rather than test anything.
	got := pair.takePayloads(len(recorded), 3*time.Second)
	if len(got) != len(recorded) {
		t.Fatalf("%s: the client wrote %d frames, the recording has %d\n  go: %v",
			tc.id, len(got), len(recorded), got)
	}
	for i := range recorded {
		if got[i] == recorded[i] {
			continue
		}
		t.Errorf("%s: request frame %d differs\n  recorded: %s\n  go:       %s",
			tc.id, i, recorded[i], got[i])
	}
}

// --- driver --------------------------------------------------------------------------

// replayPair is a Client and a Server joined by an in-memory socket whose client's writes are
// recorded. served is signalled the first time a Promise handler runs, which is how R04 knows
// its request is on the wire before it cancels.
type replayPair struct {
	client   *Client
	server   *Server
	recorder *rpctest.Recorder
	served   chan struct{}
}

// newReplayClientPair wires the stub services from record-rpc.mjs — the ones that produced
// the recordings — behind a Go server, with a Go client on the other side. R05's channel stays
// unregistered, as in the recording.
func newReplayClientPair(t *testing.T, tc clientReplayCase) *replayPair {
	t.Helper()
	pair := rpctest.NewPair()
	ctx := t.Context()
	server := NewServer(pair.A, ctx, Options{TimeoutDelayMS: 300})

	served := make(chan struct{}, 1)
	markServed := func() {
		select {
		case served <- struct{}{}:
		default:
		}
	}

	// R05 and R01 register nothing on purpose: R05's whole point is a call for a channel the
	// server never registers, and R01 is the handshake with no request at all.
	if tc.channel != "" && tc.channel != "unregistered" {
		server.RegisterChannel(tc.channel, &stubChannel{
			callFunc: func(callCtx context.Context, method string, args []Value) (Value, error) {
				markServed()
				return replayCallResult(tc.channel, method, args)
			},
			listenFunc: func(context.Context, string, Value) (<-chan Value, func(), error) {
				// The recorded scenario fires one event and disposes; firing without a delay
				// keeps the ordering deterministic.
				out := make(chan Value, 1)
				out <- "hello-event"
				close(out)
				return out, func() {}, nil
			},
		})
	}

	go func() { _ = server.Serve(ctx) }()
	recorder := rpctest.Record(pair.B)
	return &replayPair{client: NewClient(recorder), server: server, recorder: recorder, served: served}
}

func (p *replayPair) Close() {
	p.client.Close()
	p.server.Close()
}

// takePayloads returns the client's request frames as payload hex, waiting up to timeout for
// `want` of them to appear. Re-encoding what was written makes the comparison with the
// recording a plain string equality, which is what a byte-level difference in the codec shows
// up as.
func (p *replayPair) takePayloads(want int, timeout time.Duration) []string {
	deadline := time.Now().Add(timeout)
	var out []string
	for {
		for _, payload := range p.recorder.Payloads(0) {
			out = append(out, hex.EncodeToString(payload))
		}
		if len(out) >= want || !time.Now().Before(deadline) {
			return out
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// replayCallResult reproduces the stub services in record-rpc.mjs. They are the services that
// produced the recordings, so a Go client replaying a scenario sees the same answers the TS
// client saw.
func replayCallResult(channel, method string, args []Value) (Value, error) {
	switch channel + "." + method {
	case "demo.echo":
		if len(args) == 1 {
			return args[0], nil
		}
		return Undefined, nil
	case "slow.wait":
		<-time.After(5 * time.Second)
		return "too late", nil
	case "binary.reverse":
		in, ok := args[0].([]byte)
		if !ok {
			return Undefined, nil
		}
		out := make([]byte, len(in))
		for i := range in {
			out[i] = in[len(in)-1-i]
		}
		return out, nil
	case "nested.wrap":
		obj, _ := args[0].(Object)
		payload, _ := obj.Get("payload")
		name, _ := obj.Get("name")
		return NewObject("payload", payload, "name", name), nil
	case "failing.boom":
		return nil, &Error{
			Name:         "ExplodedError",
			Message:      "channel exploded",
			Code:         "E_EXPLODE",
			RetryAfterMs: int32(42),
		}
	case "subscribe-shaped.subscribe":
		return NewObject("ack", NewObject(
			"subscriptionId", "s1",
			"mode", "snapshot",
			"logEpoch", "e1",
		)), nil
	}
	return nil, &Error{Message: "Method not found: " + method}
}
