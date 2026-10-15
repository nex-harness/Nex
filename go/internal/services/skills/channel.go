// channel.go: the rpc.Channel adapter for ISkillsService ("skills"). No
// on* events exist on this interface.
package skills

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts the service to the RPC surface.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Listen: ISkillsService has no on* events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

// Call dispatches one ISkillsService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if len(args) == 0 {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	var params struct {
		WorkspacePath     string `json:"workspacePath"`
		WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
		SkillID           string `json:"skillId,omitempty"`
		Enabled           bool   `json:"enabled,omitempty"`
		Prompt            string `json:"prompt,omitempty"`
	}
	if err := decodeArg(args[0], &params); err != nil {
		return nil, err
	}
	switch method {
	case "list":
		return c.Service.List(ctx, params.WorkspacePath, params.WorkspaceIdentity)
	case "setEnabled":
		return nil, c.Service.SetEnabled(ctx, params.WorkspacePath, params.WorkspaceIdentity, params.SkillID, params.Enabled)
	case "buildPromptContext":
		return c.Service.BuildPromptContext(ctx, params.WorkspacePath, params.WorkspaceIdentity, params.Prompt)
	case "copyToCommon":
		newPath, err := c.Service.CopyToCommon(ctx, params.WorkspacePath, params.WorkspaceIdentity, params.SkillID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"newPath": newPath}, nil
	case "removeFromCommon":
		return nil, c.Service.RemoveFromCommon(ctx, params.WorkspacePath, params.WorkspaceIdentity, params.SkillID)
	case "deleteSkill":
		return nil, c.Service.DeleteSkill(ctx, params.WorkspacePath, params.WorkspaceIdentity, params.SkillID)
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
