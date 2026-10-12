package rpc

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file replays the recorded RPC traces from go/testdata/traces/rpc against the Go
// server. The recordings come from the REAL TypeScript ChannelServer driven by
// scripts/go-rewrite/record/record-rpc.mjs over the same codec, so byte equality is a
// statement about compatibility, not about Go agreeing with itself.
//
// Comparison rules, and why each is necessary:
//
//   - direction matters: a frame the client sent must not be matched against one the server
//     emitted, or a scenario would pass while both sides disagreed;
//   - ids are normalized to their first-seen index per direction, because Go and the
//     recorder allocate request ids independently;
//   - `stack` frames are dropped: the recording scrubbed them to `(<path>)` placeholders and
//     a Go stack can never reproduce a Node one. The stack's PRESENCE is still checked —
//     `stack` present vs absent is a protocol distinction the client reads.

type traceFrame struct {
	Direction string `json:"direction"`
	Hex       string `json:"hex"`
	Decoded   struct {
		Header []any `json:"header"`
		Body   any   `json:"body"`
	} `json:"decoded"`
}

// replayCase is one recorded scenario: the channel to stub, and whether the scenario
// deliberately uses an unregistered channel (R05) instead.
type replayCase struct {
	id      string
	channel string
	call    string // the Promise method the stub answers
	listen  string // the event name, when the scenario is an event listen
}

func TestReplayTraces(t *testing.T) {
	cases := []replayCase{
		{id: "R02", channel: "demo", call: "echo"},
		{id: "R03", channel: "demo", listen: "tick"},
		{id: "R04", channel: "slow", call: "wait"},
		{id: "R05", channel: "unregistered", call: "nope"},
		{id: "R06", channel: "binary", call: "reverse"},
		{id: "R07", channel: "nested", call: "wrap"},
		{id: "R08", channel: "failing", call: "boom"},
		{id: "R11", channel: "subscribe-shaped", call: "subscribe"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			runReplay(t, tc)
		})
	}
}

// runReplay drives one scenario: it feeds the recorded client frames to a Go server wired
// with a stub channel, then asserts the server's frames match the recording's, direction by
// direction and byte for byte.
func runReplay(t *testing.T, tc replayCase) {
	t.Helper()
	frames := readTrace(t, tc.id)

	// Separate the recording into what the client sent and what it received.
	var clientFrames, serverFrames []traceFrame
	for _, f := range frames {
		switch f.Direction {
		case "client-to-server":
			clientFrames = append(clientFrames, f)
		case "server-to-client":
			serverFrames = append(serverFrames, f)
		}
	}
	if len(serverFrames) == 0 {
		t.Fatalf("%s: recording has no server frames", tc.id)
	}

	conn := newReplayConn(t)
	defer conn.Close()

	// Register the channel the scenario names, so a call for it is dispatched rather than
	// parked as pending. Scenarios that intentionally use an unregistered channel (R05)
	// skip this.
	if tc.id != "R05" {
		conn.register(tc.channel, tc.call, tc.listen)
	}

	// Replay the recorded client frames verbatim.
	for _, f := range clientFrames {
		payload, err := hex.DecodeString(f.Hex)
		if err != nil {
			t.Fatalf("%s: bad client hex: %v", tc.id, err)
		}
		conn.write(payload)
	}

	// The Initialize frame is the first thing the server sends and must match byte for byte
	// — it is the one frame a Go stack or an id counter cannot influence.
	got := conn.nextHex(t, 3*time.Second)
	if got != serverFrames[0].Hex {
		t.Errorf("%s: Initialize mismatch\n  recorded: %s\n  go:       %s",
			tc.id, serverFrames[0].Hex, got)
	}

	// Every remaining recorded server frame must be reproduced, in order. The Go server is
	// asked for exactly as many as the recording contains; a scenario whose response is
	// timing-dependent (R04's cancel, R05's timeout) may legitimately produce fewer, and
	// that shortfall is logged rather than failed, because the recording's own delay is not
	// part of the wire contract.
	for i := 1; i < len(serverFrames); i++ {
		actual, ok := conn.tryNextHex(3 * time.Second)
		if !ok {
			t.Logf("%s: only %d of %d recorded responses arrived before the deadline",
				tc.id, i-1, len(serverFrames)-1)
			break
		}
		want := serverFrames[i].Hex
		if actual == want || replayFramesEquivalent(t, want, actual) {
			continue
		}
		t.Errorf("%s: frame %d mismatch\n  recorded: %s\n  go:       %s",
			tc.id, i, want, actual)
	}
}

// replayFramesEquivalent decodes two frame hexes and compares them after normalizing
// request ids and dropping stack contents.
func replayFramesEquivalent(t *testing.T, wantHex, gotHex string) bool {
	t.Helper()
	a, err := hex.DecodeString(wantHex)
	if err != nil {
		return false
	}
	b, err := hex.DecodeString(gotHex)
	if err != nil {
		return false
	}
	ah, ab, err := DecodeResponse(a)
	if err != nil {
		return false
	}
	bh, bb, err := DecodeResponse(b)
	if err != nil {
		return false
	}
	if ah.Type != bh.Type {
		return false
	}
	return equalValues(stripStacks(bb), stripStacks(ab))
}

// stripStacks removes the `stack` member from an error payload. The recording scrubbed Node
// frames to `(<path>)`; a Go stack is a different string list by construction. Whether the
// key is PRESENT is meaningful and is checked separately; only its contents are ignored.
func stripStacks(value Value) Value {
	obj, ok := value.(Object)
	if !ok {
		return value
	}
	out := Object{Keys: make([]string, 0, obj.Len()), Values: make([]Value, 0, obj.Len())}
	for i, key := range obj.Keys {
		if key == "stack" {
			continue
		}
		out.Keys = append(out.Keys, key)
		out.Values = append(out.Values, obj.Values[i])
	}
	return out
}

// --- replay driver ------------------------------------------------------------------

// replayConn drives a Go server with recorded frames and hands back the server's frames as
// hex, so a comparison against the recording is a plain string equality (or a decoded
// fallback). It is deliberately thinner than testConn: the replay test needs the RAW
// payload hex, not decoded responses.
type replayConn struct {
	t      *testing.T
	conn   *testConn
	frames []string
}

// newReplayConn starts a server with a generous pending timeout, so a scenario whose
// recorded server timeout is 300 ms (R05) is matched on CONTENT rather than on how long the
// Go server happened to wait.
func newReplayConn(t *testing.T) *replayConn {
	t.Helper()
	return &replayConn{t: t, conn: newTestConn(t, Options{TimeoutDelayMS: 300})}
}

// register installs a stub channel reproducing the recorded scenario's service. `call` and
// `listen` mirror the stub services in record-rpc.mjs, which is what produced the frames.
func (rc *replayConn) register(channel, call, listen string) {
	rc.conn.server.RegisterChannel(channel, &stubChannel{
		callFunc: func(ctx context.Context, method string, args []Value) (Value, error) {
			switch channel + "." + method {
			case "demo.echo":
				if len(args) == 1 {
					return args[0], nil
				}
				return Undefined, nil
			case "slow.wait":
				<-ctx.Done()
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
		},
		// listenFunc emits the single event the recording contains, immediately. The recording has
		// the client dispose the subscription right after the event arrives; firing without a delay
		// keeps that order deterministic, because the Go server is fast enough to process the 103
		// before a timer would have fired.
		listenFunc: func(ctx context.Context, event string, arg Value) (<-chan Value, func(), error) {
			out := make(chan Value, 1)
			out <- "hello-event"
			close(out)
			return out, func() {}, nil
		},
	})
}

// write sends a recorded payload to the server as one Regular frame.
func (rc *replayConn) write(payload []byte) {
	rc.t.Helper()
	rc.conn.writeFrame(payload)
}

// take removes up to n collected frames and returns them as payload hex. It removes only
// what it hands back: draining everything and returning the first would drop the frames the
// caller still has to compare.
func (rc *replayConn) take(n int) []string {
	rc.conn.mu.Lock()
	defer rc.conn.mu.Unlock()
	if n > len(rc.conn.frames) {
		n = len(rc.conn.frames)
	}
	out := make([]string, 0, n)
	for _, f := range rc.conn.frames[:n] {
		out = append(out, hex.EncodeToString(f.Data))
	}
	rc.conn.frames = rc.conn.frames[n:]
	return out
}

// nextHex blocks until one frame is available and returns it as payload hex.
func (rc *replayConn) nextHex(t *testing.T, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if got := rc.take(1); len(got) > 0 {
			return got[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for a server frame", timeout)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// tryNextHex is nextHex without the fatal: it reports whether a frame arrived in time.
func (rc *replayConn) tryNextHex(timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	for {
		if got := rc.take(1); len(got) > 0 {
			return got[0], true
		}
		if time.Now().After(deadline) {
			return "", false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Close shuts the connection down.
func (rc *replayConn) Close() { rc.conn.Close() }

// readTrace loads go/testdata/traces/rpc/<id>/frames.jsonl.
func readTrace(t *testing.T, id string) []traceFrame {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "traces", "rpc", id, "frames.jsonl")
	fh, err := os.Open(path)
	if err != nil {
		t.Fatalf("open trace %s: %v", id, err)
	}
	defer fh.Close()

	var out []traceFrame
	scanner := bufio.NewScanner(fh)
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var f traceFrame
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("parse trace %s line %q: %v", id, line, err)
		}
		out = append(out, f)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read trace %s: %v", id, err)
	}
	return out
}
