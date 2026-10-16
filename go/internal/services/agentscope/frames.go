package agentscope

import (
	"context"
	"sync"
)

// frames.go is the downstream half of frame routing: reading one workspace's frame
// stream and deciding, per frame, whether this connection owns it.
//
// It is separate from ownership.go (which owns the decision's data) because the
// decision's INPUT is the part worth isolating: a workspace stream carries all
// three topic families and every subscription on that workspace, including the ones
// other windows own. A frame that is not owned here must be dropped, not
// forwarded — cross-delivery between two windows on the same topic is the failure
// this whole layer exists to prevent.

// StartFrames subscribes to one workspace's frame stream and routes it until ctx
// ends or the returned func is called.
//
// Stop must never block: the socket-close path calls it from a context that is
// often already cancelled, and a base service whose stream does not close on
// unsubscribe would otherwise hang the close forever. Stop therefore releases the
// pump on its own channel and delivers the stream unsubscribe without waiting for
// the pump to acknowledge it. It is safe to call more than once, because the RPC
// adapter calls it from the socket-close path while the pump may already have ended.
func (s *Scope) StartFrames(ctx context.Context, params []byte) (func(), error) {
	if err := s.handshake.assertOpen(s.isDisposed()); err != nil {
		return nil, err
	}
	incoming, err := decodeParams(params)
	if err != nil {
		return nil, err
	}
	stream, stop, err := s.base.TopicFrames(ctx, incoming.target())
	if err != nil {
		return nil, err
	}
	if stop == nil {
		// No stream is a legitimate answer for a workspace with no runtime; the
		// facade still owns nothing, so there is nothing to route.
		return func() {}, nil
	}

	halt := make(chan struct{})
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for {
			select {
			case <-ctx.Done():
				return
			case <-halt:
				return
			case raw, ok := <-stream:
				if !ok {
					return
				}
				s.routeFrame(incoming.target(), raw)
			}
		}
	}()

	released := make(chan struct{})
	go func() {
		// The pump is released either by our own halt or because the stream ended
		// on its own; either way the caller can return without waiting for a base
		// service that never closes its channel.
		select {
		case <-halt:
		case <-pumpDone:
		}
		close(released)
	}()

	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			close(halt)
			stop()
		})
		<-released
	}, nil
}

// routeFrame is the per-frame routing decision. A frame this connection does not own
// and has no pending subscribe for is dropped silently: the workspace stream is
// shared, so "not mine" is the normal case, not an error.
func (s *Scope) routeFrame(target Target, raw []byte) {
	frame, err := DecodeFrame(raw)
	if err != nil {
		// A frame that cannot be routed at all (no topic / subscription id) belongs
		// to nobody, and there is no owner to raise a typed fault on. The owned
		// assembler (internal/agentproc/proto) is what turns a routable frame with a
		// broken body into a fault; this layer's job is only the route.
		return
	}
	for _, kind := range routedKinds(frame.Topic) {
		s.owned.deliverIncomingFrame(kind, target, frame)
	}
}

// routedKinds maps a topic prefix to the kind that could own it. The three families
// share one workspace stream and are told apart by prefix, exactly as the base
// service's comment says ("与 conversation 同一通知，按 topic 前缀分流").
func routedKinds(topic string) []Kind {
	for _, kind := range []Kind{KindConversation, KindSessionsIndex, KindWorkspaceConfig} {
		if hasTopicPrefix(kind, topic) {
			return []Kind{kind}
		}
	}
	return nil
}

func hasTopicPrefix(kind Kind, topic string) bool {
	prefix := string(kind) + "/"
	return len(topic) > len(prefix) && topic[:len(prefix)] == prefix
}
