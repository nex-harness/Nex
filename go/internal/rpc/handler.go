package rpc

import "context"

// This file holds the server-side handler contract and the small helpers that adapt a request
// body to it. It is the Go spelling of the handler-facing half of packages/rpc/src —
// IServerChannel in channels.shared.ts plus the request-shaping rules in proxy-channel.ts.

// Channel is the server side of one named service. It is the Go transcription of
// IServerChannel: `Call` maps to `call(ctx, command, arg, token)` and `Listen` to
// `listen(ctx, event, arg)`, both of which ProxyChannel.fromService auto-generates from a
// service object.
//
// A ProxyChannel service exposes plain methods as RPC methods and any `onX` (uppercase X) as
// an event, with `onDynamicX` being a METHOD that RETURNS the event and receives the listen
// argument. Go has no runtime reflection here, so the naming convention is applied by the
// Channel implementation rather than by the server; the server only routes by name.
type Channel interface {
	// Call invokes `service[method](...args)` — args is the deserialized request body, which
	// for the proxy convention is the positional argument array. Returning a *RawError sends
	// the rejection as PromiseErrorObj (203); any other error becomes PromiseError (202).
	Call(ctx context.Context, method string, args []Value) (Value, error)
	// Listen subscribes to `service.onX(event)` (or `onDynamicX(arg)`, which is called with
	// arg and returns the event) and returns a channel of values plus an unsubscribe func.
	// Events are sent on the channel until unsubscribe is called; the server copies them to
	// the connection and never blocks on this channel beyond a bounded buffer.
	//
	// CONTRACT: the producer must stop when unsubscribe is called and must not block forever
	// on a send nobody reads. The server relies on this for back-pressure — a listener whose
	// bounded queue overflows is abandoned and unsubscribed, so a producer that ignores
	// unsubscribe would park a goroutine for the life of the process. A producer that never
	// blocks (dropping or coalescing instead of waiting for a reader) satisfies this trivially.
	Listen(ctx context.Context, event string, arg Value) (<-chan Value, func(), error)
}

// RawError makes a handler reject with a raw (non-Error) value, which the server sends as
// PromiseErrorObj (203) carrying Value verbatim — the TS `throw "plain string"` case. It is
// distinct from Error, which is the default Go rejection and maps to PromiseError (202).
type RawError struct{ Value Value }

func (r *RawError) Error() string { return "rpc: raw error" }

// toArgs coerces a decoded request body into the positional args slice. The proxy convention
// sends the arguments as the body array; a non-array body becomes a single argument, matching
// `target.apply(handler, args || [])` where a non-array `arg` reaches the service as itself.
func toArgs(body Value) []Value {
	if IsUndefined(body) {
		return nil
	}
	if args, ok := body.([]Value); ok {
		return args
	}
	return []Value{body}
}

// neverValues returns a channel that never produces. A Channel.Listen that returns a nil
// source (no events to deliver) is treated as an endless-but-silent subscription rather than
// a nil channel, which would panic on send; the listener simply never receives and stays
// alive until disposed.
func neverValues() <-chan Value { return make(chan Value) }
