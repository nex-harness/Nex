// service.go: the rpc.Channel for the process telemetry service.
//
// The channel name is "process" (ServiceChannels.Process), owned by the
// desktop host in TS: the host reads the whole-machine table and answers
// main's resource-manager requests. In Go the server side owns it, so the
// constructor takes the sampler and the agent registry as seams — the process
// table is read exactly once per request, never in the main process.
//
// The methods are the TS interface's: processResourceSample (one sample per
// call) and processChildProcesses (the CLI-side reply, proxied through the
// injected agent). Events are the four process/* notifications, which the
// CLI pushes rather than the host polling for.
package processtelemetry

import (
	"context"
	"encoding/json"

	"github.com/tylinux/nex/go/internal/rpc"
)

// ChannelName is the rpc channel name (ServiceChannels.Process).
const ChannelName = "process"

// Notification names (nexProtocolNotifications).
const (
	NotificationResourceSample     = "process/resourceSample"
	NotificationMCPTelemetry       = "process/mcpTelemetry"
	NotificationMCPResourceSamples = "process/mcpResourceSamples"
	NotificationToolExecResource   = "process/toolExecResource"
)

// AgentService is the process/childProcesses seam. In TS the resource manager
// host asks the CLI through IProcessService; in Go the agent bridge (step 3.10)
// supplies it. A failed call leaves Children empty and every descendant falls
// into the cli group — never an error the panel would show.
type AgentService interface {
	// ProcessChildProcesses lists the MCP children of the agent owning the
	// workspace.
	ProcessChildProcesses(ctx context.Context, request ChildProcessesRequest) (ChildProcessesResult, error)
}

// ChildProcessesRequest ports the process/childProcesses params (empty) plus
// the workspace identity triple every agent request forwards.
type ChildProcessesRequest struct {
	WorkspacePath     string
	WorkspaceIdentity string
	RemoteSessionID   string
}

// ChildProcessesResult ports nexProcessChildProcessesResultSchema. The wire
// keys are the protocol's own (camelCase), not Go's default field names.
type ChildProcessesResult struct {
	Processes []ChildProcess `json:"processes"`
}

// ServiceOptions ports the constructor options.
type ServiceOptions struct {
	Sampler *Sampler
	Agent   AgentService
	// Hosts maps a window/host pid to the agents running under it. The
	// resource manager asks for one host's subtree.
	Agents func() []Agent
	// HostPID is the pid whose descendants are attributed.
	HostPID func() int
}

// Service is the process telemetry service.
type Service struct {
	sampler *Sampler
	agent   AgentService
	agents  func() []Agent
	hostPID func() int
}

// New builds the service.
func New(options ServiceOptions) *Service {
	sampler := options.Sampler
	if sampler == nil {
		sampler = NewSampler(SamplerOptions{ReadTable: DefaultTableReader()})
	}
	return &Service{
		sampler: sampler, agent: options.Agent,
		agents: options.Agents, hostPID: options.HostPID,
	}
}

// ResourceSampleResult is the reply of processResourceSample: the attributed
// rows plus the machine facts the panel renders them against.
type ResourceSampleResult struct {
	SampledAt        int64          `json:"sampledAt"`
	LogicalCPUCount  int            `json:"logicalCpuCount"`
	Processes        []UsageProcess `json:"processes"`
	IdentityVerified bool           `json:"identityVerified"`
}

// ProcessResourceSample ports the host-side sampling entry point: read the
// table once, attribute every Host descendant, return the rows.
func (s *Service) ProcessResourceSample(ctx context.Context) (ResourceSampleResult, error) {
	samples, ok := s.sampler.Sample(ctx)
	if !ok {
		// A skipped round must not be reported as "no processes": the panel
		// distinguishes "not sampled" from "nothing running".
		return ResourceSampleResult{IdentityVerified: false}, errRoundSkipped
	}
	hostPID := 0
	if s.hostPID != nil {
		hostPID = s.hostPID()
	}
	agents := []Agent{}
	if s.agents != nil {
		agents = s.agents()
	}
	return ResourceSampleResult{
		SampledAt:        s.sampler.now(),
		LogicalCPUCount:  s.sampler.cpuCount(),
		Processes:        AttributeHostProcessTree(AttributeOptions{Samples: samples, HostPID: hostPID, Agents: agents}),
		IdentityVerified: true,
	}, nil
}

// ChildProcesses ports the process/childProcesses passthrough: the host
// forwards the CLI's reply so the resource manager can attribute MCP roots.
func (s *Service) ChildProcesses(ctx context.Context, request ChildProcessesRequest) (ChildProcessesResult, error) {
	if s.agent == nil {
		return ChildProcessesResult{}, nil
	}
	return s.agent.ProcessChildProcesses(ctx, request)
}

// roundSkippedError marks a round that was not sampled (table read failed).
type roundSkippedError struct{}

func (roundSkippedError) Error() string { return "Conversation process table read was skipped" }

// errRoundSkipped is returned when the whole-machine table could not be read.
var errRoundSkipped error = roundSkippedError{}

// IsRoundSkipped reports whether err is the "round skipped" marker.
func IsRoundSkipped(err error) bool {
	_, ok := err.(roundSkippedError)
	return ok
}

// Channel adapts the service to the RPC surface.
type Channel struct {
	Service *Service
}

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches one method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "processResourceSample":
		result, err := c.Service.ProcessResourceSample(ctx)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "childProcesses":
		var params struct {
			WorkspacePath     string `json:"workspacePath"`
			WorkspaceIdentity string `json:"workspaceIdentity"`
			RemoteSessionID   string `json:"remoteSessionId"`
		}
		if len(args) > 0 {
			if err := decodeArg(args[0], &params); err != nil {
				return nil, err
			}
		}
		result, err := c.Service.ChildProcesses(ctx, ChildProcessesRequest{
			WorkspacePath:     params.WorkspacePath,
			WorkspaceIdentity: params.WorkspaceIdentity,
			RemoteSessionID:   params.RemoteSessionID,
		})
		if err != nil {
			return nil, err
		}
		return result, nil
	default:
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
}

// Listen serves the four process/* notifications. They are CLI-pushed in TS
// (the host never synthesises them), so there is no per-connection stream to
// open here: the agent bridge delivers them through the channel's writer.
func (c *Channel) Listen(_ context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	switch event {
	case NotificationResourceSample, NotificationMCPTelemetry,
		NotificationMCPResourceSamples, NotificationToolExecResource:
		stream := make(chan rpc.Value)
		return stream, func() { close(stream) }, nil
	default:
		return nil, nil, &rpc.Error{Message: "Event not found: " + event}
	}
}

// decodeArg decodes the first RPC argument into target.
func decodeArg(arg rpc.Value, target any) error {
	data, err := json.Marshal(arg)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return &rpc.Error{Message: "invalid params: " + err.Error()}
	}
	return nil
}
