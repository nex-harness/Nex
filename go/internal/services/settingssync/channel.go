// channel.go adapts the settingssync Service to rpc.Channel.
package settingssync

import (
	"context"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts Service to rpc.Channel.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches one ISettingsSyncService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	var params map[string]any
	if len(args) > 0 {
		params, _ = args[0].(map[string]any)
	}
	switch method {
	case "getClaudeAgentsFileMigrationStatus":
		status, err := c.Service.GetClaudeAgentsFileMigrationStatus(ctx, decodeWorkspaceRef(params))
		if err != nil {
			return nil, err
		}
		return rpc.NewObject(
			"sourcePath", status.SourcePath,
			"targetPath", status.TargetPath,
			"sourceExists", status.SourceExists,
			"targetExists", status.TargetExists,
			"supported", status.Supported,
			"unavailableReason", status.UnavailableReason,
		), nil
	case "copyClaudeAgentsFileToNexAgentsFile":
		request := CopyClaudeAgentsRequest{WorkspaceRef: decodeWorkspaceRef(params)}
		if v, ok := params["overwrite"].(bool); ok {
			request.Overwrite = v
		}
		result, err := c.Service.CopyClaudeAgentsFileToNexAgentsFile(ctx, request)
		if err != nil {
			return nil, err
		}
		return rpc.NewObject(
			"sourcePath", result.SourcePath,
			"targetPath", result.TargetPath,
			"status", result.Status,
			"overwritten", result.Overwritten,
			"skippedReason", result.SkippedReason,
		), nil
	case "detect":
		request := DetectRequest{WorkspaceRef: decodeWorkspaceRef(params)}.withParams(params)
		result, err := c.Service.Detect(ctx, request)
		if err != nil {
			return nil, err
		}
		return discoveryToRPC(result), nil
	case "importSelected":
		request := ImportSelectedRequest{WorkspaceRef: decodeWorkspaceRef(params)}
		if raw, ok := params["selections"].([]any); ok {
			for _, item := range raw {
				if obj, ok := item.(map[string]any); ok {
					request.Selections = append(request.Selections, decodeSelection(obj))
				}
			}
		}
		result, err := c.Service.ImportSelected(ctx, request)
		if err != nil {
			return nil, err
		}
		return importResultToRPC(result), nil
	case "getFirstRunPromptState":
		state, err := c.Service.GetFirstRunPromptState(ctx)
		if err != nil {
			return nil, err
		}
		return rpc.NewObject("handled", state.Handled), nil
	case "markFirstRunPromptHandled":
		if err := c.Service.MarkFirstRunPromptHandled(ctx); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return nil, &rpc.Error{Message: "Method not found: " + method}
}

// Listen: no events (importSelected's onProgress callback is a request
// parameter, not a channel event).
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

func decodeWorkspaceRef(params map[string]any) WorkspaceRef {
	ref := WorkspaceRef{}
	if v, ok := params["workspacePath"].(string); ok {
		ref.WorkspacePath = v
	}
	if v, ok := params["workspaceIdentity"].(string); ok {
		ref.WorkspaceIdentity = v
	}
	return ref
}

func (r DetectRequest) withParams(params map[string]any) DetectRequest {
	if arr, ok := params["categories"].([]any); ok {
		for _, item := range arr {
			if s, ok := item.(string); ok {
				r.Categories = append(r.Categories, s)
			}
		}
	}
	if v, ok := params["intent"].(string); ok {
		r.Intent = v
	}
	return r
}

func decodeSelection(obj map[string]any) Selection {
	selection := Selection{}
	selection.Agent, _ = obj["agent"].(string)
	selection.Category, _ = obj["category"].(string)
	selection.SourceScope, _ = obj["sourceScope"].(string)
	selection.TargetScope, _ = obj["targetScope"].(string)
	selection.ImportMode, _ = obj["importMode"].(string)
	selection.SkillPaths = decodeStrings(obj["skillPaths"])
	selection.CommandPaths = decodeStrings(obj["commandPaths"])
	selection.PluginPaths = decodeStrings(obj["pluginPaths"])
	selection.McpServerPaths = decodeStrings(obj["mcpServerPaths"])
	return selection
}

func decodeStrings(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
