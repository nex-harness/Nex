// channel.go: the rpc.Channel adapter for the retired IPluginsService.
package plugins

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts the service to the RPC surface.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Listen: IPluginsService has no on* events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

// Call dispatches one IPluginsService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if len(args) == 0 {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	var params struct {
		WorkspacePath     string `json:"workspacePath"`
		WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
		Source            string `json:"source,omitempty"`
		Marketplace       string `json:"marketplace,omitempty"`
		PluginName        string `json:"pluginName,omitempty"`
		Enabled           bool   `json:"enabled,omitempty"`
	}
	if err := decodeArg(args[0], &params); err != nil {
		return nil, err
	}
	switch method {
	case "getOverview":
		return c.Service.GetOverview(ctx, params.WorkspacePath, params.WorkspaceIdentity)
	case "addMarketplace":
		return nil, c.Service.AddMarketplace(ctx, params.WorkspacePath, params.WorkspaceIdentity, params.Source)
	case "removeMarketplace":
		return nil, c.Service.RemoveMarketplace(ctx, params.WorkspacePath, params.WorkspaceIdentity, params.Marketplace)
	case "updateMarketplace":
		return nil, c.Service.UpdateMarketplace(ctx, params.WorkspacePath, params.WorkspaceIdentity, params.Marketplace)
	case "installPlugin":
		return nil, c.Service.InstallPlugin(ctx, params.WorkspacePath, params.WorkspaceIdentity, params.PluginName, params.Marketplace)
	case "uninstallPlugin":
		return nil, c.Service.UninstallPlugin(ctx, params.WorkspacePath, params.WorkspaceIdentity, params.PluginName, params.Marketplace)
	case "setEnabled":
		return nil, c.Service.SetPluginEnabled(ctx, params.WorkspacePath, params.WorkspaceIdentity, params.PluginName, params.Marketplace, params.Enabled)
	default:
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
}

func decodeArg(arg rpc.Value, target any) error {
	if arg == nil {
		return nil
	}
	data, err := json.Marshal(arg)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return nil
}
