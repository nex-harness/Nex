// channel.go: the rpc.Channel adapter for IHooksService ("hooks").
// grantWorkspaceHookTrust is optional in the TS interface and forwards to
// the agent authority (Phase 4); it is rejected here until that bridge
// exists — a service/UI must never write the trust store directly.
package hooktrust

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts the service to the RPC surface.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Listen: IHooksService has no on* events (its notification surface is the
// agent-side operation progress, not this channel).
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

// Call dispatches one IHooksService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if len(args) == 0 {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	var params struct {
		WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
		WorkspacePath     string `json:"workspacePath"`
		Hooks             []Hook `json:"hooks,omitempty"`
	}
	if err := decodeArg(args[0], &params); err != nil {
		return nil, err
	}
	switch method {
	case "loadHooks":
		result, err := c.Service.LoadHooks(ctx, params.WorkspacePath, params.WorkspaceIdentity)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "saveHooks":
		return nil, c.Service.SaveHooks(ctx, params.WorkspacePath, params.WorkspaceIdentity, params.Hooks)
	case "grantWorkspaceHookTrust":
		// 无 task/session 的预信任必须由 Agent authority 重新发现 canonical
		// snapshot 后写 store；Phase 4 的 agent bridge 之前拒绝，防止 service
		// 层绕过权限边界。
		return nil, &rpc.Error{Message: "grantWorkspaceHookTrust is not available in this runtime"}
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
