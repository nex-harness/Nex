package taskindex_test

// fakeagent_test.go — a controllable Agent double: it lets a test drive the
// exact frame sequences (including gaps, duplicates and out-of-order
// deliveries) that the real agent produces.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/tylinux/nex/go/internal/services/taskindex"
)

// codedError carries an agent service error code so the syncer's retry-kind
// branch can be exercised.
type codedError struct {
	code string
}

func (e codedError) Error() string     { return "agent error " + e.code }
func (e codedError) ErrorCode() string { return e.code }

func errProviderNotReady() error { return codedError{code: taskindex.CodeProviderNotReady} }
func errRuntimeUnavailable() error {
	return codedError{code: taskindex.CodeRuntimeUnavailable}
}
func errTransient() error { return codedError{code: "TRANSPORT_CLOSED"} }

type fakeAgent struct {
	mu sync.Mutex

	subscriptions []string
	unsubscribed  []string
	resyncs       []fakeResync
	subscribeErr  error
	resyncErr     error
	// subscribeResult overrides the ACK (subscriptionId, mode, logEpoch).
	subscriptionCounter int
	attempts            int
	logEpoch            string
	subscribeMode       string

	sessions  map[string]taskindex.SessionSnapshot
	readErr   error
	readCount map[string]int

	handlers  []func(taskindex.SessionFrameEvent)
	workspace taskindex.WorkspaceTarget

	lifecycleHandlers []func(taskindex.RuntimeLifecycleEvent)
	removeLifecycle   []func()
}

type fakeResync struct {
	SubscriptionID string
	Base           *taskindex.ResumeBase
	ForceSnapshot  bool
}

func newFakeAgent() *fakeAgent {
	return &fakeAgent{
		logEpoch:      "epoch-1",
		subscribeMode: "snapshot",
		sessions:      map[string]taskindex.SessionSnapshot{},
		readCount:     map[string]int{},
	}
}

func (f *fakeAgent) SubscribeSessionsIndex(_ context.Context, _ taskindex.WorkspaceTarget, _, _ string) (taskindex.SubscribeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.subscribeErr != nil {
		return taskindex.SubscribeResult{}, f.subscribeErr
	}
	f.subscriptionCounter++
	f.subscriptions = append(f.subscriptions, fmt.Sprintf("sub-%d", f.subscriptionCounter))
	return taskindex.SubscribeResult{Ack: taskindex.SubscribeAck{
		SubscriptionID: fmt.Sprintf("sub-%d", f.subscriptionCounter),
		Mode:           f.subscribeMode,
		LogEpoch:       f.logEpoch,
	}}, nil
}

func (f *fakeAgent) ResyncSessionsIndex(_ context.Context, _ taskindex.WorkspaceTarget, subscriptionID string, base *taskindex.ResumeBase, forceSnapshot bool) (taskindex.ResyncResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resyncs = append(f.resyncs, fakeResync{SubscriptionID: subscriptionID, Base: base, ForceSnapshot: forceSnapshot})
	if f.resyncErr != nil {
		return taskindex.ResyncResult{}, f.resyncErr
	}
	return taskindex.ResyncResult{Ack: taskindex.SubscribeAck{
		SubscriptionID: subscriptionID,
		Mode:           "resume",
		LogEpoch:       f.logEpoch,
	}}, nil
}

func (f *fakeAgent) UnsubscribeSessionsIndex(_ context.Context, _ taskindex.WorkspaceTarget, subscriptionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unsubscribed = append(f.unsubscribed, subscriptionID)
	return nil
}

func (f *fakeAgent) ReadSession(_ context.Context, target taskindex.SessionTarget) (taskindex.SessionSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readCount[target.SessionID]++
	if f.readErr != nil {
		return taskindex.SessionSnapshot{}, f.readErr
	}
	snapshot, ok := f.sessions[target.SessionID]
	if !ok {
		return taskindex.SessionSnapshot{}, fmt.Errorf("no session %s", target.SessionID)
	}
	return snapshot, nil
}

func (f *fakeAgent) Frames(_ taskindex.WorkspaceTarget, handler func(taskindex.SessionFrameEvent)) {
	f.mu.Lock()
	f.handlers = append(f.handlers, handler)
	f.mu.Unlock()
}

func (f *fakeAgent) OnRuntimeLifecycle(handler func(taskindex.RuntimeLifecycleEvent)) func() {
	f.mu.Lock()
	f.lifecycleHandlers = append(f.lifecycleHandlers, handler)
	index := len(f.lifecycleHandlers) - 1
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.lifecycleHandlers[index] = func(taskindex.RuntimeLifecycleEvent) {}
		f.mu.Unlock()
	}
}

func (f *fakeAgent) emit(event taskindex.RuntimeLifecycleEvent) {
	f.mu.Lock()
	handlers := append([]func(taskindex.RuntimeLifecycleEvent){}, f.lifecycleHandlers...)
	f.mu.Unlock()
	for _, h := range handlers {
		h(event)
	}
}

// deliver pushes a frame to every registered handler.
func (f *fakeAgent) deliver(frame taskindex.SessionFrame, deliveryKind string) {
	f.mu.Lock()
	handlers := append([]func(taskindex.SessionFrameEvent){}, f.handlers...)
	f.mu.Unlock()
	for _, h := range handlers {
		h(taskindex.SessionFrameEvent{Frame: frame, DeliveryKind: deliveryKind})
	}
}

func (f *fakeAgent) deliverFault(reason string, deliveryKind string) {
	f.mu.Lock()
	handlers := append([]func(taskindex.SessionFrameEvent){}, f.handlers...)
	f.mu.Unlock()
	for _, h := range handlers {
		h(taskindex.SessionFrameEvent{DeliveryKind: deliveryKind, FaultReasonCode: reason})
	}
}

func (f *fakeAgent) lastSubscription() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.subscriptions) == 0 {
		return ""
	}
	return f.subscriptions[len(f.subscriptions)-1]
}

func (f *fakeAgent) subscribeAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

func (f *fakeAgent) subscriptionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subscriptions)
}

func (f *fakeAgent) resyncLog() []fakeResync {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeResync{}, f.resyncs...)
}

func (f *fakeAgent) unsubscribeLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.unsubscribed...)
}

func (f *fakeAgent) putSession(snapshot taskindex.SessionSnapshot) {
	f.mu.Lock()
	f.sessions[snapshot.SessionID] = snapshot
	f.mu.Unlock()
}

// ── frame builders ───────────────────────────────────────────────────────────

func snapshotFrame(topic, subscriptionID string, fromSeq, toSeq int64, logEpoch string, sessions []taskindex.SessionSummary) taskindex.SessionFrame {
	payload := map[string]any{
		"kind": "snapshot",
		"snapshot": map[string]any{
			"protocolVersion": 1,
			"workspaceId":     topic,
			"logEpoch":        logEpoch,
			"sessions":        sessions,
		},
	}
	raw, _ := json.Marshal(payload)
	return taskindex.SessionFrame{
		Topic: topic, SubscriptionID: subscriptionID,
		FromSeq: fromSeq, ToSeq: toSeq, Payload: raw,
	}
}

func deltaFrame(topic, subscriptionID string, fromSeq, toSeq int64, deltas []taskindex.SessionDelta) taskindex.SessionFrame {
	raw, _ := json.Marshal(map[string]any{"kind": "deltas", "deltas": deltas})
	return taskindex.SessionFrame{
		Topic: topic, SubscriptionID: subscriptionID,
		FromSeq: fromSeq, ToSeq: toSeq, Payload: raw,
	}
}

func summary(id string, phase string, createdAt, lastActivity int64, title string) taskindex.SessionSummary {
	return taskindex.SessionSummary{
		SessionID: id, Title: title, Phase: phase,
		CreatedAt: createdAt, LastActivityAt: lastActivity,
	}
}

func upsert(s taskindex.SessionSummary) taskindex.SessionDelta {
	return taskindex.SessionDelta{Op: "session.upserted", Session: &s}
}

func remove(id string) taskindex.SessionDelta {
	return taskindex.SessionDelta{Op: "session.removed", SessionID: id}
}
