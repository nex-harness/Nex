package system

// channel.go adapts the system Service to internal/rpc's Channel interface
// under the "system" channel name.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts Service to rpc.Channel.
type Channel struct {
	Service *Service
}

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches one ISystemService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "info":
		info, err := c.Service.Info(ctx)
		if err != nil {
			return nil, err
		}
		return rpc.NewObject("homedir", info.Homedir, "platform", info.Platform), nil
	case "listIntegratedTerminalShells":
		shells, err := c.Service.ListIntegratedTerminalShells(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(shells))
		for i, s := range shells {
			out[i] = rpc.NewObject("dialect", s.Dialect, "id", s.ID, "label", s.Label, "path", s.Path, "source", s.Source)
		}
		return out, nil
	case "probeIntranet":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		request, err := decodeProbeRequest(args[0])
		if err != nil {
			return nil, err
		}
		result, err := c.Service.ProbeIntranet(ctx, request)
		if err != nil {
			return nil, err
		}
		return probeResultToValue(result), nil
	}
	return nil, &rpc.Error{Message: "Method not found: " + method}
}

// Listen: no events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

// decodeProbeRequest re-marshals the decoded JSON tree to use the struct tags
// (nil-vs-absent distinctions of port/timeout/… survive because the tree
// came from the RPC decoder).
func decodeProbeRequest(v rpc.Value) (ProbeRequest, error) {
	if v == nil {
		return ProbeRequest{}, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return ProbeRequest{}, fmt.Errorf("system: invalid probeIntranet request")
	}
	var request ProbeRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return ProbeRequest{}, fmt.Errorf("system: invalid probeIntranet request")
	}
	return request, nil
}

func probeResultToValue(r ProbeResult) rpc.Value {
	results := make([]any, len(r.Results))
	for i, res := range r.Results {
		switch v := res.(type) {
		case TcpTargetResult:
			obj := rpc.NewObject(
				"targetId", v.TargetID, "kind", "tcp", "host", v.Host, "port", v.Port,
				"reachable", v.Reachable, "attemptCount", v.AttemptCount, "latencyMs", nil,
			)
			if v.LatencyMs != nil {
				obj.Set("latencyMs", *v.LatencyMs)
			}
			if v.Error != "" {
				obj.Set("error", v.Error)
			}
			results[i] = obj
		case ServiceTargetResult:
			obj := rpc.NewObject(
				"targetId", v.TargetID, "kind", "service", "url", v.URL,
				"reachable", v.Reachable, "attemptCount", v.AttemptCount, "latencyMs", nil,
			)
			if v.LatencyMs != nil {
				obj.Set("latencyMs", *v.LatencyMs)
			}
			if v.Marker != "" {
				obj.Set("marker", v.Marker)
			}
			if v.Error != "" {
				obj.Set("error", v.Error)
			}
			results[i] = obj
		default:
			results[i] = nil
		}
	}
	return rpc.NewObject(
		"isIntranet", r.IsIntranet,
		"reachedTargetCount", r.ReachedTargetCount,
		"requiredSuccessCount", r.RequiredSuccessCount,
		"totalTargets", r.TotalTargets,
		"checkedAt", r.CheckedAt,
		"strategy", r.Strategy,
		"results", results,
	)
}
