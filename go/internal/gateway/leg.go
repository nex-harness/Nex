package gateway

// leg.go is the type that owns one client connection's relationship with the Node upstream: the
// socket itself, the id→route map that answers a cancel, and the single reader goroutine that
// moves frames from Node to the client.
//
// Concurrency (the ordering guarantee in the plan's diagram):
//
//	client ──frames──► gateway ──same bytes──► Node
//	client ◄──frames── gateway ◄──same bytes── Node
//
//	one goroutine per direction, no shared buffer between them. The client→Node direction is
//	serialized by Upstream.writeMu (one concurrent writer is all a WebSocket allows); the
//	Node→client direction is this leg's single Run loop. Per-id ordering therefore follows from
//	"each direction is written by exactly one writer", not from any per-id bookkeeping — the
//	id→route map exists only to decide whether a cancel is forwarded at all.

import (
	"context"
	"errors"
	"sync"

	"github.com/tylinux/nex/go/internal/adapters/ws"
	"github.com/tylinux/nex/go/internal/rpc"
)

// Upstream is one open Node leg. Safe for concurrent use.
type Upstream struct {
	socket *ws.Conn
	log    Logger

	// writeMu serializes writes to the upstream socket. The library permits one concurrent
	// writer, and ordering depends on it: the Go handler goroutines that produce responses are
	// independent, so without this a 101 cancel could overtake the 100 it cancels.
	writeMu sync.Mutex
	// closed guards writes to a socket the reader already tore down.
	closed bool

	done      chan struct{}
	closeOnce sync.Once

	// onEnd fires once when the leg ends, with the cause. It is what turns an upstream drop into
	// "close the client socket" rather than a half-open connection nobody notices.
	onEnd    func(error)
	endOnce  sync.Once
	sendToMu sync.Mutex
	// end is closed once the leg has fully finished.
	end chan struct{}
}

// OnEnd registers the end-of-leg callback. Set it before Run starts: a later call is ignored, so a
// racing reader can never fire a callback that was never installed.
func (u *Upstream) OnEnd(fn func(error)) { u.onEnd = fn }

// Done is closed when the leg is closed (not necessarily when the reader has returned). Tests wait
// on it instead of sleeping.
func (u *Upstream) Done() <-chan struct{} { return u.done }

// Ended is closed when the reader loop has returned. This is the stronger signal: a goroutine-leak
// assertion wants "the reader is gone", which Done alone does not prove because Close closes it.
func (u *Upstream) Ended() <-chan struct{} { return u.end }

// Write sends one already-framed message upstream. The bytes must be the client's own frame, not a
// re-encoding of it.
func (u *Upstream) Write(ctx context.Context, data []byte) error {
	u.writeMu.Lock()
	defer u.writeMu.Unlock()
	if u.closed {
		return errors.New("gateway: upstream is closed")
	}
	return u.socket.Write(ctx, data)
}

// Close ends the leg. It is idempotent.
func (u *Upstream) Close() error {
	var err error
	u.closeOnce.Do(func() {
		u.writeMu.Lock()
		u.closed = true
		u.writeMu.Unlock()
		err = u.socket.Close()
		close(u.done)
	})
	return err
}

// Run pumps frames from Node to the client until the leg ends or ctx is done.
//
// It reads RAW WebSocket MESSAGES, not RPC frames: a message is one ProtocolMessage, but the
// zero-copy requirement means the gateway must not re-frame what it forwards. fn receives the exact
// bytes and decides: the forwarder writes them to the client, and the Initialize filter drops the
// one 200 frame Go must swallow.
func (u *Upstream) Run(ctx context.Context, fn func(message []byte) error) error {
	defer close(u.end)
	err := u.pump(ctx, fn)
	u.endOnce.Do(func() {
		if u.onEnd != nil {
			u.onEnd(err)
		}
	})
	return err
}

func (u *Upstream) pump(ctx context.Context, fn func([]byte) error) error {
	for {
		data, err := u.socket.Read(ctx)
		if err != nil {
			return err
		}
		if err := fn(data); err != nil {
			return err
		}
	}
}

// frameInspect is what the gateway reads out of a client frame to route it. Nothing else is
// decoded: see the file header on zero-copy forwarding.
type frameInspect struct {
	// OK is false when the frame is not a decodable Regular request; the caller then cannot route
	// it and must not guess.
	OK bool
	// RequestType is header[0] (100/101/102/103).
	RequestType int
	// RequestID is header[1], used to look up the route of an in-flight request.
	RequestID int
	// ChannelName is header[2]. Only meaningful for RequestPromise and RequestEventListen: a
	// cancel (101) or dispose (103) carries only [type, id], so its channel is empty.
	ChannelName string
	// MethodName is header[3]. Read because it is in the same array the channel name came from,
	// and it is what makes an unknown-method failure attributable to a channel the client named.
	MethodName string
}

// inspectPayload reads only the header of an already-framed Regular payload.
//
// payload is what MessageReader.Read returns: the frame BODY, with the 13-byte ProtocolMessage
// header already stripped, and what will be written upstream verbatim. It stops after the header
// array, because the body that follows can be a megabyte of attachment bytes and no routing
// decision needs it.
//
// OK is false when the payload does not decode as a request header — a Regular frame carrying
// something other than channel traffic. That frame is dispatched locally so the channel server's
// own codec decides what it is, rather than the gateway forwarding on a channel guess.
func inspectPayload(payload []byte) frameInspect {
	header, err := rpc.DecodeRequestHeader(payload)
	if err != nil {
		return frameInspect{}
	}
	return frameInspect{
		OK:          true,
		RequestType: header.Type,
		RequestID:   header.ID,
		ChannelName: header.ChannelName,
		MethodName:  header.Name,
	}
}

// isInitializeMessage reports whether a frame FROM THE UPSTREAM is the Initialize (200) response,
// which Go swallows: the client already received one from the Go rpc.Server when the connection was
// built, and a second would be an observable difference from the Node server, where there is
// exactly one per connection.
func isInitializeMessage(message []byte) bool {
	decoded, ok, err := rpc.DecodeFrame(message)
	if err != nil || !ok || decoded.Type != rpc.MsgRegular {
		return false
	}
	header, err := rpc.DecodeResponseHeader(decoded.Data)
	if err != nil {
		return false
	}
	return header.Type == rpc.ResponseInitialize
}
