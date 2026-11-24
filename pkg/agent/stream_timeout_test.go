package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
)

type streamTimeoutMockProvider struct {
	mu      sync.Mutex
	calls   int
	handler func(callIdx int, ctx context.Context, ch chan<- ai.StreamEvent)
}

func (m *streamTimeoutMockProvider) Driver() ai.Driver { return ai.DriverAnthropic }

func (m *streamTimeoutMockProvider) Stream(ctx context.Context, _ *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
	m.mu.Lock()
	idx := m.calls
	m.calls++
	fn := m.handler
	m.mu.Unlock()

	ch := make(chan ai.StreamEvent, 16)
	go func() {
		defer close(ch)
		fn(idx, ctx, ch)
	}()
	return ch, nil
}

func (m *streamTimeoutMockProvider) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func userMsgPtr(text string) *ai.Message {
	m := ai.NewUserTextMessage(text)
	return &m
}

func TestLoopStreamIdleTimeoutRetriesBeforeFirstTokenAndSucceeds(t *testing.T) {
	prov := &streamTimeoutMockProvider{
		handler: func(callIdx int, ctx context.Context, ch chan<- ai.StreamEvent) {
			if callIdx == 0 {
				// Simulate upstream 200 OK hang before first token until canceled by idle watchdog.
				<-ctx.Done()
				ch <- ai.StreamEvent{Type: ai.StreamEventError, Err: ctx.Err()}
				return
			}
			msg := &ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock("recovered after retry")}}
			ch <- ai.StreamEvent{Type: ai.StreamEventTextDelta, TextDelta: "recovered after retry"}
			ch <- ai.StreamEvent{Type: ai.StreamEventDone, Message: msg, StopReason: ai.StopReasonEndTurn}
		},
	}

	loop := NewLoop(LoopConfig{
		SessionID:         "sess_idle_retry",
		Model:             "claude-sonnet-4-5",
		Provider:          prov,
		MaxRetries:        2,
		RetryBaseDelay:    5 * time.Millisecond,
		StreamIdleTimeout: 40 * time.Millisecond,
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	msg, err := loop.Run(ctx, userMsgPtr("hello"))
	if err != nil {
		t.Fatalf("expected retry recovery to succeed, got err=%v", err)
	}
	if prov.CallCount() != 2 {
		t.Fatalf("expected 2 provider calls (1 idle hang + 1 retry), got %d", prov.CallCount())
	}
	if msg == nil || len(msg.Content) == 0 || msg.Content[0].Text != "recovered after retry" {
		t.Fatalf("unexpected assistant message: %+v", msg)
	}
}

func TestLoopStreamIdleTimeoutDoesNotKillActiveLongStream(t *testing.T) {
	prov := &streamTimeoutMockProvider{
		handler: func(_ int, ctx context.Context, ch chan<- ai.StreamEvent) {
			parts := []string{"chunk1 ", "chunk2 ", "chunk3 ", "chunk4 ", "chunk5"}
			full := ""
			for _, p := range parts {
				select {
				case <-time.After(20 * time.Millisecond):
				case <-ctx.Done():
					ch <- ai.StreamEvent{Type: ai.StreamEventError, Err: ctx.Err()}
					return
				}
				full += p
				ch <- ai.StreamEvent{Type: ai.StreamEventTextDelta, TextDelta: p}
			}
			msg := &ai.Message{Role: ai.RoleAssistant, Content: []ai.ContentBlock{ai.TextBlock(full)}}
			ch <- ai.StreamEvent{Type: ai.StreamEventDone, Message: msg, StopReason: ai.StopReasonEndTurn}
		},
	}

	// Total stream time ~100ms > StreamIdleTimeout (45ms), but each inter-chunk gap is only 20ms.
	loop := NewLoop(LoopConfig{
		SessionID:         "sess_active_long",
		Model:             "claude-sonnet-4-5",
		Provider:          prov,
		MaxRetries:        1,
		StreamIdleTimeout: 45 * time.Millisecond,
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	msg, err := loop.Run(ctx, userMsgPtr("stream slowly"))
	if err != nil {
		t.Fatalf("expected active long stream to complete without idle timeout, got err=%v", err)
	}
	if prov.CallCount() != 1 {
		t.Fatalf("expected single provider call, got %d", prov.CallCount())
	}
	if msg == nil || len(msg.Content) == 0 || msg.Content[0].Text != "chunk1 chunk2 chunk3 chunk4 chunk5" {
		t.Fatalf("unexpected streamed text: %+v", msg)
	}
}

func TestLoopStreamIdleTimeoutMidStreamStopsWithErrorWithoutRetry(t *testing.T) {
	prov := &streamTimeoutMockProvider{
		handler: func(_ int, ctx context.Context, ch chan<- ai.StreamEvent) {
			ch <- ai.StreamEvent{Type: ai.StreamEventTextDelta, TextDelta: "partial output "}
			<-ctx.Done()
			ch <- ai.StreamEvent{Type: ai.StreamEventError, Err: ctx.Err()}
		},
	}

	var mu sync.Mutex
	var events []Event
	loop := NewLoop(LoopConfig{
		SessionID:         "sess_midstream_hang",
		Model:             "claude-sonnet-4-5",
		Provider:          prov,
		MaxRetries:        2,
		RetryBaseDelay:    5 * time.Millisecond,
		StreamIdleTimeout: 40 * time.Millisecond,
		OnEvent: func(ev Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := loop.Run(ctx, userMsgPtr("hang midstream"))
	if !errors.Is(err, ErrStreamIdleTimeout) {
		t.Fatalf("expected error wrapping ErrStreamIdleTimeout, got %v", err)
	}
	if prov.CallCount() != 1 {
		t.Fatalf("expected mid-stream idle timeout NOT to retry (preventing duplicate text), got %d calls", prov.CallCount())
	}

	mu.Lock()
	defer mu.Unlock()
	var sawTurnErr string
	for _, ev := range events {
		if ev.Type == EventTurnEnd && ev.Error != "" {
			sawTurnErr = ev.Error
		}
	}
	if sawTurnErr == "" {
		t.Fatalf("expected EventTurnEnd to carry stream error, got events=%+v", events)
	}
}
