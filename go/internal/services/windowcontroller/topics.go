package windowcontroller

// topics.go owns the two topics' cursors and the subscription registry, and it
// is the only place a frame is constructed. The rules, each from controller.ts:
//
//   - Each topic owns an independent (logEpoch, seq). They start at two
//     DISTINCT epochs: a consumer that watched one topic must not be told its
//     cursor is current on the other, so the epochs are separately generated.
//   - subscribe sends the snapshot SYNCHRONOUSLY before returning the ack. The
//     TS calls params.onFrame(snapshotFrame(...)) inside subscribe and then
//     returns the ack; a UI that applied the ack before the snapshot would
//     briefly render an empty list. Keep that order.
//   - A delta batch with ZERO deltas publishes NOTHING and does not advance the
//     cursor. Advancing on an empty batch would make the next real delta carry a
//     fromSeq the consumer never saw, which reads as a gap and forces a resync.
//   - resync re-sends the snapshot for the CURRENT cursor (fromSeq 0) and returns
//     the ack; a resync for an unknown subscription is an error, because silently
//     acknowledging would leave the consumer with no state and no error.

type topicCursor struct {
	logEpoch string
	seq      int64
}

type subscriber struct {
	topic          string
	subscriptionID string
	onFrame        func(frame any)
}

// frameEnvelope is the common frame header. The two topic frame types differ only
// in payload, so the cursor bookkeeping is shared and the payload is attached by
// the caller.
type frameEnvelope struct {
	subscriptionID string
	topic          string
	logEpoch       string
	fromSeq        int64
	toSeq          int64
	sentAt         int64
}

// taskFrame / workspaceFrame are the concrete frames delivered to subscribers.
// They are the JSON values the wire carries.
type taskFrame struct {
	frameEnvelope
	Payload taskFramePayload `json:"payload"`
}

type taskFramePayload struct {
	Kind     string                `json:"kind"`
	Snapshot *tasksSnapshotPayload `json:"snapshot,omitempty"`
	Deltas   []TaskDelta           `json:"deltas,omitempty"`
}

type tasksSnapshotPayload struct {
	ProtocolVersion int       `json:"protocolVersion"`
	LogEpoch        string    `json:"logEpoch"`
	Tasks           []TaskRow `json:"tasks"`
}

type workspaceFrame struct {
	frameEnvelope
	Payload workspaceFramePayload `json:"payload"`
}

type workspaceFramePayload struct {
	Kind     string                     `json:"kind"`
	Snapshot *workspacesSnapshotPayload `json:"snapshot,omitempty"`
	Deltas   []WorkspaceDelta           `json:"deltas,omitempty"`
}

type workspacesSnapshotPayload struct {
	ProtocolVersion int             `json:"protocolVersion"`
	LogEpoch        string          `json:"logEpoch"`
	Workspaces      []WorkspaceFact `json:"workspaces"`
}

// snapshotKind / deltasKind are the payload discriminants.
const (
	snapshotKind = "snapshot"
	deltasKind   = "deltas"
)

// newProjectionCursors seeds the two topic cursors with distinct epochs.
func newProjectionCursors(createID func() string) map[string]*topicCursor {
	return map[string]*topicCursor{
		TopicTasksIndex: {logEpoch: createID(), seq: 0},
		TopicWorkspaces: {logEpoch: createID(), seq: 0},
	}
}

// snapshotTaskFrame builds the tasks-index snapshot frame for a subscriber. The
// snapshot always starts at seq 0 (a snapshot is a full state, not a delta) and
// its payload logEpoch matches the frame logEpoch (a schema refinement).
func snapshotTaskFrame(sub *subscriber, cursor *topicCursor, rows []TaskRow, now int64) taskFrame {
	if rows == nil {
		rows = []TaskRow{}
	}
	return taskFrame{
		frameEnvelope: frameEnvelope{
			subscriptionID: sub.subscriptionID,
			topic:          TopicTasksIndex,
			logEpoch:       cursor.logEpoch,
			fromSeq:        0,
			toSeq:          cursor.seq,
			sentAt:         now,
		},
		Payload: taskFramePayload{
			Kind: snapshotKind,
			Snapshot: &tasksSnapshotPayload{
				ProtocolVersion: 1,
				LogEpoch:        cursor.logEpoch,
				Tasks:           rows,
			},
		},
	}
}

// snapshotWorkspaceFrame builds the workspaces snapshot frame.
func snapshotWorkspaceFrame(sub *subscriber, cursor *topicCursor, facts []WorkspaceFact, now int64) workspaceFrame {
	if facts == nil {
		facts = []WorkspaceFact{}
	}
	return workspaceFrame{
		frameEnvelope: frameEnvelope{
			subscriptionID: sub.subscriptionID,
			topic:          TopicWorkspaces,
			logEpoch:       cursor.logEpoch,
			fromSeq:        0,
			toSeq:          cursor.seq,
			sentAt:         now,
		},
		Payload: workspaceFramePayload{
			Kind: snapshotKind,
			Snapshot: &workspacesSnapshotPayload{
				ProtocolVersion: 1,
				LogEpoch:        cursor.logEpoch,
				Workspaces:      facts,
			},
		},
	}
}

// publishTaskDeltas advances the tasks-index cursor by one and fans the batch out
// to every subscriber of that topic. An empty batch is a no-op (no cursor move).
//
// The cursor advance and the subscriber snapshot happen in ONE critical section:
// two sources refresh in parallel, and reading the cursor outside the lock let
// both claim the same fromSeq (a gap the consumer could never reconcile) and race
// the seq write itself. The DELIVERY still runs after the unlock, so a slow or
// re-entrant consumer cannot block another source's refresh.
func (p *projection) publishTaskDeltas(deltas []TaskDelta, now int64) {
	if len(deltas) == 0 {
		return
	}
	p.mu.Lock()
	cursor := p.cursors[TopicTasksIndex]
	frame := taskFrame{
		frameEnvelope: frameEnvelope{
			topic:    TopicTasksIndex,
			logEpoch: cursor.logEpoch,
			fromSeq:  cursor.seq,
			toSeq:    cursor.seq + 1,
			sentAt:   now,
		},
		Payload: taskFramePayload{Kind: deltasKind, Deltas: deltas},
	}
	cursor.seq++
	subs := p.subscribersLocked(TopicTasksIndex)
	p.mu.Unlock()
	p.deliverTaskFrame(frame, subs)
}

// publishWorkspaceDeltas is the workspaces-topic twin of publishTaskDeltas.
func (p *projection) publishWorkspaceDeltas(deltas []WorkspaceDelta, now int64) {
	if len(deltas) == 0 {
		return
	}
	p.mu.Lock()
	cursor := p.cursors[TopicWorkspaces]
	frame := workspaceFrame{
		frameEnvelope: frameEnvelope{
			topic:    TopicWorkspaces,
			logEpoch: cursor.logEpoch,
			fromSeq:  cursor.seq,
			toSeq:    cursor.seq + 1,
			sentAt:   now,
		},
		Payload: workspaceFramePayload{Kind: deltasKind, Deltas: deltas},
	}
	cursor.seq++
	subs := p.subscribersLocked(TopicWorkspaces)
	p.mu.Unlock()
	p.deliverWorkspaceFrame(frame, subs)
}

// deliverTaskFrame hands one delta frame to each subscriber. Each subscriber gets
// its OWN frame value with its own subscriptionId, so a handler that mutates or
// retains the frame it receives cannot corrupt another subscriber's copy.
func (p *projection) deliverTaskFrame(frame taskFrame, subs []*subscriber) {
	for _, sub := range subs {
		out := frame
		out.subscriptionID = sub.subscriptionID
		sub.onFrame(out)
	}
}

// deliverWorkspaceFrame is deliverTaskFrame's twin.
func (p *projection) deliverWorkspaceFrame(frame workspaceFrame, subs []*subscriber) {
	for _, sub := range subs {
		out := frame
		out.subscriptionID = sub.subscriptionID
		sub.onFrame(out)
	}
}

// subscribersLocked is the subscriber snapshot for a caller that already holds
// p.mu. The slice is a COPY, so the fan-out runs unlocked: a subscriber that
// calls back into the projection (or blocks) must not hold the projection mutex,
// or a slow consumer would deadlock every other consumer's publish.
func (p *projection) subscribersLocked(topic string) []*subscriber {
	out := make([]*subscriber, 0, len(p.subscribers))
	for _, sub := range p.subscribers {
		if sub.topic == topic {
			out = append(out, sub)
		}
	}
	return out
}

// subscribe registers a subscriber for a topic, delivers the snapshot, and
// returns the ack. The snapshot is delivered before the ack is returned.
func (p *projection) subscribe(topic string, subscriptionID string, onFrame func(any)) {
	sub := &subscriber{topic: topic, subscriptionID: subscriptionID, onFrame: onFrame}
	p.mu.Lock()
	p.subscribers[subscriptionID] = sub
	cursor := p.cursors[topic]
	now := p.now()
	// Build the snapshot from the CURRENT state while still holding the lock, so
	// the snapshot reflects a consistent point and its toSeq matches the cursor.
	// The frame is delivered after the lock is released (below).
	var frame any
	if topic == TopicTasksIndex {
		frame = snapshotTaskFrame(sub, cursor, allRows(p.sources), now)
	} else {
		frame = snapshotWorkspaceFrame(sub, cursor, allWorkspaceFacts(p.sources), now)
	}
	p.mu.Unlock()
	// Deliver the snapshot before the caller observes the ack.
	onFrame(frame)
}

// resync re-delivers the snapshot for an existing subscription and returns the
// cursor ack. An unknown subscription is an error.
func (p *projection) resync(subscriptionID string) error {
	p.mu.Lock()
	sub := p.subscribers[subscriptionID]
	if sub == nil {
		p.mu.Unlock()
		return errUnknownSubscription(subscriptionID)
	}
	cursor := p.cursors[sub.topic]
	now := p.now()
	var frame any
	if sub.topic == TopicTasksIndex {
		frame = snapshotTaskFrame(sub, cursor, allRows(p.sources), now)
	} else {
		frame = snapshotWorkspaceFrame(sub, cursor, allWorkspaceFacts(p.sources), now)
	}
	p.mu.Unlock()
	sub.onFrame(frame)
	return nil
}

// unsubscribe removes a subscriber. Removing an unknown subscription is a no-op
// (the TS disposes a Map entry that may already be gone); it is not an error,
// because the caller disposing a connection must not have to track every id.
func (p *projection) unsubscribe(subscriptionID string) {
	p.mu.Lock()
	delete(p.subscribers, subscriptionID)
	p.mu.Unlock()
}

// errUnknownSubscription is resync's refusal for an id this projection never
// issued (a stale UI cleanup that raced a dispose).
type unknownSubscriptionError string

func (e unknownSubscriptionError) Error() string { return string(e) }

func errUnknownSubscription(id string) error {
	return unknownSubscriptionError("no controller subscription for id=" + id)
}
