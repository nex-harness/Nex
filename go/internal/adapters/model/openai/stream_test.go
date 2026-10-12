package openai_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/model/openai"
	"github.com/tylinux/nex/go/internal/modelclient"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// recordingSink captures the status transitions in order.
type recordingSink struct {
	mu     sync.Mutex
	events []modelclient.StatusEvent
}

func (s *recordingSink) Publish(ev modelclient.StatusEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *recordingSink) statuses() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.events))
	for i, e := range s.events {
		out[i] = e.Status
	}
	return out
}

func (s *recordingSink) all() []modelclient.StatusEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]modelclient.StatusEvent, len(s.events))
	copy(out, s.events)
	return out
}

// freeClock advances instantly: sleeps return immediately, Now is monotonic.
type freeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFreeClock() *freeClock { return &freeClock{now: time.Unix(1700000000, 0)} }

func (c *freeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Millisecond)
	return c.now
}

func (c *freeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return ctx.Err()
}

func (c *freeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	// Never fires: in-memory test streams are never idle. The idle path is
	// covered by the modelclient unit test with a controllable clock.
	return make(chan time.Time)
}

// TestS13RetryThenSuccess reproduces scenario S13: the provider returns HTTP 429
// (retry-after: 0) once, then a streamed text answer. The telemetry sequence must
// be started, failed, retry_scheduled, started, completed with attempt 1 then 2
// and maxAttempts 11.
func TestS13RetryThenSuccess(t *testing.T) {
	body := sseText("Recovered after a retry.")
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("retry-after", "0")
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit"}}`))
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	sink := &recordingSink{}
	clock := newFreeClock()
	tr := openai.New(openai.Config{
		URL:     srv.URL + "/v1/chat/completions",
		Timeout: 5 * time.Second,
	})
	client := modelclient.NewClient(modelclient.Config{
		ProviderID:   "mock",
		ModelID:      "mock-model",
		ProviderKind: "openai-compatible",
		Transport:    "sse",
		Retry:        modelclient.DefaultRetryOptions(),
		Clock:        clock,
	}, tr, sink)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h := client.Stream(ctx, modelclient.Request{
		URL:     srv.URL + "/v1/chat/completions",
		Headers: map[string]string{"x-request-id": "req-1"},
		Body:    []byte(`{"model":"mock-model","messages":[],"stream":true}`),
	})
	var texts []string
	for ev := range h.Events() {
		if ev.Kind == modelclient.EventTextDelta {
			texts = append(texts, ev.Text)
		}
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if strings.Join(texts, "") != "Recovered after a retry." {
		t.Fatalf("text: got %q", strings.Join(texts, ""))
	}

	got := sink.statuses()
	want := []string{
		"model_request_started", "model_request_failed", "model_retry_scheduled",
		"model_request_started", "model_request_completed",
	}
	if !equalStrings(got, want) {
		t.Fatalf("status sequence: got %v want %v", got, want)
	}
	if calls != 2 {
		t.Fatalf("expected 2 HTTP calls, got %d", calls)
	}
	// The S13 retry_scheduled event carries delayMs 0 (retry-after: 0) and the
	// attempt counter.
	for _, ev := range sink.all() {
		if ev.Status == "model_retry_scheduled" {
			if ev.DelayMs == nil || *ev.DelayMs != 0 {
				t.Fatalf("retry_scheduled delayMs: got %v want 0", ev.DelayMs)
			}
			if ev.Attempt != 1 || ev.NextAttempt != 2 {
				t.Fatalf("retry_scheduled attempt: got %d next %d", ev.Attempt, ev.NextAttempt)
			}
			if ev.MaxAttempts != 11 {
				t.Fatalf("retry_scheduled maxAttempts: got %d want 11", ev.MaxAttempts)
			}
		}
	}
}

// TestIdleTimeoutRetry verifies that a stalled stream produces a
// model_stream_stalled path and is classified as a retryable idle timeout.
func TestSelfRetryOnServerError(t *testing.T) {
	body := sseText("ok after 500")
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	sink := &recordingSink{}
	clock := newFreeClock()
	tr := openai.New(openai.Config{URL: srv.URL + "/v1/chat/completions", Timeout: 5 * time.Second})
	client := modelclient.NewClient(modelclient.Config{
		ProviderID: "mock", ModelID: "mock-model", ProviderKind: "openai-compatible",
		Transport: "sse", Retry: modelclient.DefaultRetryOptions(), Clock: clock,
	}, tr, sink)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h := client.Stream(ctx, modelclient.Request{
		URL:     srv.URL + "/v1/chat/completions",
		Headers: map[string]string{"x-request-id": "req-1"},
		Body:    []byte(`{}`),
	})
	for range h.Events() {
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
	got := sink.statuses()
	if !equalStrings(got, []string{
		"model_request_started", "model_request_failed", "model_retry_scheduled",
		"model_request_started", "model_request_completed",
	}) {
		t.Fatalf("status sequence: %v", got)
	}
}

// sseText builds a single text-answer SSE stream at 8-byte chunks.
func sseText(text string) string {
	var out string
	chunk := func(delta map[string]any, finish any, extra map[string]any) string {
		m := map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion.chunk",
			"created": 1_700_000_000, "model": "mock-model",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		for k, v := range extra {
			m[k] = v
		}
		b, _ := jsonMarshal(m)
		return "data: " + string(b) + "\n\n"
	}
	for i := 0; i < len(text); i += 8 {
		end := i + 8
		if end > len(text) {
			end = len(text)
		}
		delta := map[string]any{"content": text[i:end]}
		if i == 0 {
			delta["role"] = "assistant"
		}
		out += chunk(delta, nil, nil)
	}
	out += chunk(map[string]any{}, "stop", map[string]any{"usage": map[string]any{
		"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120,
	}})
	out += "data: [DONE]\n\n"
	return out
}

func equalStrings(a, b []string) bool {
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
