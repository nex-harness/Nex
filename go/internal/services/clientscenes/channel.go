// channel.go adapts the clientscenes Service to rpc.Channel.
package clientscenes

import (
	"context"
	"encoding/json"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts Service to rpc.Channel.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches one IClientScenesService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "list":
		response, err := c.Service.List(ctx)
		if err != nil {
			return nil, err
		}
		return rpc.NewObject(
			"code", response.Code,
			"msg", response.Msg,
			"data", json.RawMessage(response.Data),
		), nil
	}
	return nil, &rpc.Error{Message: "Method not found: " + method}
}

// Listen: no events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}
