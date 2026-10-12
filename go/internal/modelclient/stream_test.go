package modelclient

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStream serves fixed chunks, then an optional terminal error.
type fakeStream struct {
	chunks [][]byte
	err    error
	read   int
}

func (s *fakeStream) Read(p []byte) (int, error) {
	if s.read >= len(s.chunks) {
		if s.err != nil {
			return 0, s.err
		}
		return 0, io.EOF
	}
	n := copy(p, s.chunks[s.read])
	s.read++
	return n, nil
}
func (s *fakeStream) Close() error { return nil }

type fakeTransport struct {
	mu       sync.Mutex
	handlers []func() (*Response, error)
	calls    int
}

func (t *fakeTransport) Do(ctx context.Context, req Request) (*Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	idx := t.calls
	t.calls++
	if len(t.handlers) == 0 {
		return nil, errors.New("no handler")
	}
	if idx >= len(t.handlers) {
		idx = len(t.handlers) - 1
	}
	return t.handlers[idx]()
}

func sseStream(text string) *fakeStream {
	return &fakeStream{chunks: [][]byte{[]byte(text)}}
}

const textSSE = "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n" +
	"data: [DONE]\n\n"

type captureSink struct {
	mu     sync.Mutex
	events []StatusEvent
}

func (s *captureSink) Publish(ev StatusEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}
func (s *captureSink) statuses() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.events))
	for i, e := range s.events {
		out[i] = e.Status
	}
	return out
}

type stepClock struct {
	mu   sync.Mutex
	now  time.Time
	fire chan time.Time // optional idle fire
}

func newStepClock() *stepClock { return &stepClock{now: time.Unix(1700000000, 0)} }
func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *stepClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return ctx.Err()
}
func (c *stepClock) After(d time.Duration) <-chan time.Time {
	if c.fire != nil {
		return c.fire
	}
	return make(chan time.Time)
}

func newTestClient(t *testing.T, tr Transport, sink StatusSink, clock Clock) *Client {
	return NewClient(Config{
		ProviderID:   "mock",
		ModelID:      "mock-model",
		ProviderKind: "openai-compatible",
		Transport:    "sse",
		Retry:        DefaultRetryOptions(),
		Clock:        clock,
	}, tr, sink)
}

func drainText(h *StreamHandle) string {
	var b strings.Builder
	for ev := range h.Events() {
		if ev.Kind == EventTextDelta {
			b.WriteString(ev.Text)
		}
	}
	return b.String()
}

func TestStreamHappyPathTelemetry(t *testing.T) {
	sink := &captureSink{}
	tr := &fakeTransport{handlers: []func() (*Response, error){
		func() (*Response, error) {
			return &Response{StatusCode: 200, Stream: sseStream(textSSE)}, nil
		},
	}}
	c := newTestClient(t, tr, sink, newStepClock())
	h := c.Stream(context.Background(), Request{URL: "http://x", Headers: map[string]string{"x-request-id": "r1"}})
	if got := drainText(h); got != "hello" {
		t.Fatalf("text: %q", got)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	// S02/S03 record only started then completed on the happy path.
	want := []string{"model_request_started", "model_request_completed"}
	if !eqStatuses(sink.statuses(), want) {
		t.Fatalf("statuses: got %v want %v", sink.statuses(), want)
	}
	if h.Usage() == nil || h.Usage().TotalTokens == nil || *h.Usage().TotalTokens != 2 {
		t.Fatalf("usage not captured: %+v", h.Usage())
	}
	if h.FinishReason() != FinishStop {
		t.Fatalf("finish: %q", h.FinishReason())
	}
}

func TestStreamIdleTimeoutIsRetryable(t *testing.T) {
	sink := &captureSink{}
	clock := newStepClock()
	// First attempt never yields bytes and the idle timer fires; second succeeds.
	clock.fire = make(chan time.Time)
	tr := &fakeTransport{handlers: []func() (*Response, error){
		func() (*Response, error) {
			return &Response{StatusCode: 200, Stream: &blockingStream{}}, nil
		},
		func() (*Response, error) {
			return &Response{StatusCode: 200, Stream: sseStream(textSSE)}, nil
		},
	}}
	c := newTestClient(t, tr, sink, clock)
	h := c.Stream(context.Background(), Request{URL: "http://x", Headers: map[string]string{"x-request-id": "r1"}})
	// Fire the idle timeout once.
	clock.fire <- clock.Now()
	got := drainText(h)
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got != "hello" {
		t.Fatalf("text after retry: %q", got)
	}
	statuses := sink.statuses()
	// started, failed(idle), retry_scheduled, started, completed
	if !eqStatuses(statuses, []string{
		"model_request_started", "model_request_failed", "model_retry_scheduled",
		"model_request_started", "model_request_completed",
	}) {
		t.Fatalf("statuses: %v", statuses)
	}
	for _, ev := range sink.events {
		if ev.Status == "model_request_failed" {
			if ev.Reason != ReasonStreamIdleTimeout {
				t.Fatalf("idle failure reason: %q", ev.Reason)
			}
			if !ev.Retryable {
				t.Fatal("idle timeout should be retryable")
			}
		}
	}
}

type blockingStream struct{}

func (s *blockingStream) Read(p []byte) (int, error) {
	select {} // block forever
}
func (s *blockingStream) Close() error { return nil }

func TestStreamRetryBoundaryBlocksReplay(t *testing.T) {
	sink := &captureSink{}
	// First attempt emits text then a mid-stream error; visible output committed
	// means no retry, even for a retryable network code.
	partial := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"
	tr := &fakeTransport{handlers: []func() (*Response, error){
		func() (*Response, error) {
			return &Response{StatusCode: 200, Stream: &fakeStream{chunks: [][]byte{[]byte(partial)}, err: NewAttrError(ErrorAttrs{Code: "ECONNRESET", Message: "reset"})}}, nil
		},
	}}
	c := newTestClient(t, tr, sink, newStepClock())
	h := c.Stream(context.Background(), Request{URL: "http://x", Headers: map[string]string{"x-request-id": "r1"}})
	_ = drainText(h)
	err := h.Wait()
	if err == nil {
		t.Fatal("expected terminal error")
	}
	if tr.calls != 1 {
		t.Fatalf("expected 1 call (no replay after visible output), got %d", tr.calls)
	}
	statuses := sink.statuses()
	if !eqStatuses(statuses, []string{"model_request_started", "model_request_failed"}) {
		t.Fatalf("statuses: %v", statuses)
	}
}

func TestStreamRetriesWhenOnlyPreludeCommitted(t *testing.T) {
	sink := &captureSink{}
	// First attempt errors before any visible content; retry is allowed.
	tr := &fakeTransport{handlers: []func() (*Response, error){
		func() (*Response, error) {
			return &Response{StatusCode: 200, Stream: &fakeStream{err: NewAttrError(ErrorAttrs{Code: "ECONNRESET", Message: "reset"})}}, nil
		},
		func() (*Response, error) {
			return &Response{StatusCode: 200, Stream: sseStream(textSSE)}, nil
		},
	}}
	c := newTestClient(t, tr, sink, newStepClock())
	h := c.Stream(context.Background(), Request{URL: "http://x", Headers: map[string]string{"x-request-id": "r1"}})
	got := drainText(h)
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got != "hello" {
		t.Fatalf("text: %q", got)
	}
	if tr.calls != 2 {
		t.Fatalf("expected 2 calls, got %d", tr.calls)
	}
}

func eqStatuses(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
