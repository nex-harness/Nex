package browserws

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nex-harness/nex/server/internal/local"
	"github.com/nex-harness/nex/server/internal/runners"
)

func testConnection(t *testing.T, queueSize int) *connection {
	t.Helper()
	return &connection{
		deps: Deps{
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			Registry: runners.NewRegistry(slog.New(slog.NewTextHandler(io.Discard, nil))),
			Local:    local.NewRegistry(),
		},
		id:       "test",
		outbound: make(chan []byte, queueSize),
		done:     make(chan struct{}),
		listens:  make(map[uint32]func()),
		pending:  make(map[uint32]*pendingPromise),
	}
}

// A slow browser (writer never draining) must be closed on overflow instead of
// blocking the producer. enqueue is called from runner event pumps.
func TestEnqueueOverflowClosesWithoutBlocking(t *testing.T) {
	c := testConnection(t, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			c.enqueue(make([]byte, 16))
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueue blocked on a full outbound queue")
	}

	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if !closed {
		t.Fatal("overflow did not close the connection")
	}
}

func TestEnqueueRejectsOversizedFrame(t *testing.T) {
	c := testConnection(t, 4)
	c.enqueue(make([]byte, maxOutboundFrameBytes+1))
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if !closed {
		t.Fatal("oversized frame did not close the connection")
	}
	if len(c.outbound) != 0 {
		t.Fatal("oversized frame was queued")
	}
}
