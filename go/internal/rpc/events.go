package rpc

import "sync"

// This file is the Go transcription of the Emitter/Event part of
// packages/rpc/src/foundation.ts that a channel server needs, plus the bounded queue the TS
// does not have because a JS emitter calls its listeners synchronously and a slow listener
// can only block its caller.
//
// Ownership: the Server owns every listener. A channel's Listen returns a Go channel plus an
// unsubscribe func; the Server copies events off that channel with one goroutine per
// listener and is the only writer to the connection, so no send ever contends with the read
// loop or with another listener.

// EventBufferSize is the per-listener queue depth. It is deliberately small: a client that
// stops reading its socket cannot slow the server down by asking for more, because the queue
// is where the events would otherwise accumulate.
const EventBufferSize = 64

// listener bridges one Channel.Listen subscription to the connection.
//
// Values arrive on `source` (the channel returned by Channel.Listen) and leave on `out`,
// which the Server's delivery loop drains. `out` is closed by the Server when the
// subscription ends; the goroutine here only ever sends on it, so a send racing a close is
// impossible because both run under the Server's write lock.
type listener struct {
	id          int
	source      <-chan Value
	out         chan Value
	unsubscribe func()
	// unsubscribeOnce guarantees the Channel's unsubscribe runs exactly once, no matter how
	// many teardown paths reach it.
	unsubscribeOnce sync.Once
	// stop tells `run` to stop pulling from the source. It does NOT cancel the delivery
	// goroutine, which still flushes what is already buffered — a dispose means "no more
	// events", not "discard the ones already produced".
	stop     chan struct{}
	stopOnce sync.Once
	// abandon ends the subscription outright: connection teardown, a duplicate id, or an
	// overflow. Unlike stop it also drops buffered events, so a send already waiting for
	// queue room gives up at once instead of holding the delivery goroutine.
	abandon     chan struct{}
	abandonOnce sync.Once

	// overflowed is closed exactly once when the subscription outran `out`'s capacity. The
	// delivery loop selects on it to send the documented overflow error and stop.
	overflowed chan struct{}
	once       sync.Once
}

// newListener starts the single goroutine that owns one subscription. It returns
// immediately: dispatch must not add a blocking step to the read loop.
func newListener(id int, source <-chan Value, unsubscribe func()) *listener {
	l := &listener{
		id:          id,
		source:      source,
		out:         make(chan Value, EventBufferSize),
		unsubscribe: unsubscribe,
		stop:        make(chan struct{}),
		abandon:     make(chan struct{}),
		overflowed:  make(chan struct{}),
	}
	go l.run()
	return l
}

// run moves events from source to out. One `default` arm is the bounded-buffer overflow
// policy: rather than block the producer (which is the channel's own emitter, potentially on
// the read-loop dispatch goroutine) or grow without limit, the subscription is abandoned and
// marked overflowed so the delivery loop can report it and stop. It never touches the
// connection, so a stalled socket cannot wedge this loop.
// run moves events from the source into the bounded buffer.
//
// The inner send is deliberately non-blocking with an explicit overflow arm and no stop
// arms: a value taken from the source is EITHER buffered or it overflows, never dropped by a
// select race with a concurrent dispose. That is what lets deliverEvents drain `out` after a
// graceful dispose knowing the buffer holds every event that was ever accepted.
//
// A graceful stop is honoured only after one last non-blocking attempt to take from the
// source. Without that attempt, a dispose arriving in the same instant as the first event
// would let the stop arm win and drop an event the producer had already handed over — which
// is exactly what a fast client does when it subscribes and immediately unsubscribes.
func (l *listener) run() {
	for {
		select {
		case <-l.abandon:
			return
		default:
		}
		select {
		case <-l.abandon:
			return
		case <-l.stop:
			l.drainSource()
			return
		case value, ok := <-l.source:
			if !ok {
				return
			}
			select {
			case l.out <- value:
			default:
				// The bounded buffer is full: the subscription outran the connection.
				// Report it and abandon rather than block the producer (which may be
				// running on the read-loop dispatch goroutine) or grow without limit.
				l.markOverflowed()
				return
			}
		}
	}
}

// drainSource moves everything the source can hand over without blocking into the buffer,
// so a graceful dispose still delivers the events that were already produced. It stops at
// the buffer's capacity and marks the subscription overflowed if more remain, which keeps the
// same bound the steady-state path enforces.
func (l *listener) drainSource() {
	for {
		select {
		case <-l.abandon:
			return
		case value, ok := <-l.source:
			if !ok {
				return
			}
			select {
			case l.out <- value:
			default:
				l.markOverflowed()
				return
			}
		default:
			return
		}
	}
}

func (l *listener) markOverflowed() { l.once.Do(func() { close(l.overflowed) }) }

// dispose stops the subscription gracefully: it tells `run` to pull no more events and
// unsubscribes from the channel, while leaving the already-buffered events to be delivered.
//
// This is the 103 EventDispose path. Idempotent and callable from any goroutine, which is
// what lets the handler and connection teardown race on one listener.
//
// The unsubscribe is invoked exactly once, guarded separately from the stop signal, because
// the two teardown paths (graceful dispose and immediate abandon) both funnel through here —
// and an unsubscribe that closed a channel twice would take down the process.
func (l *listener) dispose() {
	l.stopOnce.Do(func() { close(l.stop) })
	l.unsubscribeOnce.Do(func() {
		if l.unsubscribe != nil {
			l.unsubscribe()
		}
	})
}

// abandon ends the subscription immediately: the delivery goroutine gives up on anything
// still buffered. Used where continuing would be wrong — connection teardown, a duplicate
// request id displacing this listener, and overflow.
func (l *listener) abandonNow() {
	l.dispose()
	l.abandonOnce.Do(func() { close(l.abandon) })
}

// onEventListen handles request type 102: it subscribes to the channel's event and starts the
// one delivery goroutine that will forward it as EventFire frames.
//
// A Listen that fails still owes the client an answer for its request id — without one the
// subscriber would wait for events that can never come — so it is reported as a PromiseError,
// the same shape a failing call uses.
func (s *Server) onEventListen(header RequestHeader, body Value) {
	ch, ok := s.channel(header.ChannelName)
	if !ok {
		s.collectPendingRequest(header, body)
		return
	}

	source, unsubscribe, err := ch.Listen(s.ctx, header.Name, body)
	if err != nil {
		s.sendPromiseError(header.ID, err)
		return
	}
	if source == nil {
		// A nil source would panic the moment anything tried to send on it; treat it as a
		// subscription that simply never fires.
		source = neverValues()
	}

	l := newListener(header.ID, source, unsubscribe)
	active := &activeRequest{dispose: l.dispose, abandon: l.abandonNow}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		l.dispose()
		return
	}
	s.mu.Unlock()

	// A duplicate listen id displaces the previous subscription: both would fire on one id
	// and the client cannot attribute the events, so the newest listen owns the id.
	if evicted := s.beginRequest(header.ID, active); evicted != nil {
		evicted.release(true)
	}

	go s.deliverEvents(l, active)
}

// deliverEvents forwards one listener's events to the client as EventFire frames until the
// subscription ends. It runs on its own goroutine per listener, so a slow subscription blocks
// only itself.
//
// On a dispose the loop does not return immediately: it drains whatever `run` already moved
// into `out` and then stops. A 103 means "no MORE events", not "throw away the ones already
// produced" — the TypeScript emitter has the same property, because a listener invoked
// before dispose has already run. Without this, a fast dispose could cancel a subscription
// whose first event had been produced but not yet forwarded, and the client would silently
// miss data it had legitimately subscribed for.
func (s *Server) deliverEvents(l *listener, active *activeRequest) {
	defer s.finishRequest(l.id, active)
	draining := false
	for {
		// Once disposed, only already-buffered events are sent; the select below then
		// prefers them over the stop signal.
		select {
		case value := <-l.out:
			// l.stop is the escape hatch: if this subscription is disposed or overflows
			// while the frame is queued, the send gives up instead of holding the delivery
			// goroutine until the writer's timeout.
			s.sendResponse(ResponseEventFire, l.id, bodyOrUndefined(value), l.abandon)
			continue
		default:
		}
		if draining {
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case <-l.overflowed:
			// Dispose FIRST, then report.
			//
			// Order matters. The unsubscribe is what stops the producer, and the overflow
			// report writes to the very socket that failed to drain — so reporting first
			// would park the producer behind a write that may never complete, which is the
			// unbounded growth the overflow policy exists to prevent. Disposing first bounds
			// the producer; the report that follows is best effort and is dropped if the
			// connection is already gone.
			l.abandonNow()
			s.sendResponse(ResponseEventFire, l.id, bodyOrUndefined(errorPayloadTraced(ErrEventOverflow, s.opts.TraceID)), nil)
			return
		case <-l.stop:
			// Stop producing, but flush what is already buffered before exiting.
			draining = true
		case value := <-l.out:
			s.sendResponse(ResponseEventFire, l.id, bodyOrUndefined(value), l.abandon)
		}
	}
}
