package rpc

import "context"

// This file holds the Client's bookkeeping: the request-id counter, the registries of
// in-flight calls and live listeners, and the single write path. It is split out of
// client.go only for size — every function here is called from there and shares its state.
//
// Ownership: Client.mu guards nextID, pending, listeners, failure and closed. The lock is
// what makes event delivery safe: fireEvent sends on a listener's channel while holding it,
// and every close of that channel (dispose, overflow, teardown) happens under the same lock
// after the listener has been removed from the map. A send can therefore never meet a close,
// which is the one race a Go channel cannot recover from.

func (c *Client) allocateID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	return id
}

func (c *Client) register(id int, call *pendingCall) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.pending[id] = call
	return true
}

func (c *Client) stillPending(id int, call *pendingCall) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, ok := c.pending[id]
	return ok && current == call
}

func (c *Client) registerListener(id int, l *clientListener) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.listeners[id] = l
	return true
}

func (c *Client) stillListening(id int, l *clientListener) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, ok := c.listeners[id]
	return ok && current == l
}

// closeListener removes a listener and closes its event channel under the lock, reporting
// whether this call was the one that removed it. That report is what gives exactly one caller
// the right to send the 103 — a dispose racing an overflow must not double-dispose.
//
// Closing under the lock is the whole point: fireEvent sends on the same channel while
// holding it, so a send can never meet a close.
func (c *Client) closeListener(id int, l *clientListener) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, ok := c.listeners[id]
	if !ok || current != l {
		return false
	}
	delete(c.listeners, id)
	l.close()
	return true
}

// abandon removes a call the caller has stopped waiting for. It reports whether the request
// frame is already on the wire, so the caller sends a cancel only when there is something
// to cancel.
func (c *Client) abandon(id int, call *pendingCall, cause error) bool {
	c.mu.Lock()
	current, ok := c.pending[id]
	if !ok || current != call {
		c.mu.Unlock()
		return false
	}
	delete(c.pending, id)
	sent := call.sent
	c.mu.Unlock()
	call.settle(nil, cause)
	return sent
}

func (c *Client) open() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClientClosed
	}
	return nil
}

// cause returns why the connection ended. It is only meaningful after a failed register or
// a failed awaitInit.
func (c *Client) cause() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure == nil {
		return ErrConnectionClosed
	}
	return c.failure
}

func (c *Client) markInit(err error) {
	c.initOnce.Do(func() {
		c.initErr = err
		close(c.initDone)
	})
}

// awaitInit parks until the server has said Initialize, the connection ends, or ctx is
// done. A connection that ends is reported as the cause, not as a cancellation: the caller
// asked for nothing to be cancelled, it lost the connection.
func (c *Client) awaitInit(ctx context.Context) error {
	select {
	case <-c.initDone:
		return c.initErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// sendPromise writes one Promise request, recording on the call that it is now on the wire.
// It reports false when the call stopped being pending first, which is the cancel/close race
// a check-then-write would otherwise turn into a request nobody is waiting for.
func (c *Client) sendPromise(id int, call *pendingCall, channel, method string, body Value) bool {
	payload, err := EncodeRequest(RequestHeader{
		Type: RequestPromise, ID: id, ChannelName: channel, Name: method,
	}, body)
	if err != nil {
		return false
	}
	c.mu.Lock()
	current, ok := c.pending[id]
	if !ok || current != call {
		c.mu.Unlock()
		return false
	}
	call.sent = true
	c.mu.Unlock()
	c.write(payload)
	return true
}

func (c *Client) sendCancelOrDispose(kind, id int) {
	payload, err := EncodeCancelOrDispose(kind, id)
	if err != nil {
		return
	}
	c.write(payload)
}

// write frames a payload and hands it to the writer. A serialization failure drops the
// frame, as the TS `try { protocol.send } catch {}` does: a payload the codec cannot encode
// has no wire representation, and dropping it keeps a healthy connection healthy.
func (c *Client) write(payload []byte) {
	c.writer.enqueue(EncodeFrame(Frame{Type: MsgRegular, Data: payload}), nil)
}

// argsOrUndefined is the body for a positional call: the argument array itself, or Undefined
// when there are none.
func argsOrUndefined(args []Value) Value {
	if len(args) == 0 {
		return Undefined
	}
	return args
}
