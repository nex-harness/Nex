// service_test.go (step 3.8): the service + rpc.Channel surface. The channel
// is the only place the two server-owned methods and the four process/*
// notifications are reachable, so both the payloads and the failure mode of a
// skipped sampling round are pinned here.
package processtelemetry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/tylinux/nex/go/internal/rpc"
)

// stubAgent answers process/childProcesses.
type stubAgent struct {
	result  ChildProcessesResult
	err     error
	request ChildProcessesRequest
	calls   int
}

func (a *stubAgent) ProcessChildProcesses(_ context.Context, request ChildProcessesRequest) (ChildProcessesResult, error) {
	a.calls++
	a.request = request
	return a.result, a.err
}

func newFixtureService(t *testing.T, rows []ResourceRow, readOK bool) (*Service, *Sampler) {
	t.Helper()
	now := int64(1_700_000_000_000)
	sampler := NewSampler(SamplerOptions{
		ReadTable: func(context.Context) ([]ResourceRow, bool) { return rows, readOK },
		Now:       func() int64 { return now },
		// Two cores: a table row is attributed without a CPU differential.
		LogicalCPUCount: func() int { return 2 },
	})
	return New(ServiceOptions{
		Sampler: sampler,
		HostPID: func() int { return 100 },
		Agents: func() []Agent {
			return []Agent{{
				PID: 200, Provider: "nex-demo", WorkspacePath: "/home/alice/proj",
				Children: []ChildProcess{{
					PID: 300, ServerName: "fs", MCPSource: MCPSourceBuiltin, PluginName: "files",
				}},
			}}
		},
	}), sampler
}

func fixtureRows() []ResourceRow {
	return []ResourceRow{
		{PID: 100, PPID: 1, RSSKb: 1000, CPUTimeMs: 0, Command: "/opt/nex/host"},
		{PID: 200, PPID: 100, RSSKb: 2000, CPUTimeMs: 0, Command: "/opt/nex/nex-agent-nex-demo"},
		{PID: 300, PPID: 200, RSSKb: 3000, CPUTimeMs: 0, Command: "/opt/nex/mcp"},
	}
}

func TestProcessResourceSampleAttributesTheHostSubtree(t *testing.T) {
	service, _ := newFixtureService(t, fixtureRows(), true)
	result, err := service.ProcessResourceSample(context.Background())
	if err != nil {
		t.Fatalf("processResourceSample: %v", err)
	}
	if !result.IdentityVerified {
		t.Fatal("a completed round must report identityVerified")
	}
	if result.LogicalCPUCount != 2 || result.SampledAt == 0 {
		t.Fatalf("machine facts = %#v", result)
	}
	if len(result.Processes) != 2 {
		t.Fatalf("processes = %#v", result.Processes)
	}
	byPID := map[int]UsageProcess{}
	for _, row := range result.Processes {
		byPID[row.PID] = row
	}
	if byPID[200].GroupKey != "cli" || byPID[300].GroupKey != "builtin:files" {
		t.Fatalf("attribution = %#v", byPID)
	}
}

func TestProcessResourceSampleReportsASkippedRoundDistinctly(t *testing.T) {
	service, _ := newFixtureService(t, nil, false)
	result, err := service.ProcessResourceSample(context.Background())
	if !IsRoundSkipped(err) {
		t.Fatalf("error = %v, want the round-skipped marker", err)
	}
	if result.IdentityVerified || len(result.Processes) != 0 {
		t.Fatalf("a skipped round must not look like an empty machine: %#v", result)
	}
}

func TestChannelDispatchAndErrorMapping(t *testing.T) {
	service, _ := newFixtureService(t, fixtureRows(), true)
	channel := &Channel{Service: service}
	ctx := context.Background()

	value, err := channel.Call(ctx, "processResourceSample", nil)
	if err != nil {
		t.Fatalf("processResourceSample: %v", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if decoded["identityVerified"] != true {
		t.Fatalf("wire payload = %s", encoded)
	}

	if _, err := channel.Call(ctx, "nope", nil); err == nil {
		t.Fatal("an unknown method must fail")
	}
}

func TestChannelSurfacesTheSkippedRoundAsAnError(t *testing.T) {
	service, _ := newFixtureService(t, nil, false)
	channel := &Channel{Service: service}
	if _, err := channel.Call(context.Background(), "processResourceSample", nil); err == nil {
		t.Fatal("a skipped round must not be reported as an empty result")
	}
}

func TestChildProcessesPassesTheWorkspaceIdentity(t *testing.T) {
	agent := &stubAgent{result: ChildProcessesResult{Processes: []ChildProcess{
		{PID: 300, ServerName: "fs", MCPSource: MCPSourceBuiltin, PluginName: "files"},
	}}}
	service, _ := newFixtureService(t, fixtureRows(), true)
	service.agent = agent
	channel := &Channel{Service: service}

	value, err := channel.Call(context.Background(), "childProcesses", []rpc.Value{
		map[string]any{
			"workspacePath": "/w/a", "workspaceIdentity": "ws-1", "remoteSessionId": "r-1",
		},
	})
	if err != nil {
		t.Fatalf("childProcesses: %v", err)
	}
	if agent.calls != 1 {
		t.Fatalf("agent calls = %d", agent.calls)
	}
	if agent.request.WorkspaceIdentity != "ws-1" || agent.request.RemoteSessionID != "r-1" ||
		agent.request.WorkspacePath != "/w/a" {
		t.Fatalf("the identity triple must be forwarded: %#v", agent.request)
	}
	if encoded, _ := json.Marshal(value); !json.Valid(encoded) {
		t.Fatalf("childProcesses payload is not valid JSON: %s", encoded)
	}
}

func TestChildProcessesWithoutAnAgentReturnsEmpty(t *testing.T) {
	service, _ := newFixtureService(t, fixtureRows(), true)
	channel := &Channel{Service: service}
	value, err := channel.Call(context.Background(), "childProcesses", nil)
	if err != nil {
		t.Fatalf("childProcesses: %v", err)
	}
	encoded, _ := json.Marshal(value)
	if string(encoded) != `{"processes":null}` && string(encoded) != `{}` {
		t.Fatalf("payload = %s", encoded)
	}
}

func TestChildProcessesSurfacesTheAgentError(t *testing.T) {
	agent := &stubAgent{err: errors.New("agent gone")}
	service, _ := newFixtureService(t, fixtureRows(), true)
	service.agent = agent
	channel := &Channel{Service: service}
	if _, err := channel.Call(context.Background(), "childProcesses", nil); err == nil {
		t.Fatal("an agent failure must surface")
	}
}

func TestChannelListenServesTheFourProcessNotifications(t *testing.T) {
	service, _ := newFixtureService(t, fixtureRows(), true)
	channel := &Channel{Service: service}
	for _, event := range []string{
		NotificationResourceSample, NotificationMCPTelemetry,
		NotificationMCPResourceSamples, NotificationToolExecResource,
	} {
		stream, cancel, err := channel.Listen(context.Background(), event, nil)
		if err != nil {
			t.Fatalf("listen %s: %v", event, err)
		}
		if stream == nil {
			t.Fatalf("listen %s returned no stream", event)
		}
		// Cancelling must close the stream so a subscriber cannot leak.
		cancel()
		if _, open := <-stream; open {
			t.Fatalf("cancel did not close the %s stream", event)
		}
	}
	if _, _, err := channel.Listen(context.Background(), "process/unknown", nil); err == nil {
		t.Fatal("an unknown event must fail")
	}
}

func TestNewServiceDefaultsToThePlatformTableReader(t *testing.T) {
	service := New(ServiceOptions{})
	if service.sampler == nil || service.sampler.readTable == nil {
		t.Fatal("the default sampler must carry a platform table reader")
	}
	if service.sampler.cpuCount() < 1 {
		t.Fatalf("logical cpu count = %d", service.sampler.cpuCount())
	}
}
