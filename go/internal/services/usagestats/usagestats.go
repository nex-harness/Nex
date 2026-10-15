// Package usagestats is the IUsageStatsService port (step 3.8): App Usage
// reads the agent database's real statistics through the Nex Protocol. The
// service is a thin forwarder — the query semantics (range, timeZone)
// belong to the agent side (getAppUsageStats), which is Phase 4's agent
// bridge. Until 3.10 lands, this package provides the channel surface and
// the exact request passthrough; the AgentService seam is injected so the
// 3.10 cutover wires the real client without touching this file.
//
// Source: packages/services/src/usage-stats/*.ts.
package usagestats

import (
	"context"
	"encoding/json"

	"github.com/tylinux/nex/go/internal/rpc"
)

// ChannelName is the rpc channel name (ServiceChannels.UsageStats).
const ChannelName = "usage-stats"

// Request ports AppUsageRequest.
type Request struct {
	Range    string `json:"range"`
	TimeZone string `json:"timeZone,omitempty"`
}

// AgentService is the seam the TS expresses as
// Pick<INexAgentService, "getAppUsageStats">.
type AgentService interface {
	// GetAppUsageStats returns the raw AppUsageSnapshot JSON value (the
	// snapshot shape is the agent's; this service does not reshape it).
	GetAppUsageStats(ctx context.Context, request Request) (json.RawMessage, error)
}

// Service ports createUsageStatsService.
type Service struct {
	Agent AgentService
}

// New ports createUsageStatsService.
func New(agent AgentService) *Service { return &Service{Agent: agent} }

// GetAppUsageSnapshot ports getAppUsageSnapshot: a pure forward of range +
// timeZone.
func (s *Service) GetAppUsageSnapshot(ctx context.Context, request Request) (json.RawMessage, error) {
	return s.Agent.GetAppUsageStats(ctx, request)
}

// Channel adapts the service to the RPC surface.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches getAppUsageSnapshot.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if method != "getAppUsageSnapshot" {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	var request Request
	if len(args) > 0 {
		if data, err := json.Marshal(args[0]); err == nil {
			if err := json.Unmarshal(data, &request); err != nil {
				return nil, &rpc.Error{Message: "invalid params: " + err.Error()}
			}
		}
	}
	raw, err := c.Service.GetAppUsageSnapshot(ctx, request)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

// Listen: IUsageStatsService has no on* events.
func (c *Channel) Listen(_ context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}
