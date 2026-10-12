package modelclient

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingClock is a fake Clock: it advances a virtual now on Sleep and records
// every delay, so a retry run is fully deterministic and no wall-clock time is
// spent waiting.
type recordingClock struct {
	mu       sync.Mutex
	now      time.Time
	sleeps   []time.Duration
	fire     chan time.Time // optional idle fire
	sleepErr error
}

func newRecordingClock() *recordingClock {
	return &recordingClock{now: time.Unix(1700000000, 0).UTC()}
}

func (c *recordingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *recordingClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	err := c.sleepErr
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return ctx.Err()
}

func (c *recordingClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	fire := c.fire
	c.mu.Unlock()
	if fire != nil {
		return fire
	}
	return make(chan time.Time)
}

// elapsed returns the sum of every recorded sleep.
func (c *recordingClock) elapsed() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var total time.Duration
	for _, d := range c.sleeps {
		total += d
	}
	return total
}

// jitterFree resolves the retry options with jitter disabled so the delay the
// test asserts is the pure backoff curve (2s, 4s, 8s, … capped at 60s) or the
// provider's Retry-After, whichever wins.
func jitterFree(maxAttempts int) ResolvedRetryOptions {
	return ResolveRetryOptions(&RetryOptions{MaxAttempts: &maxAttempts, Jitter: boolPtr(false)}, nil)
}

func boolPtr(v bool) *bool { return &v }

// setupFailureResponses returns a transport that fails the first failures with
// the given setup responses and then succeeds with a normal text stream.
func setupFailureResponses(failures []*Response, terminal *Response) *fakeTransport {
	handlers := make([]func() (*Response, error), 0, len(failures)+1)
	for _, f := range failures {
		resp := f
		handlers = append(handlers, func() (*Response, error) { return resp, nil })
	}
	handlers = append(handlers, func() (*Response, error) { return terminal, nil })
	return &fakeTransport{handlers: handlers}
}

func textResponse() *Response {
	return &Response{StatusCode: 200, Stream: sseStream(textSSE)}
}

func retryDelays(sink *captureSink) []int64 {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var out []int64
	for _, ev := range sink.events {
		if ev.Status == "model_retry_scheduled" && ev.DelayMs != nil {
			out = append(out, *ev.DelayMs)
		}
	}
	return out
}

func retryEventCounts(sink *captureSink) (started, retried, failed int) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, ev := range sink.events {
		switch ev.Status {
		case "model_request_started":
			started++
		case "model_retry_scheduled":
			retried++
		case "model_request_failed":
			failed++
		}
	}
	return
}

// TestRetryWithFakeClock429RetryAfter covers the rate-limited path: the
// provider's Retry-After overrides the backoff curve, and the request succeeds
// on the second attempt.
func TestRetryWithFakeClock429RetryAfter(t *testing.T) {
	sink := &captureSink{}
	clock := newRecordingClock()
	rateLimited := &Response{
		StatusCode: 429,
		StatusText: "Too Many Requests",
		Headers:    map[string]string{"retry-after": "3"},
		BodyText:   `{"error":{"message":"slow down"}}`,
	}
	tr := setupFailureResponses([]*Response{rateLimited}, textResponse())
	c := newTestClient(t, tr, sink, clock)
	c.cfg.Retry = jitterFree(11)

	h := c.Stream(context.Background(), Request{URL: "http://x", Headers: map[string]string{"x-request-id": "r1"}})
	if got := drainText(h); got != "hello" {
		t.Fatalf("text: %q", got)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if tr.calls != 2 {
		t.Fatalf("attempt count: got %d want 2", tr.calls)
	}
	// Retry-After: 3 wins over the 2000ms curve on attempt 1.
	delays := retryDelays(sink)
	if len(delays) != 1 || delays[0] != 3000 {
		t.Fatalf("delays: got %v want [3000]", delays)
	}
	if got := clock.elapsed().Milliseconds(); got != 3000 {
		t.Fatalf("elapsed: got %d want 3000", got)
	}
	started, retried, failed := retryEventCounts(sink)
	if started != 2 || retried != 1 || failed != 1 {
		t.Fatalf("events: started=%d retried=%d failed=%d want 2/1/1", started, retried, failed)
	}
}

// TestRetryWithFakeClock500Backoff covers the server-error path: no Retry-After,
// so the delay follows the backoff curve across several attempts.
func TestRetryWithFakeClock500Backoff(t *testing.T) {
	sink := &captureSink{}
	clock := newRecordingClock()
	serverError := func() *Response {
		return &Response{StatusCode: 500, StatusText: "Internal Server Error"}
	}
	tr := setupFailureResponses(
		[]*Response{serverError(), serverError(), serverError()},
		textResponse(),
	)
	c := newTestClient(t, tr, sink, clock)
	c.cfg.Retry = jitterFree(11)

	h := c.Stream(context.Background(), Request{URL: "http://x", Headers: map[string]string{"x-request-id": "r1"}})
	if got := drainText(h); got != "hello" {
		t.Fatalf("text: %q", got)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if tr.calls != 4 {
		t.Fatalf("attempt count: got %d want 4", tr.calls)
	}
	// base 2000ms, factor 2, no jitter: attempts 1..3 -> 2000, 4000, 8000.
	want := []int64{2000, 4000, 8000}
	got := retryDelays(sink)
	if len(got) != len(want) {
		t.Fatalf("delays: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delays: got %v want %v", got, want)
		}
	}
	if elapsed := clock.elapsed().Milliseconds(); elapsed != 14000 {
		t.Fatalf("elapsed: got %d want 14000", elapsed)
	}
}

// TestRetryWithFakeClockNetworkReset covers the transport-error path: a socket
// reset before any output is classified as a retryable network failure.
func TestRetryWithFakeClockNetworkReset(t *testing.T) {
	sink := &captureSink{}
	clock := newRecordingClock()
	reset := NewAttrError(ErrorAttrs{Code: "ECONNRESET", Message: "socket hang up"})
	tr := &fakeTransport{handlers: []func() (*Response, error){
		func() (*Response, error) { return nil, reset },
		func() (*Response, error) { return nil, reset },
		func() (*Response, error) { return textResponse(), nil },
	}}
	c := newTestClient(t, tr, sink, clock)
	c.cfg.Retry = jitterFree(11)

	h := c.Stream(context.Background(), Request{URL: "http://x", Headers: map[string]string{"x-request-id": "r1"}})
	if got := drainText(h); got != "hello" {
		t.Fatalf("text: %q", got)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if tr.calls != 3 {
		t.Fatalf("attempt count: got %d want 3", tr.calls)
	}
	delays := retryDelays(sink)
	if len(delays) != 2 || delays[0] != 2000 || delays[1] != 4000 {
		t.Fatalf("delays: got %v want [2000 4000]", delays)
	}
	// The failure must have been classified as a retryable network error.
	sink.mu.Lock()
	var failures []StatusEvent
	for _, ev := range sink.events {
		if ev.Status == "model_request_failed" {
			failures = append(failures, ev)
		}
	}
	sink.mu.Unlock()
	if len(failures) != 2 {
		t.Fatalf("expected 2 failure events, got %d", len(failures))
	}
	for _, ev := range failures {
		if ev.Reason != ReasonNetworkError || !ev.Retryable || ev.StatusCode != nil {
			t.Fatalf("network failure event: %+v", ev)
		}
	}
}

// TestRetryWithFakeClockExhaustsBudget pins the attempt cap: the request is
// attempted maxAttempts times and then fails terminally.
func TestRetryWithFakeClockExhaustsBudget(t *testing.T) {
	sink := &captureSink{}
	clock := newRecordingClock()
	rateLimited := &Response{
		StatusCode: 429,
		StatusText: "Too Many Requests",
		BodyText:   `{"error":{"message":"slow down"}}`,
	}
	// Every attempt fails; maxAttempts is 3.
	failures := []*Response{rateLimited, rateLimited, rateLimited}
	tr := setupFailureResponses(failures, nil)
	c := newTestClient(t, tr, sink, clock)
	c.cfg.Retry = jitterFree(3)

	h := c.Stream(context.Background(), Request{URL: "http://x", Headers: map[string]string{"x-request-id": "r1"}})
	_ = drainText(h)
	err := h.Wait()
	if err == nil {
		t.Fatal("expected a terminal error")
	}
	failure, ok := FailureOf(err)
	if !ok {
		t.Fatalf("terminal error is not an AttemptError: %v", err)
	}
	if failure.Code != CodeModelRateLimited || !failure.Retryable {
		t.Fatalf("terminal failure: %+v", failure)
	}
	if tr.calls != 3 {
		t.Fatalf("attempt count: got %d want 3 (maxAttempts)", tr.calls)
	}
	// Only two retries are scheduled; the third attempt is the last one.
	if delays := retryDelays(sink); len(delays) != 2 {
		t.Fatalf("delays: got %v want 2 retries", delays)
	}
}

// TestRetryWithFakeClockTerminalIsNotRetried proves the retry decision is the
// classifier's, not the loop's: a 401 is terminal so no delay is ever scheduled.
func TestRetryWithFakeClockTerminalIsNotRetried(t *testing.T) {
	sink := &captureSink{}
	clock := newRecordingClock()
	unauthorized := &Response{
		StatusCode: 401,
		StatusText: "Unauthorized",
		BodyText:   `{"error":{"message":"bad key"}}`,
	}
	tr := setupFailureResponses([]*Response{unauthorized}, textResponse())
	c := newTestClient(t, tr, sink, clock)
	c.cfg.Retry = jitterFree(11)

	h := c.Stream(context.Background(), Request{URL: "http://x", Headers: map[string]string{"x-request-id": "r1"}})
	_ = drainText(h)
	if err := h.Wait(); err == nil {
		t.Fatal("expected a terminal error")
	}
	if tr.calls != 1 {
		t.Fatalf("attempt count: got %d want 1 (401 is terminal)", tr.calls)
	}
	if delays := retryDelays(sink); len(delays) != 0 {
		t.Fatalf("no retry should be scheduled, got delays %v", delays)
	}
	if clock.elapsed() != 0 {
		t.Fatalf("clock advanced %v with no retry", clock.elapsed())
	}
}

// TestRetryWithFakeClockCancelledContext proves a cancelled context stops the
// loop instead of burning the remaining budget.
func TestRetryWithFakeClockCancelledContext(t *testing.T) {
	sink := &captureSink{}
	clock := newRecordingClock()
	clock.sleepErr = context.Canceled
	rateLimited := &Response{StatusCode: 429, StatusText: "Too Many Requests"}
	tr := setupFailureResponses([]*Response{rateLimited}, textResponse())
	c := newTestClient(t, tr, sink, clock)
	c.cfg.Retry = jitterFree(11)

	ctx, cancel := context.WithCancel(context.Background())
	h := c.Stream(ctx, Request{URL: "http://x", Headers: map[string]string{"x-request-id": "r1"}})
	_ = drainText(h)
	err := h.Wait()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if tr.calls != 1 {
		t.Fatalf("attempt count: got %d want 1", tr.calls)
	}
	if delays := retryDelays(sink); len(delays) != 1 {
		t.Fatalf("one retry should have been scheduled before the sleep: %v", delays)
	}
	cancel()
}

// TestRetryWithFakeClockBackoffCap pins the 60s ceiling of the delay curve.
func TestRetryWithFakeClockBackoffCap(t *testing.T) {
	opts := jitterFree(11)
	// attempt 7 -> 2000 * 2^6 = 128000ms, capped to 60000.
	if got := CalculateRetryDelay(opts, 7, nil, nil); got != DefaultRetryMaxDelayMs {
		t.Fatalf("capped delay: got %d want %d", got, DefaultRetryMaxDelayMs)
	}
	// An absurd Retry-After is ignored in favour of the cap.
	absurd := int64(10 * 60 * 1000)
	if got := CalculateRetryDelay(opts, 7, &absurd, nil); got != DefaultRetryMaxDelayMs {
		t.Fatalf("absurd retry-after: got %d want %d", got, DefaultRetryMaxDelayMs)
	}
	// A modest Retry-After under the curve wins.
	modest := int64(5000)
	if got := CalculateRetryDelay(opts, 1, &modest, nil); got != modest {
		t.Fatalf("retry-after: got %d want %d", got, modest)
	}
}

// TestRetryWithFakeClockEmptyCompletion covers the empty-completion retry path
// (empty-completion-retry.ts): a turn that produced no text, no tool call and no
// usage is retried exactly once, using the backoff curve, and is terminal after
// that.
func TestRetryWithFakeClockEmptyCompletion(t *testing.T) {
	// Empty completion: the provider finishes cleanly with nothing in the body.
	empty := &Response{
		StatusCode: 200,
		Stream: &fakeStream{chunks: [][]byte{[]byte(
			"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n")}},
	}
	text := textResponse()

	if !CanRetryEmptyCompletion(false, 1, 11, 0) {
		t.Fatal("first empty completion should be retryable")
	}
	if CanRetryEmptyCompletion(false, 1, 11, 1) {
		t.Fatal("second empty completion must not be retried (max 1)")
	}
	if CanRetryEmptyCompletion(false, 1, 1, 0) {
		t.Fatal("an exhausted attempt budget must not retry")
	}
	if CanRetryEmptyCompletion(true, 1, 11, 0) {
		t.Fatal("an aborted request must not retry")
	}

	failure := EmptyCompletionFailure()
	if failure.Code != CodeInvalidModelResponse || failure.Message != EmptyCompletionMessage {
		t.Fatalf("empty completion failure: %+v", failure)
	}
	if !failure.Retryable || failure.Reason != ReasonUnknown || failure.RetryReason != ReasonServerError {
		t.Fatalf("empty completion is retryable via server_error: %+v", failure)
	}
	// The synthetic failure is retryable end to end.
	if !IsRetryableFailure(failure) {
		t.Fatal("empty completion failure should be retryable")
	}
	// It has no Retry-After, so the delay comes from the curve.
	if got := CalculateRetryDelay(jitterFree(11), 1, nil, nil); got != DefaultRetryBaseDelayMs {
		t.Fatalf("empty completion delay: got %d want %d", got, DefaultRetryBaseDelayMs)
	}

	// The Stream loop itself does not judge emptiness: a clean EOF with no
	// content is a completed turn as far as the transport is concerned, and no
	// backoff is spent inside it. Detecting "no text, no tool calls, no usage"
	// and deciding to re-run the turn belongs to the caller's turn loop, which
	// gates on CanRetryEmptyCompletion + EmptyCompletionFailure above.
	clock := newRecordingClock()
	sink := &captureSink{}
	tr := setupFailureResponses([]*Response{empty, empty}, text)
	c := newTestClient(t, tr, sink, clock)
	c.cfg.Retry = jitterFree(11)

	h := c.Stream(context.Background(), Request{URL: "http://x", Headers: map[string]string{"x-request-id": "r1"}})
	_ = drainText(h)
	if err := h.Wait(); err != nil {
		t.Fatalf("a clean empty stream is not a transport error: %v", err)
	}
	if tr.calls != 1 {
		t.Fatalf("attempt count: got %d want 1 (empty completion is not retried in Stream)", tr.calls)
	}
	if got := clock.elapsed(); got != 0 {
		t.Fatalf("no backoff should be spent inside Stream: %v", got)
	}
	if h.Usage() != nil {
		t.Fatalf("empty completion should carry no usage: %+v", h.Usage())
	}
	if h.FinishReason() != FinishStop {
		t.Fatalf("empty completion still finishes normally: %q", h.FinishReason())
	}
}

// TestRetryWithFakeClockSleepErrorIsReturned keeps a clock that fails to sleep
// from being silently ignored.
func TestRetryWithFakeClockSleepErrorIsReturned(t *testing.T) {
	sink := &captureSink{}
	clock := newRecordingClock()
	clock.sleepErr = errors.New("clock failure")
	serverError := &Response{StatusCode: 503, StatusText: "Service Unavailable"}
	tr := setupFailureResponses([]*Response{serverError}, textResponse())
	c := newTestClient(t, tr, sink, clock)
	c.cfg.Retry = jitterFree(11)

	h := c.Stream(context.Background(), Request{URL: "http://x", Headers: map[string]string{"x-request-id": "r1"}})
	_ = drainText(h)
	err := h.Wait()
	if err == nil || !strings.Contains(err.Error(), "clock failure") {
		t.Fatalf("expected the sleep error to surface, got %v", err)
	}
	if tr.calls != 1 {
		t.Fatalf("attempt count: got %d want 1", tr.calls)
	}
}
