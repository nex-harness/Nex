// clientapi.go is the observing and shutting-down surface of the protocol client:
// the subscription registry (notifications, request timeouts, close), the
// interaction-handler registry, the pending-request counters the process manager
// uses, and the close path.
//
// It is separate from the correlation code in client.go because none of it
// allocates a request id or waits for a response — this is what a caller touches
// when it owns a client rather than when it is calling one.
package proto

import (
	"context"
	"encoding/json"
	"fmt"
)

// OnNotification subscribes to one method's notifications. The empty method name
// subscribes to every notification, which is what the connection scope (3.10.3)
// needs to route a frame by (connectionId, topic).
//
// Delivery is non-blocking and lossy under a slow consumer, by design: the frame
// pump must not stall on a UI-bound subscriber, and the v4 reassembler's ordinal
// rules turn a missed frame into a typed fault rather than a silently wrong one.
func (c *Client) OnNotification(method string) (<-chan Notification, func()) {
	ch := make(chan Notification, 64)
	c.mu.Lock()
	c.notified[method] = append(c.notified[method], ch)
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		list := c.notified[method]
		for i, existing := range list {
			if existing == ch {
				c.notified[method] = append(list[:i], list[i+1:]...)
				return
			}
		}
	}
}

// OnRequestTimeout subscribes to business-request timeouts. The process manager
// uses this to decide that a live process has a dead protocol loop and must be
// recycled; an observation request never fires it.
func (c *Client) OnRequestTimeout() (<-chan RequestTimeoutEvent, func()) {
	ch := make(chan RequestTimeoutEvent, 8)
	c.mu.Lock()
	c.timeouts = append(c.timeouts, ch)
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for i, existing := range c.timeouts {
			if existing == ch {
				c.timeouts = append(c.timeouts[:i], c.timeouts[i+1:]...)
				return
			}
		}
	}
}

// OnClose returns a channel closed when the protocol leg ends. It is closed
// already when the leg is over, so a caller subscribing late cannot miss it.
func (c *Client) OnClose() <-chan struct{} {
	ch := make(chan struct{})
	c.mu.Lock()
	if c.disposed {
		c.mu.Unlock()
		close(ch)
		return ch
	}
	c.closed = append(c.closed, ch)
	c.mu.Unlock()
	return ch
}

// RegisterInteractionHandler installs the answer for one interaction/* method; a
// nil handler removes it and restores the method-not-found default.
//
// This is the routing seam step 3.10.2 provides: each agent→host request
// (permission prompt, user input, browser control, provider runtime headers,
// official MCP auth headers) is answered by the handler registered on the
// connection that owns it, and for the browser family by the window that owns it.
func (c *Client) RegisterInteractionHandler(method string, fn InteractionHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fn == nil {
		delete(c.interactions, method)
		return
	}
	c.interactions[method] = fn
}

// InteractionHandlerFor reports the handler installed for a method, or nil.
func (c *Client) InteractionHandlerFor(method string) InteractionHandler {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.interactions[method]
}

// IsDisposed reports whether the client has been closed. A caller that reuses a
// process-manager entry must check this first: a client whose process was
// recycled still looks resident until its close has been delivered, and sending to
// it would fail with a confusing transport error instead of the honest one.
func (c *Client) IsDisposed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.disposed
}

// PendingCount is the number of requests awaiting a response.
func (c *Client) PendingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

// PendingOperationCount is the subset the process manager uses for runtime health
// and idle reclaim. An observation must not keep a process alive, so it is
// excluded here exactly as it is in the TS client.
func (c *Client) PendingOperationCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, p := range c.pending {
		if !p.observation {
			n++
		}
	}
	return n
}

// Close rejects every waiter, disposes the storage gate and closes the transport.
// It is idempotent, and the first reason wins: a later close must not rewrite the
// root cause of a rejected request.
func (c *Client) Close() { c.closeWithReason("") }

// CloseWithReason closes the client and records why, which is what the pending
// waiters see in their error (the TS transport "close" reason).
func (c *Client) CloseWithReason(reason string) { c.closeWithReason(reason) }

func (c *Client) closeWithReason(reason string) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.disposed = true
		c.closeReason = reason
		c.mu.Unlock()
		c.closeCancel()
		c.gate.Dispose()
		c.rejectAll(c.closeError(reason))
		c.transport.Close()
	})
}

// CloseReason reports why the leg ended, which is what the pending waiters were
// rejected with and what the service logs. The first cause wins: a host-side close
// after an agent crash must not rewrite a crash as a graceful shutdown.
func (c *Client) CloseReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeReason
}

// Wait blocks until the frame pump has exited. Close followed by Wait is the Go
// equivalent of the TS disposeAndWait: the goroutine this client owns is provably
// gone, which is what the leak check asserts.
func (c *Client) Wait() { <-c.closeDone }

func (c *Client) closeError(reason string) error {
	if reason == "" {
		return ErrTransportClosed
	}
	return fmt.Errorf("%w: %s", ErrTransportClosed, reason)
}

func (c *Client) assertUsable() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disposed {
		return ErrDisposed
	}
	return nil
}

// ensure the handler contract keeps its context: dropping it would remove the
// unblock-on-close property that keeps a UI prompt goroutine from leaking.
var _ func(context.Context, AgentRequest) (json.RawMessage, error) = InteractionHandler(nil)
