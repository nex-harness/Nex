// client_test.go covers the protocol client's correlation rules against a scripted
// agent: out-of-order answers, cancellation, timeout, the observation/business
// timeout split, close, notification fan-out, the interaction routing seam and
// the storage startup gate.
package proto

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/protocol"
)

// newTestClient wires a client to a scripted agent and tears both down.
func newTestClient(t *testing.T, opts Options, script func(*fakeTransport)) (*Client, *fakeTransport) {
	t.Helper()
	transport := newFakeTransport()
	script(transport)
	client := New(transport, opts)
	t.Cleanup(func() {
		client.Close()
		client.Wait()
	})
	return client, transport
}

// TestTwoConcurrentCallsGetTheirOwnResponses is the core correlation rule: two
// calls in flight, the agent answers them in the opposite order, and each waiter
// must receive its own result. A correlation bug that swapped them would pass any
// single-call test.
func TestTwoConcurrentCallsGetTheirOwnResponses(t *testing.T) {
	const calls = 2
	gate := make(chan protocol.Message, calls)
	transport := newFakeTransport()
	transport.on("alpha", func(req protocol.Message) []protocol.Message {
		gate <- req
		return nil
	})
	transport.on("beta", func(req protocol.Message) []protocol.Message {
		gate <- req
		return nil
	})
	client := New(transport, Options{})
	defer func() { client.Close(); client.Wait() }()

	type outcome struct {
		method string
		value  string
		err    error
	}
	results := make(chan outcome, calls)
	for _, spec := range [][2]string{{"alpha", "A"}, {"beta", "B"}} {
		go func(method, want string) {
			res, err := client.Call(context.Background(), method, nil, CallOptions{Timeout: 2 * time.Second})
			got := ""
			if res != nil {
				got = string(res.Raw)
			}
			results <- outcome{method: method, value: got, err: err}
		}(spec[0], spec[1])
	}

	// Collect both requests, then answer them in reverse order.
	seen := map[string]protocol.Message{}
	for i := 0; i < calls; i++ {
		select {
		case req := <-gate:
			seen[req.Method] = req
		case <-time.After(2 * time.Second):
			t.Fatal("the client never sent both requests")
		}
	}
	beta, alpha := seen["beta"], seen["alpha"]
	if beta.ID.String() == alpha.ID.String() {
		t.Fatalf("both calls used request id %s", alpha.ID)
	}
	transport.deliver(protocol.Message{Kind: protocol.KindResponse, ID: beta.ID, Result: json.RawMessage(`"B"`)})
	transport.deliver(protocol.Message{Kind: protocol.KindResponse, ID: alpha.ID, Result: json.RawMessage(`"A"`)})

	got := map[string]string{}
	for i := 0; i < calls; i++ {
		select {
		case o := <-results:
			if o.err != nil {
				t.Fatalf("%s: %v", o.method, o.err)
			}
			got[o.method] = o.value
		case <-time.After(2 * time.Second):
			t.Fatal("a waiter never resolved")
		}
	}
	if got["alpha"] != `"A"` || got["beta"] != `"B"` {
		t.Fatalf("results crossed: alpha=%s beta=%s", got["alpha"], got["beta"])
	}
}

// TestResponseForAnUnknownIDIsDropped: an id nobody is waiting for must not panic
// and must not be delivered to a later request (ids are never recycled, so this is
// the same guarantee as "a late response after a timeout is inert").
func TestResponseForAnUnknownIDIsDropped(t *testing.T) {
	client, transport := newTestClient(t, Options{}, func(f *fakeTransport) {})
	transport.deliver(protocol.Message{Kind: protocol.KindResponse, ID: protocol.IntID(999), Result: json.RawMessage(`"stray"`)})
	transport.reply("after", "ok")
	res, err := client.Call(context.Background(), "after", nil, CallOptions{Timeout: time.Second})
	if err != nil {
		t.Fatalf("call after a stray response: %v", err)
	}
	if string(res.Raw) != `"ok"` {
		t.Errorf("result = %s", res.Raw)
	}
}

// TestCancellationRemovesTheWaiterAndDoesNotWrite: a context cancelled before the
// write must not put the request on the wire at all — the agent would otherwise
// run a model task whose response nobody can receive.
func TestCancellationRemovesTheWaiterAndDoesNotWrite(t *testing.T) {
	client, transport := newTestClient(t, Options{}, func(f *fakeTransport) {})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Call(ctx, "never", nil, CallOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := transport.SentMethods(); len(got) != 0 {
		t.Fatalf("a cancelled request was written: %v", got)
	}
	if n := client.PendingCount(); n != 0 {
		t.Errorf("PendingCount = %d after cancellation, want 0", n)
	}
}

// TestCancellationWhileInFlightSettlesOnce: cancelling a request the agent has
// already received must reject the caller, clear the watchdog and leave the
// pending table empty.
func TestCancellationWhileInFlightSettlesOnce(t *testing.T) {
	client, transport := newTestClient(t, Options{}, func(f *fakeTransport) {
		f.on("slow", func(protocol.Message) []protocol.Message { return nil })
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.Call(ctx, "slow", nil, CallOptions{Timeout: 5 * time.Second})
		done <- err
	}()
	waitForPending(t, client, 1)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the cancelled call never returned")
	}
	if n := client.PendingCount(); n != 0 {
		t.Errorf("PendingCount = %d after cancellation, want 0", n)
	}
	// A late answer to the abandoned id must change nothing.
	transport.deliver(protocol.Message{Kind: protocol.KindResponse, ID: protocol.IntID(1), Result: json.RawMessage(`"late"`)})
	if n := client.PendingCount(); n != 0 {
		t.Errorf("a late response resurrected a waiter: PendingCount = %d", n)
	}
}

// TestTimeoutRejectsWithTimeoutErrorAndFiresTheEvent: a business request that
// times out is both an error for its caller AND an event for the process manager,
// which is the only thing that can retire a process whose event loop is dead.
func TestTimeoutRejectsWithTimeoutErrorAndFiresTheEvent(t *testing.T) {
	client, _ := newTestClient(t, Options{}, func(f *fakeTransport) {
		f.on("hang", func(protocol.Message) []protocol.Message { return nil })
	})
	events, _ := client.OnRequestTimeout()
	_, err := client.Call(context.Background(), "hang", nil, CallOptions{Timeout: 30 * time.Millisecond})
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v, want *TimeoutError", err)
	}
	if timeoutErr.Method != "hang" || timeoutErr.Timeout != 30*time.Millisecond {
		t.Errorf("timeout error = %+v", timeoutErr)
	}
	select {
	case ev := <-events:
		if ev.Method != "hang" {
			t.Errorf("timeout event method = %q", ev.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("no request-timeout event was fired")
	}
	if n := client.PendingCount(); n != 0 {
		t.Errorf("PendingCount = %d after timeout, want 0", n)
	}
}

// TestObservationTimeoutDoesNotFireTheEvent is the other half of the rule: a short
// resource probe that finds nothing is not evidence of a broken process, so it
// must not be escalated into a recycle.
func TestObservationTimeoutDoesNotFireTheEvent(t *testing.T) {
	client, _ := newTestClient(t, Options{}, func(f *fakeTransport) {
		f.on("probe", func(protocol.Message) []protocol.Message { return nil })
	})
	events, _ := client.OnRequestTimeout()
	_, err := client.Call(context.Background(), "probe", nil, CallOptions{
		Lifecycle: LifecycleObservation,
		Timeout:   30 * time.Millisecond,
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want a timeout", err)
	}
	select {
	case ev := <-events:
		t.Fatalf("an observation timeout fired the recycle event: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestPendingOperationCountExcludesObservations: idle reclaim keys off business
// work only; a probe must not keep a process alive.
func TestPendingOperationCountExcludesObservations(t *testing.T) {
	client, _ := newTestClient(t, Options{}, func(f *fakeTransport) {
		f.on("probe", func(protocol.Message) []protocol.Message { return nil })
		f.on("work", func(protocol.Message) []protocol.Message { return nil })
	})
	go func() { _, _ = client.Call(context.Background(), "work", nil, CallOptions{Timeout: time.Second}) }()
	go func() {
		_, _ = client.Call(context.Background(), "probe", nil, CallOptions{
			Lifecycle: LifecycleObservation, Timeout: time.Second,
		})
	}()
	waitForPending(t, client, 2)
	if got := client.PendingCount(); got != 2 {
		t.Errorf("PendingCount = %d, want 2", got)
	}
	if got := client.PendingOperationCount(); got != 1 {
		t.Errorf("PendingOperationCount = %d, want 1 (the observation must not count)", got)
	}
}

// TestAgentErrorAnswerBecomesRequestError: a JSON-RPC error response settles the
// waiter with a typed error carrying the agent's code and message.
func TestAgentErrorAnswerBecomesRequestError(t *testing.T) {
	client, _ := newTestClient(t, Options{}, func(f *fakeTransport) {
		f.fail("nope/not-a-method", protocol.CodeMethodNotFound, "Method not found: nope/not-a-method")
	})
	_, err := client.Call(context.Background(), "nope/not-a-method", nil, CallOptions{Timeout: time.Second})
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("err = %v, want *RequestError", err)
	}
	if !IsMethodNotFound(err) {
		t.Errorf("IsMethodNotFound = false for %v", err)
	}
	if reqErr.Method != "nope/not-a-method" || !strings.Contains(reqErr.Message, "not found") {
		t.Errorf("error = %+v", reqErr)
	}
}

// TestNotificationFanOutPerMethod: subscribers only see their own method, plus
// the wildcard subscriber sees everything.
func TestNotificationFanOutPerMethod(t *testing.T) {
	client, transport := newTestClient(t, Options{}, func(f *fakeTransport) {})
	alpha, _ := client.OnNotification("a/one")
	beta, _ := client.OnNotification("b/two")
	all, _ := client.OnNotification("")

	transport.Push(notificationLine(t, "a/one", map[string]any{"v": 1}))
	transport.Push(notificationLine(t, "b/two", map[string]any{"v": 2}))

	if got := waitNotification(t, alpha); got.Method != "a/one" {
		t.Errorf("a/one subscriber got %q", got.Method)
	}
	if got := waitNotification(t, beta); got.Method != "b/two" {
		t.Errorf("b/two subscriber got %q", got.Method)
	}
	first := waitNotification(t, all)
	second := waitNotification(t, all)
	if first.Method == second.Method {
		t.Errorf("the wildcard subscriber only saw %q twice", first.Method)
	}
}

// TestUnsubscribeStopsDelivery: removing a subscriber must stop the fan-out, or a
// disposed scope keeps receiving.
func TestUnsubscribeStopsDelivery(t *testing.T) {
	client, transport := newTestClient(t, Options{}, func(f *fakeTransport) {})
	ch, unsubscribe := client.OnNotification("a/one")
	unsubscribe()
	transport.Push(notificationLine(t, "a/one", nil))
	select {
	case n := <-ch:
		t.Fatalf("an unsubscribed channel still received %+v", n)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestInteractionRequestWithoutHandlerAnswersMethodNotFound is the documented
// default: with no host handler the client answers -32601, which the agent
// already degrades on. Anything else would leave the agent waiting forever.
func TestInteractionRequestWithoutHandlerAnswersMethodNotFound(t *testing.T) {
	_, transport := newTestClient(t, Options{}, func(f *fakeTransport) {})
	for _, method := range InteractionMethods {
		transport.Push(requestLine(t, 42, method, map[string]any{"sessionId": "s1"}))
	}
	for range InteractionMethods {
		msg := waitSent(t, transport)
		if msg.Kind != protocol.KindError {
			t.Fatalf("%s: answered with %s, want an error", msg.Method, msg.Kind)
		}
		if msg.Error.Code != protocol.CodeMethodNotFound {
			t.Errorf("%s: code = %d, want %d (method-not-found)", msg.Method, msg.Error.Code, protocol.CodeMethodNotFound)
		}
	}
}

// TestInteractionHandlerReceivesTheProgrammedAnswer is the routing seam: a
// registered handler answers the agent's permission request with its own result,
// and the agent's request id is preserved.
func TestInteractionHandlerReceivesTheProgrammedAnswer(t *testing.T) {
	client, transport := newTestClient(t, Options{}, func(f *fakeTransport) {})
	answered := make(chan AgentRequest, 1)
	client.RegisterInteractionHandler(InteractionRequestPermission,
		func(_ context.Context, req AgentRequest) (json.RawMessage, error) {
			answered <- req
			return json.RawMessage(`{"outcome":"allow"}`), nil
		})
	if got := client.InteractionHandlerFor(InteractionRequestPermission); got == nil {
		t.Fatal("the handler was not registered")
	}
	transport.Push(requestLine(t, 7, InteractionRequestPermission, map[string]any{"sessionId": "s1"}))

	msg := waitSent(t, transport)
	if msg.Kind != protocol.KindResponse {
		t.Fatalf("answered with %s: %+v", msg.Kind, msg)
	}
	if msg.ID.String() != "7" {
		t.Errorf("answered id %s, want 7 (the agent's own request id)", msg.ID)
	}
	if string(msg.Result) != `{"outcome":"allow"}` {
		t.Errorf("result = %s", msg.Result)
	}
	select {
	case req := <-answered:
		if req.Method != InteractionRequestPermission {
			t.Errorf("handler saw method %q", req.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("the handler never ran")
	}
}

// TestInteractionHandlerErrorChoosesTheCode: a handler that fails answers with the
// JSON-RPC error it chose, which is how the host reports "the user denied this"
// rather than a generic internal error.
func TestInteractionHandlerErrorChoosesTheCode(t *testing.T) {
	client, transport := newTestClient(t, Options{}, func(f *fakeTransport) {})
	client.RegisterInteractionHandler(InteractionRequestUserInput,
		func(context.Context, AgentRequest) (json.RawMessage, error) {
			return nil, &RequestError{Method: InteractionRequestUserInput, Code: protocol.CodeInvalidParams, Message: "user cancelled"}
		})
	transport.Push(requestLine(t, 9, InteractionRequestUserInput, map[string]any{}))
	msg := waitSent(t, transport)
	if msg.Kind != protocol.KindError || msg.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("answer = %+v, want error %d", msg, protocol.CodeInvalidParams)
	}
	if !strings.Contains(msg.Error.Message, "cancelled") {
		t.Errorf("message = %q", msg.Error.Message)
	}
}

// TestHandlersArePerMethod: registering a permission handler must not answer a
// browser request. Two connections (or two windows) can therefore register
// different answers for the same method without colliding here.
func TestHandlersArePerMethod(t *testing.T) {
	client, transport := newTestClient(t, Options{}, func(f *fakeTransport) {})
	client.RegisterInteractionHandler(InteractionRequestPermission,
		func(context.Context, AgentRequest) (json.RawMessage, error) {
			return json.RawMessage(`{"outcome":"allow"}`), nil
		})
	transport.Push(requestLine(t, 3, InteractionBrowserExecute, map[string]any{"action": "click"}))
	msg := waitSent(t, transport)
	if msg.Kind != protocol.KindError || msg.Error.Code != protocol.CodeMethodNotFound {
		t.Fatalf("an unregistered method answered %+v", msg)
	}
}

// TestHandlerContextIsCancelledOnClose: a handler blocked on a UI prompt must
// unblock when the leg ends, or every closed connection leaks a goroutine.
func TestHandlerContextIsCancelledOnClose(t *testing.T) {
	transport := newFakeTransport()
	entered := make(chan context.Context, 1)
	transport.on(InteractionRequestUserInput, func(protocol.Message) []protocol.Message { return nil })
	client := New(transport, Options{})
	defer func() { client.Close(); client.Wait() }()
	client.RegisterInteractionHandler(InteractionRequestUserInput,
		func(ctx context.Context, _ AgentRequest) (json.RawMessage, error) {
			entered <- ctx
			<-ctx.Done()
			return nil, ctx.Err()
		})
	transport.Push(requestLine(t, 1, InteractionRequestUserInput, map[string]any{}))
	var ctx context.Context
	select {
	case ctx = <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never ran")
	}
	client.Close()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the handler context was not cancelled on close")
	}
	client.Wait()
}

// TestTransportCloseRejectsEveryWaiter: when the agent dies, every pending call
// fails with the transport error and the close channel fires — nothing may be left
// parked on a process that is gone.
func TestTransportCloseRejectsEveryWaiter(t *testing.T) {
	transport := newFakeTransport()
	transport.on("hang", func(protocol.Message) []protocol.Message { return nil })
	client := New(transport, Options{})
	closed := client.OnClose()
	const calls = 3
	errs := make(chan error, calls)
	for i := 0; i < calls; i++ {
		go func() {
			_, err := client.Call(context.Background(), "hang", nil, CallOptions{Timeout: time.Minute})
			errs <- err
		}()
	}
	waitForPending(t, client, calls)
	transport.End("agent_exited: code=1")
	for i := 0; i < calls; i++ {
		select {
		case err := <-errs:
			if !errors.Is(err, ErrTransportClosed) {
				t.Errorf("err = %v, want a transport-closed error", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a waiter was not rejected on transport close")
		}
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("the close channel never fired")
	}
	if !client.IsDisposed() {
		t.Error("the client is not disposed after its leg ended")
	}
	client.Wait()
}

// TestMalformedFrameClosesTheLeg: a line the protocol parser rejects poisons the
// stream (the reader cannot resynchronise), so it must close the transport with
// the protocol_parse_error reason rather than skipping the line.
func TestMalformedFrameClosesTheLeg(t *testing.T) {
	transport := newFakeTransport()
	client := New(transport, Options{})
	defer func() { client.Close(); client.Wait() }()
	transport.Push(`{"id":1,"result":`)
	transport.Push(`this is not json`)
	client.Wait()
	if reason := client.CloseReason(); !strings.HasPrefix(reason, "protocol_parse_error") {
		t.Errorf("close reason = %q, want a protocol_parse_error", reason)
	}
	if _, err := client.Call(context.Background(), "anything", nil, CallOptions{}); !errors.Is(err, ErrDisposed) {
		t.Errorf("a call after a parse error returned %v, want ErrDisposed", err)
	}
}

// TestDisposedClientRejectsCalls: sending to a client whose process was recycled
// must fail with the honest error, not a confusing transport failure.
func TestDisposedClientRejectsCalls(t *testing.T) {
	client, _ := newTestClient(t, Options{}, func(f *fakeTransport) {})
	client.Close()
	if _, err := client.Call(context.Background(), "x", nil, CallOptions{}); !errors.Is(err, ErrDisposed) {
		t.Errorf("Call after Close = %v, want ErrDisposed", err)
	}
	if err := client.Notify(context.Background(), "x", nil); !errors.Is(err, ErrDisposed) {
		t.Errorf("Notify after Close = %v, want ErrDisposed", err)
	}
	client.Wait()
}

// TestWriteFailureRejectsTheCall: a dead stdin must not leave the caller waiting
// for a response that can never arrive.
func TestWriteFailureRejectsTheCall(t *testing.T) {
	client, transport := newTestClient(t, Options{}, func(f *fakeTransport) {})
	want := errors.New("stdin closed")
	transport.SetSendError(want)
	if _, err := client.Call(context.Background(), "x", nil, CallOptions{}); !errors.Is(err, want) {
		t.Errorf("err = %v, want the write error", err)
	}
	if n := client.PendingCount(); n != 0 {
		t.Errorf("PendingCount = %d after a failed write, want 0", n)
	}
}

// TestResultValidationRejectsABadShape: the Validate hook is the Go stand-in for
// the TS result zod schema; a malformed result must reject the call, not hand the
// caller a value it will misread.
func TestResultValidationRejectsABadShape(t *testing.T) {
	client, _ := newTestClient(t, Options{}, func(f *fakeTransport) {
		f.reply("shape", map[string]any{"n": "not-a-number"})
	})
	_, err := client.Call(context.Background(), "shape", nil, CallOptions{
		Timeout: time.Second,
		Validate: func(raw json.RawMessage) (any, error) {
			var v struct {
				N int `json:"n"`
			}
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, err
			}
			return v.N, nil
		},
	})
	if err == nil {
		t.Fatal("a result that failed validation was accepted")
	}
	var unmarshalErr *json.UnmarshalTypeError
	if !errors.As(err, &unmarshalErr) {
		t.Errorf("err = %v, want the validation error", err)
	}
}

// TestEnsureCapabilitiesCachesThePositiveAnswer: negotiation runs once per client.
// A host restart does not mean the remote CLI restarted, so re-asking every call
// would be pure latency — and the negative answer is deliberately not cached.
func TestEnsureCapabilitiesCachesThePositiveAnswer(t *testing.T) {
	client, transport := newTestClient(t, Options{}, func(f *fakeTransport) {
		f.reply(MethodRuntimeCapabilities, map[string]any{"independentPlanState": true})
	})
	caps, err := client.EnsureCapabilities(context.Background())
	if err != nil {
		t.Fatalf("first negotiation: %v", err)
	}
	if !caps.IndependentPlanState {
		t.Error("independentPlanState = false, want true")
	}
	// Drop the scripted answer: a second negotiation must be served from the cache.
	transport.mu.Lock()
	delete(transport.handlers, MethodRuntimeCapabilities)
	transport.mu.Unlock()
	if _, err := client.EnsureCapabilities(context.Background()); err != nil {
		t.Fatalf("cached negotiation: %v", err)
	}
}

// TestEnsureCapabilitiesRejectsADeclinedCapability is the fail-closed direction: an
// agent that does not advertise independentPlanState must produce an explicit
// unsupported error, never a silent "assume yes".
func TestEnsureCapabilitiesRejectsADeclinedCapability(t *testing.T) {
	client, transport := newTestClient(t, Options{}, func(f *fakeTransport) {
		f.reply(MethodRuntimeCapabilities, map[string]any{"independentPlanState": false})
	})
	if _, err := client.EnsureCapabilities(context.Background()); !errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("err = %v, want ErrCapabilityUnsupported", err)
	}
	// The negative answer must not be cached.
	transport.mu.Lock()
	transport.handlers[MethodRuntimeCapabilities] = func(req protocol.Message) []protocol.Message {
		return []protocol.Message{{Kind: protocol.KindResponse, ID: req.ID, Result: json.RawMessage(`{"independentPlanState":true}`)}}
	}
	transport.mu.Unlock()
	caps, err := client.EnsureCapabilities(context.Background())
	if err != nil {
		t.Fatalf("re-negotiation after a decline: %v", err)
	}
	if !caps.IndependentPlanState {
		t.Error("the second negotiation did not take effect")
	}
}

// TestEnsureCapabilitiesPropagatesMethodNotFound: an old agent that has no
// runtime/capabilities answers -32601, which is a capability absence, not a bug.
func TestEnsureCapabilitiesPropagatesMethodNotFound(t *testing.T) {
	client, _ := newTestClient(t, Options{}, func(f *fakeTransport) {
		f.fail(MethodRuntimeCapabilities, protocol.CodeMethodNotFound, "Method not found: runtime/capabilities")
	})
	if _, err := client.EnsureCapabilities(context.Background()); !IsMethodNotFound(err) {
		t.Fatalf("err = %v, want a method-not-found", err)
	}
}

// TestConcurrentCallsAreRaceFree drives many callers at once under -race: the
// correlation map, the id sequence and the watchdog timers are all shared state.
func TestConcurrentCallsAreRaceFree(t *testing.T) {
	client, transport := newTestClient(t, Options{}, func(f *fakeTransport) {
		f.on("work", func(req protocol.Message) []protocol.Message {
			// Answer every request twice: the second answer finds no waiter and
			// must be dropped without disturbing anything.
			res := protocol.Message{Kind: protocol.KindResponse, ID: req.ID, Result: json.RawMessage(`{"ok":true}`)}
			return []protocol.Message{res, res}
		})
	})
	var wg sync.WaitGroup
	const callers = 24
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := client.Call(context.Background(), "work", map[string]any{"i": i}, CallOptions{Timeout: 5 * time.Second}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a concurrent call failed: %v", err)
	}
	if got := len(transport.Sent()); got != callers {
		t.Errorf("sent %d requests, want %d", got, callers)
	}
	if n := client.PendingCount(); n != 0 {
		t.Errorf("PendingCount = %d after every call resolved, want 0", n)
	}
}

// waitForPending blocks until the client has n pending requests.
func waitForPending(t *testing.T, client *Client, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if client.PendingCount() == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("PendingCount = %d, want %d", client.PendingCount(), n)
}

func waitSent(t *testing.T, transport *fakeTransport) protocol.Message {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sent := transport.Sent(); len(sent) > 0 {
			// Consume: these tests assert on the LAST message the client wrote.
			transport.mu.Lock()
			last := sent[len(sent)-1]
			transport.sent = transport.sent[:len(sent)-1]
			transport.mu.Unlock()
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the client never wrote a message")
	return protocol.Message{}
}

func waitNotification(t *testing.T, ch <-chan Notification) Notification {
	t.Helper()
	select {
	case n := <-ch:
		return n
	case <-time.After(2 * time.Second):
		t.Fatal("no notification arrived")
		return Notification{}
	}
}

func notificationLine(t *testing.T, method string, params any) string {
	t.Helper()
	raw := json.RawMessage("null")
	if params != nil {
		var err error
		raw, err = json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
	}
	out, err := protocol.Encode(protocol.Message{Kind: protocol.KindNotification, Method: method, Params: raw, HasParams: true})
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func requestLine(t *testing.T, id int64, method string, params any) string {
	t.Helper()
	raw := json.RawMessage("null")
	if params != nil {
		var err error
		raw, err = json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
	}
	out, err := protocol.Encode(protocol.Message{Kind: protocol.KindRequest, ID: protocol.IntID(id), Method: method, Params: raw, HasParams: true})
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
