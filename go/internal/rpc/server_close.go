package rpc

// sendResponse and Close live in their own file so server.go stays under the
// 400-line archcheck limit. They are the write path and the teardown path; the
// dispatch table stays in server.go.

// sendResponse encodes one response into a Regular frame and hands it to the connection's
// writer.
//
// Framing happens here, at the single write path, so no caller can forget it: the peer reads
// with MessageReader, which expects the 13-byte header and ignores anything else.
//
// `abandon` lets a caller give up waiting for queue room; pass nil when the caller has
// nothing to abandon (the writer's own timeout then bounds the wait). A delivery goroutine
// passes its listener's stop channel so a subscription that has already ended is not held up
// by a full queue.
//
// A serialization failure drops the response, exactly as the TS `try { protocol.send } catch
// {}` does — a payload the codec cannot encode has no wire representation and the TS
// swallows it rather than closing a connection that is otherwise healthy.
func (s *Server) sendResponse(respType, id int, body Value, abandon <-chan struct{}) {
	payload, err := EncodeResponse(ResponseHeader{Type: respType, ID: id}, body)
	if err != nil {
		return
	}
	s.writer.enqueue(EncodeFrame(Frame{Type: MsgRegular, Data: payload}), abandon)
}

// Close tears down the connection: cancels every in-flight call and listener and marks the
// server closed. It is idempotent and safe to call from Serve's defer and from a handler.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	active := make([]*activeRequest, 0, len(s.activeRequests))
	for _, a := range s.activeRequests {
		active = append(active, a)
	}
	s.activeRequests = map[int]*activeRequest{}
	s.mu.Unlock()

	for _, a := range active {
		a.release(true)
	}
	s.cancel()
	// Stop the writer AFTER cancelling the context, so a writer parked inside a socket
	// write is released by the cancellation rather than waited on.
	s.writer.stop()
}
