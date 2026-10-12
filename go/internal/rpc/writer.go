package rpc

import (
	"context"
	"errors"
	"sync"
	"time"
)

// This file is the connection's write path.
//
// It is a single writer goroutine draining a BOUNDED queue rather than a mutex around
// Socket.Write, and the reason is back-pressure. A mutex would be correct for ordering but
// wrong for liveness: a peer that stops reading parks one Write while every other writer —
// every unrelated Promise response, every other listener — piles up behind the lock, so one
// stalled subscription stops the whole connection from making progress. With a bounded queue
// the failure is contained and observable instead: the queue fills, the connection is closed
// with ErrWriteBackpressure, and the client learns why.
//
// The goroutine is the only writer, which is what keeps frames from interleaving — the
// property a mutex provided for free.

// DefaultOutboundQueueSize is how many framed responses may wait to be written before the
// connection is declared stuck. Sized well above EventBufferSize so a healthy but briefly
// slow client is never disconnected, and well below anything that would matter as memory:
// each entry is one frame the server had already produced.
const DefaultOutboundQueueSize = 256

// ErrWriteBackpressure is why the connection was torn down when the outbound queue stayed
// full. It means the peer stopped reading: writes queued but did not drain, so continuing
// would either grow memory without bound or wedge every other request behind one slow
// reader. Closing is the documented outcome — the client reconnects, which the web client
// already handles.
var ErrWriteBackpressure = errors.New("rpc: outbound queue overflowed; peer is not reading")

// DefaultEnqueueTimeout bounds how long a producer waits for room in the outbound queue
// before the connection is declared back-pressured. It must be long enough that a healthy
// but briefly slow peer is never disconnected, and short enough that a dead peer is noticed
// in seconds rather than holding goroutines indefinitely.
const DefaultEnqueueTimeout = 5 * time.Second

// writer owns the socket's write side for one connection.
type writer struct {
	queue     chan []byte
	done      chan struct{}
	closeOnce sync.Once
	cancel    context.CancelFunc
	// queueTimeout is how long enqueue waits for room before giving up.
	queueTimeout time.Duration

	// onOverflow runs once, when a producer could not be handed off in time. It is the
	// connection's abort path, so it is separated from the queue mechanics here.
	onOverflow func()
}

// newWriter starts the writer goroutine. The goroutine exits when its context is cancelled
// (which the Server does on Close) or when the socket write fails.
func newWriter(ctx context.Context, sock Socket, capacity int, enqueueTimeout time.Duration, onOverflow func()) *writer {
	if capacity <= 0 {
		capacity = DefaultOutboundQueueSize
	}
	if enqueueTimeout <= 0 {
		enqueueTimeout = DefaultEnqueueTimeout
	}
	writeCtx, cancel := context.WithCancel(ctx)
	w := &writer{
		queue:        make(chan []byte, capacity),
		done:         make(chan struct{}),
		cancel:       cancel,
		queueTimeout: enqueueTimeout,
		onOverflow:   onOverflow,
	}
	go func() {
		defer close(w.done)
		defer cancel()
		for {
			select {
			case <-writeCtx.Done():
				return
			case frame := <-w.queue:
				if err := sock.Write(writeCtx, frame); err != nil {
					return
				}
			}
		}
	}()
	return w
}

// enqueue hands a frame to the writer.
//
// It blocks until the frame is queued, the writer stops, abandon fires, or the enqueue
// deadline expires. Blocking is deliberate: it is how back-pressure reaches the producer. A
// delivery goroutine that waits here stops draining its listener's queue, so a listener whose
// peer is slow overflows ITS bounded buffer and is closed with the documented error — the
// failure is then attributed to the slow subscription instead of silently accumulating frames.
//
// `abandon` is the caller's own escape hatch. Without it a delivery goroutine would be
// pinned in enqueue for the whole deadline, unable to notice that its subscription already
// ended; with it, a disposed or overflowed listener stops waiting at once.
//
// The deadline is what keeps a dead peer from parking producers forever: past it the frame
// is dropped and the connection is aborted with ErrWriteBackpressure, which is the only
// correct outcome once waiting has stopped helping.
func (w *writer) enqueue(frame []byte, abandon <-chan struct{}) {
	select {
	case <-w.done:
		return // writer already stopped; the frame has nowhere to go
	default:
	}
	timer := time.NewTimer(w.queueTimeout)
	defer timer.Stop()
	select {
	case w.queue <- frame:
	case <-w.done:
	case <-abandon:
	case <-timer.C:
		w.closeOnce.Do(func() {
			if w.onOverflow != nil {
				w.onOverflow()
			}
		})
	}
}

// stop ends the writer goroutine. It is idempotent and never waits, because a writer parked
// in a socket write must not be able to block connection teardown.
func (w *writer) stop() {
	w.cancel()
}
