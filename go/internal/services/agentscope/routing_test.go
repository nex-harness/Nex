package agentscope

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

// routing_test.go turns every comment-described past bug of
// nexAgentConnectionScope.ts into a regression test. Each test names the rule it
// protects in its doc comment, so a future edit that drops the rule fails here with
// the reason rather than with a mysterious flake.

// recorder collects the frames a scope delivered, in order.
type recorder struct {
	mu     sync.Mutex
	frames []string
	closed bool
}

func (r *recorder) attach(scope *Scope) {
	scope.SetDeliver(func(_ string, frame Frame) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.closed {
			return
		}
		r.frames = append(r.frames, string(frame.Raw))
	})
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.frames...)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.frames)
}

// waitFor polls until the condition holds or the deadline passes. The tests assert
// on ORDER and COUNT, both of which are decided by the pump goroutine, so a
// fixed sleep would be a flaky substitute for the property being tested.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestFramesAreDeliveredToTheirOwnConnection is the core cross-delivery rule: two
// connections subscribed to the SAME workspace and the SAME topic must each see
// only their own frames. A shared workspace emitter with no ownership filter is the
// bug this layer exists to prevent — the UI would render another window's rows.
func TestFramesAreDeliveredToTheirOwnConnection(t *testing.T) {
	for _, mode := range []ClientMode{ClientModeContinuous, ClientModeReplayable} {
		for _, role := range []Role{RoleTerminalClient, RoleTrustedHostRelay} {
			name := fmt.Sprintf("%s/%s", mode, role)
			t.Run(name, func(t *testing.T) {
				base := newFakeBase()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				scopeA := newScope(t, base, "conn-a", mode, role)
				scopeB := newScope(t, base, "conn-b", mode, role)
				if role == RoleTerminalClient {
					completeHandshake(t, scopeA, "client-a", nil)
					completeHandshake(t, scopeB, "client-b", nil)
				}
				recA, recB := &recorder{}, &recorder{}
				recA.attach(scopeA)
				recB.attach(scopeB)
				_, err := scopeA.StartFrames(ctx, rawParams(`{"workspacePath":"/ws"}`))
				if err != nil {
					t.Fatalf("A start frames: %v", err)
				}
				_, err = scopeB.StartFrames(ctx, rawParams(`{"workspacePath":"/ws"}`))
				if err != nil {
					t.Fatalf("B start frames: %v", err)
				}
				ackA, err := scopeA.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
				if err != nil {
					t.Fatalf("A subscribe: %v", err)
				}
				ackB, err := scopeB.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
				if err != nil {
					t.Fatalf("B subscribe: %v", err)
				}
				if ackA.SubscriptionID == ackB.SubscriptionID {
					t.Fatalf("the base must issue distinct ids per subscription (both %s)", ackA.SubscriptionID)
				}

				base.push("/ws", frameJSON("conversation/s1", ackA.SubscriptionID, `"deliveryKind":"online"`))
				base.push("/ws", frameJSON("conversation/s1", ackB.SubscriptionID, `"deliveryKind":"online"`))

				waitFor(t, "both connections to receive their frame", func() bool {
					return recA.count() == 1 && recB.count() == 1
				})
				if got := recA.snapshot()[0]; !containsSubID(got, ackA.SubscriptionID) {
					t.Fatalf("A received %s, expected its own %s", got, ackA.SubscriptionID)
				}
				if got := recB.snapshot()[0]; !containsSubID(got, ackB.SubscriptionID) {
					t.Fatalf("B received %s, expected its own %s", got, ackB.SubscriptionID)
				}
			})
		}
	}
}

func containsSubID(haystack, subscriptionID string) bool {
	return len(haystack) > 0 && len(subscriptionID) > 0 &&
		jsonHasMember(haystack, "subscriptionId", subscriptionID)
}

func jsonHasMember(document, key, value string) bool {
	var fields map[string]any
	if err := json.Unmarshal([]byte(document), &fields); err != nil {
		return false
	}
	return fields[key] == any(value)
}

// TestUnownedFrameIsDropped: a frame on a topic this connection never subscribed to
// is dropped, not delivered. The workspace stream is shared, so "not mine" is the
// normal case and forwarding it would be the cross-delivery bug in its purest form.
func TestUnownedFrameIsDropped(t *testing.T) {
	base := newFakeBase()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scope := newScope(t, base, "dropper", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	rec := &recorder{}
	rec.attach(scope)
	raceFrames := rawParams(`{"workspacePath":"/ws"}`)
	_, err := scope.StartFrames(ctx, raceFrames)
	if err != nil {
		t.Fatalf("start frames: %v", err)
	}
	base.push("/ws", frameJSON("conversation/nobody", "sub-x", ""))

	// Give the pump a chance to misdeliver, then prove nothing arrived.
	time.Sleep(80 * time.Millisecond)
	if got := rec.count(); got != 0 {
		t.Fatalf("an unowned frame was delivered (%d frames)", got)
	}
}

// TestFramesBeforeSubscribeAckAreStagedAndReleased is the ACK-race regression. A CLI
// notification can arrive before the RPC subscribe response does; the older facade
// registered the owner only after awaiting, so those early frames were dropped for
// good and the UI stayed loading. They must be staged and released IN ARRIVAL ORDER
// after the ACK, and only for the subscription id the ACK returned.
func TestFramesBeforeSubscribeAckAreStagedAndReleased(t *testing.T) {
	base := newFakeBase()
	base.subscribeGate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scope := newScope(t, base, "early-frames", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	rec := &recorder{}
	rec.attach(scope)
	dropFrames := rawParams(`{"workspacePath":"/ws"}`)
	_, err := scope.StartFrames(ctx, dropFrames)
	if err != nil {
		t.Fatalf("start frames: %v", err)
	}

	done := make(chan *SubscribeAck, 1)
	errCh := make(chan error, 1)
	go func() {
		ack, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
		if err != nil {
			errCh <- err
			return
		}
		done <- ack
	}()

	waitFor(t, "the subscribe to be waiting on its ACK", func() bool {
		base.mu.Lock()
		defer base.mu.Unlock()
		return base.subscribeGateHits == 1
	})
	// The base has issued sub-1 but the facade has not been told; push two frames.
	base.push("/ws", frameJSON("conversation/s1", "sub-1", `"logicalFrameOrdinal":1`))
	base.push("/ws", frameJSON("conversation/s1", "sub-1", `"logicalFrameOrdinal":2`))
	// The ACK must not land before the pump has actually staged both frames, or the
	// window would be released while still empty — which is a race in the TEST, not
	// in the facade: the whole point of the staging window is that the frames arrive
	// before the ACK.
	waitFor(t, "both frames to be staged", func() bool {
		return stagedFrameCount(scope) == 2
	})
	close(base.subscribeGate)

	select {
	case err := <-errCh:
		t.Fatalf("subscribe: %v", err)
	case ack := <-done:
		if ack.SubscriptionID != "sub-1" {
			t.Fatalf("unexpected ack %+v", ack)
		}
	}
	waitFor(t, "the staged frames to be released", func() bool { return rec.count() == 2 })
	frames := rec.snapshot()
	if !containsOrdinal(frames[0], 1) {
		t.Fatalf("arrival order was not preserved: first released frame is %s", frames[0])
	}
	if !containsOrdinal(frames[1], 2) {
		t.Fatalf("arrival order was not preserved: second released frame is %s", frames[1])
	}
}

func containsOrdinal(document string, ordinal int) bool {
	var fields struct {
		LogicalFrameOrdinal int `json:"logicalFrameOrdinal"`
	}
	return json.Unmarshal([]byte(document), &fields) == nil && fields.LogicalFrameOrdinal == ordinal
}

// TestStagingReleasesOnlyTheAckedSubscription is the "don't guess the owner" rule:
// when two subscriptions on the SAME topic are pending at once, only the frames
// whose subscriptionId matches the ACK that just landed are released. Releasing both
// would hand one connection's frames to the other.
func TestStagingReleasesOnlyTheAckedSubscription(t *testing.T) {
	base := newFakeBase()
	base.subscribeGate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scopeA := newScope(t, base, "twin-a", ClientModeContinuous, RoleTerminalClient)
	scopeB := newScope(t, base, "twin-b", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scopeA, "client-a", nil)
	completeHandshake(t, scopeB, "client-b", nil)
	recA, recB := &recorder{}, &recorder{}
	recA.attach(scopeA)
	recB.attach(scopeB)
	_, err := scopeA.StartFrames(ctx, rawParams(`{"workspacePath":"/ws"}`))
	if err != nil {
		t.Fatalf("A frames: %v", err)
	}
	_, err = scopeB.StartFrames(ctx, rawParams(`{"workspacePath":"/ws"}`))
	if err != nil {
		t.Fatalf("B frames: %v", err)
	}

	errA := make(chan error, 1)
	errB := make(chan error, 1)
	go func() {
		_, err := scopeA.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
		errA <- err
	}()
	go func() {
		_, err := scopeB.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
		errB <- err
	}()
	waitFor(t, "both subscribes to be pending", func() bool {
		base.mu.Lock()
		defer base.mu.Unlock()
		return base.subscribeGateHits == 2
	})
	// Both scopes have a pending ownership for the same topic; A will get sub-1 and
	// B sub-2. Push a frame for each. The workspace stream is shared, so each frame
	// is staged into BOTH windows (four entries) — which is exactly the ambiguity
	// the per-subscription-id release rule has to resolve.
	base.push("/ws", frameJSON("conversation/s1", "sub-1", ""))
	base.push("/ws", frameJSON("conversation/s1", "sub-2", ""))
	waitFor(t, "the frames to be staged into both windows", func() bool {
		return stagedFrameCount(scopeA) == 2 && stagedFrameCount(scopeB) == 2
	})
	close(base.subscribeGate)
	if err := <-errA; err != nil {
		t.Fatalf("A subscribe: %v", err)
	}
	if err := <-errB; err != nil {
		t.Fatalf("B subscribe: %v", err)
	}
	waitFor(t, "each scope to receive exactly one staged frame", func() bool {
		return recA.count() == 1 && recB.count() == 1
	})
	// Which connection won sub-1 and which won sub-2 is a race between the two
	// goroutines, so the assertion is the property itself: each scope released
	// EXACTLY the frame for its own subscription id, and the two are different
	// frames. A facade that released the whole staged batch would hand one
	// connection's frame to the other.
	frameA, frameB := recA.snapshot()[0], recB.snapshot()[0]
	if frameA == frameB {
		t.Fatalf("both scopes received the same frame %s; ownership was not applied", frameA)
	}
	if !isPhysicalFrame(frameA) || !isPhysicalFrame(frameB) {
		t.Fatalf("a released frame is not a physical wire frame: %s / %s", frameA, frameB)
	}
	var decodedA, decodedB struct {
		SubscriptionID string `json:"subscriptionId"`
	}
	if err := json.Unmarshal([]byte(frameA), &decodedA); err != nil {
		t.Fatalf("decode A frame: %v", err)
	}
	if err := json.Unmarshal([]byte(frameB), &decodedB); err != nil {
		t.Fatalf("decode B frame: %v", err)
	}
	if decodedA.SubscriptionID == decodedB.SubscriptionID {
		t.Fatalf("both scopes released subscriptionId %s", decodedA.SubscriptionID)
	}
}

// stagedFrameCount reports how many frames the ACK-race windows are currently
// holding, so a test can wait for the pump rather than sleep.
func stagedFrameCount(scope *Scope) int {
	scope.owned.mu.Lock()
	pendings := []*pendingOwnership{}
	for _, group := range scope.owned.pendingByOwnership {
		for pending := range group {
			pendings = append(pendings, pending)
		}
	}
	scope.owned.mu.Unlock()
	total := 0
	for _, pending := range pendings {
		pending.mu.Lock()
		total += len(pending.frames)
		pending.mu.Unlock()
	}
	return total
}

func isPhysicalFrame(raw string) bool {
	var fields struct {
		Kind string `json:"kind"`
		Wire int    `json:"wireVersion"`
	}
	return json.Unmarshal([]byte(raw), &fields) == nil && fields.Kind == "complete" && fields.Wire == HostProtocolVersion
}

// TestStagingOverflowFailsLoudly is the overflow regression. Dropping the OLDEST
// staged frame (what a naive bounded buffer does) turns one logical frame into a
// permanent hole in the sequence; the facade must instead clear the WHOLE batch and
// fail the subscribe with a typed fault, and reverse-unsubscribe the subscription it
// just opened so the CLI is not left holding an orphan.
func TestStagingOverflowFailsLoudly(t *testing.T) {
	if testing.Short() {
		t.Skip("the byte budget test allocates up to the 32 MiB limit")
	}
	base := newFakeBase()
	base.subscribeGate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scope := newScope(t, base, "overflow", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	rec := &recorder{}
	rec.attach(scope)
	stageFrames := rawParams(`{"workspacePath":"/ws"}`)
	_, err := scope.StartFrames(ctx, stageFrames)
	if err != nil {
		t.Fatalf("start frames: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
		errCh <- err
	}()
	waitFor(t, "the subscribe to be pending", func() bool {
		base.mu.Lock()
		defer base.mu.Unlock()
		return base.subscribeGateHits == 1
	})
	// Push more than the frame-count cap (1024).
	for i := 0; i < MaxPendingOwnershipFrames+8; i++ {
		base.push("/ws", frameJSON("conversation/s1", "sub-1", fmt.Sprintf(`"logicalFrameOrdinal":%d`, i)))
	}
	// Give the pump time to stage them before the ACK lands.
	waitFor(t, "the staging window to fill", func() bool {
		scope.owned.mu.Lock()
		defer scope.owned.mu.Unlock()
		for _, group := range scope.owned.pendingByOwnership {
			for pending := range group {
				if pending.isOverflow() {
					return true
				}
			}
		}
		return false
	})
	close(base.subscribeGate)
	err = <-errCh
	if FaultCode(err) != FaultFrameStagingOverflow {
		t.Fatalf("subscribe got %v, want %s", err, FaultFrameStagingOverflow)
	}
	// The subscription the base opened must be reverse-unsubscribed, not leaked.
	waitFor(t, "the reverse unsubscribe", func() bool {
		return len(base.callsTo("unsubscribe:conversation")) == 1
	})
	if rec.count() != 0 {
		t.Fatalf("an overflowed subscribe delivered %d frames", rec.count())
	}
}

// TestLateAckAfterCloseIsReverseUnsubscribed is the dispose-race regression: a port
// close can race a subscribe ACK. The late ACK must NOT re-register an owner on a
// disposed facade (that would leave a phantom owner nothing can ever unsubscribe),
// and it must be reverse-unsubscribed immediately.
func TestLateAckAfterCloseIsReverseUnsubscribed(t *testing.T) {
	base := newFakeBase()
	base.subscribeGate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scope, err := New(base, Config{ConnectionID: "late-ack", ClientMode: ClientModeContinuous, Role: RoleTerminalClient})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	completeHandshake(t, scope, "client-a", nil)

	errCh := make(chan error, 1)
	go func() {
		_, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
		errCh <- err
	}()
	waitFor(t, "the subscribe to be pending", func() bool {
		base.mu.Lock()
		defer base.mu.Unlock()
		return base.subscribeGateHits == 1
	})
	// Close the port while the ACK is still in flight.
	if err := scope.Dispose(ctx); err != nil {
		t.Fatalf("dispose: %v", err)
	}
	close(base.subscribeGate)
	if code := FaultCode(<-errCh); code != FaultClosed {
		t.Fatalf("the late ACK got %v, want %s", code, FaultClosed)
	}
	waitFor(t, "the reverse unsubscribe", func() bool {
		return len(base.callsTo("unsubscribe:conversation")) >= 1
	})
}

// TestLateAckAfterRuntimeRestartIsRefusedWithoutUnsubscribing is the generation
// regression. A late ACK from the dead runtime may carry the SAME subscription id
// the new runtime just issued. The facade must drop the local owner and refuse — and
// must NOT send an unsubscribe, because that would tear down the NEW runtime's live
// subscription.
func TestLateAckAfterRuntimeRestartIsRefusedWithoutUnsubscribing(t *testing.T) {
	base := newFakeBase()
	base.subscribeGate = make(chan struct{})
	watcher := &fakeWatcher{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scope, err := New(base, Config{ConnectionID: "gen", ClientMode: ClientModeContinuous, Role: RoleTerminalClient})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	completeHandshake(t, scope, "client-a", nil)
	scope.AttachRuntimeWatch(watcher)

	errCh := make(chan error, 1)
	go func() {
		_, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
		errCh <- err
	}()
	waitFor(t, "the subscribe to be pending", func() bool {
		base.mu.Lock()
		defer base.mu.Unlock()
		return base.subscribeGateHits == 1
	})
	// The runtime recycles while the ACK is out.
	watcher.emit(RuntimeLifecycleEvent{WorkspaceKey: "/ws", Generation: 2, State: RuntimeUnavailable})
	close(base.subscribeGate)
	if code := FaultCode(<-errCh); code != FaultRuntimeRestarted {
		t.Fatalf("the late ACK got %v, want %s", code, FaultRuntimeRestarted)
	}
	if got := len(base.callsTo("unsubscribe:conversation")); got != 0 {
		t.Fatalf("the facade unsubscribed %d time(s) against the NEW runtime; it must not", got)
	}
}

// fakeWatcher is the base service's lifecycle surface for the tests.
type fakeWatcher struct {
	mu       sync.Mutex
	callback func(RuntimeLifecycleEvent)
	restart  func(string)
}

func (w *fakeWatcher) WatchRuntimeLifecycle(fn func(RuntimeLifecycleEvent)) func() {
	w.mu.Lock()
	w.callback = fn
	w.mu.Unlock()
	return func() {}
}

func (w *fakeWatcher) WatchRuntimeRestarted(fn func(string)) func() {
	w.mu.Lock()
	w.restart = fn
	w.mu.Unlock()
	return func() {}
}

func (w *fakeWatcher) emit(event RuntimeLifecycleEvent) {
	w.mu.Lock()
	callback := w.callback
	w.mu.Unlock()
	if callback != nil {
		callback(event)
	}
}

// TestAvailableIsNotAnInvalidationBoundary is the cold-start regression. On a cold
// start the subscribe registers its pending ownership, the SAME startup publishes
// available(gen 1), and only then does the ACK arrive. Treating available as an
// invalidation boundary fails every first subscribe.
func TestAvailableIsNotAnInvalidationBoundary(t *testing.T) {
	base := newFakeBase()
	base.subscribeGate = make(chan struct{})
	watcher := &fakeWatcher{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scope, err := New(base, Config{ConnectionID: "cold-start", ClientMode: ClientModeContinuous, Role: RoleTerminalClient})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	completeHandshake(t, scope, "client-a", nil)
	scope.AttachRuntimeWatch(watcher)

	errCh := make(chan error, 1)
	go func() {
		_, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
		errCh <- err
	}()
	waitFor(t, "the subscribe to be pending", func() bool {
		base.mu.Lock()
		defer base.mu.Unlock()
		return base.subscribeGateHits == 1
	})
	// available(gen 1) arrives BEFORE the ACK, exactly as a cold start does.
	watcher.emit(RuntimeLifecycleEvent{WorkspaceKey: "/ws", Generation: 1, State: RuntimeAvailable})
	close(base.subscribeGate)
	if err := <-errCh; err != nil {
		t.Fatalf("a cold-start subscribe was failed by its own available event: %v", err)
	}
}

// TestUnavailableWithoutRestartStillInvalidates is the exit-without-restart
// regression. A facade that only watched restarts would keep the dead owners when the
// runtime exited and never came back; the next UI cleanup would then rebuild
// unsubscribe params for a subscription that no longer exists, which walks the
// start-if-needed path and spawns an agent just to tear down nothing.
func TestUnavailableWithoutRestartStillInvalidates(t *testing.T) {
	base := newFakeBase()
	watcher := &fakeWatcher{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scope, err := New(base, Config{ConnectionID: "exit-only", ClientMode: ClientModeContinuous, Role: RoleTerminalClient})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	completeHandshake(t, scope, "client-a", nil)
	scope.AttachRuntimeWatch(watcher)
	ack, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	watcher.emit(RuntimeLifecycleEvent{WorkspaceKey: "/ws", Generation: 1, State: RuntimeUnavailable})
	// The owner is gone: an unsubscribe for it is now a no-op and must NOT reach the
	// base, because the runtime that held it is gone too.
	if err := scope.Unsubscribe(ctx, KindConversation,
		subscribeParams(t, "/ws", "", "s1", map[string]any{"subscriptionId": ack.SubscriptionID})); err != nil {
		t.Fatalf("unsubscribe after invalidation: %v", err)
	}
	if got := len(base.callsTo("unsubscribe:conversation")); got != 0 {
		t.Fatalf("unsubscribe reached the base %d time(s) after the runtime was invalidated", got)
	}
}

// TestResyncRequiresOwnership: resync is refused for a subscription this connection
// does not own (fault.subscription.notOwned) — otherwise one window could re-arbitrate
// another's subscription and reset its watermark.
func TestResyncRequiresOwnership(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "resync-owner", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	if _, err := scope.Resync(ctx, KindConversation,
		subscribeParams(t, "/ws", "", "s1", map[string]any{"subscriptionId": "sub-1"})); FaultCode(err) != FaultNotOwned {
		t.Fatalf("resync of an unowned subscription got %v, want %s", err, FaultNotOwned)
	}
	ack, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, err := scope.Resync(ctx, KindConversation,
		subscribeParams(t, "/ws", "", "s1", map[string]any{"subscriptionId": ack.SubscriptionID})); err != nil {
		t.Fatalf("resync of an owned subscription: %v", err)
	}
}

// TestResyncOnAnotherWorkspaceIsRefused: ownership is keyed by workspace too, so a
// subscription id from one workspace cannot be resynced against another.
func TestResyncOnAnotherWorkspaceIsRefused(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "resync-ws", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	ack, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws-a", "", "s1", nil))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, err := scope.Resync(ctx, KindConversation,
		subscribeParams(t, "/ws-b", "", "s1", map[string]any{"subscriptionId": ack.SubscriptionID})); FaultCode(err) != FaultNotOwned {
		t.Fatalf("cross-workspace resync got %v, want %s", err, FaultNotOwned)
	}
}

// TestResubscribeReplacesTheOwnership: a second subscribe for the same (kind,
// workspace, topic, connection) retires the older entry, so the older subscription id
// no longer resolves. Without the reverse index, the old owner would linger and a
// frame on it would be delivered to a connection that had moved on.
func TestResubscribeReplacesTheOwnership(t *testing.T) {
	base := newFakeBase()
	ctx := context.Background()
	scope := newScope(t, base, "resub", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	first, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("first subscribe: %v", err)
	}
	second, err := scope.Subscribe(ctx, KindConversation, subscribeParams(t, "/ws", "", "s1", nil))
	if err != nil {
		t.Fatalf("second subscribe: %v", err)
	}
	if first.SubscriptionID == second.SubscriptionID {
		t.Fatal("the base must issue a fresh id")
	}
	// The old id no longer resolves.
	if _, err := scope.Resync(ctx, KindConversation,
		subscribeParams(t, "/ws", "", "s1", map[string]any{"subscriptionId": first.SubscriptionID})); FaultCode(err) != FaultNotOwned {
		t.Fatalf("the retired subscription still resolved: %v", err)
	}
	// The new id does.
	if _, err := scope.Resync(ctx, KindConversation,
		subscribeParams(t, "/ws", "", "s1", map[string]any{"subscriptionId": second.SubscriptionID})); err != nil {
		t.Fatalf("the live subscription did not resolve: %v", err)
	}
}

// TestThreeTopicFamiliesAreRoutedIndependently: conversation, sessions-index and
// workspace-config share one workspace stream and are told apart by prefix. Each
// family must deliver only its own frames.
func TestThreeTopicFamiliesAreRoutedIndependently(t *testing.T) {
	base := newFakeBase()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scope := newScope(t, base, "families", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	rec := &recorder{}
	rec.attach(scope)
	twinFrames := rawParams(`{"workspacePath":"/ws"}`)
	_, err := scope.StartFrames(ctx, twinFrames)
	if err != nil {
		t.Fatalf("start frames: %v", err)
	}
	cases := []struct {
		kind  Kind
		topic func(workspace string) string
	}{
		{KindConversation, func(string) string { return "conversation/s1" }},
		{KindSessionsIndex, func(ws string) string { return "sessions-index/" + ws }},
		{KindWorkspaceConfig, func(ws string) string { return "workspace-config/" + ws }},
	}
	ids := map[Kind]string{}
	for _, tc := range cases {
		ack, err := scope.Subscribe(ctx, tc.kind, subscribeParams(t, "/ws", "", "s1", nil))
		if err != nil {
			t.Fatalf("%s subscribe: %v", tc.kind, err)
		}
		ids[tc.kind] = ack.SubscriptionID
	}
	// Push a frame for every family plus one for an unowned topic.
	for _, tc := range cases {
		base.push("/ws", frameJSON(tc.topic("/ws"), ids[tc.kind], ""))
	}
	base.push("/ws", frameJSON("unknown-family/x", "sub-x", ""))
	waitFor(t, "the three family frames", func() bool { return rec.count() == 3 })
	for i, tc := range cases {
		if !containsSubID(rec.snapshot()[i], ids[tc.kind]) {
			t.Fatalf("frame %d is %s, expected %s's frame %s", i, rec.snapshot()[i], tc.kind, ids[tc.kind])
		}
	}
}

// TestUnroutableFrameIsDroppedWithoutAFault: a frame with no topic cannot be routed
// at all, so there is no owner to raise a typed fault on. It is dropped. The typed
// faults for a routable frame with a broken BODY are the owned assembler's job
// (internal/agentproc/proto), not this layer's.
func TestUnroutableFrameIsDroppedWithoutAFault(t *testing.T) {
	base := newFakeBase()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scope := newScope(t, base, "unroutable", ClientModeContinuous, RoleTerminalClient)
	completeHandshake(t, scope, "client-a", nil)
	rec := &recorder{}
	rec.attach(scope)
	postDisposeFrames := rawParams(`{"workspacePath":"/ws"}`)
	_, err := scope.StartFrames(ctx, postDisposeFrames)
	if err != nil {
		t.Fatalf("start frames: %v", err)
	}
	for _, raw := range []string{
		`{"kind":"complete","logicalFrameId":"x"}`, // no topic / subscriptionId
		`not json at all`,
		`[]`,
	} {
		base.push("/ws", raw)
	}
	time.Sleep(60 * time.Millisecond)
	if rec.count() != 0 {
		t.Fatalf("an unroutable frame was delivered (%d frames)", rec.count())
	}
}
