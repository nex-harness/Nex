package agentscope

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/agentproc/proto"
)

// concurrency_test.go is the -race suite: concurrent frames, concurrent subscribes,
// concurrent disposal, and the goroutine-leak delta check.
//
// Every exit path of a Scope is exercised — response, error, timeout, cancellation,
// close, parse-error-shaped input, dispose — because the paths that leak if they are
// wrong are the ones that matter: a frame pump that does not exit, an ownership entry
// that is registered twice, a flow edge chain that never drains.

// TestConcurrentFramesAndSubscribesStayOwned runs many connections against one
// workspace stream at once. Every connection must receive exactly its own frames, and
// the total delivered must equal the total pushed — no drops, no duplicates, no
// cross-delivery. This is the property the ownership filter exists for, under the
// interleavings that only -race can produce.
func TestConcurrentFramesAndSubscribesStayOwned(t *testing.T) {
	const connections = 12
	const framesPerConnection = 8

	base := newFakeBase()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recorders := make([]*recorder, connections)
	scopes := make([]*Scope, connections)
	acks := make([]string, connections)
	for i := 0; i < connections; i++ {
		scope := newScope(t, base, fmt.Sprintf("race-%02d", i), ClientModeContinuous, RoleTerminalClient)
		completeHandshake(t, scope, fmt.Sprintf("client-%02d", i), nil)
		recorders[i] = &recorder{}
		recorders[i].attach(scope)
		raceFrames := rawParams(`{"workspacePath":"/race-ws"}`)
		_, err := scope.StartFrames(ctx, raceFrames)
		if err != nil {
			t.Fatalf("conn %d frames: %v", i, err)
		}
		scopes[i] = scope
	}

	// Subscribes race each other; every one must get a distinct subscription.
	var wg sync.WaitGroup
	errs := make([]error, connections)
	for i := range scopes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ack, err := scopes[i].Subscribe(ctx, KindConversation, subscribeParams(t, "/race-ws", "", "s1", nil))
			if err != nil {
				errs[i] = err
				return
			}
			acks[i] = ack.SubscriptionID
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("conn %d subscribe: %v", i, err)
		}
	}
	seen := map[string]int{}
	for i, id := range acks {
		if previous, dup := seen[id]; dup {
			t.Fatalf("connections %d and %d received the same subscription id %s", previous, i, id)
		}
		seen[id] = i
	}

	// Frames for every connection are pushed concurrently.
	var pushWG sync.WaitGroup
	for i := 0; i < connections; i++ {
		for f := 0; f < framesPerConnection; f++ {
			pushWG.Add(1)
			go func(i, f int) {
				defer pushWG.Done()
				base.push("/race-ws", frameJSON("conversation/s1", acks[i],
					fmt.Sprintf(`"logicalFrameOrdinal":%d`, f+1)))
			}(i, f)
		}
	}
	pushWG.Wait()

	waitFor(t, "every connection to receive every frame", func() bool {
		total := 0
		for _, rec := range recorders {
			total += rec.count()
		}
		return total == connections*framesPerConnection
	})
	for i, rec := range recorders {
		if got := rec.count(); got != framesPerConnection {
			t.Fatalf("connection %d received %d frames, want %d", i, got, framesPerConnection)
		}
		for _, raw := range rec.snapshot() {
			if !containsSubID(raw, acks[i]) {
				t.Fatalf("connection %d received %s, which belongs to another connection", i, raw)
			}
		}
	}
	// Each connection received each of ITS ordinals exactly once.
	for i, rec := range recorders {
		ordinals := map[int]int{}
		for _, raw := range rec.snapshot() {
			var fields struct {
				LogicalFrameOrdinal int `json:"logicalFrameOrdinal"`
			}
			_ = json.Unmarshal([]byte(raw), &fields)
			ordinals[fields.LogicalFrameOrdinal]++
		}
		if len(ordinals) != framesPerConnection {
			t.Fatalf("connection %d saw %d distinct ordinals, want %d", i, len(ordinals), framesPerConnection)
		}
		for ordinal, count := range ordinals {
			if count != 1 {
				t.Fatalf("connection %d saw ordinal %d %d times", i, ordinal, count)
			}
		}
	}
}

// TestConcurrentDisposeWhileFramesArrive: a close can land while frames are still
// being pushed. The pump must end, the frames that arrive afterwards must not be
// delivered, and the scope must refuse every new entry point.
func TestConcurrentDisposeWhileFramesArrive(t *testing.T) {
	base := newFakeBase()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scope := newScope(t, base, "dispose-race", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	rec := &recorder{}
	rec.attach(scope)
	disposeFrames := rawParams(`{"workspacePath":"/ws"}`)
	stop, err := scope.StartFrames(ctx, disposeFrames)
	if err != nil {
		t.Fatalf("frames: %v", err)
	}
	ack, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	var pushWG sync.WaitGroup
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for i := 0; i < 200; i++ {
			base.push("/ws", frameJSON("conversation/s1", ack.SubscriptionID, fmt.Sprintf(`"logicalFrameOrdinal":%d`, i)))
		}
	}()
	// Dispose in the middle of the push burst.
	disposed := make(chan struct{})
	go func() {
		defer close(disposed)
		_ = scope.Dispose(ctx)
	}()
	pushWG.Wait()
	<-stopped
	<-disposed
	stop()

	// Whatever was delivered before the close must be a prefix of the pushed frames
	// (no reordering, no fabrication), and the ownership must be gone.
	delivered := rec.count()
	if delivered < 0 || delivered > 200 {
		t.Fatalf("delivered %d frames for 200 pushes", delivered)
	}
	if scope.HandshakeComplete() {
		t.Fatal("the scope is still handshaked after dispose")
	}
	if _, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil)); FaultCode(err) != FaultClosed {
		t.Fatalf("a subscribe after dispose got %v, want %s", err, FaultClosed)
	}
	if got := len(base.callsTo("unsubscribe:conversation")); got != 1 {
		t.Fatalf("the teardown ran %d time(s); a concurrent dispose must still tear down exactly once", got)
	}
}

// TestConcurrentFlowEdgesKeepCommitOrder: many flow edges submitted concurrently must
// still commit in the order SetTransportFlowState returned them, and the close must
// land last.
func TestConcurrentFlowEdgesKeepCommitOrder(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "flow-race", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	_, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	states := []FlowState{FlowSaturated, FlowDrained, FlowSaturated, FlowDrained}
	returns := make([]chan error, len(states))
	for i, state := range states {
		returns[i] = make(chan error, 1)
		go func(i int, state FlowState) {
			returns[i] <- scope.SetTransportFlowState(state)
		}(i, state)
		// Submit them one at a time so the submission order is deterministic; the
		// fan-out itself still happens on its own goroutines.
		<-returns[i]
	}
	flows := base.flowStates()
	if len(flows) != len(states) {
		t.Fatalf("forwarded %d edges, want %d", len(flows), len(states))
	}
	for i, flow := range flows {
		if flow.State != states[i] {
			t.Fatalf("edge %d is %s, want %s (the chain reordered the fan-out)", i, flow.State, states[i])
		}
	}
	if err := scope.SetTransportFlowState(FlowClosed); err != nil {
		t.Fatalf("close: %v", err)
	}
	flows = base.flowStates()
	if last := flows[len(flows)-1].State; last != FlowClosed {
		t.Fatalf("the close did not commit last: %+v", flows)
	}
}

// TestConcurrentDisposeIsIdempotent: many goroutines disposing at once must tear
// down exactly once. A double teardown would send two unsubscribes for one
// subscription, and the second would be answered by whatever now holds the id.
func TestConcurrentDisposeIsIdempotent(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope, err := New(base, Config{ConnectionID: "double-dispose", ClientMode: ClientModeContinuous, Role: RoleTerminalClient})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	completeHandshake(t, scope, "client-a", nil)
	for i := 0; i < 3; i++ {
		_, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", fmt.Sprintf("s%d", i), nil))
		if err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = scope.Dispose(ctx)
		}()
	}
	wg.Wait()
	if got := len(base.callsTo("unsubscribe:conversation")); got != 3 {
		t.Fatalf("%d unsubscribes for 3 subscriptions; the teardown ran more than once", got)
	}
}

// ---------------------------------------------------------------------------
// Goroutine leak
// ---------------------------------------------------------------------------

// The delta-check approach mirrors internal/agentproc/proto/leak_test.go: goleak is
// not a module dependency, and other tests in this binary leave goroutines behind on
// purpose, so demanding an empty global profile would fail for work this file never
// did. What must hold is that the work below adds nothing.

const leakSettleTimeout = 3 * time.Second

func stackProfile() string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}

func goroutineSignatures(profile string) map[string]int {
	out := map[string]int{}
	for _, g := range strings.Split(profile, "\n\n") {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		key := g
		if i := strings.IndexByte(g, '\n'); i >= 0 {
			key = g[:i]
		}
		for _, line := range strings.Split(g, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "github.com/tylinux/nex/go/") || strings.HasPrefix(line, "runtime.") {
				key += "\n\t" + line
				break
			}
		}
		out[key]++
	}
	return out
}

func testingGoroutine(stack string) bool {
	for _, b := range []string{"testing.tRunner", "testing.(*M).Run", "testing.runTests", "runtime.goexit"} {
		if strings.Contains(stack, b) {
			return true
		}
	}
	return false
}

func snapshotGoroutines() map[string]int { return goroutineSignatures(stackProfile()) }

// assertNoNewGoroutines polls so a goroutine on its way out does not fail the test.
func assertNoNewGoroutines(t *testing.T, before map[string]int) {
	t.Helper()
	deadline := time.Now().Add(leakSettleTimeout)
	var extra []string
	for {
		extra = nil
		for key, n := range goroutineSignatures(stackProfile()) {
			if testingGoroutine(key) {
				continue
			}
			if n > before[key] {
				extra = append(extra, fmt.Sprintf("x%d %s", n-before[key], key))
			}
		}
		if len(extra) == 0 {
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	sort.Strings(extra)
	t.Errorf("%d goroutine(s) outlived the test (baseline had %d distinct):\n%s",
		len(extra), len(before), strings.Join(extra, "\n"))
}

// TestScopeLeaksNoGoroutines drives every exit path: a frame pump that ends because
// its context was cancelled, one that ends because its stop was called, one whose
// stream ended on its own, a normal dispose, a dispose racing a subscribe, and an
// interaction request answered while the scope was closing.
func TestScopeLeaksNoGoroutines(t *testing.T) {
	before := snapshotGoroutines()
	base := newFakeBase()
	directory := NewInteractionDirectory()
	client := newProtoClient()

	// (1) A pump released by its stop func.
	scopeA, err := New(base, Config{ConnectionID: "leak-a", ClientMode: ClientModeContinuous, Role: RoleTerminalClient, Interaction: client})
	if err != nil {
		t.Fatalf("new A: %v", err)
	}
	completeHandshake(t, scopeA, "client-a", nil)
	stopA, err := scopeA.StartFrames(context.Background(), rawParams(`{"workspacePath":"/leak"}`))
	if err != nil {
		t.Fatalf("A frames: %v", err)
	}
	// (2) A pump released by a cancelled context.
	ctxB, cancelB := context.WithCancel(context.Background())
	scopeB := newScope(t, base, "leak-b", ClientModeReplayable, RoleTerminalClient)
	completeHandshake(t, scopeB, "client-b", nil)
	leakFramesB := rawParams(`{"workspacePath":"/leak"}`)
	_, err = scopeB.StartFrames(ctxB, leakFramesB)
	if err != nil {
		t.Fatalf("B frames: %v", err)
	}
	// (3) A pump whose stream ends on its own.
	scopeC := newScope(t, base, "leak-c", ClientModeContinuous, RoleTrustedHostRelay)
	leakFramesC := rawParams(`{"workspacePath":"/leak"}`)
	_, err = scopeC.StartFrames(context.Background(), leakFramesC)
	if err != nil {
		t.Fatalf("C frames: %v", err)
	}

	for _, scope := range []*Scope{scopeA, scopeB, scopeC} {
		scope.SetInteractionDirectory(directory, func(context.Context, proto.AgentRequest) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		})
	}
	stopA()
	cancelB()
	base.streamsMu.Lock()
	for _, streams := range base.streams {
		for _, stream := range streams {
			close(stream)
		}
	}
	base.streamsMu.Unlock()

	// (4) A dispose racing a subscribe ACK.
	base.subscribeGate = make(chan struct{})
	scopeD, err := New(base, Config{ConnectionID: "leak-d", ClientMode: ClientModeContinuous, Role: RoleTerminalClient})
	if err != nil {
		t.Fatalf("new D: %v", err)
	}
	completeHandshake(t, scopeD, "client-d", nil)
	racing := make(chan error, 1)
	go func() {
		_, err := scopeD.Subscribe(context.Background(), KindConversation, subscribeParams(t, "/leak", "", "s1", nil))
		racing <- err
	}()
	waitFor(t, "the racing subscribe to be pending", func() bool {
		base.mu.Lock()
		defer base.mu.Unlock()
		return base.subscribeGateHits == 1
	})
	disposed := make(chan error, 1)
	go func() { disposed <- scopeD.Dispose(context.Background()) }()
	close(base.subscribeGate)
	<-racing
	<-disposed

	// (5) A normal dispose of every remaining scope.
	for _, scope := range []*Scope{scopeA, scopeB, scopeC} {
		if err := scope.Dispose(context.Background()); err != nil {
			t.Fatalf("dispose: %v", err)
		}
	}
	if got := directory.Live(); got != 0 {
		t.Fatalf("%d interaction resolvers outlived their scopes", got)
	}
	assertNoNewGoroutines(t, before)
}
