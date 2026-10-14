// channel.go adapts the clientconfig Service to rpc.Channel.

package clientconfig

import (
	"context"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts Service to rpc.Channel.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches one IClientConfigService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if len(args) == 0 {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	params, _ := args[0].(map[string]any)
	switch method {
	case "getSnapshot":
		forceRefresh, _ := params["forceRefresh"].(bool)
		snapshot, err := c.Service.GetSnapshot(ctx, forceRefresh)
		if err != nil {
			return nil, err
		}
		if snapshot == nil {
			return nil, nil
		}
		return rpc.NewObject("pluginStoreOrder", snapshot.PluginStoreOrder), nil
	}
	return nil, &rpc.Error{Message: "Method not found: " + method}
}

// Listen: no events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}
