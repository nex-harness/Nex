// Package rpctest provides an in-memory rpc.Socket pair for driving a channel server without
// a real WebSocket. It is the Go equivalent of `createQueuePair()` in
// packages/rpc/src/protocol.ts, which the phase-0 trace recorder uses to capture RPC frames.
//
// Step 2.3 deliberately does not wire a WebSocket yet (that is step 2.7), so the transport
// here is a pair of unbounded in-memory queues: the same Socket interface the framing layer
// expects, with byte-level writes that the peer's reader reassembles.
package rpctest

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed is returned by Read and Write once an endpoint has been closed.
var ErrClosed = errors.New("rpctest: endpoint closed")

// endpoint is one side of a pair.
type endpoint struct {
	in  chan []byte
	out chan []byte

	closeOnce sync.Once
	closed    chan struct{}
}

// writeErr is the sticky error delivered to a blocked reader once the writer side closes.
func (e *endpoint) Read(ctx context.Context) ([]byte, error) {
	select {
	case <-e.closed:
		return nil, ErrClosed
	case data := <-e.in:
		return data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *endpoint) Write(ctx context.Context, data []byte) error {
	select {
	case <-e.closed:
		return ErrClosed
	case e.out <- append([]byte(nil), data...):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close marks this endpoint closed. The peer's Read unblocks with io.EOF once the queued
// bytes have drained, mirroring a real socket close.
func (e *endpoint) Close() error {
	e.closeOnce.Do(func() { close(e.closed) })
	return nil
}

// Pair is two connected in-memory endpoints.
type Pair struct {
	A *Endpoint
	B *Endpoint
}

// Endpoint is the exported handle for one side of the pair.
type Endpoint struct {
	*endpoint
}

// NewPair returns two connected endpoints. Bytes written to A are readable from B and vice
// versa.
func NewPair() Pair {
	ab := make(chan []byte, 1024)
	ba := make(chan []byte, 1024)
	a := &Endpoint{&endpoint{in: ba, out: ab, closed: make(chan struct{})}}
	b := &Endpoint{&endpoint{in: ab, out: ba, closed: make(chan struct{})}}
	return Pair{A: a, B: b}
}

// CloseAll closes both endpoints, unblocking every reader and writer.
func (p Pair) CloseAll() {
	p.A.Close()
	p.B.Close()
}
