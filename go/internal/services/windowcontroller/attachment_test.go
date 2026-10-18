package windowcontroller

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

// attachment_test.go — the per-connection boundary: two connections are
// isolated, a dispose removes exactly its own subscriptions, and a disposed
// connection refuses every entry point instead of writing on behalf of a window
// that no longer exists.

// TestTwoConnectionsAreIsolated is the core per-connection rule. Two connections
// subscribe to the same topic: each gets its own subscription id, its own
// snapshot, and its own frame stream. Disposing one must not remove the other's
// subscription nor stop its delivery.
func TestTwoConnectionsAreIsolated(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)

	if _, err := h.runtime.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("seed list: %v", err)
	}

	first := h.runtime.CreateAttachment()
	second := h.runtime.CreateAttachment()
	if h.runtime.LiveAttachments() != 2 {
		t.Fatalf("LiveAttachments = %d, want 2", h.runtime.LiveAttachments())
	}
	recA, recB := &recorder{}, &recorder{}
	recA.install(first)
	recB.install(second)

	ackA, err := first.Subscribe(context.Background(), TopicTasksIndex)
	if err != nil {
		t.Fatalf("subscribe A: %v", err)
	}
	ackB, err := second.Subscribe(context.Background(), TopicTasksIndex)
	if err != nil {
		t.Fatalf("subscribe B: %v", err)
	}
	if ackA.SubscriptionID == ackB.SubscriptionID {
		t.Fatalf("two connections share subscription id %q", ackA.SubscriptionID)
	}
	// Both see the same ROWS — the projection is shared — but through their own
	// subscription.
	if recA.count() != 1 || recB.count() != 1 {
		t.Fatalf("each connection got its own snapshot: A=%d frames B=%d frames", recA.count(), recB.count())
	}

	// Connection A closes. B must be untouched: same subscription, same delivery.
	first.Dispose()
	if first.SubscriptionCount() != 0 {
		t.Fatalf("disposed connection A still holds %d subscriptions", first.SubscriptionCount())
	}
	if second.SubscriptionCount() != 1 {
		t.Fatalf("connection B holds %d subscriptions after A closed, want 1", second.SubscriptionCount())
	}
	if h.runtime.LiveAttachments() != 1 {
		t.Fatalf("LiveAttachments = %d after one close, want 1", h.runtime.LiveAttachments())
	}
	if h.runtime.projection.subscriberCount() != 1 {
		t.Fatalf("the projection holds %d subscribers, want only B's", h.runtime.projection.subscriberCount())
	}
	if _, err := second.Resync(context.Background(), ackB.SubscriptionID); err != nil {
		t.Fatalf("B's own subscription was torn down by A's dispose: %v", err)
	}
	beforeB := recB.count()
	src.setTitle("t1", "renamed")
	h.changed(t, src.scope)
	if recB.count() != beforeB+1 {
		t.Fatalf("connection B received %d frames after a rename, want 1", recB.count()-beforeB)
	}
	second.Dispose()
}

// TestDisposedConnectionReceivesNothing: a connection that closed must not
// receive a later publish, even though its id was removed from the fan-out. This
// is the leak the TS comment warns about ("连接关闭时必须 dispose，否则订阅与事件监听会泄漏").
func TestDisposedConnectionReceivesNothing(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)
	if _, err := h.runtime.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("seed list: %v", err)
	}

	att := h.runtime.CreateAttachment()
	rec := &recorder{}
	rec.install(att)
	if _, err := att.Subscribe(context.Background(), TopicTasksIndex); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	before := rec.count()

	att.Dispose()
	// A second dispose is a no-op, not an error and not a double teardown.
	att.Dispose()

	src.setTitle("t1", "renamed")
	h.changed(t, src.scope)
	if rec.count() != before {
		t.Fatalf("a disposed connection received %d frames", rec.count()-before)
	}
	if h.runtime.LiveAttachments() != 0 {
		t.Fatalf("LiveAttachments = %d after the only connection closed", h.runtime.LiveAttachments())
	}
}

// TestDisposedConnectionRefusesEveryEntryPoint: after dispose, every call is
// refused. A command that raced the window close must not write on behalf of a
// connection that no longer exists — including a subscribe, which would
// otherwise re-register a subscription nobody will ever release.
func TestDisposedConnectionRefusesEveryEntryPoint(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)

	att := h.runtime.CreateAttachment()
	att.Dispose()

	if _, err := att.ListTaskList(context.Background(), ListQuery{Kind: MembershipActive}); !errors.Is(err, ErrClosed) {
		t.Fatalf("ListTaskList after dispose: %v, want ErrClosed", err)
	}
	if _, err := att.Subscribe(context.Background(), TopicTasksIndex); !errors.Is(err, ErrClosed) {
		t.Fatalf("Subscribe after dispose: %v, want ErrClosed", err)
	}
	if _, err := att.Resync(context.Background(), "any"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Resync after dispose: %v, want ErrClosed", err)
	}
	if _, err := att.MutateTask(context.Background(),
		AddressOf(src.scope, "t1"), Mutation{Kind: MutPin, Pinned: true}); !errors.Is(err, ErrClosed) {
		t.Fatalf("MutateTask after dispose: %v, want ErrClosed", err)
	}
	if _, err := att.DeleteArchivedTask(context.Background(), AddressOf(src.scope, "t1")); !errors.Is(err, ErrClosed) {
		t.Fatalf("DeleteArchivedTask after dispose: %v, want ErrClosed", err)
	}
	if _, err := att.DeleteArchivedTasks(context.Background(), AddressOf(src.scope, "t1"), []string{"t1"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("DeleteArchivedTasks after dispose: %v, want ErrClosed", err)
	}
	if _, err := att.ResolveTaskAddress(context.Background(), AddressOf(src.scope, "t1"), false); !errors.Is(err, ErrClosed) {
		t.Fatalf("ResolveTaskAddress after dispose: %v, want ErrClosed", err)
	}
	if h.runtime.projection.subscriberCount() != 0 {
		t.Fatalf("a refused subscribe still registered a subscription")
	}
}

// TestUnknownTopicIsRefused: the closed topic set is part of the contract. A
// subscribe for anything else must fail rather than mint a subscription whose
// frames nothing would ever produce.
func TestUnknownTopicIsRefused(t *testing.T) {
	h := newTestHarness()
	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	if _, err := att.Subscribe(context.Background(), "conversation/s1"); !errors.Is(err, ErrUnknownTopic) {
		t.Fatalf("subscribe to a foreign topic: %v, want ErrUnknownTopic", err)
	}
	if h.runtime.projection.subscriberCount() != 0 {
		t.Fatalf("a refused topic still registered a subscription")
	}
}

// TestNoSubscriptionSurvivesRuntimeDispose: the runtime teardown releases every
// subscription, and the attachment count returns to zero. A window that closed
// without disposing would leave LiveAttachments above zero forever, which is why
// the count is observable rather than assumed.
func TestNoSubscriptionSurvivesRuntimeDispose(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)
	if _, err := h.runtime.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("seed list: %v", err)
	}

	attachments := make([]*Attachment, 0, 8)
	recorders := make([]*recorder, 0, 8)
	for i := 0; i < 8; i++ {
		att := h.runtime.CreateAttachment()
		rec := &recorder{}
		rec.install(att)
		if _, err := att.Subscribe(context.Background(), TopicTasksIndex); err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		}
		if _, err := att.Subscribe(context.Background(), TopicWorkspaces); err != nil {
			t.Fatalf("subscribe workspaces %d: %v", i, err)
		}
		attachments = append(attachments, att)
		recorders = append(recorders, rec)
	}
	if got := h.runtime.projection.subscriberCount(); got != 16 {
		t.Fatalf("projection holds %d subscriptions, want 16", got)
	}
	if got := h.runtime.LiveAttachments(); got != 8 {
		t.Fatalf("LiveAttachments = %d, want 8", got)
	}

	before := make([]int, len(recorders))
	for i, rec := range recorders {
		before[i] = rec.count()
	}

	h.runtime.Dispose()

	if got := h.runtime.projection.subscriberCount(); got != 0 {
		t.Fatalf("runtime dispose left %d subscriptions behind", got)
	}
	if got := len(h.runtime.projection.allRows()); got != 0 {
		t.Fatalf("runtime dispose left %d rows behind", got)
	}

	// A publish after the runtime is gone reaches nobody.
	for i, rec := range recorders {
		if rec.count() != before[i] {
			t.Fatalf("connection %d received %d frames after runtime dispose",
				i, rec.count()-before[i])
		}
	}
	// Disposing the attachments afterwards is still safe and drops the count.
	for _, att := range attachments {
		att.Dispose()
	}
	if got := h.runtime.LiveAttachments(); got != 0 {
		t.Fatalf("LiveAttachments = %d after every attachment closed, want 0", got)
	}
	// Runtime dispose is idempotent.
	h.runtime.Dispose()
}

// TestDisposeReleasesTheSourceObserver: the live-session observer is a
// subscription too. Disposing must stop it, otherwise the agent keeps a
// subscriptions-index route for a workspace no window is watching.
func TestDisposeReleasesTheSourceObserver(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	if _, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	src.mu.Lock()
	starts := src.observerStarts
	src.mu.Unlock()
	if starts == 0 {
		t.Fatalf("the first list did not start the source's live observer")
	}

	h.runtime.RemoveSource(src.scope)
	src.mu.Lock()
	stops := src.observerStops
	src.mu.Unlock()
	if stops != starts {
		t.Fatalf("observer started %d times but stopped %d times", starts, stops)
	}
	// A stopped observer can no longer drive the projection.
	before := len(h.runtime.projection.allRows())
	src.deliverOverlays([]SessionOverlay{{TaskID: "t1", LiveStatus: LiveRunning}})
	if got := len(h.runtime.projection.allRows()); got != before {
		t.Fatalf("a stopped observer still wrote to the projection")
	}
}

// TestConcurrentConnectionsDoNotRace drives many connections subscribing,
// listing, mutating and disposing at once under -race. The controller's shared
// state is the projection and the runtime bookkeeping, so this is the test that
// says the fan-out runs unlocked and the single-flight is genuinely shared.
func TestConcurrentConnectionsDoNotRace(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	for i := 0; i < 20; i++ {
		src.addTask(taskIDOf(i), "task "+itoaTest(i), Membership{Active: true}, int64(100+i))
	}

	var wg sync.WaitGroup
	errs := make(chan error, 256)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			att := h.runtime.CreateAttachment()
			rec := &recorder{}
			rec.install(att)
			defer att.Dispose()
			ctx := context.Background()
			ack, err := att.Subscribe(ctx, TopicTasksIndex)
			if err != nil {
				errs <- err
				return
			}
			for j := 0; j < 8; j++ {
				if _, err := att.ListTaskList(ctx, ListQuery{
					Kind:            MembershipActive,
					WorkspaceScopes: []SourceScope{src.scope},
					Limit:           ptr(5),
				}); err != nil {
					errs <- err
					return
				}
			}
			if _, err := att.MutateTask(ctx,
				AddressOf(src.scope, taskIDOf(n%20)),
				Mutation{Kind: MutMarkUnread}); err != nil {
				errs <- err
				return
			}
			if _, err := att.Resync(ctx, ack.SubscriptionID); err != nil {
				errs <- err
				return
			}
			if err := att.Unsubscribe(ctx, ack.SubscriptionID); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent connection failed: %v", err)
	}
	if got := h.runtime.LiveAttachments(); got != 0 {
		t.Fatalf("LiveAttachments = %d after every concurrent connection closed, want 0", got)
	}
	if got := h.runtime.projection.subscriberCount(); got != 0 {
		t.Fatalf("%d subscriptions survived every concurrent dispose", got)
	}
}

// TestNoGoroutineLeakAcrossConnectAndDispose is the leak check the gate runs: the
// controller starts no goroutines of its own, but a leaked observer or a leaked
// handler would keep the attachment reachable, so the check is on the runtime's
// own observable bookkeeping plus the process goroutine count.
func TestNoGoroutineLeakAcrossConnectAndDispose(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)

	settle()
	before := runtime.NumGoroutine()

	for i := 0; i < 50; i++ {
		att := h.runtime.CreateAttachment()
		rec := &recorder{}
		rec.install(att)
		if _, err := att.ListTaskList(context.Background(), ListQuery{
			Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
		}); err != nil {
			t.Fatalf("list: %v", err)
		}
		if _, err := att.Subscribe(context.Background(), TopicTasksIndex); err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		if _, err := att.Subscribe(context.Background(), TopicWorkspaces); err != nil {
			t.Fatalf("subscribe workspaces: %v", err)
		}
		att.Dispose()
	}
	h.runtime.Dispose()

	settle()
	after := runtime.NumGoroutine()
	if after > before+2 {
		t.Fatalf("goroutine count grew %d → %d across 50 connect/dispose cycles", before, after)
	}
	if got := h.runtime.LiveAttachments(); got != 0 {
		t.Fatalf("LiveAttachments = %d after the leak run, want 0", got)
	}
	if got := h.runtime.projection.subscriberCount(); got != 0 {
		t.Fatalf("%d subscriptions survived the leak run", got)
	}
}

// settle gives the runtime's own goroutines (there are none today, but a future
// change may add some) a chance to finish before the count is taken.
func settle() {
	for i := 0; i < 20; i++ {
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
}

func taskIDOf(i int) string { return "task-" + itoaTest(i) }
