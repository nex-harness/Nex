package rpc

import (
	"errors"
	"sync"
)

// This file holds the Client's public error values and the two per-request types it
// registers. They are split out of client.go for size; nothing here is independent of it.

// Errors a caller can test for with errors.Is.
var (
	// ErrConnectionClosed is what every in-flight call and every buffered event fails with
	// when the connection ends — locally via Close, or because the socket or the read loop
	// failed. It wraps the underlying cause, so `errors.Is(err, ErrConnectionClosed)` holds
	// in both cases while the cause stays inspectable.
	//
	// The TypeScript client rejects with `new Error("ChannelClient disposed")` and sets
	// `name = "ConnectionClosed"` on it; a transport failure rejects with the raw reason.
	// Go has one sentinel for both, so a caller can distinguish "we closed it" from "the
	// peer went away" only by the wrapped text.
	ErrConnectionClosed = errors.New("rpc: connection closed")
	// ErrClientClosed is what a call made after Close is rejected with. It is a distinct
	// value from ErrConnectionClosed because nothing was in flight and nothing failed:
	// the call never reached the wire, so reporting it as a connection failure would
	// misdescribe what happened.
	ErrClientClosed = errors.New("rpc: channel client is closed")
)

// pendingCall is one Promise request awaiting its 201/202/203.
type pendingCall struct {
	result Value
	err    error
	// sent records whether the request frame is already on the wire, so a cancel sends the
	// 101 only when there is something for the server to cancel. Guarded by Client.mu.
	sent bool
	// done is closed exactly once, by whichever of the three settling paths (response,
	// cancel, teardown) removed the call from Client.pending first.
	done chan struct{}
}

func (p *pendingCall) settle(result Value, err error) {
	p.result, p.err = result, err
	close(p.done)
}

// clientListener is one active subscription. out is buffered at EventBufferSize and is the
// only channel the consumer reads.
//
// OVERFLOW POLICY: the read loop must never block, so a consumer that falls behind more
// than EventBufferSize events loses the subscription instead of the connection. The client
// half of the policy therefore differs from the server's by one step: the server reports
// overflow as a final EventFire carrying the error payload, but a Go client cannot push
// that payload onto a channel nobody is draining without parking a goroutine forever, so
// it sends the 103 and closes the channel. A consumer that ranges over out sees the stream
// end — the same shape as a normal dispose.
type clientListener struct {
	out  chan Value
	once sync.Once // guards close(out); terminate and the overflow path both close
}

func (l *clientListener) close() { l.once.Do(func() { close(l.out) }) }
