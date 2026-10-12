package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/rpc/rpctest"
)

// TestClientCloseLeavesNoGoroutines closes a connection that carries 100 in-flight calls and
// 10 live listeners and requires that everything the client started has stopped.
//
// It is the mirror of TestServerCloseLeavesNoGoroutines: a client that leaks a goroutine per
// request would, over a long-lived gateway connection, leak faster than the server it drives.
// The same profile-delta shape is used for the same reason (see that test): other tests leave
// goroutines behind on purpose, so the claim is "closing releases what this connection
// started".
func TestClientCloseLeavesNoGoroutines(t *testing.T) {
	const calls = 100
	const listeners = 10

	pair := rpctest.NewPair()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	server := NewServer(pair.A, ctx, Options{})
	// Calls block until cancelled; listeners fire steadily so a delivery goroutine is live
	// for each of the ten when the connection closes.
	server.RegisterChannel("busy", &stubChannel{
		callFunc: func(callCtx context.Context, _ string, _ []Value) (Value, error) {
			<-callCtx.Done()
			return nil, callCtx.Err()
		},
		listenFunc: func(listenCtx context.Context, _ string, _ Value) (<-chan Value, func(), error) {
			out := make(chan Value)
			stop := make(chan struct{})
			var once sync.Once
			go func() {
				for {
					select {
					case out <- "tick":
					case <-stop:
						return
					case <-listenCtx.Done():
						return
					}
				}
			}()
			// unsubscribe is what ends the subscription, so it owns the stop signal. The
			// producer is a goroutine THIS test started, and the leak assertion below counts
			// every goroutine — a stub that kept producing would fail the test for a reason
			// that has nothing to do with the client.
			return out, func() { once.Do(func() { close(stop) }) }, nil
		},
	})
	go func() { _ = server.Serve(ctx) }()

	before := snapshotGoroutines()

	client := NewClient(rpctest.Record(pair.B))
	var wg sync.WaitGroup
	failures := make(chan error, calls)

	// The calls are drained into `failures` rather than left to hang: a Close that leaves a
	// call waiting forever would fail the goroutine assertion anyway, but a caller blocked on
	// a result it can never get is a different (and louder) failure to report.
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := client.Call(context.Background(), "busy", "wait", nil)
			failures <- err
		}()
	}

	// Each listener is disposed only by the teardown, so its consumer goroutine has to drain
	// what arrives; a consumer that does not drain is exactly the overflow case, handled by a
	// separate test.
	for i := 0; i < listeners; i++ {
		events, _, err := client.Listen(t.Context(), "busy", "tick", Undefined)
		if err != nil {
			t.Fatalf("Listen %d: %v", i, err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range events { //nolint:revive // draining until the channel closes is the point
			}
		}()
	}

	// Give the work a moment to actually start, so the test measures teardown of live
	// requests rather than of requests that never began.
	time.Sleep(100 * time.Millisecond)

	client.Close()
	server.Close()
	pair.CloseAll()

	select {
	case <-client.Done():
	case <-time.After(2 * time.Second):
		t.Error("the client's read loop did not stop after Close")
	}
	wg.Wait()

	// Every in-flight call must have been released with a connection error, not left hanging.
	for i := 0; i < calls; i++ {
		err := <-failures
		if err == nil {
			t.Fatalf("call %d returned no error after Close; the connection failed silently", i)
		}
	}

	assertNoNewGoroutines(t, before)
}
