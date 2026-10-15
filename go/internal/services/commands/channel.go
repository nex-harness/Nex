// channel.go: the rpc.Channel adapter for ICommandsService ("commands").
// No on* events exist on this interface.
package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts the service to the RPC surface.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Listen: ICommandsService has no on* events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

// Call dispatches one ICommandsService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if len(args) == 0 {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	switch method {
	case "list":
		var params struct {
			AgentSource       string `json:"agentSource,omitempty"`
			WorkspacePath     string `json:"workspacePath,omitempty"`
			WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		return c.Service.List(ctx, params.AgentSource, params.WorkspacePath, params.WorkspaceIdentity)
	case "writeCommandFile":
		var params struct {
			AgentSource       string        `json:"agentSource,omitempty"`
			StorageLevel      string        `json:"storageLevel,omitempty"`
			WorkspacePath     string        `json:"workspacePath,omitempty"`
			WorkspaceIdentity string        `json:"workspaceIdentity,omitempty"`
			Config            commandConfig `json:"config"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		command, err := c.Service.WriteCommandFile(ctx, params.AgentSource, params.StorageLevel, params.WorkspacePath, params.Config)
		if err != nil {
			return nil, err
		}
		return map[string]any{"command": command}, nil
	case "updateCommandFile":
		var params struct {
			AgentSource       string        `json:"agentSource,omitempty"`
			StorageLevel      string        `json:"storageLevel,omitempty"`
			WorkspacePath     string        `json:"workspacePath,omitempty"`
			WorkspaceIdentity string        `json:"workspaceIdentity,omitempty"`
			OldFilePath       string        `json:"oldFilePath,omitempty"`
			Config            commandConfig `json:"config"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		command, err := c.Service.UpdateCommandFile(ctx, params.AgentSource, params.StorageLevel, params.WorkspacePath, params.OldFilePath, params.Config)
		if err != nil {
			return nil, err
		}
		return map[string]any{"command": command}, nil
	case "deleteCommandFile":
		var params struct {
			FilePath string `json:"filePath"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		return nil, c.Service.DeleteCommandFile(ctx, params.FilePath)
	case "setCommandEnabled":
		var params struct {
			FilePath string `json:"filePath"`
			Enabled  bool   `json:"enabled"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		return nil, c.Service.SetCommandEnabled(ctx, params.FilePath, params.Enabled)
	case "getPrimaryUserCommandsDirectory":
		var params struct {
			AgentSource string `json:"agentSource,omitempty"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		path, err := c.Service.GetPrimaryUserCommandsDirectory(ctx, params.AgentSource)
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": path}, nil
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
