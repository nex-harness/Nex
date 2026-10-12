package rpctest

import (
	"context"
	"sync"
	"time"
)

// Recorder observes the bytes an endpoint writes without consuming them, so a test can
// assert on the frames one side produced while both endpoints keep working. It is the write
// side only: reading is untouched, because the endpoint's own reader is the consumer of its
// inbound bytes.
type Recorder struct {
	// inner is the endpoint whose Write is wrapped.
	inner *Endpoint

	mu    sync.Mutex
	wrote [][]byte

	closed chan struct{}
	once   sync.Once
}

// Record wraps an endpoint so every Write is also copied into an in-memory log. The returned
// Recorder is the endpoint's Socket: pass it where the real endpoint would go.
func Record(inner *Endpoint) *Recorder {
	return &Recorder{inner: inner, closed: make(chan struct{})}
}

// Read delegates to the wrapped endpoint.
func (r *Recorder) Read(ctx context.Context) ([]byte, error) { return r.inner.Read(ctx) }

// Write delegates and then records. The copy happens after a successful write so the log
// contains exactly the frames that reached the wire.
func (r *Recorder) Write(ctx context.Context, data []byte) error {
	if err := r.inner.Write(ctx, data); err != nil {
		return err
	}
	r.mu.Lock()
	r.wrote = append(r.wrote, append([]byte(nil), data...))
	r.mu.Unlock()
	return nil
}

// Close closes the wrapped endpoint and stops any waiter.
func (r *Recorder) Close() error {
	r.once.Do(func() { close(r.closed) })
	return r.inner.Close()
}

// Frames returns everything written so far and drains the log, so each caller sees only what
// it caused. It waits up to timeout for the first frame to appear.
func (r *Recorder) Frames(timeout time.Duration) [][]byte {
	deadline := time.Now().Add(timeout)
	for {
		r.mu.Lock()
		count := len(r.wrote)
		r.mu.Unlock()
		if count > 0 || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.wrote
	r.wrote = nil
	return out
}

// Payloads returns the body of every frame written so far and drains the log, so each caller
// sees only what it caused. The 13-byte framing header is stripped: the useful content of an
// RPC frame is its serialized payload, and leaving the header on would make every consumer
// re-implement the framing. It waits up to timeout for the first frame to appear.
func (r *Recorder) Payloads(timeout time.Duration) [][]byte {
	return payloads(r.Frames(timeout))
}

// payloads strips the framing header from each frame, dropping any too short to hold one.
func payloads(frames [][]byte) [][]byte {
	const headerSize = 13
	out := make([][]byte, 0, len(frames))
	for _, frame := range frames {
		if len(frame) < headerSize {
			continue
		}
		out = append(out, frame[headerSize:])
	}
	return out
}
