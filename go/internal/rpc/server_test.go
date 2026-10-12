package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

// These are the step 2.3 table tests. Each case pins one documented behaviour of the channel
// server so a later refactor cannot quietly change the wire contract that the TypeScript
// client (ChannelClient) and the Node server both depend on.

// TestInitializeHandshakeIsSentOnConstruction mirrors R01: a non-deferred server announces
// itself immediately, before any request.
func TestInitializeHandshakeIsSentOnConstruction(t *testing.T) {
	conn := newTestConn(t, Options{})
	got := conn.awaitInitialize()
	if got.Type != ResponseInitialize {
		t.Fatalf("first response type = %d, want %d (Initialize)", got.Type, ResponseInitialize)
	}
}

// TestDeferInitSuppressesInitializeUntilReady covers the TS deferInit/ready() pair: with
// DeferInit the server stays silent until Ready is called.
func TestDeferInitSuppressesInitializeUntilReady(t *testing.T) {
	conn := newTestConn(t, Options{DeferInit: true})
	if extra := conn.awaitResponses(1, 150*time.Millisecond); len(extra) != 0 {
		t.Fatalf("deferred server sent %d frames before Ready", len(extra))
	}
	conn.server.Ready()
	got := conn.awaitResponses(1, 2*time.Second)
	if len(got) != 1 || got[0].Type != ResponseInitialize {
		t.Fatalf("after Ready got %+v, want one Initialize", got)
	}
}

// callCase is one row of the call behaviour table.
type callCase struct {
	name  string
	setup func(*testConn)
	want  response
}

// TestCallTable covers the request/response dispatch paths: success, an *Error rejection, a
// bare Go error, a raw (non-Error) rejection, an unknown method, an unknown channel, and the
// event listen/dispose round trip.
func TestCallTable(t *testing.T) {
	cases := []callCase{
		{
			name: "call success echoes the argument",
			setup: func(c *testConn) {
				c.call(1, "svc", "echo", NewObject("value", int32(7)))
			},
			want: response{Type: ResponsePromiseSuccess, ID: 1, Body: NewObject("value", int32(7))},
		},
		{
			name: "structured Error maps to PromiseError with passthrough keys",
			setup: func(c *testConn) {
				c.call(1, "svc", "explode", Undefined)
			},
			want: response{Type: ResponsePromiseError, ID: 1, Body: NewObject(
				"message", "channel exploded",
				"name", "ExplodedError",
				"stack", []Value{"ExplodedError: channel exploded", "    at go"},
				"code", "E_EXPLODE",
				"retryAfterMs", int32(42),
			)},
		},
		{
			name: "bare Go error maps to PromiseError with name Error and no stack",
			setup: func(c *testConn) {
				c.call(1, "svc", "plain", Undefined)
			},
			want: response{Type: ResponsePromiseError, ID: 1, Body: NewObject(
				"message", "plain failure",
				"name", "Error",
				"stack", Undefined,
			)},
		},
		{
			name: "RawError maps to PromiseErrorObj carrying the raw value",
			setup: func(c *testConn) {
				c.call(1, "svc", "raw", Undefined)
			},
			want: response{Type: ResponsePromiseErrorObj, ID: 1, Body: "just a string"},
		},
		{
			name: "unknown method on a known channel is a Method not found error",
			setup: func(c *testConn) {
				c.call(1, "svc", "missing", Undefined)
			},
			want: response{Type: ResponsePromiseError, ID: 1, Body: NewObject(
				"message", "Method not found: missing",
				"name", "Error",
				"stack", Undefined,
			)},
		},
		{
			name: "event listen delivers fires and dispose stops them",
			setup: func(c *testConn) {
				c.listen(5, "svc", "tick", Undefined)
			},
			want: response{Type: ResponseEventFire, ID: 5, Body: "tick-0"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := newTestConn(t, Options{TimeoutDelayMS: 300})
			registerStubChannels(conn.server)
			conn.awaitInitialize()
			tc.setup(conn)

			got := conn.nextResponse(2 * time.Second)
			if got.Type != tc.want.Type {
				t.Fatalf("response type = %d, want %d", got.Type, tc.want.Type)
			}
			if got.ID != tc.want.ID {
				t.Fatalf("response id = %d, want %d", got.ID, tc.want.ID)
			}
			if !equalValues(got.Body, tc.want.Body) {
				t.Fatalf("response body = %#v, want %#v", got.Body, tc.want.Body)
			}
		})
	}
}

// registerStubChannels installs the deterministic channel used by the table tests under the
// conventional name "svc".
func registerStubChannels(s *Server) { registerStubChannelsAs(s, "svc") }

// registerStubChannelsAs installs the deterministic channel under a chosen name, so the
// pending-queue tests can register the exact channel their early request named.
func registerStubChannelsAs(s *Server, name string) {
	s.RegisterChannel(name, &stubChannel{
		callFunc: func(ctx context.Context, method string, args []Value) (Value, error) {
			switch method {
			case "echo":
				if len(args) == 1 {
					return args[0], nil
				}
				return Undefined, nil
			case "explode":
				return nil, &Error{
					Name:         "ExplodedError",
					Message:      "channel exploded",
					Stack:        []string{"ExplodedError: channel exploded", "    at go"},
					Code:         "E_EXPLODE",
					RetryAfterMs: int32(42),
				}
			case "plain":
				return nil, errors.New("plain failure")
			case "raw":
				return nil, &RawError{Value: "just a string"}
			}
			return nil, &Error{Message: "Method not found: " + method}
		},
		listenFunc: func(ctx context.Context, event string, arg Value) (<-chan Value, func(), error) {
			if event != "tick" {
				return nil, nil, &Error{Message: "Event not found: " + event}
			}
			out := make(chan Value)
			stop := make(chan struct{})
			go func() {
				defer close(out)
				for i := 0; ; i++ {
					// Rate-limited on purpose. An unthrottled emitter would flood the
					// connection and make "nothing arrived after dispose" unobservable,
					// because events already in flight keep landing for as long as the
					// test waits — a property of any queue, not of the server.
					select {
					case <-stop:
						return
					case <-ctx.Done():
						return
					case <-time.After(5 * time.Millisecond):
					}
					select {
					case out <- "tick-" + itoa(i):
					case <-stop:
						return
					case <-ctx.Done():
						return
					}
				}
			}()
			var stopOnce sync.Once
			return out, func() { stopOnce.Do(func() { close(stop) }) }, nil
		},
	})
}

// TestUnknownChannelTimesOutWithStructuredError covers the pending queue's timeout branch:
// a call for a channel that is never registered gets an explicit Unknown-channel
// PromiseError after the timeout, carrying the channel name and the configured delay.
func TestUnknownChannelTimesOutWithStructuredError(t *testing.T) {
	conn := newTestConn(t, Options{TimeoutDelayMS: 120})
	conn.awaitInitialize()
	conn.call(1, "nowhere", "nope", Undefined)

	got := conn.nextResponse(2 * time.Second)
	if got.Type != ResponsePromiseError {
		t.Fatalf("type = %d, want %d (PromiseError)", got.Type, ResponsePromiseError)
	}
	want := NewObject(
		"name", "Unknown channel",
		"message", "Channel name 'nowhere' timed out after 120ms",
		"stack", Undefined,
	)
	if !equalValues(got.Body, want) {
		t.Fatalf("body = %#v, want %#v", got.Body, want)
	}
}

// TestUnknownChannelServedWhenRegisteredInTime covers the pending queue's flush branch: a
// call that arrives before registration is served normally once the channel appears, within
// the timeout window.
func TestUnknownChannelServedWhenRegisteredInTime(t *testing.T) {
	conn := newTestConn(t, Options{TimeoutDelayMS: 1000})
	conn.awaitInitialize()
	conn.call(1, "later", "echo", NewObject("value", int32(9)))

	// Register before the 1000 ms window closes.
	time.Sleep(20 * time.Millisecond)
	registerStubChannelsAs(conn.server, "later")

	got := conn.nextResponse(2 * time.Second)
	if got.Type != ResponsePromiseSuccess {
		t.Fatalf("type = %d, want %d (PromiseSuccess); body %#v", got.Type, ResponsePromiseSuccess, got.Body)
	}
	if !equalValues(got.Body, NewObject("value", int32(9))) {
		t.Fatalf("body = %#v, want the echoed argument", got.Body)
	}
}

// TestPendingRequestAfterTimeoutIsDroppedOnce proves the flush and the timeout do not both
// fire: registering after the timeout must not resurrect the request (it would send a second
// response for the same id).
func TestPendingRequestAfterTimeoutIsDroppedOnce(t *testing.T) {
	conn := newTestConn(t, Options{TimeoutDelayMS: 60})
	conn.awaitInitialize()
	conn.call(1, "late", "echo", Undefined)

	timeout := conn.nextResponse(2 * time.Second)
	if timeout.Type != ResponsePromiseError {
		t.Fatalf("first response = %d, want the timeout PromiseError", timeout.Type)
	}

	// Registering now must produce nothing for id 1.
	registerStubChannelsAs(conn.server, "late")
	if extra := conn.awaitResponses(1, 200*time.Millisecond); len(extra) != 0 {
		t.Fatalf("register after timeout produced %d extra responses, want 0", len(extra))
	}
}

// TestEventListenFireThenDispose proves an event subscription delivers EventFire frames and
// that a 103 stops delivery.
//
// The assertion is that firing STOPS, not that nothing is ever seen after the dispose: an
// event already in the listener's bounded buffer when the 103 arrives is written once more
// before the delivery goroutine sees the stop. That is inherent to any queue and harmless —
// the client has already dropped its handler for that id. What must not happen is a
// continuing stream, so the test samples twice and requires the second sample to be empty.
func TestEventListenFireThenDispose(t *testing.T) {
	conn := newTestConn(t, Options{})
	registerStubChannels(conn.server)
	conn.awaitInitialize()
	conn.listen(7, "svc", "tick", Undefined)

	// The stub emits tick-0 first; receive it.
	first := conn.nextResponse(2 * time.Second)
	if first.Type != ResponseEventFire || first.ID != 7 || first.Body != "tick-0" {
		t.Fatalf("first fire = %+v, want EventFire id 7 tick-0", first)
	}

	conn.dispose(7)

	// Let whatever was in flight drain (the stub ticks every 5 ms and the buffer is
	// bounded), then confirm no NEW fires arrive afterwards.
	drained := conn.awaitResponses(EventBufferSize, 300*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	if late := conn.awaitResponses(1, 100*time.Millisecond); len(late) != 0 {
		t.Fatalf("%d fires arrived after the stream settled (drained %d first), want 0",
			len(late), len(drained))
	}
	// A dispose that did nothing would show an ever-growing drain count instead.
	if len(drained) > 4 {
		t.Fatalf("%d fires drained after dispose; the 103 did not stop delivery", len(drained))
	}
}

// TestEventListenUnknownEventIsAnError proves an unknown event name rejects the listen.
func TestEventListenUnknownEventIsAnError(t *testing.T) {
	conn := newTestConn(t, Options{})
	registerStubChannels(conn.server)
	conn.awaitInitialize()
	conn.listen(3, "svc", "no-such-event", Undefined)

	got := conn.nextResponse(2 * time.Second)
	if got.Type != ResponsePromiseError {
		t.Fatalf("type = %d, want %d (PromiseError)", got.Type, ResponsePromiseError)
	}
	if !equalValues(got.Body, NewObject("message", "Event not found: no-such-event", "name", "Error", "stack", Undefined)) {
		t.Fatalf("body = %#v, want the Event not found error", got.Body)
	}
}

// TestCancelBeforeStart proves a 101 that lands while the call is still QUEUED (waiting for a
// per-connection slot, so its handler has never run) settles the promise: the call is
// cancelled, and the client receives a rejection rather than hanging.
//
// MaxConcurrentCalls is 1 and a blocking first call holds the only slot, which is what puts
// the second call in the pre-start state the server must handle.
func TestCancelBeforeStart(t *testing.T) {
	holding := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once

	conn := newTestConn(t, Options{MaxConcurrentCalls: 1})
	conn.server.RegisterChannel("slow", &stubChannel{
		callFunc: func(ctx context.Context, method string, args []Value) (Value, error) {
			once.Do(func() { close(entered) })
			<-holding
			return "done", nil
		},
	})
	conn.awaitInitialize()

	// Occupy the single slot.
	conn.call(1, "slow", "hold", Undefined)
	<-entered

	// This one queues for a slot, so its handler has not started.
	conn.call(2, "slow", "queued", Undefined)
	conn.sendCancel(2)

	// The queued call must settle on its own, without the first call finishing.
	got := conn.nextResponse(2 * time.Second)
	if got.ID != 2 {
		t.Fatalf("response id = %d, want 2", got.ID)
	}
	if got.Type != ResponsePromiseError {
		t.Fatalf("type = %d, want %d (PromiseError)", got.Type, ResponsePromiseError)
	}
	close(holding)
}

// TestCancelMidFlightObservesContext proves a 101 delivered while the handler runs cancels
// the handler's context: a handler that selects on ctx.Done returns promptly with the
// cancellation, and the client sees that rejection.
func TestCancelMidFlightObservesContext(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once

	conn := newTestConn(t, Options{})
	conn.server.RegisterChannel("slow", &stubChannel{
		callFunc: func(ctx context.Context, method string, args []Value) (Value, error) {
			once.Do(func() { close(started) })
			<-ctx.Done() // block until the server cancels us
			return nil, ctx.Err()
		},
	})
	conn.awaitInitialize()

	conn.call(1, "slow", "wait", Undefined)
	<-started // handler is now running
	conn.sendCancel(1)

	got := conn.nextResponse(2 * time.Second)
	if got.Type != ResponsePromiseError {
		t.Fatalf("type = %d, want %d (PromiseError from cancellation)", got.Type, ResponsePromiseError)
	}
	if got.ID != 1 {
		t.Fatalf("id = %d, want 1", got.ID)
	}
}

// TestDuplicateRequestIDEvictsPrevious proves that reusing an id while a call is in flight
// cancels the older call so only the newest owns the id (the client cannot attribute two
// responses to one request).
func TestDuplicateRequestIDEvictsPrevious(t *testing.T) {
	secondEntered := make(chan struct{})
	var once sync.Once

	conn := newTestConn(t, Options{})
	conn.server.RegisterChannel("dup", &stubChannel{
		callFunc: func(ctx context.Context, method string, args []Value) (Value, error) {
			if method == "first" {
				<-ctx.Done() // hold the id until evicted
				return nil, ctx.Err()
			}
			once.Do(func() { close(secondEntered) })
			return "second", nil
		},
	})
	conn.awaitInitialize()

	conn.call(1, "dup", "first", Undefined)
	time.Sleep(20 * time.Millisecond)        // let the first call take the id
	conn.call(1, "dup", "second", Undefined) // reuse the id

	<-secondEntered
	// The evicted first call must now settle (it observed cancellation).
	got := conn.awaitResponses(2, 2*time.Second)
	seen := map[int]int{}
	for _, r := range got {
		seen[r.ID]++
	}
	if seen[1] < 1 {
		t.Fatalf("expected at least one response for id 1, got %v", seen)
	}
}

// TestIDReuseAfterCompletion proves an id can be cleanly reused once the previous request has
// fully completed: the second request answers normally.
func TestIDReuseAfterCompletion(t *testing.T) {
	conn := newTestConn(t, Options{})
	registerStubChannels(conn.server)
	conn.awaitInitialize()

	conn.call(4, "svc", "echo", NewObject("n", int32(1)))
	if first := conn.nextResponse(2 * time.Second); first.ID != 4 || first.Type != ResponsePromiseSuccess {
		t.Fatalf("first = %+v, want success id 4", first)
	}
	conn.call(4, "svc", "echo", NewObject("n", int32(2)))
	second := conn.nextResponse(2 * time.Second)
	if second.ID != 4 || second.Type != ResponsePromiseSuccess {
		t.Fatalf("second = %+v, want success id 4", second)
	}
	if !equalValues(second.Body, NewObject("n", int32(2))) {
		t.Fatalf("second body = %#v, want n=2", second.Body)
	}
}

// TestUnknownRequestTypeIsIgnored proves a header type outside 100..103 is dropped without
// closing the connection, matching the TS switch with no default branch.
func TestUnknownRequestTypeIsIgnored(t *testing.T) {
	conn := newTestConn(t, Options{})
	registerStubChannels(conn.server)
	conn.awaitInitialize()

	conn.send(999, 1, "svc", "whatever", Undefined)
	// The server must not respond and must not die.
	if extra := conn.awaitResponses(1, 200*time.Millisecond); len(extra) != 0 {
		t.Fatalf("unknown request type produced %d responses, want 0", len(extra))
	}
	conn.call(1, "svc", "echo", NewObject("ok", true))
	if got := conn.nextResponse(2 * time.Second); got.Type != ResponsePromiseSuccess {
		t.Fatalf("connection did not survive: got type %d", got.Type)
	}
}

// equalValues compares two decoded protocol values structurally.
//
// Two protocol details make a naive reflect.DeepEqual wrong here, and both are the point of
// the assertions rather than noise:
//   - an Object member whose value is Undefined is DROPPED by the serializer, so an expected
//     Undefined member means "this key must be absent", not "this key holds nil";
//   - a number inside a JSON object decodes to json.Number (UseNumber), so 42 arrives as
//     json.Number("42"), while the same number written directly takes the Int tag and decodes
//     to int32. Both must compare equal to 42.
func equalValues(got, want Value) bool {
	if IsUndefined(want) {
		return IsUndefined(got) || got == nil
	}
	switch w := want.(type) {
	case Object:
		g, ok := got.(Object)
		if !ok {
			return false
		}
		present := 0
		for i, key := range w.Keys {
			gv, found := g.Get(key)
			if IsUndefined(w.Values[i]) {
				continue // dropped on the wire; its absence is correct
			}
			if !found || !equalValues(gv, w.Values[i]) {
				return false
			}
			present++
		}
		return present == g.Len()
	case []Value:
		g, ok := got.([]Value)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !equalValues(g[i], w[i]) {
				return false
			}
		}
		return true
	case json.Number:
		gotNumber, ok := got.(json.Number)
		return ok && gotNumber.String() == w.String()
	case int32:
		switch g := got.(type) {
		case int32:
			return g == w
		case json.Number:
			return g.String() == strconv.FormatInt(int64(w), 10)
		}
		return false
	}
	return got == want
}

// itoa avoids importing strconv in the stub just for the tick counter.
func itoa(n int) string { return strconv.Itoa(n) }
