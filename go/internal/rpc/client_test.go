package rpc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/rpc/rpctest"
)

// This file drives a real Client against a real Server over the in-memory socket pair, so
// both directions use the production framing and codec and only the transport is faked.
//
// The client's own writes are collected (nothing in the client reads its write side), which
// is what lets a test assert on the frames it caused — the cancel and dispose cases are about
// those frames, not just about the error the caller sees.

// TestClientCallRoundTrip exercises one call in every shape the codec distinguishes, so the
// round trip is checked at the tag level rather than only for a string.
func TestClientCallRoundTrip(t *testing.T) {
	pair := newClientPair(t, func(s *Server) {
		s.RegisterChannel("demo", &stubChannel{
			callFunc: func(_ context.Context, method string, args []Value) (Value, error) {
				if method != "echo" {
					return nil, &Error{Message: "Method not found: " + method}
				}
				if len(args) != 1 {
					return Undefined, nil
				}
				return args[0], nil
			},
		})
	})

	echoed, err := pair.client.Call(t.Context(), "demo", "echo", []Value{
		NewObject("payload", []byte{1, 2, 255}, "name", "x", "count", int32(7)),
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	obj, ok := echoed.(Object)
	if !ok {
		t.Fatalf("echo returned %T, want Object", echoed)
	}
	payload, ok := obj.Get("payload")
	if !ok {
		t.Fatal("nested Uint8Array did not survive the round trip")
	}
	bytes, ok := payload.([]byte)
	if !ok || len(bytes) != 3 || bytes[2] != 255 {
		t.Errorf("nested bytes = %v, want [1 2 255]", payload)
	}
	if name, _ := obj.Get("name"); name != "x" {
		t.Errorf("name = %v, want x", name)
	}
	if !equalValues(mustGet(obj, "count"), int32(7)) {
		t.Errorf("count = %v, want 7", mustGet(obj, "count"))
	}

	// A top-level binary and a float-ish value round-trip through their own tags.
	raw, err := pair.client.Call(t.Context(), "demo", "echo", []Value{[]byte{9, 8, 7}})
	if err != nil {
		t.Fatalf("Call with binary: %v", err)
	}
	if got, ok := raw.([]byte); !ok || len(got) != 3 || got[0] != 9 {
		t.Errorf("binary echo = %v (%T), want [9 8 7]", raw, raw)
	}
}

// TestClientCallPromiseErrorRebuildsError pins the passthrough contract: the TS client
// copies nine keys from the PromiseError payload onto the rebuilt Error, and each one is
// application state the caller cannot reconstruct.
func TestClientCallPromiseErrorRebuildsError(t *testing.T) {
	pair := newClientPair(t, func(s *Server) {
		s.RegisterChannel("failing", &stubChannel{
			callFunc: func(context.Context, string, []Value) (Value, error) {
				return nil, &Error{
					Name:         "ExplodedError",
					Message:      "channel exploded",
					Stack:        []string{"ExplodedError: channel exploded", "    at makeStructuredError"},
					Code:         "E_EXPLODE",
					Kind:         "transport",
					Status:       int32(503),
					RetryAfterMs: int32(42),
					Data:         NewObject("attempt", int32(2)),
					Detail:       "upstream refused",
					Details:      []Value{"a", "b"},
					TaskID:       "task-1",
					TraceID:      "trace-1",
				}
			},
		})
	})

	_, err := pair.client.Call(t.Context(), "failing", "boom", nil)
	rpcErr, ok := AsError(err)
	if !ok {
		t.Fatalf("error is %T (%v), want *rpc.Error", err, err)
	}
	if rpcErr.Name != "ExplodedError" || rpcErr.Message != "channel exploded" {
		t.Errorf("name/message = %q/%q, want ExplodedError/channel exploded", rpcErr.Name, rpcErr.Message)
	}
	if len(rpcErr.Stack) != 2 || rpcErr.Stack[0] != "ExplodedError: channel exploded" {
		t.Errorf("stack = %v, want the two frames the handler sent", rpcErr.Stack)
	}
	if rpcErr.Code != "E_EXPLODE" {
		t.Errorf("Code = %v, want E_EXPLODE", rpcErr.Code)
	}
	if rpcErr.Kind != "transport" {
		t.Errorf("Kind = %v, want transport", rpcErr.Kind)
	}
	if !equalValues(rpcErr.Status, int32(503)) {
		t.Errorf("Status = %v, want 503", rpcErr.Status)
	}
	if !equalValues(rpcErr.RetryAfterMs, int32(42)) {
		t.Errorf("RetryAfterMs = %v, want 42", rpcErr.RetryAfterMs)
	}
	data, ok := rpcErr.Data.(Object)
	if !ok || data.Len() != 1 || !equalValues(data.Values[0], int32(2)) {
		t.Errorf("Data = %v, want {attempt: 2}", rpcErr.Data)
	}
	if rpcErr.Detail != "upstream refused" {
		t.Errorf("Detail = %v", rpcErr.Detail)
	}
	if details, ok := rpcErr.Details.([]Value); !ok || len(details) != 2 {
		t.Errorf("Details = %v, want [a b]", rpcErr.Details)
	}
	if rpcErr.TaskID != "task-1" || rpcErr.TraceID != "trace-1" {
		t.Errorf("TaskID/TraceID = %v/%v, want task-1/trace-1", rpcErr.TaskID, rpcErr.TraceID)
	}

	// The rebuilt error must re-serialize to the payload the server sent, in the TS member
	// ORDER. A client that rebuilt a plain map would pass every field check above and still
	// produce a different byte sequence on the wire.
	want := NewObject(
		"message", "channel exploded",
		"name", "ExplodedError",
		"stack", []Value{"ExplodedError: channel exploded", "    at makeStructuredError"},
		"code", "E_EXPLODE",
		"kind", "transport",
		"status", int32(503),
		"retryAfterMs", int32(42),
		"data", NewObject("attempt", int32(2)),
		"detail", "upstream refused",
		"details", []Value{"a", "b"},
		"taskId", "task-1",
		"traceId", "trace-1",
	)
	if !equalValues(rpcErr.payload(), want) {
		t.Errorf("re-serialized payload = %v, want %v", rpcErr.payload(), want)
	}
}

// TestClientCallRawErrorDeliversRawValue covers the 203 path: a rejection that is not an
// Error arrives verbatim and must NOT be reshaped into a structured one.
func TestClientCallRawErrorDeliversRawValue(t *testing.T) {
	pair := newClientPair(t, func(s *Server) {
		s.RegisterChannel("raw", &stubChannel{
			callFunc: func(context.Context, string, []Value) (Value, error) {
				return nil, &RawError{Value: NewObject("reason", "plain-object-rejection")}
			},
		})
	})

	_, err := pair.client.Call(t.Context(), "raw", "reject", nil)
	var raw *RawError
	if !errors.As(err, &raw) {
		t.Fatalf("error is %T (%v), want *RawError", err, err)
	}
	obj, ok := raw.Value.(Object)
	if !ok || obj.Len() != 1 {
		t.Fatalf("raw value = %v, want the object the handler threw", raw.Value)
	}
	if reason, _ := obj.Get("reason"); reason != "plain-object-rejection" {
		t.Errorf("reason = %v", reason)
	}
	if _, isStructured := AsError(err); isStructured {
		t.Error("a 203 rejection must not masquerade as a structured *Error")
	}
}

func TestClientCallUnknownChannelAndMethod(t *testing.T) {
	pair := newClientPair(t, func(s *Server) {
		// A stub with no callFunc answers every method with `Method not found: <name>`,
		// which is what ProxyChannel.fromService throws for an unknown key.
		s.RegisterChannel("demo", &stubChannel{})
	})

	t.Run("unknown method", func(t *testing.T) {
		_, err := pair.client.Call(t.Context(), "demo", "nope", nil)
		if err == nil || !strings.Contains(err.Error(), "Method not found: nope") {
			t.Fatalf("err = %v, want the proxy-channel's Method not found", err)
		}
		if _, ok := AsError(err); !ok {
			t.Errorf("err is %T, want a structured *Error so callers can read Name", err)
		}
	})

	t.Run("unknown channel", func(t *testing.T) {
		// The server parks the request and rejects it when the pending timer fires; the
		// client must surface that rather than hang forever.
		_, err := pair.client.Call(t.Context(), "unregistered", "nope", nil)
		if err == nil {
			t.Fatal("call on an unregistered channel succeeded")
		}
		if !strings.Contains(err.Error(), "timed out after") {
			t.Fatalf("err = %v, want the pending-queue timeout", err)
		}
	})
}

// TestClientCallCancelBeforeStartSendsNothing pins the early cancellation check: the TS
// client rejects before anything reaches the socket, and it still consumes a request id —
// `lastRequestId++` runs ahead of the check, and the id sequence is visible in the recorded
// frames.
func TestClientCallCancelBeforeStartSendsNothing(t *testing.T) {
	pair := newClientPair(t, func(s *Server) {
		s.RegisterChannel("demo", &stubChannel{
			callFunc: func(context.Context, string, []Value) (Value, error) {
				t.Error("a call cancelled before it started reached the handler")
				return Undefined, nil
			},
		})
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := pair.client.Call(ctx, "demo", "echo", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if frames := pair.takeRequestsN(0, 100*time.Millisecond); len(frames) != 0 {
		t.Fatalf("client sent %d frame(s) for a pre-cancelled call, want 0", len(frames))
	}
	if got := pair.client.NextID(); got != 1 {
		t.Fatalf("next id = %d, want 1: the cancelled call must still consume id 0", got)
	}
}

// TestClientCallCancelMidFlight is the mid-flight half: the caller is released within 500 ms,
// the handler on the far side observes the cancellation within the same budget, and the 101
// is really on the wire (the caller's return alone would not prove the server was told).
func TestClientCallCancelMidFlight(t *testing.T) {
	entered := make(chan struct{}, 1)
	observed := make(chan error, 1)
	pair := newClientPair(t, func(s *Server) {
		s.RegisterChannel("slow", &stubChannel{
			callFunc: func(ctx context.Context, _ string, _ []Value) (Value, error) {
				entered <- struct{}{}
				<-ctx.Done()
				observed <- ctx.Err()
				return nil, ctx.Err()
			},
		})
	})

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := pair.client.Call(ctx, "slow", "wait", nil)
		result <- err
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the call never reached the handler")
	}

	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("the client did not report cancellation within 500ms")
	}
	select {
	case err := <-observed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handler observed %v, want context.Canceled", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("the server handler was still running 500ms after the cancel")
	}

	frames := pair.takeRequestsN(2, 2*time.Second)
	if len(frames) != 2 {
		t.Fatalf("client sent %d frames, want the 100 and the 101", len(frames))
	}
	if frames[0].Type != RequestPromise || frames[1].Type != RequestPromiseCancel || frames[1].ID != frames[0].ID {
		t.Errorf("frames = %+v, want a Promise (100) then a PromiseCancel (101) for the same id", frames)
	}
}

// TestClientListenDeliversAndDisposes covers the event path end to end: values arrive in
// order, dispose sends the 103, the consumer's channel is closed, and dispose is idempotent.
func TestClientListenDeliversAndDisposes(t *testing.T) {
	unsubscribed := make(chan struct{})
	pair := newClientPair(t, func(s *Server) {
		s.RegisterChannel("ticker", &stubChannel{
			listenFunc: func(_ context.Context, _ string, _ Value) (<-chan Value, func(), error) {
				out := make(chan Value)
				go func() {
					defer close(out)
					for i := int32(0); ; i++ {
						select {
						case out <- NewObject("tick", i):
						case <-unsubscribed:
							return
						}
					}
				}()
				return out, func() { close(unsubscribed) }, nil
			},
		})
	})

	events, dispose, err := pair.client.Listen(t.Context(), "ticker", "tick", Undefined)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	for i := int32(0); i < 3; i++ {
		select {
		case event := <-events:
			obj, ok := event.(Object)
			if !ok {
				t.Fatalf("event %d is %T, want Object", i, event)
			}
			if tick, _ := obj.Get("tick"); !equalValues(tick, i) {
				t.Fatalf("event %d tick = %v, want %d (events must stay in order)", i, tick, i)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("event %d never arrived", i)
		}
	}

	dispose()

	// The channel must close, or a consumer ranging over it would hang forever. Go does not
	// discard a buffered channel's values on close, so anything already delivered is still
	// readable here — but the count is not the assertion; the close is.
	if count, closed := drainEvents(events, 2*time.Second); !closed {
		t.Errorf("the event channel was still open after dispose (%d more events buffered)", count)
	}
	select {
	case <-unsubscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("the 103 never reached the channel's unsubscribe")
	}

	dispose() // idempotent: no second 103
	// Wait for both frames: the dispose is written through the shared writer, so under load
	// it can still be queued when the unsubscribe has already been observed server-side
	// (the 103 is a buffered send the reader picks up immediately). Asserting on a fixed
	// sleep would make this test flaky rather than strict.
	frames := pair.takeRequestsN(2, 2*time.Second)
	disposes, listens := 0, 0
	for _, f := range frames {
		switch f.Type {
		case RequestEventDispose:
			disposes++
		case RequestEventListen:
			listens++
		}
	}
	if listens != 1 || disposes != 1 {
		t.Errorf("client sent %d listen and %d dispose frames, want 1 and 1", listens, disposes)
	}
}

// TestClientListenDisposeBeforeInitializeSendsNothing is the race the TS emitter's
// onWillAddFirstListener defers through: a listener disposed before the handshake must not
// leave a request on the wire for a subscription nobody owns.
func TestClientListenDisposeBeforeInitializeSendsNothing(t *testing.T) {
	pair := rpctest.NewPair()
	ctx := t.Context()
	server := NewServer(pair.A, ctx, Options{DeferInit: true})
	defer server.Close()
	go func() { _ = server.Serve(ctx) }()

	// The recorder wraps the client's endpoint, so it sees what the client writes while the
	// client keeps the read side to itself.
	writes := rpctest.Record(pair.B)
	client := NewClient(writes)
	defer client.Close()

	_, dispose, err := client.Listen(ctx, "demo", "tick", Undefined)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	dispose()

	// Even after the handshake arrives, a disposed listener must never send its EventListen.
	// A 103 IS expected — dispose always tells the server to drop the subscription, exactly
	// like the TS emitter's onDidRemoveLastListener — but without the 102 there is no
	// subscription for it to refer to.
	server.Ready()
	for _, payload := range writes.Payloads(150 * time.Millisecond) {
		header, _, err := DecodeRequest(payload)
		if err != nil {
			t.Fatalf("the client wrote an undecodable frame: %v", err)
		}
		if header.Type == RequestEventListen {
			t.Fatalf("a disposed listener sent its EventListen (id %d)", header.ID)
		}
	}
}

// TestClientCallWaitsForInitialize proves the deferral: with the server holding Initialize
// back, a call must not complete and must not reach the handler until the handshake arrives.
func TestClientCallWaitsForInitialize(t *testing.T) {
	pair := rpctest.NewPair()
	ctx := t.Context()
	server := NewServer(pair.A, ctx, Options{DeferInit: true})
	defer server.Close()
	go func() { _ = server.Serve(ctx) }()

	entered := make(chan struct{}, 1)
	server.RegisterChannel("demo", &stubChannel{
		callFunc: func(context.Context, string, []Value) (Value, error) {
			entered <- struct{}{}
			return "served", nil
		},
	})

	client := NewClient(rpctest.Record(pair.B))
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		_, err := client.Call(ctx, "demo", "echo", nil)
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("the call completed before Initialize: %v", err)
	case <-entered:
		t.Fatal("the request reached the handler before Initialize")
	case <-time.After(150 * time.Millisecond):
	}

	server.Ready()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("call after Initialize: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the call did not proceed after Initialize")
	}
}

// TestClientListenerOverflowDisposesSubscription pins the client half of the bounded-buffer
// policy: a consumer that falls more than EventBufferSize behind loses its subscription, not
// the connection. The read loop must never block on a slow consumer, so it cannot wait for
// room; it disposes the listener and closes the channel instead, and the rest of the
// connection keeps working.
func TestClientListenerOverflowDisposesSubscription(t *testing.T) {
	release := make(chan struct{})
	pair := newClientPair(t, func(s *Server) {
		s.RegisterChannel("flood", &stubChannel{
			listenFunc: func(_ context.Context, _ string, _ Value) (<-chan Value, func(), error) {
				out := make(chan Value)
				go func() {
					defer close(out)
					for i := int32(0); ; i++ {
						select {
						case out <- i:
						case <-release:
							return
						}
					}
				}()
				return out, func() {}, nil
			},
		})
	})
	defer close(release)

	events, _, err := pair.client.Listen(t.Context(), "flood", "tick", Undefined)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	// Read nothing: the buffer fills and the overflow path takes over.
	deadline := time.Now().Add(5 * time.Second)
	for pair.client.listenerCount() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := pair.client.listenerCount(); got != 0 {
		t.Fatalf("%d listener(s) still registered after overflow; the subscription was not disposed", got)
	}

	// The consumer's channel closes rather than silently stopping, so a `range` over it ends.
	for range events { //nolint:revive // draining until close is the assertion
	}

	// And the connection survives: a second call still works.
	if _, err := pair.client.Call(t.Context(), "demo", "echo", nil); err == nil {
		t.Error("the flood channel was registered, so the follow-up call should have failed")
	} else if !strings.Contains(err.Error(), "timed out after") {
		t.Errorf("the connection did not survive the overflow: %v", err)
	}
}

// TestClientCloseReleasesCallers is the fail-closed contract the TS client's dispose
// implements: when a connection ends, every request already sent — and every one still parked
// on Initialize — must fail. A request left waiting would hold whatever in-flight bookkeeping
// its caller has, forever.
func TestClientCloseReleasesCallers(t *testing.T) {
	pair := newClientPair(t, func(s *Server) {
		s.RegisterChannel("slow", &stubChannel{
			callFunc: func(ctx context.Context, _ string, _ []Value) (Value, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		})
	})

	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := pair.client.Call(context.Background(), "slow", "wait", nil)
			results <- err
		}()
	}
	// Let both requests reach the handler before the teardown.
	time.Sleep(100 * time.Millisecond)

	pair.client.Close()

	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, ErrConnectionClosed) {
				t.Errorf("call %d returned %v, want ErrConnectionClosed", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("call %d was never released by Close", i)
		}
	}

	// A call made after Close is rejected without touching the socket: it never reached the
	// wire, so reporting it as a connection failure would misdescribe what happened.
	if _, err := pair.client.Call(t.Context(), "slow", "wait", nil); !errors.Is(err, ErrClientClosed) {
		t.Errorf("a call after Close returned %v, want ErrClientClosed", err)
	}

	// Close is idempotent, and the read loop really stopped.
	pair.client.Close()
	select {
	case <-pair.client.Done():
	case <-time.After(2 * time.Second):
		t.Error("the read loop did not stop")
	}
}

// --- driver --------------------------------------------------------------------------

// clientPair is a Client and a Server joined by an in-memory socket, plus a record of the
// frames the client wrote.
type clientPair struct {
	t        *testing.T
	client   *Client
	server   *Server
	requests *rpctest.Recorder
}

// newClientPair starts a Go server on one side of an in-memory socket pair and a Go client on
// the other. register runs between them, which is how each test wires its channels.
//
// The client's endpoint is wrapped in a Recorder rather than read by a collector: the client
// already owns the read side of its socket, and a second reader would steal the server's
// responses instead of observing the client's requests. The pair's pending timeout is 300 ms
// so an unknown-channel rejection is observable without slowing the suite to the protocol's
// 1 s default.
func newClientPair(t *testing.T, register func(*Server)) *clientPair {
	t.Helper()
	pair := rpctest.NewPair()
	ctx := t.Context()

	server := NewServer(pair.A, ctx, Options{TimeoutDelayMS: 300})
	register(server)
	go func() { _ = server.Serve(ctx) }()

	recorder := rpctest.Record(pair.B)
	client := NewClient(recorder)
	t.Cleanup(func() {
		client.Close()
		server.Close()
		pair.CloseAll()
	})
	return &clientPair{t: t, client: client, server: server, requests: recorder}
}

// takeRequestsN drains the client's written frames, waiting up to timeout for `want` of them.
// The count matters: frames go through the shared bounded writer, so a frame the caller has
// already seen the EFFECT of can still be in flight when the assertion runs. Waiting for a
// fixed count is what keeps that from turning into a flaky sleep.
func (cp *clientPair) takeRequestsN(want int, timeout time.Duration) []clientFrame {
	deadline := time.Now().Add(timeout)
	var out []clientFrame
	for {
		for _, payload := range cp.requests.Payloads(0) {
			header, _, err := DecodeRequest(payload)
			if err != nil {
				// A frame the request codec cannot read is itself the failure: the client
				// wrote something no server could dispatch.
				cp.t.Errorf("the client wrote an undecodable request: %v", err)
				continue
			}
			out = append(out, clientFrame{Type: header.Type, ID: header.ID, Name: header.Name})
		}
		// want == 0 means "assert nothing was written", which can only be checked by
		// waiting the whole timeout: there is no positive signal to wait for.
		if (want > 0 && len(out) >= want) || (want == 0 && !time.Now().Before(deadline)) {
			return out
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// clientFrame is one decoded client request, kept as the header fields the tests assert on.
type clientFrame struct {
	Type int
	ID   int
	Name string
}

// drainEvents reads an event channel until it closes, reporting how many values it saw and
// whether the close arrived before the timeout.
func drainEvents(events <-chan Value, timeout time.Duration) (int, bool) {
	deadline := time.After(timeout)
	count := 0
	for {
		select {
		case _, open := <-events:
			if !open {
				return count, true
			}
			count++
		case <-deadline:
			return count, false
		}
	}
}

// mustGet reads an object member for an assertion, so the failure message can show what was
// actually there.
func mustGet(obj Object, key string) Value {
	value, _ := obj.Get(key)
	return value
}
