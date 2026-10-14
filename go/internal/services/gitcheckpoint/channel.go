// channel.go adapts the checkpoint Service to internal/rpc's Channel interface
// under the "git-checkpoint" channel name (ServiceChannels.GitCheckpoint).
package gitcheckpoint

import (
	"context"

	"github.com/tylinux/nex/go/internal/rpc"
	"github.com/tylinux/nex/go/internal/services/git"
)

// Channel adapts Service to rpc.Channel.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches one IGitCheckpointService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if len(args) == 0 || args[0] == nil {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	params, _ := args[0].(map[string]any)
	workspacePath, _ := params["workspacePath"].(string)
	switch method {
	case "createCheckpoint":
		meta, err := c.Service.CreateCheckpoint(ctx, workspacePath)
		if err != nil {
			return nil, err
		}
		return checkpointMetaToValue(meta), nil
	case "diffCheckpoints":
		fromID, _ := params["fromCheckpointId"].(string)
		toID, _ := params["toCheckpointId"].(string)
		diff, err := c.Service.DiffCheckpoints(ctx, workspacePath, fromID, toID)
		if err != nil {
			return nil, err
		}
		files := make([]any, len(diff.Files))
		for i, file := range diff.Files {
			obj := rpc.NewObject(
				"path", file.Path,
				"repoRelativePath", file.RepoRelativePath,
				"workspaceRelativePath", file.WorkspaceRelativePath,
				"kind", string(file.Kind),
				"added", file.Added,
				"removed", file.Removed,
			)
			if file.OriginalPath != nil {
				obj.Set("originalPath", *file.OriginalPath)
			}
			files[i] = obj
		}
		return rpc.NewObject(
			"fromCheckpointId", diff.FromCheckpointID,
			"toCheckpointId", diff.ToCheckpointID,
			"files", files,
		), nil
	case "restoreBetweenCheckpoints":
		fromID, _ := params["fromCheckpointId"].(string)
		toID, _ := params["toCheckpointId"].(string)
		force, _ := params["force"].(bool)
		result, err := c.Service.RestoreBetweenCheckpoints(ctx, workspacePath, fromID, toID, force)
		if err != nil {
			return nil, err
		}
		obj := rpc.NewObject("success", result.Success)
		if result.Conflicts != nil {
			conflicts := make([]any, len(result.Conflicts))
			for i, conflict := range result.Conflicts {
				conflicts[i] = rpc.NewObject(
					"path", conflict.Path,
					"repoRelativePath", conflict.RepoRelativePath,
					"workspaceRelativePath", conflict.WorkspaceRelativePath,
					"reason", string(conflict.Reason),
				)
			}
			obj.Set("conflicts", conflicts)
		}
		if result.RestoredPaths != nil {
			obj.Set("restoredPaths", result.RestoredPaths)
		}
		return obj, nil
	case "deleteCheckpoint":
		checkpointID, _ := params["checkpointId"].(string)
		return nil, c.Service.DeleteCheckpoint(ctx, workspacePath, checkpointID)
	}
	return nil, &rpc.Error{Message: "Method not found: " + method}
}

// Listen: IGitCheckpointService has no events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

func checkpointMetaToValue(meta git.GitCheckpointMeta) rpc.Value {
	return rpc.NewObject(
		"checkpointId", meta.CheckpointID,
		"workspacePath", meta.WorkspacePath,
		"repoRoot", meta.RepoRoot,
		"workspaceInRepoPath", meta.WorkspaceInRepoPath,
		"createdAt", meta.CreatedAt,
		"refName", meta.RefName,
		"commitOid", meta.CommitOid,
		"scope", meta.Scope,
	)
}
