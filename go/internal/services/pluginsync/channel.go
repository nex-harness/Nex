// channel.go: the rpc.Channel adapter for IPluginSyncService ("plugin-sync").
// No on* events exist on this interface.
package pluginsync

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts the service to the RPC surface.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Listen: IPluginSyncService has no on* events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

// Call dispatches one IPluginSyncService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "listLocalUserPluginCandidates":
		if len(args) != 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		result, err := c.Service.ListLocalUserPluginCandidates(ctx)
		if err != nil {
			return nil, err
		}
		if result.Candidates == nil {
			result.Candidates = []Candidate{}
		}
		return result, nil
	case "listRemoteUserPluginStatuses":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		var params struct {
			Plugins []struct {
				PluginID      string `json:"pluginId"`
				DirectoryName string `json:"directoryName"`
			} `json:"plugins"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		refs := make([]struct{ PluginID, DirectoryName string }, 0, len(params.Plugins))
		for _, plugin := range params.Plugins {
			refs = append(refs, struct{ PluginID, DirectoryName string }{plugin.PluginID, plugin.DirectoryName})
		}
		return c.Service.ListRemoteUserPluginStatuses(ctx, refs)
	case "exportPluginsArchive":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		var params struct {
			PluginIDs []string `json:"pluginIds"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		return c.Service.ExportPluginsArchive(ctx, params.PluginIDs)
	case "exportMarketplaceSourceArchive":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		var params struct {
			MarketplaceID string         `json:"marketplaceId"`
			PluginNames   []string       `json:"pluginNames"`
			Source        map[string]any `json:"source"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		return c.Service.ExportMarketplaceSourceArchive(ctx, params.MarketplaceID, params.PluginNames, params.Source)
	case "importPluginsArchive":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		var params struct {
			Archive   []byte `json:"archive"`
			Overwrite bool   `json:"overwrite,omitempty"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		result, err := c.Service.ImportPluginsArchive(ctx, params.Archive, params.Overwrite)
		if err != nil {
			return nil, err
		}
		if result.Results == nil {
			result.Results = []ImportResultItem{}
		}
		return result, nil
	case "checkRemoteUserPluginWriteAccess":
		if len(args) != 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		return c.Service.CheckRemoteUserPluginWriteAccess(ctx)
	case "importMarketplaceSourceArchive":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		var params struct {
			Archive   []byte `json:"archive"`
			Overwrite bool   `json:"overwrite,omitempty"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		return c.Service.ImportMarketplaceSourceArchive(ctx, params.Archive, params.Overwrite)
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
