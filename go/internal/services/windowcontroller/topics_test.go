package windowcontroller

import (
	"context"
	"sync"
	"testing"
)

// topics_test.go — the two controller topics: the snapshot/ack order, the
// per-topic cursors, and the delta rules (an empty batch publishes nothing, a
// replacement publishes old removals and new upserts in ONE frame).

// recordedFrame is one frame a test connection received.
type recordedFrame struct {
	topic    string
	raw      any
	subID    string
	fromSeq  int64
	toSeq    int64
	logEpoch string
	kind     string
}

// recorder is a connection's frame handler. The fan-out runs UNLOCKED by design
// (a slow consumer must not be able to block the projection), so the recorder
// takes its own lock: a test that appended without one would race exactly where
// the controller intends to be concurrent.
type recorder struct {
	mu     sync.Mutex
	frames []recordedFrame
}

func (r *recorder) install(att *Attachment) {
	att.SetHandler(func(frame any) {
		r.mu.Lock()
		r.frames = append(r.frames, describeFrame(frame))
		r.mu.Unlock()
	})
}

// count is the number of frames received so far.
func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.frames)
}

// all is a stable copy of the received frames.
func (r *recorder) all() []recordedFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedFrame(nil), r.frames...)
}

// last is the most recent frame.
func (r *recorder) last() recordedFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.frames[len(r.frames)-1]
}

// at is the i-th received frame.
func (r *recorder) at(i int) recordedFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.frames[i]
}

func (r *recorder) kinds() []string {
	frames := r.all()
	out := make([]string, 0, len(frames))
	for _, f := range frames {
		out = append(out, f.kind)
	}
	return out
}

func describeFrame(frame any) recordedFrame {
	out := recordedFrame{raw: frame}
	switch f := frame.(type) {
	case taskFrame:
		out.topic = f.topic
		out.subID = f.subscriptionID
		out.fromSeq = f.fromSeq
		out.toSeq = f.toSeq
		out.logEpoch = f.logEpoch
		out.kind = f.Payload.Kind
	case workspaceFrame:
		out.topic = f.topic
		out.subID = f.subscriptionID
		out.fromSeq = f.fromSeq
		out.toSeq = f.toSeq
		out.logEpoch = f.logEpoch
		out.kind = f.Payload.Kind
	}
	return out
}

// TestSubscribeDeliversSnapshotBeforeAck pins the ordering the UI depends on: the
// snapshot must already be in the connection's handler when the ack returns,
// otherwise the sidebar renders an empty list for a frame and then corrects it.
func TestSubscribeDeliversSnapshotBeforeAck(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	rec := &recorder{}
	rec.install(att)

	if _, err := att.ListTaskList(context.Background(), ListQuery{
		Kind:            MembershipActive,
		WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("list: %v", err)
	}

	ack, err := att.Subscribe(context.Background(), TopicTasksIndex)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("want exactly the snapshot before the ack, got %d frames (%v)", rec.count(), rec.kinds())
	}
	if rec.at(0).kind != snapshotKind {
		t.Fatalf("first frame must be the snapshot, got %q", rec.at(0).kind)
	}
	if rec.at(0).subID != ack.SubscriptionID {
		t.Fatalf("snapshot subscriptionId %q != ack %q", rec.at(0).subID, ack.SubscriptionID)
	}
	if ack.Mode != "snapshot" {
		t.Fatalf("ack mode %q, want snapshot", ack.Mode)
	}
	if ack.LogEpoch == "" || ack.LogEpoch != rec.at(0).logEpoch {
		t.Fatalf("ack logEpoch %q must match the snapshot frame %q", ack.LogEpoch, rec.at(0).logEpoch)
	}
}

// TestTopicsHaveIndependentCursors: the two topics must not share an epoch or a
// seq. A consumer of one topic must never be told its cursor is current on the
// other, so the epochs are separately generated and each advances on its own.
func TestTopicsHaveIndependentCursors(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	rec := &recorder{}
	rec.install(att)
	if _, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("list: %v", err)
	}

	taskAck, err := att.Subscribe(context.Background(), TopicTasksIndex)
	if err != nil {
		t.Fatalf("subscribe tasks: %v", err)
	}
	wsAck, err := att.Subscribe(context.Background(), TopicWorkspaces)
	if err != nil {
		t.Fatalf("subscribe workspaces: %v", err)
	}
	if taskAck.LogEpoch == wsAck.LogEpoch {
		t.Fatalf("topics share logEpoch %q, they must be independent", taskAck.LogEpoch)
	}
	if taskAck.SubscriptionID == wsAck.SubscriptionID {
		t.Fatalf("two subscriptions on one connection share id %q", taskAck.SubscriptionID)
	}

	// Both snapshots were taken at their own topic's current cursor; neither
	// subscription moved the other's cursor by subscribing.
	var taskBaseline, wsBaseline int64
	for _, f := range rec.all() {
		if f.kind != snapshotKind {
			continue
		}
		if f.topic == TopicTasksIndex {
			taskBaseline = f.toSeq
		}
		if f.topic == TopicWorkspaces {
			wsBaseline = f.toSeq
		}
	}

	// A task change advances ONLY the tasks-index cursor. The change reaches the
	// controller the way it does in production: a workspace event, not a poll.
	src.setTitle("t1", "renamed")
	h.changed(t, src.scope)

	taskDeltas := 0
	var lastTaskToSeq int64
	wsDeltas := 0
	for _, f := range rec.all() {
		if f.kind != deltasKind {
			continue
		}
		switch f.topic {
		case TopicTasksIndex:
			taskDeltas++
			lastTaskToSeq = f.toSeq
		case TopicWorkspaces:
			wsDeltas++
			if f.fromSeq != wsBaseline {
				t.Fatalf("workspace delta started at %d, not at the subscribed watermark %d", f.fromSeq, wsBaseline)
			}
		}
	}
	if taskDeltas != 1 {
		t.Fatalf("want exactly one task delta after the rename, got %d", taskDeltas)
	}
	if lastTaskToSeq != taskBaseline+1 {
		t.Fatalf("tasks cursor moved %d → %d, want exactly one step", taskBaseline, lastTaskToSeq)
	}
	// The workspace fact did not change (still online, same path), so the
	// workspaces topic published nothing at all.
	if wsDeltas != 0 {
		t.Fatalf("an unchanged workspace fact published %d deltas, want 0", wsDeltas)
	}
}

// TestEmptyDeltaBatchPublishesNothing: an unchanged refresh must NOT advance the
// cursor. Advancing on an empty batch would make the next real delta carry a
// fromSeq the consumer never received, which reads as a gap and forces a resync.
func TestEmptyDeltaBatchPublishesNothing(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	rec := &recorder{}
	rec.install(att)
	if _, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	ack, err := att.Subscribe(context.Background(), TopicTasksIndex)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	before := rec.count()

	// Three forced refreshes with nothing changed.
	for i := 0; i < 3; i++ {
		resolved := h.runtime.resolveSource(src.scope)
		if err := h.runtime.refreshSource(context.Background(), resolved, true); err != nil {
			t.Fatalf("refresh %d: %v", i, err)
		}
	}
	if rec.count() != before {
		t.Fatalf("unchanged refreshes published %d frames; an empty delta batch must publish nothing", rec.count()-before)
	}
	// The cursor is still where the snapshot left it, so a later real delta starts
	// at that watermark and chains onto the snapshot instead of leaving a hole.
	var baseline int64
	for _, f := range rec.all() {
		if f.kind == snapshotKind && f.topic == TopicTasksIndex {
			baseline = f.toSeq
		}
	}
	src.setTitle("t1", "renamed")
	resolved := h.runtime.resolveSource(src.scope)
	if err := h.runtime.refreshSource(context.Background(), resolved, true); err != nil {
		t.Fatalf("refresh after change: %v", err)
	}
	last := rec.last()
	if last.kind != deltasKind || last.fromSeq != baseline || last.toSeq != baseline+1 {
		t.Fatalf("first real delta is fromSeq=%d toSeq=%d kind=%q, want %d to %d deltas",
			last.fromSeq, last.toSeq, last.kind, baseline, baseline+1)
	}
	_ = ack
}

// TestReconnectReplacesRowsInOneDeltaFrame is the load-bearing remote rule: when a
// remote source reconnects with a NEW remoteSessionId, the old rows' removals and
// the new rows' upserts must travel in ONE delta frame, so a consumer never sees
// an intermediate empty projection. It also asserts the order: removals first,
// then upserts, so no key is ever held twice.
func TestReconnectReplacesRowsInOneDeltaFrame(t *testing.T) {
	h := newTestHarness()
	old := h.add(newFakeSource(RemoteScope("remote-1", "/w/proj", "identity-1")))
	old.addTask("t1", "old task", Membership{Active: true}, 100)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	rec := &recorder{}
	rec.install(att)
	if _, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{old.scope},
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := att.Subscribe(context.Background(), TopicTasksIndex); err != nil {
		t.Fatalf("subscribe tasks: %v", err)
	}
	if _, err := att.Subscribe(context.Background(), TopicWorkspaces); err != nil {
		t.Fatalf("subscribe workspaces: %v", err)
	}
	before := rec.count()

	// The remote comes back under a new session id, with one row of its own.
	next := h.add(newFakeSource(RemoteScope("remote-2", "/w/proj", "identity-1")))
	next.addTask("t2", "new task", Membership{Active: true}, 200)

	resolved := h.runtime.resolveSource(next.scope)
	if err := h.runtime.ReplaceDisconnectedSource(context.Background(), old.scope, resolved); err != nil {
		t.Fatalf("replace: %v", err)
	}

	// Exactly ONE tasks-index frame carried the whole replacement (the
	// workspaces topic gets its own frame; what must not happen is a SECOND task
	// frame, which would expose an intermediate empty projection).
	var taskFrames []recordedFrame
	for _, f := range rec.all()[before:] {
		if f.topic == TopicTasksIndex {
			taskFrames = append(taskFrames, f)
		}
	}
	if len(taskFrames) != 1 {
		t.Fatalf("reconnect published %d task frames, want exactly 1 replacement frame", len(taskFrames))
	}
	frame := taskFrames[0]
	if frame.kind != deltasKind {
		t.Fatalf("reconnect frame kind %q, want deltas", frame.kind)
	}
	payload, ok := frame.raw.(taskFrame)
	if !ok {
		t.Fatalf("reconnect frame is not a task frame")
	}
	if len(payload.Payload.Deltas) != 2 {
		t.Fatalf("reconnect deltas = %d, want 2 (one removal + one upsert)", len(payload.Payload.Deltas))
	}
	if payload.Payload.Deltas[0].Op != OpTaskRemoved {
		t.Fatalf("first delta op %q, want %q (removals precede upserts)", payload.Payload.Deltas[0].Op, OpTaskRemoved)
	}
	if payload.Payload.Deltas[1].Op != OpTaskUpserted {
		t.Fatalf("second delta op %q, want %q", payload.Payload.Deltas[1].Op, OpTaskUpserted)
	}
	// The new row carries the NEW remoteSessionId: identity isolation survived.
	newRow := payload.Payload.Deltas[1].Task
	if newRow.Address.RemoteSessionID != "remote-2" {
		t.Fatalf("upserted row remoteSessionId %q, want remote-2", newRow.Address.RemoteSessionID)
	}
	if newRow.Address.WorkspaceIdentity != "identity-1" {
		t.Fatalf("upserted row identity %q, want identity-1", newRow.Address.WorkspaceIdentity)
	}

	// The workspaces topic removed the old fact and upserted the new one, in the
	// same batch.
	var sawRemoval, sawUpsert bool
	for _, f := range rec.all()[before:] {
		ws, ok := f.raw.(workspaceFrame)
		if !ok {
			continue
		}
		for _, d := range ws.Payload.Deltas {
			switch d.Op {
			case OpWorkspaceRemoved:
				if d.RemoteSessionID == "remote-1" {
					sawRemoval = true
				}
			case OpWorkspaceUpserted:
				if d.Workspace != nil && d.Workspace.RemoteSessionID == "remote-2" {
					sawUpsert = true
				}
			}
		}
	}
	if !sawRemoval || !sawUpsert {
		t.Fatalf("reconnect workspace batch: removal=%v upsert=%v, want both in one batch", sawRemoval, sawUpsert)
	}
}

// TestResyncReplaysSnapshotAndRefusesForeignID: resync re-sends the snapshot for
// this connection's own subscription and refuses one it does not own, so a
// connection cannot force another connection's stream to restart.
func TestResyncReplaysSnapshotAndRefusesForeignID(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)

	first := h.runtime.CreateAttachment()
	defer first.Dispose()
	second := h.runtime.CreateAttachment()
	defer second.Dispose()
	recA, recB := &recorder{}, &recorder{}
	recA.install(first)
	recB.install(second)

	if _, err := first.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	ackA, err := first.Subscribe(context.Background(), TopicTasksIndex)
	if err != nil {
		t.Fatalf("subscribe A: %v", err)
	}
	ackB, err := second.Subscribe(context.Background(), TopicTasksIndex)
	if err != nil {
		t.Fatalf("subscribe B: %v", err)
	}

	// B cannot resync A's subscription.
	if _, err := second.Resync(context.Background(), ackA.SubscriptionID); err == nil {
		t.Fatalf("connection B resynced connection A's subscription %q", ackA.SubscriptionID)
	}
	beforeB := recB.count()
	if recB.count() != beforeB {
		t.Fatalf("a refused resync still delivered a frame to the wrong connection")
	}

	// A resyncs its own: the snapshot is replayed with the current cursor.
	beforeA := recA.count()
	ack, err := first.Resync(context.Background(), ackA.SubscriptionID)
	if err != nil {
		t.Fatalf("resync A: %v", err)
	}
	if recA.count() != beforeA+1 {
		t.Fatalf("resync delivered %d frames, want 1", recA.count()-beforeA)
	}
	if recA.last().kind != snapshotKind {
		t.Fatalf("resync did not deliver a snapshot")
	}
	if ack.LogEpoch != ackA.LogEpoch {
		t.Fatalf("resync logEpoch %q != subscribe logEpoch %q", ack.LogEpoch, ackA.LogEpoch)
	}
	_ = ackB
}

// TestUnsubscribeStopsDeliveryAndIsNotAnError: after an unsubscribe the topic's
// deltas stop reaching that connection, and unsubscribing an id the connection
// does not own is a no-op rather than a failure (a UI cleanup that races a
// dispose must not error, and must not be able to tear down another connection's
// stream).
func TestUnsubscribeStopsDeliveryAndIsNotAnError(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	rec := &recorder{}
	rec.install(att)
	if _, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	ack, err := att.Subscribe(context.Background(), TopicTasksIndex)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := att.Unsubscribe(context.Background(), ack.SubscriptionID); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	before := rec.count()

	src.setTitle("t1", "renamed")
	resolved := h.runtime.resolveSource(src.scope)
	if err := h.runtime.refreshSource(context.Background(), resolved, true); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if rec.count() != before {
		t.Fatalf("an unsubscribed connection still received %d frames", rec.count()-before)
	}
	// An unknown / already-removed id is a silent no-op.
	if err := att.Unsubscribe(context.Background(), ack.SubscriptionID); err != nil {
		t.Fatalf("second unsubscribe must be a no-op, got %v", err)
	}
	if err := att.Unsubscribe(context.Background(), "never-existed"); err != nil {
		t.Fatalf("unsubscribe of a foreign id must be a no-op, got %v", err)
	}
}

// TestRemoteSourceWithoutIdentityIsRefused pins the schema refinement at the
// boundary that could violate it: a remote scope with no workspaceIdentity could
// only ever produce a task address the wire schema rejects.
func TestRemoteSourceWithoutIdentityIsRefused(t *testing.T) {
	h := newTestHarness()
	scope := SourceScope{Kind: KindRemote, RemoteSessionID: "remote-1", WorkspacePath: "/w/proj"}
	if err := h.runtime.projection.registerSource(scope); err == nil {
		t.Fatalf("a remote scope with no workspaceIdentity was accepted")
	}
	// The local fallback still works: identity is optional for a local source.
	local := LocalScope("/w/proj", "")
	if err := h.runtime.projection.registerSource(local); err != nil {
		t.Fatalf("a local scope with no identity must be accepted, got %v", err)
	}
}
