// channel.go: the rpc.Channel adapter for IMcpSyncService ("mcp-sync").
// No on* events exist on this interface.
package mcpsync

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts the service to the RPC surface.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Listen: IMcpSyncService has no on* events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

// Call dispatches one IMcpSyncService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "loadMcpFromUserDirectory":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		var params struct {
			WorkspacePath string `json:"workspacePath,omitempty"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		return c.Service.LoadMcpFromUserDirectory(ctx, params.WorkspacePath)
	case "listWorkspaceMcpServerStatuses":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		var params map[string]any
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		return c.Service.ListWorkspaceMcpServerStatuses(ctx, params)
	case "saveMcpToUserDirectory":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		var payload map[string]any
		if err := decodeArg(args[0], &payload); err != nil {
			return nil, err
		}
		return nil, c.Service.SaveMcpToUserDirectory(ctx, payload)
	case "listLocalUserMcpCandidates":
		if len(args) != 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		result, err := c.Service.ListLocalUserMcpCandidates(ctx)
		if err != nil {
			return nil, err
		}
		if result.Candidates == nil {
			result.Candidates = []Candidate{}
		}
		return result, nil
	case "listRemoteUserMcpStatuses":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		var params struct {
			Names []string `json:"names"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		return c.Service.ListRemoteUserMcpStatuses(ctx, params.Names)
	case "exportMcpServers":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		var params struct {
			ServerIDs []string `json:"serverIds"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		return c.Service.ExportMcpServers(ctx, params.ServerIDs)
	case "checkRemoteUserMcpWriteAccess":
		if len(args) != 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		return c.Service.CheckRemoteUserMcpWriteAccess(ctx)
	case "importMcpServers":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		var params struct {
			Servers             []ExportedServer `json:"servers"`
			LocalHomeDir        string           `json:"localHomeDir"`
			LocalWorkspacePath  string           `json:"localWorkspacePath,omitempty"`
			RemoteWorkspacePath string           `json:"remoteWorkspacePath,omitempty"`
			Overwrite           bool             `json:"overwrite,omitempty"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		result, err := c.Service.ImportMcpServers(ctx, params.Servers, params.LocalHomeDir,
			params.LocalWorkspacePath, params.RemoteWorkspacePath, params.Overwrite)
		if err != nil {
			return nil, err
		}
		if result.Results == nil {
			result.Results = []ImportResultItem{}
		}
		return result, nil
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
