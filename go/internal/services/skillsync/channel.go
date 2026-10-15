// channel.go: the rpc.Channel adapter for ISkillSyncService ("skill-sync").
// No on* events exist on this interface.
package skillsync

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts the service to the RPC surface.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Listen: ISkillSyncService has no on* events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

// Call dispatches one ISkillSyncService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "listLocalUserSkillCandidates":
		if len(args) != 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		return c.Service.ListLocalUserSkillCandidates(ctx)
	case "listRemoteUserSkillStatuses":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		var params struct {
			DirectoryNames []string `json:"directoryNames"`
			Skills         []struct {
				DirectoryName string `json:"directoryName"`
				Name          string `json:"name"`
			} `json:"skills,omitempty"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		requested := make([]struct {
			DirectoryName string
			Name          string
		}, 0, len(params.Skills))
		for _, skill := range params.Skills {
			requested = append(requested, struct {
				DirectoryName string
				Name          string
			}{DirectoryName: skill.DirectoryName, Name: skill.Name})
		}
		return c.Service.ListRemoteUserSkillStatuses(ctx, params.DirectoryNames, requested)
	case "exportSkillsArchive":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		var params struct {
			SkillIDs []string `json:"skillIds"`
		}
		if err := decodeArg(args[0], &params); err != nil {
			return nil, err
		}
		return c.Service.ExportSkillsArchive(ctx, params.SkillIDs)
	case "checkRemoteUserSkillWriteAccess":
		if len(args) != 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		return c.Service.CheckRemoteUserSkillWriteAccess(ctx)
	case "importSkillsArchive":
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
		result, err := c.Service.ImportSkillsArchive(ctx, params.Archive, params.Overwrite)
		if err != nil {
			return nil, err
		}
		return resultToValue(result)
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

// resultToValue normalizes nil slices to JSON [] (the TS always sends
// arrays) and keeps the archive as a byte array.
func resultToValue(result *ImportResult) (rpc.Value, error) {
	if result == nil {
		return nil, nil
	}
	if result.Results == nil {
		result.Results = []ImportResultItem{}
	}
	return result, nil
}
