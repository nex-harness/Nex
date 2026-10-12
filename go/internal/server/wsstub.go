package server

// wsstub.go holds the placeholder channel a connection serves until step 2.8 installs the gateway
// router. It exists so this step can prove the whole path — browser/desktop socket, 13-byte
// framing, value codec, per-connection rpc.Server — with a round trip nobody has to stub out.
// Nothing here is a service: it is a fixed shape the tests drive, and step 2.8 deletes it.

import (
	"context"
	"sync"

	"github.com/tylinux/nex/go/internal/rpc"
)

// StubChannelName is the channel registered on each connection while the router is a stub. The
// name is namespaced so it cannot collide with a real service channel name, which is a bare
// service identifier (`services.exposeOnChannelServer` in the TypeScript).
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

// defaultRegistrar is the registrar NewWebSocket installs when none is set: one stub channel per
// connection, with no shared state, so two connections can never observe each other.
func defaultRegistrar(server *rpc.Server) func() {
	server.RegisterChannel(StubChannelName, &StubChannel{})
	return nil
}

// Compile-time proof that the stub satisfies the channel contract internal/rpc dispatches on.
var _ rpc.Channel = (*StubChannel)(nil)
