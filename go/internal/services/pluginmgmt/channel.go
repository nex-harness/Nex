// channel.go: the rpc.Channel adapter for IPluginManagementService
// ("plugin-management"). The onDynamicPluginOperationProgress event belongs
// to the agent bridge (Phase 4); this channel rejects it explicitly.
package pluginmgmt

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts the service to the RPC surface.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Listen: onDynamicPluginOperationProgress is agent-owned (Phase 4).
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

// Call dispatches one IPluginManagementService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if len(args) == 0 {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	var params struct {
		PluginID    string `json:"pluginId,omitempty"`
		PluginName  string `json:"pluginName,omitempty"`
		Marketplace string `json:"marketplace,omitempty"`
		Source      string `json:"source,omitempty"`
		Enabled     bool   `json:"enabled,omitempty"`
		View        struct {
			PluginPath string `json:"pluginPath,omitempty"`
		} `json:"view,omitempty"`
		PluginPath string `json:"pluginPath,omitempty"`
	}
	if err := decodeArg(args[0], &params); err != nil {
		return nil, err
	}
	switch method {
	case "listPlugins":
		return c.Service.ListPlugins(ctx)
	case "getPluginsOverview":
		return c.Service.GetPluginsOverview(ctx)
	case "describePlugin":
		manifest, path, err := c.Service.DescribePlugin(ctx, params.PluginID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"manifest": manifest, "path": path}, nil
	case "validatePlugin":
		pluginPath := params.PluginPath
		if pluginPath == "" {
			pluginPath = params.View.PluginPath
		}
		return c.Service.ValidatePlugin(ctx, pluginPath)
	case "installPlugin":
		return nil, c.Service.InstallPlugin(ctx, params.PluginName, params.Marketplace)
	case "uninstallPlugin":
		return nil, c.Service.UninstallPlugin(ctx, params.PluginName, params.Marketplace)
	case "setPluginEnabled":
		return nil, c.Service.SetPluginEnabled(ctx, params.PluginName, params.Marketplace, params.Enabled)
	case "addPluginMarketplace":
		return nil, c.Service.AddPluginMarketplace(ctx, params.Source)
	case "removePluginMarketplace", "updatePluginMarketplace", "updatePlugin",
		"restoreBuiltinPlugin", "configurePlugin", "resetPluginConfig",
		"cancelPluginOperation", "getPluginReferenceCatalog", "resolveSuggestedPluginReference":
		return nil, &rpc.Error{Message: "Method not found in this runtime: " + method}
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
