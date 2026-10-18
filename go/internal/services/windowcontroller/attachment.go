package windowcontroller

import (
	"context"
	"sync"
)

// attachment.go is the per-connection service: the object an RPC channel hands to
// one connection, and the object whose Dispose is that connection's cleanup.
//
// What is per-connection and what is shared is the whole design:
//
//	shared (the runtime): the projected rows, the two topic cursors, the source
//	  bookkeeping. Two windows looking at the same workspace must see the SAME
//	  list; a second copy would be a second source of truth.
//	per-connection (the attachment): the subscription ids, the frame emitter and
//	  the set of topics it is watching. A connection that never subscribed must
//	  receive nothing, and disposing it must remove exactly its own ids and leave
//	  every other connection's ids in place.
//
// The TS expresses the same split as runtime.service (a process-level instance
// registered on the channel) plus runtime.createAttachmentService() per
// WebSocket, where the returned object is `{ service, dispose }` and the `dispose`
// is deliberately stripped from the object the client can see.

const (
	// VisibilityForeground / VisibilityBackground are the two controller subscribe
	// visibilities. The controller accepts both; it does not filter frames by
	// them, because the projection already publishes one authoritative batch to
	// every subscriber of the topic.
	VisibilityForeground = "foreground"
	VisibilityBackground = "background"
)

// Attachment is one connection's controller service.
type Attachment struct {
	rt *Runtime

	mu sync.Mutex
	// subscriptions is this attachment's subscription id → topic. It is the ONLY
	// thing Dispose may unsubscribe: an id that belongs to another attachment is
	// invisible here and therefore unreachable.
	subscriptions map[string]string
	// handler is where this connection's frames are delivered. It is set by the
	// channel adapter and may be nil, in which case frames are still computed and
	// dropped (the projection stays authoritative; nothing is buffered for a
	// connection that has not wired delivery).
	handler  func(frame any)
	disposed bool
}

func newAttachment(rt *Runtime) *Attachment {
	return &Attachment{rt: rt, subscriptions: map[string]string{}}
}

// SetHandler installs this connection's frame delivery hook. The channel adapter
// calls it once, before it exposes the service.
func (a *Attachment) SetHandler(fn func(frame any)) {
	a.mu.Lock()
	a.handler = fn
	a.mu.Unlock()
}

func (a *Attachment) deliver(frame any) {
	a.mu.Lock()
	fn := a.handler
	a.mu.Unlock()
	if fn != nil {
		fn(frame)
	}
}

// isDisposed reports whether this attachment has been torn down.
func (a *Attachment) isDisposed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.disposed
}

// Subscribe opens a subscription on a controller topic. It returns the ack
// (subscriptionId, mode, logEpoch) after the snapshot has been delivered.
//
// The subscription id is minted HERE, per attachment, so two connections watching
// the same topic never collide and one connection's dispose cannot remove
// another's stream.
func (a *Attachment) Subscribe(ctx context.Context, topic string) (SubscribeAck, error) {
	if a.isDisposed() {
		return SubscribeAck{}, ErrClosed
	}
	if topic != TopicWorkspaces && topic != TopicTasksIndex {
		return SubscribeAck{}, ErrUnknownTopic
	}
	subscriptionID := a.rt.createID()
	cursor := a.rt.projection.cursorFor(topic)

	// Register BEFORE the snapshot so a publish that races the subscribe is not
	// lost: the subscriber is already in the fan-out set when its snapshot is
	// built, and the snapshot reflects the state at that instant. The projection
	// delivers the snapshot inside subscribe, before this returns.
	a.mu.Lock()
	if a.disposed {
		a.mu.Unlock()
		return SubscribeAck{}, ErrClosed
	}
	a.subscriptions[subscriptionID] = topic
	a.mu.Unlock()

	a.rt.projection.subscribe(topic, subscriptionID, a.deliver)

	return SubscribeAck{SubscriptionID: subscriptionID, Mode: "snapshot", LogEpoch: cursor.logEpoch}, nil
}

// Resync re-delivers the snapshot for one of this attachment's subscriptions.
func (a *Attachment) Resync(ctx context.Context, subscriptionID string) (SubscribeAck, error) {
	if a.isDisposed() {
		return SubscribeAck{}, ErrClosed
	}
	if !a.owns(subscriptionID) {
		return SubscribeAck{}, ErrNotOwned
	}
	if err := a.rt.projection.resync(subscriptionID); err != nil {
		return SubscribeAck{}, err
	}
	topic := a.topicOf(subscriptionID)
	cursor := a.rt.projection.cursorFor(topic)
	return SubscribeAck{SubscriptionID: subscriptionID, Mode: "snapshot", LogEpoch: cursor.logEpoch}, nil
}

// Unsubscribe drops one of this attachment's subscriptions. An id this attachment
// does not own is a no-op, not an error: a UI cleanup that races a dispose must
// not be able to tear down another connection's subscription, and it must not fail
// either.
func (a *Attachment) Unsubscribe(ctx context.Context, subscriptionID string) error {
	if !a.owns(subscriptionID) {
		return nil
	}
	a.mu.Lock()
	delete(a.subscriptions, subscriptionID)
	a.mu.Unlock()
	a.rt.projection.unsubscribe(subscriptionID)
	return nil
}

// owns reports whether the id is one of THIS attachment's subscriptions.
func (a *Attachment) owns(subscriptionID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.subscriptions[subscriptionID]
	return ok
}

func (a *Attachment) topicOf(subscriptionID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.subscriptions[subscriptionID]
}

// ListTaskList answers a list query. It is the same projection every connection
// reads; the attachment adds no state of its own.
func (a *Attachment) ListTaskList(ctx context.Context, query ListQuery) (ListResult, error) {
	if a.isDisposed() {
		return ListResult{}, ErrClosed
	}
	return a.rt.ListTaskList(ctx, query)
}

// MutateTask applies one mutation.
func (a *Attachment) MutateTask(ctx context.Context, addr TaskAddress, mutation Mutation) (*taskMeta, error) {
	if a.isDisposed() {
		return nil, ErrClosed
	}
	return a.rt.MutateTask(ctx, addr, mutation)
}

// DeleteArchivedTask conditionally removes one archived task.
func (a *Attachment) DeleteArchivedTask(ctx context.Context, addr TaskAddress) (bool, error) {
	if a.isDisposed() {
		return false, ErrClosed
	}
	return a.rt.DeleteArchivedTask(ctx, addr)
}

// DeleteArchivedTasks conditionally removes a batch of archived tasks.
func (a *Attachment) DeleteArchivedTasks(ctx context.Context, addr TaskAddress, taskIDs []string) (ArchivedDeletionResult, error) {
	if a.isDisposed() {
		return ArchivedDeletionResult{}, ErrClosed
	}
	return a.rt.DeleteArchivedTasks(ctx, addr, taskIDs)
}

// ResolveTaskAddress resolves a sidebar address to its owning source.
func (a *Attachment) ResolveTaskAddress(ctx context.Context, addr TaskAddress, allowMissing bool) (TaskAddress, error) {
	if a.isDisposed() {
		return TaskAddress{}, ErrClosed
	}
	return a.rt.ResolveTaskAddress(ctx, addr, allowMissing)
}

// SubscriptionCount reports this attachment's live subscriptions, so a test (or an
// operator diagnostic) can assert that a dispose really released them.
func (a *Attachment) SubscriptionCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.subscriptions)
}

// Dispose tears down this connection's attachment. It is idempotent and it is the
// ONLY teardown that connection needs: every subscription it created is removed
// from the projection's fan-out, so a later publish cannot reach a dead
// connection, and no other connection's state is touched.
//
// Disposing an attachment does NOT remove rows: the projection is shared and
// still owned by the runtime. Two windows keep seeing the same list until the
// window is gone entirely, at which point the runtime's own dispose runs.
func (a *Attachment) Dispose() {
	a.mu.Lock()
	if a.disposed {
		a.mu.Unlock()
		return
	}
	a.disposed = true
	ids := make([]string, 0, len(a.subscriptions))
	for id := range a.subscriptions {
		ids = append(ids, id)
	}
	a.subscriptions = map[string]string{}
	// The handler is released so a publish already holding this attachment cannot
	// keep it (and through it the runtime) reachable from a client's callback.
	a.handler = nil
	a.mu.Unlock()

	for _, id := range ids {
		a.rt.projection.unsubscribe(id)
	}
	a.rt.releaseAttachment()
}
