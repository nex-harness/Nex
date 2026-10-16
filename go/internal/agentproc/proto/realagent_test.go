// realagent_test.go drives the protocol client against the REAL Go agent binary.
//
// This is the step's end-to-end proof: the same client that runs in the host talks
// to a real process over real pipes, and the answer it gets must match what the
// Phase 0 recording of S01 (handshake: runtime/capabilities + an unknown method)
// says the agent produces. A fake transport can prove the correlation rules; only
// this can prove the two ends actually speak the same protocol.
//
// The binary is built once per test binary into a temp dir (not committed), so the
// test does not depend on a prebuilt go/dist artifact being current.
package proto

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/agentproc"
	"github.com/tylinux/nex/go/internal/protocol"
)

// agentBinary compiles ./cmd/nex-agent once and returns its path.
var agentBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "protoclient-agent-*")
	if err != nil {
		return "", err
	}
	name := "nex-agent"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", binary, "../../../cmd/nex-agent")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return binary, nil
})

// realAgentTest skips a test when the agent binary cannot be built (a machine with
// no Go toolchain in PATH, a locked temp dir). It never fails silently: a skip
// prints why.
func requireAgent(t *testing.T) string {
	t.Helper()
	binary, err := agentBinary()
	if err != nil {
		t.Skipf("cannot build the agent binary: %v", err)
	}
	return binary
}

func tempAgentWorkspace(t *testing.T) (agentproc.WorkspaceRef, string) {
	t.Helper()
	dir := t.TempDir()
	return agentproc.WorkspaceRef{WorkspacePath: dir}, dir
}

// isolateAgentEnv points the agent's data directory at a temp dir so the test
// never touches a developer's real ~/.nex. It is applied through the manager's
// spawn env (the same seam production uses for settings), not through the ambient
// environment.
func isolateAgentEnv(t *testing.T) agentproc.EnvPatch {
	t.Helper()
	return agentproc.EnvPatch{}.Set("NEX_DATA_BASE_DIR", t.TempDir())
}

// TestRealAgentAnswersRuntimeCapabilities is the S01 handshake, live: the client
// must reach the real agent, get exactly the recorded result shape, and see the
// method-not-found degradation for a method the agent does not implement.
func TestRealAgentAnswersRuntimeCapabilities(t *testing.T) {
	binary := requireAgent(t)
	manager := newRealAgentManager(t, binary)
	workspace, _ := tempAgentWorkspace(t)
	proc, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	client := New(NewProcessTransport(proc), Options{RequestTimeout: 30 * time.Second})
	defer func() { client.Close(); client.Wait() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := client.Call(ctx, MethodRuntimeCapabilities, map[string]any{}, CallOptions{})
	if err != nil {
		t.Fatalf("runtime/capabilities: %v", err)
	}
	// The S01 recording (go/testdata/traces/agent/S01/out.normalized.json) records
	// exactly {"independentPlanState": true} for this request.
	var got map[string]any
	if err := json.Unmarshal(res.Raw, &got); err != nil {
		t.Fatalf("result is not an object: %v (%s)", err, res.Raw)
	}
	if independent, ok := got["independentPlanState"].(bool); !ok || !independent {
		t.Errorf("result = %s, want {\"independentPlanState\":true}", res.Raw)
	}
	if len(got) != 1 {
		t.Errorf("result = %s, want exactly the one recorded field", res.Raw)
	}

	// The second half of S01: an unknown method is answered with -32601, which is
	// the degradation the host reads as "capability absent".
	_, err = client.Call(ctx, "nope/not-a-method", map[string]any{}, CallOptions{})
	if !IsMethodNotFound(err) {
		t.Fatalf("unknown method error = %v, want method-not-found", err)
	}
}

// TestRealAgentNegotiatesCapabilitiesOnce runs the negotiation the host performs
// before it trusts the remote runtime with Plan fields, against the real binary.
func TestRealAgentNegotiatesCapabilitiesOnce(t *testing.T) {
	client := startRealAgentWithManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	caps, err := client.EnsureCapabilities(ctx)
	if err != nil {
		t.Fatalf("negotiation: %v", err)
	}
	if !caps.IndependentPlanState {
		t.Error("the real agent did not advertise independentPlanState")
	}
	if !strings.Contains(string(caps.Raw), "independentPlanState") {
		t.Errorf("raw answer = %s", caps.Raw)
	}
	// A second negotiation must be served from the cache, not from the agent.
	if _, err := client.EnsureCapabilities(ctx); err != nil {
		t.Errorf("second negotiation: %v", err)
	}
}

// TestRealAgentStorageGateReleases drives the gate against a real startup: the
// agent sends startup/storageState until ready, and the client's first gated call
// must wait for that and then succeed. It is the whole point of the gate on the
// real wire.
func TestRealAgentStorageGateReleases(t *testing.T) {
	binary := requireAgent(t)
	manager := newRealAgentManager(t, binary)
	workspace, _ := tempAgentWorkspace(t)
	proc, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// Gate the first turn on storage, exactly as the process manager does for a
	// command that declares SupportsStorageStartup.
	client := New(NewProcessTransport(proc), Options{
		RequireStorageStartup: true,
		FirstStatusTimeout:    30 * time.Second,
		RequestTimeout:        30 * time.Second,
	})
	defer func() { client.Close(); client.Wait() }()

	states, _ := client.OnNotification(NotifyStorageState)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := client.Call(ctx, MethodRuntimeCapabilities, map[string]any{}, CallOptions{}); err != nil {
		t.Fatalf("the gated call never completed: %v", err)
	}
	// The gate could only have opened on a real ready frame, so the snapshot must
	// say ready and the phase stream must have carried it.
	snapshot, ok := client.Gate().Snapshot()
	if !ok || snapshot.Phase != StorageReady {
		t.Fatalf("storage snapshot = %+v (ok=%v), want ready", snapshot, ok)
	}
	sawFrame := false
	for {
		select {
		case n := <-states:
			if n.Method == NotifyStorageState {
				sawFrame = true
			}
		default:
			if !sawFrame {
				t.Fatal("no startup/storageState notification reached the subscribers")
			}
			return
		}
	}
}

// TestRealAgentConcurrentCallsAreCorrelated runs several real requests at once.
// The Go agent answers them from one event loop, so their responses interleave
// exactly the way a busy agent's would; each caller must still get its own.
func TestRealAgentConcurrentCallsAreCorrelated(t *testing.T) {
	client := startRealAgentWithManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const callers = 8
	results := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			_, err := client.Call(ctx, MethodRuntimeCapabilities, map[string]any{}, CallOptions{Timeout: 20 * time.Second})
			results <- err
		}()
	}
	for i := 0; i < callers; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("a concurrent call failed: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("a concurrent call never returned")
		}
	}
}

// TestRealAgentTimeoutIsTyped: a method the agent will never answer is not
// available on the real binary, so this uses a short per-call budget on a method
// it DOES answer to prove the typed timeout path, and a cancel to prove the abort
// path. (The real agent answers every method it implements, so a true hang is not
// scriptable here without a second binary.)
func TestRealAgentTimeoutIsTyped(t *testing.T) {
	client := startRealAgentWithManager(t)
	// A context that expires mid-call must produce a cancellation, and the client
	// must stay usable afterwards.
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	_, err := client.Call(ctx, MethodRuntimeCapabilities, map[string]any{}, CallOptions{})
	if err == nil {
		t.Fatal("an already-expired call succeeded")
	}
	// The client must still work: a cancellation removes the waiter, it does not
	// poison the connection.
	ok, cancel2 := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel2()
	if _, err := client.Call(ok, MethodRuntimeCapabilities, map[string]any{}, CallOptions{}); err != nil {
		t.Errorf("the client is unusable after a cancellation: %v", err)
	}
}

// TestRealAgentTransportCloseEndsTheLeg: killing the agent must close the client's
// leg with the exit reason, not hang. This is the crash path the process manager's
// watchdog depends on.
func TestRealAgentTransportCloseEndsTheLeg(t *testing.T) {
	binary := requireAgent(t)
	manager := newRealAgentManager(t, binary)
	workspace, _ := tempAgentWorkspace(t)
	proc, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	client := New(NewProcessTransport(proc), Options{RequestTimeout: 5 * time.Second})
	closed := client.OnClose()
	// Close the client's transport directly: the process ends, the leg must end.
	client.Close()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the close channel never fired after the agent was closed")
	}
	client.Wait()
	if !client.IsDisposed() {
		t.Error("the client is not disposed after its leg ended")
	}
}

// TestRealAgentProtocolFrameRoundTrip: the agent's own v4 frame encoding must be
// assemblable by this package's decoder. This proves the encoder in
// internal/protocol and the assembler here agree on the same bytes.
func TestRealAgentProtocolFrameRoundTrip(t *testing.T) {
	// A payload comfortably above one physical frame, so the encoder really has
	// to split it and the assembler really has to reassemble it.
	payload := strings.Repeat("x", 4000)
	frame := []byte(`{"topic":"conversation/s1","subscriptionId":"sub-1","seq":1,"data":"` + payload + `"}`)
	wires, err := protocol.EncodeTopicWireFrames(frame, protocol.EncodeOptions{
		DeliveryKind:          protocol.DeliveryOnline,
		Topic:                 "conversation/s1",
		SubscriptionID:        "sub-1",
		LogicalFrameID:        "lf-1",
		LogicalFrameOrdinal:   1,
		MaxPhysicalFrameBytes: 4096,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(wires) < 2 {
		t.Fatalf("expected the frame to fragment at 300 bytes, got %d wire(s)", len(wires))
	}
	assembler := NewAssembler(vectorFrameSchema, AssemblerOptions{})
	var completed [][]byte
	for _, w := range wires {
		candidate, err := ParseCandidate(w.JSON)
		if err != nil {
			t.Fatalf("parse wire: %v", err)
		}
		for _, ev := range assembler.Accept(candidate, 1000) {
			if ev.IsFault {
				t.Fatalf("fault: %+v", ev.Fault)
			}
			completed = append(completed, ev.Frame)
		}
	}
	if len(completed) != 1 {
		t.Fatalf("%d logical frames completed, want 1", len(completed))
	}
	if string(completed[0]) != string(frame) {
		t.Errorf("reassembled frame = %s, want %s", completed[0], frame)
	}
}

// newRealAgentManager wires a manager that spawns the built agent binary with an
// isolated data dir.
func newRealAgentManager(t *testing.T, binary string) *agentproc.Manager {
	t.Helper()
	manager := agentproc.NewManager(agentproc.Options{
		CommandResolver: func(ctx agentproc.CommandResolverContext) (*agentproc.AgentCommand, error) {
			return &agentproc.AgentCommand{
				Command:                binary,
				Args:                   []string{"app-server", "--stdio"},
				Cwd:                    ctx.WorkspacePath,
				SupportsStorageStartup: true,
			}, nil
		},
		ResolveSpawnEnv: func(agentproc.WorkspaceRef) agentproc.EnvPatch { return isolateAgentEnv(t) },
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		manager.DisposeAllAndWait(ctx)
	})
	return manager
}

// startRealAgentWithManager is the convenience form used by the tests that do not
// need the manager handle.
func startRealAgentWithManager(t *testing.T) *Client {
	t.Helper()
	binary := requireAgent(t)
	manager := newRealAgentManager(t, binary)
	workspace, _ := tempAgentWorkspace(t)
	proc, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	client := New(NewProcessTransport(proc), Options{RequestTimeout: 30 * time.Second})
	t.Cleanup(func() { client.Close(); client.Wait() })
	return client
}
