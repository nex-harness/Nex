// clientpump.go is the frame pump and the completion paths of the protocol
// client: reading stdout lines, decoding them into the four message shapes, and
// routing each to a waiter, a notification subscriber, an interaction handler or
// the close path. It is separate from client.go because these rules are dense and
// belong in one readable place.
//
// The pump owns exactly one goroutine and exits when the transport's frame channel
// closes, which happens on process exit, stdout EOF or a fatal parse error. A line
// the protocol parser rejects is fatal: the reader cannot resynchronise, so this
// is the protocol_parse_error close the TS does.
//
// Dispatch order mirrors the TS: a response, then an error, then a request, then a
// notification. The shape decides, because the wire has no jsonrpc/type field.
package proto

import (
	"errors"
	"strconv"
	"time"

	"github.com/tylinux/nex/go/internal/protocol"
)

// pump reads frames until the transport closes, then performs the close path
// exactly once.
func (c *Client) pump() {
	defer close(c.closeDone)
	for line := range c.transport.Frames() {
		c.handleLine([]byte(line))
	}
	reason := c.transport.CloseReason()
	if c.closeWithReasonIfOpen(reason) {
		c.gate.Dispose()
		c.rejectAll(c.closeError(reason))
	}
	c.fireClosed()
}

// closeWithReasonIfOpen marks the client disposed unless it already is. It
// returns true when this call performed the transition, so the caller knows it
// owns the reject pass and must not call transport.Close() (the leg is gone).
func (c *Client) closeWithReasonIfOpen(reason string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disposed {
		return false
	}
	c.disposed = true
	c.closeReason = reason
	return true
}

// handleLine decodes one frame and dispatches it.
func (c *Client) handleLine(line []byte) {
	msg, err := protocol.ParseMessage(line)
	if err != nil {
		c.closeWithReason("protocol_parse_error: " + err.Error())
		return
	}
	switch msg.Kind {
	case protocol.KindResponse:
		c.settle(c.key(msg.ID), callOutcome{res: &CallResult{Raw: msg.Result}})
	case protocol.KindError:
		c.settle(c.key(msg.ID), callOutcome{err: c.requestError(c.key(msg.ID), msg.Error)})
	case protocol.KindRequest:
		c.handleAgentRequest(msg)
	case protocol.KindNotification:
		c.handleNotification(msg)
	}
}

// key is the correlation key: the decimal form of a numeric id, the string itself
// otherwise. The TS keys its pending map by String(id), and a response carrying
// "7" must find the waiter registered for 7.
func (c *Client) key(id protocol.RequestID) string {
	if id.IsString() {
		return id.String()
	}
	if _, err := strconv.ParseInt(id.String(), 10, 64); err == nil {
		return id.String()
	}
	return id.String()
}

func (c *Client) requestError(key string, payload *protocol.RPCError) error {
	method := "request#" + key
	c.mu.Lock()
	if call, ok := c.pending[key]; ok {
		method = call.method
	}
	c.mu.Unlock()
	return &RequestError{Method: method, Code: payload.Code, Message: payload.Message, Data: payload.Data}
}

// handleAgentRequest routes an agent→host request to its handler. With no handler
// registered the client answers method-not-found, which the agent already degrades
// on: the tool runs without the prompt, the headers go unfilled, the browser call
// falls back. That is the documented default, not an error path.
func (c *Client) handleAgentRequest(msg protocol.Message) {
	if err := c.assertUsable(); err != nil {
		return
	}
	c.mu.Lock()
	handler := c.interactions[msg.Method]
	c.mu.Unlock()

	if handler == nil {
		_ = c.RespondError(msg.ID, protocol.CodeMethodNotFound, "Method not found: "+msg.Method, nil)
		return
	}
	req := AgentRequest{ID: msg.ID, Method: msg.Method, Params: msg.Params, Trace: msg.Trace}
	// The handler runs on the pump goroutine, so it sees the same ctx that dies
	// with the leg: a handler waiting on a UI prompt unblocks on transport close
	// instead of leaking. A handler that must not block the pump dispatches to
	// its own goroutine and responds later through Client.Respond.
	result, err := handler(c.pumpCtx, req)
	if err != nil {
		var reqErr *RequestError
		if errors.As(err, &reqErr) {
			_ = c.RespondError(msg.ID, reqErr.Code, reqErr.Message, reqErr.Data)
			return
		}
		_ = c.RespondError(msg.ID, protocol.CodeInternal, err.Error(), nil)
		return
	}
	_ = c.Respond(msg.ID, result)
}

// handleNotification fans a notification out and drives the storage gate.
//
// The startup/storageState case carries the TS subtlety: a custom or old agent
// cannot declare its capability in advance, so the first request may already be
// out when the first legal startup frame arrives. The gate then pauses the
// watchdogs of the in-flight requests, resumes them on ready, and rejects
// everything on failed. Only a legal frame may do this: a later notification must
// not revive a process whose startup already ended.
func (c *Client) handleNotification(msg protocol.Message) {
	if msg.Method == NotifyStorageState && c.gate.Accept(msg.Params) {
		// The first legal frame pauses the watchdogs of requests that are already
		// out: the agent is mid-migration, and a short budget must not retire the
		// runtime for that. Ready resumes them; failed ends them.
		//
		// It only applies to a gate that is actually GATING. A command that does
		// not declare SupportsStorageStartup has an inert gate, and suspending its
		// watchdogs would strand every in-flight request: nothing would ever resume
		// them, and the first request would hang for its whole (unbounded) budget.
		if c.gate.Required() {
			c.suspendTimers()
			switch snapshot, _ := c.gate.Snapshot(); snapshot.Phase {
			case StorageReady:
				c.resumeSuspendedTimers()
			case StorageFailed:
				c.rejectAll(c.gate.Terminal())
			}
		}
	}
	c.publishNotification(Notification{Method: msg.Method, Params: msg.Params, Trace: msg.Trace})
}

// publishNotification delivers to the method's subscribers and to the wildcard
// subscribers. Delivery never blocks the pump: a slow consumer drops a copy rather
// than stalling every other frame.
func (c *Client) publishNotification(notif Notification) {
	c.mu.Lock()
	exact := append([]chan Notification(nil), c.notified[notif.Method]...)
	var wildcard []chan Notification
	if notif.Method != "" {
		wildcard = append([]chan Notification(nil), c.notified[""]...)
	}
	c.mu.Unlock()
	for _, ch := range exact {
		select {
		case ch <- notif:
		default:
		}
	}
	for _, ch := range wildcard {
		select {
		case ch <- notif:
		default:
		}
	}
}

// settle removes the waiter and delivers its outcome exactly once. It is the one
// completion path for response, error, timeout, cancellation and close, so no
// waiter is delivered twice and no id resolves after removal.
func (c *Client) settle(key string, outcome callOutcome) {
	c.mu.Lock()
	call, ok := c.pending[key]
	if !ok {
		c.mu.Unlock()
		return
	}
	delete(c.pending, key)
	delete(c.suspendedCall, key)
	c.mu.Unlock()

	if call.timer != nil {
		call.timer.Stop()
	}
	if outcome.res != nil && call.validate != nil {
		value, err := call.validate(outcome.res.Raw)
		if err != nil {
			outcome = callOutcome{err: err}
		} else {
			outcome.res.Value = value
		}
	}
	if outcome.res != nil {
		outcome.res.Method = call.method
		outcome.res.RequestID = call.id
	}
	select {
	case call.result <- outcome:
	default:
	}
}

// expire is the per-call timeout. A business request that times out also fires the
// request-timeout event so the process manager can recycle a process whose event
// loop has stopped answering; an observation never does.
func (c *Client) expire(key string) {
	c.mu.Lock()
	call, ok := c.pending[key]
	if !ok {
		c.mu.Unlock()
		return
	}
	delete(c.pending, key)
	delete(c.suspendedCall, key)
	// Only a business request is escalated. A short resource probe that timed out
	// means "no data this round", not "the agent's event loop is dead"; escalating
	// it would recycle a healthy process on a slow disk.
	var notify chan RequestTimeoutEvent
	if !call.observation {
		notify = c.timeoutChannelLocked()
	}
	c.mu.Unlock()

	if notify != nil {
		select {
		case notify <- RequestTimeoutEvent{Method: call.method, RequestID: call.id, Timeout: call.timeout}:
		default:
		}
	}
	select {
	case call.result <- callOutcome{err: &TimeoutError{Method: call.method, RequestID: call.id, Timeout: call.timeout}}:
	default:
	}
}

// rejectAll fails every waiter with one error (the close path, or a storage
// failure that must not leave callers parked).
func (c *Client) rejectAll(err error) {
	c.mu.Lock()
	calls := make([]*pendingCall, 0, len(c.pending))
	for _, call := range c.pending {
		calls = append(calls, call)
	}
	c.pending = map[string]*pendingCall{}
	c.suspendedCall = map[string]bool{}
	c.mu.Unlock()
	for _, call := range calls {
		if call.timer != nil {
			call.timer.Stop()
		}
		select {
		case call.result <- callOutcome{err: err}:
		default:
		}
	}
}

// suspendTimers pauses the watchdogs of the in-flight requests. The TS does this
// with clearTimeout when the first legal startup frame arrives: the agent is
// mid-migration and a short request budget must not retire the whole runtime.
func (c *Client) suspendTimers() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, call := range c.pending {
		if call.timer != nil {
			call.timer.Stop()
			call.timer = nil
			call.suspended = true
		}
		c.suspendedCall[key] = true
	}
}

// resumeSuspendedTimers re-arms the watchdogs suspended by suspendTimers. A
// request that finished while suspended is not resurrected.
func (c *Client) resumeSuspendedTimers() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.suspendedCall {
		// A request that finished while suspended is not resurrected: it is no
		// longer in c.pending, so re-arming would leak a timer with no waiter.
		if call, ok := c.pending[key]; ok {
			call.suspended = false
			c.armTimer(call)
		}
	}
	c.suspendedCall = map[string]bool{}
}

// armTimer installs a call's watchdog. It must be called with c.mu held: the
// timer field is read by every completion path, and a plain field write would
// race the settle that stops it.
func (c *Client) armTimer(call *pendingCall) {
	call.timer = time.AfterFunc(call.timeout, func() { c.expire(call.id) })
}

// timeoutChannelLocked returns the channel a timeout event is delivered on, or nil
// when nobody subscribed (the process manager does).
func (c *Client) timeoutChannelLocked() chan RequestTimeoutEvent {
	if len(c.timeouts) == 0 {
		return nil
	}
	return c.timeouts[0]
}

// fireClosed notifies close subscribers. It runs exactly once, from the pump.
func (c *Client) fireClosed() {
	c.mu.Lock()
	channels := append([]chan struct{}(nil), c.closed...)
	c.closed = nil
	c.mu.Unlock()
	for _, ch := range channels {
		close(ch)
	}
}
