package protocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
)

// Handler answers one inbound request. Returning an *RPCError sends it verbatim;
// any other error becomes CodeInternal. Notifications get no reply.
type Handler interface {
	HandleRequest(ctx context.Context, req Message) (result []byte, err error)
	HandleNotification(ctx context.Context, n Message)
}

// ResponseHandler is implemented by handlers that send their own requests to the
// host and expect the answers back (KindResponse/KindError frames). When a
// handler implements it, Serve routes inbound responses to it instead of
// dropping them.
type ResponseHandler interface {
	HandleResponse(resp Message)
}

// PostResponseHook is implemented by handlers that need to run a callback after
// each request's response has been written (the request-scoped post-response
// outbox). Serve calls it once per handled request.
type PostResponseHook interface {
	RunPostResponse()
}

// RequestSerializer is implemented by handlers that need each request to be
// handled and answered without interleaving with another request (so responses
// are written in handler-completion order). Serve locks around HandleRequest,
// the response write and the post-response hook.
type RequestSerializer interface {
	Lock()
	Unlock()
}

// Serve reads frames from r until EOF or a fatal framing error and dispatches
// them to h. Replies are written through w, which serialises concurrent writes.
//
// A frame that is not a valid protocol message ends the session: the host
// treats the same condition as fatal on its side (protocol_parse_error closes
// the transport), so continuing would only desynchronise the two ends.
//
// Requests are handled on their own goroutine so a handler may block on an
// answer from the host (Host.Request) while the reader keeps reading. When a
// handler implements ResponseHandler, inbound responses are routed to it so
// those waits resolve; otherwise they are dropped loudly. Requests are not
// serialised against each other here; the handler owns its own exclusion.
func Serve(ctx context.Context, r io.Reader, w *FrameWriter, h Handler, log *slog.Logger) error {
	fr := NewFrameReader(r, 0)
	responder, _ := h.(ResponseHandler)
	hook, _ := h.(PostResponseHook)
	serializer, _ := h.(RequestSerializer)
	for {
		frame, err := fr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read frame: %w", err)
		}
		msg, err := ParseMessage(frame)
		if err != nil {
			return fmt.Errorf("parse frame: %w", err)
		}
		switch msg.Kind {
		case KindRequest:
			go dispatchRequest(ctx, w, h, hook, serializer, msg)
		case KindResponse, KindError:
			if responder != nil {
				responder.HandleResponse(msg)
			} else {
				log.Warn("unexpected inbound message", "kind", msg.Kind.String(), "id", msg.ID.String())
			}
		case KindNotification:
			h.HandleNotification(ctx, msg)
		}
	}
}

func dispatchRequest(ctx context.Context, w *FrameWriter, h Handler, hook PostResponseHook, serializer RequestSerializer, msg Message) {
	if serializer != nil {
		serializer.Lock()
		defer serializer.Unlock()
	}
	reply := Message{ID: msg.ID}
	result, herr := h.HandleRequest(ctx, msg)
	if herr != nil {
		var rpcErr *RPCError
		if !errors.As(herr, &rpcErr) {
			rpcErr = &RPCError{Code: CodeInternal, Message: herr.Error()}
		}
		reply.Kind, reply.Error = KindError, rpcErr
	} else {
		reply.Kind, reply.Result = KindResponse, result
	}
	_ = w.WriteMessage(reply)
	if hook != nil {
		hook.RunPostResponse()
	}
}
