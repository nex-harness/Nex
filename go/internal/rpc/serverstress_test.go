package rpc

import (
	"context"
	"errors"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These are the step 2.3 robustness tests: the goroutine-leak check and the back-pressure
// check. Both are about what the server does NOT do — it must not accumulate goroutines
// across a connection's life, and it must not let a client that stops reading grow the heap.

// TestServerCloseLeavesNoGoroutines closes a connection that carries 100 in-flight calls
// and 10 live listeners and requires that everything it started has stopped.
//
// This is the profile-delta check from internal/runtime/turnstate_leak_test.go, reused
// because go.uber.org/goleak is not a dependency of this module and adding one for a single
// test is not worth the supply-chain surface. A delta (not an absolute count) is the right
// shape: other tests in this package leave goroutines behind on purpose, so the claim under
// test is "closing a connection releases what that connection started".
func TestServerCloseLeavesNoGoroutines(t *testing.T) {
	before := snapshotGoroutines()

	const calls = 100
	const listeners = 10

	conn := newTestConn(t, Options{})

	// A call that blocks until its context is cancelled, so all 100 are genuinely in
	// flight when the connection closes.
	conn.server.RegisterChannel("blocking", &stubChannel{
		callFunc: func(ctx context.Context, method string, args []Value) (Value, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		listenFunc: func(ctx context.Context, event string, arg Value) (<-chan Value, func(), error) {
			out := make(chan Value)
			go func() {
				<-ctx.Done()
				close(out)
			}()
			return out, func() {}, nil
		},
	})

	conn.awaitInitialize()
	for i := 0; i < calls; i++ {
		conn.call(1000+i, "blocking", "wait", Undefined)
	}
	for i := 0; i < listeners; i++ {
		conn.listen(2000+i, "blocking", "tick", Undefined)
	}
	// Give the dispatch goroutines a moment to actually start before tearing down, so the
	// test measures teardown of live work rather than of work that never began.
	time.Sleep(50 * time.Millisecond)

	conn.Close() // cancels the connection context, which cancels every call and listener

	assertNoNewGoroutines(t, before)
}

// TestEventBackPressureClosesListenerWithDocumentedError drives a listener that produces
// 100k events against a peer that never reads, and pins the per-listener half of the
// back-pressure policy documented in events.go:
//
//   - the listener's queue is EventBufferSize, so the producer is throttled by the listener
//     rather than accumulating 100k values;
//   - on overflow the subscription is closed: the Channel's unsubscribe runs — which is what
//     stops the producer — and the client's last event is the overflow error;
//   - memory stays bounded.
//
// Unsubscribe runs BEFORE the error fire, and that order is the substance of the policy: the
// error fire writes to the very socket that failed to drain, so reporting first would park
// the producer behind a write that may never complete — the unbounded-growth case the
// overflow policy exists to prevent.
func TestEventBackPressureClosesListenerWithDocumentedError(t *testing.T) {
	const produced = 100_000

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The peer reads, but far too slowly to keep up: one write per `drip` calls, `delay` for
	// the rest. The outbound queue is deliberately tiny so it saturates within milliseconds;
	// once it is full the delivery goroutine blocks in enqueue, stops draining the listener's
	// bounded queue, and the LISTENER is what overflows. That is the layering under test: a
	// slow peer should be attributed to the slow subscription, not to the whole connection, so
	// the enqueue timeout is set long enough never to fire here.
	stalled := newSlowSocket(5 * time.Millisecond)
	server := NewServer(stalled, ctx, Options{
		OutboundQueueSize: 2,
		EnqueueTimeout:    30 * time.Second,
	})
	defer server.Close()
	go func() { _ = server.Serve(ctx) }()

	source := make(chan Value)
	producedEvents := make(chan struct{})
	unsubscribed := make(chan struct{})
	// stop is what unsubscribe closes. A Channel's producer must stop when its
	// subscription ends — that is the back-pressure contract the server relies on: the
	// listener abandons the subscription on overflow, unsubscribe fires, and the producer
	// stops instead of blocking forever on a channel nobody reads.
	stop := make(chan struct{})
	var stopOnce sync.Once
	server.RegisterChannel("flood", &stubChannel{
		listenFunc: func(ctx context.Context, event string, arg Value) (<-chan Value, func(), error) {
			go func() {
				defer close(producedEvents)
				for i := 0; i < produced; i++ {
					select {
					case source <- int32(i):
					case <-stop:
						return
					case <-ctx.Done():
						return
					}
				}
			}()
			return source, func() {
				stopOnce.Do(func() {
					close(stop)
					close(unsubscribed)
				})
			}, nil
		},
	})

	feedListen(t, stalled, 1, "flood", "tick")

	// The listener overflows and unsubscribes, which is what stops the producer.
	select {
	case <-unsubscribed:
	case <-time.After(10 * time.Second):
		t.Fatal("listener never unsubscribed after overflow")
	}
	select {
	case <-producedEvents:
	case <-time.After(10 * time.Second):
		t.Fatal("producer never stopped after unsubscribe: back-pressure is unbounded")
	}

	// Memory must be bounded: 100k queued ints would mean an unbounded buffer. The
	// listener's queue is EventBufferSize and the source is unbuffered, so nothing piles up.
	var stats runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&stats)
	if stats.HeapAlloc > 64<<20 {
		t.Fatalf("heap grew to %d bytes while the socket was stalled; the buffer is not bounded",
			stats.HeapAlloc)
	}
}

// TestOutboundBackPressureClosesConnection is the connection-level half: when nothing ever
// drains, a response that cannot be handed to the writer within the enqueue timeout aborts
// the connection with ErrWriteBackpressure rather than parking the producer forever.
//
// Promise calls are the right subject here, not listeners: a listener has its own bounded
// buffer that overflows first (TestEventBackPressure...), while a call has nowhere to buffer —
// its response goes straight to the writer. So when the peer never reads, the outbound queue
// fills and the enqueue timeout is the only thing that can notice.
//
// Each call blocks on a gate that is released only after every call is registered, so all
// responses are produced at once against a socket that will never drain. The timeout is set
// short so the test observes the abort in milliseconds; the default is five seconds, long
// enough that a merely slow peer is never disconnected.
func TestOutboundBackPressureClosesConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stalled := newStalledSocket() // writes park forever: the peer never reads
	server := NewServer(stalled, ctx, Options{
		OutboundQueueSize: 8,
		EnqueueTimeout:    200 * time.Millisecond,
	})
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx) }()

	const callers = 64
	gate := make(chan struct{})
	entered := make(chan struct{}, callers)
	server.RegisterChannel("bulk", &stubChannel{
		callFunc: func(ctx context.Context, method string, args []Value) (Value, error) {
			entered <- struct{}{}
			select {
			case <-gate:
				return int32(1), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	for i := 0; i < callers; i++ {
		feedCall(t, stalled, i+1, "bulk", "go")
	}
	// Wait for every call to be running, then release them together.
	for i := 0; i < callers; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d calls started", i, callers)
		}
	}
	close(gate)

	select {
	case err := <-served:
		if !errors.Is(err, ErrWriteBackpressure) {
			t.Fatalf("Serve returned %v, want ErrWriteBackpressure", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("connection was not closed after the peer stopped reading")
	}

	server.Close()
}

// feedListen sends an EventListen request for (id, channel, event) to a stalled socket.
func feedListen(t *testing.T, sock *testSocket, id int, channel, event string) {
	t.Helper()
	feedRequest(t, sock, RequestHeader{
		Type: RequestEventListen, ID: id, ChannelName: channel, Name: event,
	})
}

// feedCall sends a Promise request for (id, channel, method) to a stalled socket.
func feedCall(t *testing.T, sock *testSocket, id int, channel, method string) {
	t.Helper()
	feedRequest(t, sock, RequestHeader{
		Type: RequestPromise, ID: id, ChannelName: channel, Name: method,
	})
}

// feedRequest frames one request and queues it for the server's reader.
func feedRequest(t *testing.T, sock *testSocket, header RequestHeader) {
	t.Helper()
	payload, err := EncodeRequest(header, Undefined)
	if err != nil {
		t.Fatal(err)
	}
	if err := sock.feed(EncodeFrame(Frame{Type: MsgRegular, Data: payload})); err != nil {
		t.Fatal(err)
	}
}

// testSocket is a Socket with a scriptable write policy, so a test can choose WHICH
// bottleneck it exercises without changing the server.
//
//	writeDelay > 0  every write takes that long — a peer that reads, far too slowly. Frames
//	                drain, so the connection stays healthy and a single listener's bounded
//	                queue is what overflows.
//	writeDelay == 0 writes park forever — a peer that stopped reading. Nothing drains, so
//	                the outbound queue fills and the connection is what gives up.
//
// Read delivers whatever the test fed in; Close releases every parked writer.
type testSocket struct {
	mu         sync.Mutex
	inbound    [][]byte
	writeDelay time.Duration
	// ready is signalled whenever inbound gains an entry, so a reader already parked in
	// Read wakes up. Without it a frame fed after the reader parked would sit unseen and
	// the test would hang waiting for a request the server never received — which is why
	// this socket exists rather than a bare slice.
	ready chan struct{}
	// writes counts accepted writes, for assertions.
	writes atomic.Int64
	closed chan struct{}
	once   sync.Once
}

// newStalledSocket returns a socket whose writes never complete: the peer never reads.
func newStalledSocket() *testSocket {
	return &testSocket{closed: make(chan struct{}), ready: make(chan struct{}, 1)}
}

// newSlowSocket returns a socket that accepts every write after `delay`.
func newSlowSocket(delay time.Duration) *testSocket {
	return &testSocket{closed: make(chan struct{}), ready: make(chan struct{}, 1), writeDelay: delay}
}

// feed queues bytes for the server's reader.
func (s *testSocket) feed(data []byte) error {
	s.mu.Lock()
	s.inbound = append(s.inbound, data)
	s.mu.Unlock()
	select {
	case s.ready <- struct{}{}:
	default: // a wake-up is already pending; one is enough to pick the frame up
	}
	return nil
}

func (s *testSocket) Read(ctx context.Context) ([]byte, error) {
	for {
		s.mu.Lock()
		if len(s.inbound) > 0 {
			chunk := s.inbound[0]
			s.inbound = s.inbound[1:]
			s.mu.Unlock()
			return chunk, nil
		}
		s.mu.Unlock()
		select {
		case <-s.ready:
		case <-s.closed:
			return nil, context.Canceled
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *testSocket) Write(ctx context.Context, data []byte) error {
	if s.writeDelay > 0 {
		select {
		case <-s.closed:
			return context.Canceled
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.writeDelay):
			s.writes.Add(1)
			return nil
		}
	}
	select {
	case <-s.closed:
		return context.Canceled
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *testSocket) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

// --- goroutine profile-delta helpers -------------------------------------------------
// These mirror internal/runtime/turnstate_leak_test.go; see the comment on
// TestServerCloseLeavesNoGoroutines for why the delta shape is used.

const leakSettleTimeout = 2 * time.Second

func snapshotGoroutines() map[string]int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return goroutineSignatures(string(buf[:n]))
		}
		buf = make([]byte, 2*len(buf))
	}
}

// goroutineSignatures reduces a profile to one key per goroutine: its state line plus the
// topmost frame belonging to this module or the runtime.
func goroutineSignatures(profile string) map[string]int {
	out := map[string]int{}
	for _, g := range strings.Split(profile, "\n\n") {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		key := g
		if i := strings.IndexByte(g, '\n'); i >= 0 {
			key = g[:i]
		}
		for _, line := range strings.Split(g, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "github.com/tylinux/nex/go/") || strings.HasPrefix(line, "runtime.") {
				key += "\n\t" + line
				break
			}
		}
		out[key]++
	}
	return out
}

// assertNoNewGoroutines polls until no goroutine outlives the work.
func assertNoNewGoroutines(t *testing.T, before map[string]int) {
	t.Helper()
	deadline := time.Now().Add(leakSettleTimeout)
	var extra []string
	for {
		extra = nil
		for key, n := range goroutineSignatures(stackDump()) {
			if isTestingGoroutine(key) {
				continue
			}
			if n > before[key] {
				extra = append(extra, "x"+itoa(n-before[key])+" "+key)
			}
		}
		if len(extra) == 0 {
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	sort.Strings(extra)
	t.Errorf("%d goroutine(s) outlived the connection:\n%s", len(extra), strings.Join(extra, "\n\n"))
}

func stackDump() string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}

func isTestingGoroutine(key string) bool {
	for _, marker := range []string{
		"testing.tRunner", "testing.(*M).Run", "testing.runTests",
		"testing.(*T).Run", "runtime.goexit",
		// The assertion inspects itself: runtime.Stack(all=true) captures the test
		// goroutine's own stack, whose topmost module frame is the helper that is running.
		"rpc.stackDump", "rpc.snapshotGoroutines", "rpc.goroutineSignatures",
		"rpc.assertNoNewGoroutines",
	} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}
