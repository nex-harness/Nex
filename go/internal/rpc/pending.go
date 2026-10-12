package rpc

import "time"

// This file is the pending-request queue of ChannelServer
// (packages/rpc/src/channelServer.ts, collectPendingRequest / flushPendingRequests).
//
// The queue exists because a client can send a call for a channel the server has not
// registered yet: during startup the web UI connects before every service is wired. Rather
// than reject such a call, the server parks it for TimeoutDelayMS and serves it if the
// channel appears in the meantime; if it never does, the client gets an explicit
// Unknown-channel PromiseError instead of a silent hang.
//
// Ownership: Server.pendingRequests is the single queue, guarded by Server.mu. Each parked
// request owns its own timer; the timer and the flush race, so the queue entry is removed
// under the lock and only the side that removed it sends anything.

// pendingRequest is a request parked because its channel is not registered yet.
type pendingRequest struct {
	header RequestHeader
	body   Value
	// timer fires the Unknown-channel timeout. Stopped when the request is flushed on
	// register.
	timer *time.Timer
}

// collectPendingRequest parks a request for a channel that is not registered yet, with a
// timer that drops it with the Unknown-channel PromiseError after TimeoutDelayMS. It mirrors
// collectPendingRequest, including that a non-Promise (an EventListen) is dropped SILENTLY on
// timeout — the TS logs and returns without sending anything, because an event has no
// promise to reject and firing an unsolicited error frame would confuse the client.
func (s *Server) collectPendingRequest(header RequestHeader, body Value) {
	req := &pendingRequest{header: header, body: body}

	req.timer = time.AfterFunc(time.Duration(s.opts.TimeoutDelayMS)*time.Millisecond, func() {
		// Remove this specific request from the queue. Removing the whole channel key (as a
		// naive port would) would strand a sibling request that arrived after this one and
		// is still within its own window.
		s.mu.Lock()
		queue := s.pendingRequests[header.ChannelName]
		for i, pending := range queue {
			if pending != req {
				continue
			}
			rest := append(queue[:i:i], queue[i+1:]...)
			if len(rest) == 0 {
				delete(s.pendingRequests, header.ChannelName)
			} else {
				s.pendingRequests[header.ChannelName] = rest
			}
			break
		}
		s.mu.Unlock()

		if header.Type != RequestPromise {
			return
		}
		s.sendResponse(ResponsePromiseError, header.ID, NewObject(
			"name", "Unknown channel",
			"message", UnknownChannelMessage(header.ChannelName, s.opts.TimeoutDelayMS),
			"stack", Undefined,
		), nil)
	})

	s.mu.Lock()
	s.pendingRequests[header.ChannelName] = append(s.pendingRequests[header.ChannelName], req)
	s.mu.Unlock()
}
