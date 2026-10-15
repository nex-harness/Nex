// channel.go: the rpc.Channel adapter for IMemoryService ("memory").
// No on* events exist on this interface.
package memory

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts the service to the RPC surface.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Listen: IMemoryService has no on* events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

// Call dispatches one IMemoryService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "listProjectMemories":
		if len(args) != 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		workspaces, err := c.Service.ListProjectMemories(ctx)
		if err != nil {
			return nil, err
		}
		if workspaces == nil {
			workspaces = []WorkspaceSummary{}
		}
		return workspaces, nil
	case "readProjectMemoryFile":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		var params struct {
			WorkspaceID string `json:"workspaceId"`
			FileName    string `json:"fileName"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		return c.Service.ReadProjectMemoryFile(ctx, params.WorkspaceID, params.FileName)
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
