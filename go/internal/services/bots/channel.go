// channel.go: the rpc.Channel adapter for IBotsService ("bots").
//
// The method set is the IBotsService interface verbatim. The methods that
// belong to the session leg (status, task selection, permission/elicitation
// answers, watchAutomationRun) are routed through SessionBridge; the ones that
// only read or write the bots' own files are implemented here.
//
// No on* events exist on IBotsService.
package bots

import (
	"context"
	"encoding/json"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts the service to the RPC surface.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Listen: IBotsService has no on* events.
func (c *Channel) Listen(_ context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

// Call dispatches one IBotsService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "syncAppRuntimePreferences":
		// Step 3.10 owns the agent runtime preferences; the bots service only
		// forwards them to the remote bridges, and accepts them for the Node-side
		// bridge.
		return nil, c.Service.SyncAppRuntimePreferences(ctx, args)
	case "getStatus":
		if err := requireNoArgs(method, args); err != nil {
			return nil, err
		}
		return c.Service.GetStatus(ctx), nil
	case "getConfig":
		if err := requireNoArgs(method, args); err != nil {
			return nil, err
		}
		config, err := c.Service.GetConfig(ctx)
		if err != nil {
			return nil, err
		}
		return configToValue(config)
	case "listWorkspaceRefs":
		params, err := paramsRecord(args)
		if err != nil {
			return nil, err
		}
		refs, err := c.Service.ListWorkspaceRefs(ctx, params)
		if err != nil {
			return nil, err
		}
		return workspaceRefsToValue(refs)
	case "listBots":
		if err := requireNoArgs(method, args); err != nil {
			return nil, err
		}
		config, err := c.Service.GetConfig(ctx)
		if err != nil {
			return nil, err
		}
		return configsToValue(config.Bots)
	case "getBotStates":
		if err := requireNoArgs(method, args); err != nil {
			return nil, err
		}
		states, err := c.Service.GetBotStates(ctx)
		if err != nil {
			return nil, err
		}
		return statesToValue(states)
	case "resetBotState":
		botID, err := singleStringArg(args, "contextKey")
		if err != nil {
			return nil, err
		}
		return nil, c.Service.ResetBotState(ctx, botID)
	case "handleInboundMessage":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "invalid params"}
		}
		params, _ := args[0].(map[string]any)
		message, err := decodeInboundMessage(params)
		if err != nil {
			return nil, err
		}
		replies, err := c.Service.HandleInboundMessage(ctx, message)
		if err != nil {
			return nil, err
		}
		return outboundsToValue(replies)
	case "handleProviderCallback":
		provider, payload, err := providerCallbackArgs(args)
		if err != nil {
			return nil, err
		}
		replies, err := c.Service.HandleProviderCallback(ctx, provider, payload)
		if err != nil {
			return nil, err
		}
		return outboundsToValue(replies)
	case "handleProviderCallbackResponse":
		provider, payload, err := providerCallbackArgs(args)
		if err != nil {
			return nil, err
		}
		return toValue(c.Service.HandleProviderCallbackResponse(ctx, provider, payload))
	default:
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
}

func requireNoArgs(method string, args []rpc.Value) error {
	if len(args) != 0 {
		return &rpc.Error{Message: "Method not found: " + method}
	}
	return nil
}

func paramsRecord(args []rpc.Value) (map[string]any, error) {
	if len(args) == 0 {
		return map[string]any{}, nil
	}
	params, ok := args[0].(map[string]any)
	if !ok {
		return nil, &rpc.Error{Message: "invalid params"}
	}
	return params, nil
}

func singleStringArg(args []rpc.Value, key string) (string, error) {
	params, err := paramsRecord(args)
	if err != nil {
		return "", err
	}
	value, _ := params[key].(string)
	return value, nil
}

func providerCallbackArgs(args []rpc.Value) (Provider, map[string]any, error) {
	params, err := paramsRecord(args)
	if err != nil {
		return "", nil, err
	}
	provider, _ := params["provider"].(string)
	payload, _ := params["payload"].(map[string]any)
	if payload == nil {
		payload = map[string]any{}
	}
	return Provider(provider), payload, nil
}

// decodeInboundMessage converts the RPC params object into an InboundMessage by
// round-tripping through JSON: the shared wire shape is the contract, and both
// engines agree on it.
func decodeInboundMessage(params map[string]any) (InboundMessage, error) {
	if params == nil {
		return InboundMessage{}, &rpc.Error{Message: "invalid params"}
	}
	data, err := json.Marshal(params)
	if err != nil {
		return InboundMessage{}, &rpc.Error{Message: "invalid params"}
	}
	var message InboundMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return InboundMessage{}, &rpc.Error{Message: "invalid params"}
	}
	return message, nil
}
