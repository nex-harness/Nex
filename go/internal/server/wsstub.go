package server

// wsstub.go is the per-connection handler that serves a fixed set of channels with no upstream.
// Step 2.8's gateway replaces it in production (WebSocket.SetConnectionOwner); it stays because
// the step-2.7 tests assert transport behavior — framing, codec, capability, close semantics — and
// a test that needs to reason about a round trip must not also have to reason about a forwarding
// decision it is not testing.
//
// Its shape is the one the gateway took over: register the channels, run the connection, dispose
// on close. Nothing here is a service: the channels exist so a test has something to call.

import (
	"context"
	"sync"

	"github.com/tylinux/nex/go/internal/rpc"
)

// StubChannelName is the channel a registrar-connection serves. The name is namespaced so it
// cannot collide with a real service channel name, which is a bare service identifier
// (`services.exposeOnChannelServer` in the TypeScript).
const StubChannelName = "go.echo"

// StubChannel answers one method and refuses everything else with the exact messages
// proxy-channel.ts produces, so the error path is observable end to end over a real socket.
type StubChannel struct{}

// Call echoes the argument array back, and serves the events the concurrency test subscribes to.
func (c *StubChannel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "echo":
		// The args array is returned as an array value, not an object: the client sees the same
		// positional list it sent.
		out := make([]any, len(args))
		for i, arg := range args {
			out[i] = arg
		}
		return out, nil
	case "fail":
		return nil, rpc.NewError("stub failure")
	default:
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
}

// Listen produces one event carrying arg and then stops. The context-aware producer is the same
// contract every real service must honour (see rpc.Channel), and it is what the cancellation test
// observes: a closed context ends the subscription.
func (c *StubChannel) Listen(ctx context.Context, event string, arg rpc.Value) (<-chan rpc.Value, func(), error) {
	if event != "tick" {
		return nil, nil, &rpc.Error{Message: "Event not found: " + event}
	}
	out := make(chan rpc.Value, 1)
	stop := make(chan struct{})
	go func() {
		defer close(out)
		select {
		case out <- arg:
		case <-stop:
		case <-ctx.Done():
		}
	}()
	var once sync.Once
	return out, func() {
		once.Do(func() { close(stop) })
	}, nil
}

// defaultRegistrar is the registrar a WebSocket with no owner installed falls back to: one stub
// channel per connection, with no shared state, so two connections can never observe each other.
func defaultRegistrar(server *rpc.Server) func() {
	server.RegisterChannel(StubChannelName, &StubChannel{})
	return nil
}

// registrarOwner adapts a ChannelRegistrar to ConnectionOwner, so the endpoint code runs the
// registrar path and the gateway path through one interface.
//
// The teardown order is socket.onClose from setupChannelServer: the per-connection services first
// (the connection scope and the window attachment, which in production live behind the gateway's
// upstream leg), then the channel server, then the socket the caller closes.
type registrarOwner struct {
	endpoints *WebSocket
}

// Handle runs one connection on a fresh rpc.Server.
func (o registrarOwner) Handle(ctx context.Context, socket rpc.Socket, mode string) error {
	server := rpc.NewServer(socket, ctx, o.endpoints.rpcOpts)
	var dispose func()
	if o.endpoints.register != nil {
		dispose = o.endpoints.register(server)
	}
	defer func() {
		if dispose != nil {
			dispose()
		}
		server.Close()
	}()
	return server.Serve(ctx)
}

// Compile-time proof that the stub satisfies the channel contract internal/rpc dispatches on.
var _ rpc.Channel = (*StubChannel)(nil)

// CloseTryAgain closes plainly: the registrar path has no upstream, so there is nothing to tell
// the client to retry. Implementing it is what keeps this a ConnectionOwner and lets the endpoint
// code have one branch instead of two.
func (o registrarOwner) CloseTryAgain(socket rpc.Socket, _ string) error { return socket.Close() }

// Compile-time proof that the registrar path is a ConnectionOwner.
var _ ConnectionOwner = registrarOwner{}
