// channel.go adapts the codingplansubscription Service to rpc.Channel.
package codingplansubscription

import (
	"context"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts Service to rpc.Channel.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches one ICodingPlanSubscriptionService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if len(args) == 0 {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	params, _ := args[0].(map[string]any)
	switch method {
	case "getDynamicWorkflowClientConfig":
		forceRefresh, _ := params["forceRefresh"].(bool)
		config, err := c.Service.GetDynamicWorkflowClientConfig(ctx, forceRefresh)
		if err != nil {
			return nil, err
		}
		return rpc.NewObject("mode", config.Mode, "enabled", config.Enabled, "source", config.Source), nil
	}
	return nil, &rpc.Error{Message: "Method not found: " + method}
}

// Listen: no events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}
