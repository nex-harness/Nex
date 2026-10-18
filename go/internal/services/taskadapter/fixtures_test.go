package taskadapter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakes_test.go — the in-memory doubles the guard suite runs against. Every test in this
// package names a guard row in phase-3-guards.md, so the doubles stay deliberately
// small: they must make the DECISION observable, not simulate a CLI.

// fakeAgent records the sends the drain performed and can be made to fail.
type fakeAgent struct {
	mu       sync.Mutex
	sent     []Command
	failWith error
	// onSend runs inside the send, which is how a test enqueues a second command
	// mid-drain to exercise the D4 re-arm.
	onSend func(command Command)
}

func (f *fakeAgent) SendText(_ context.Context, _ Target, command Command) error {
	f.mu.Lock()
	f.sent = append(f.sent, command)
	hook := f.onSend
	err := f.failWith
	f.mu.Unlock()
	if hook != nil {
		hook(command)
	}
	return err
}

func (f *fakeAgent) sentCommands() []Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Command, len(f.sent))
	copy(out, f.sent)
	return out
}

// manualScheduler collects the deferred drains so a test decides when one runs. The
// default adapter schedule is a goroutine, which would make the assertions racy.
type manualScheduler struct {
	mu      sync.Mutex
	pending []func()
}

func (m *manualScheduler) schedule(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending = append(m.pending, fn)
}

func (m *manualScheduler) runAll() {
	for {
		m.mu.Lock()
		if len(m.pending) == 0 {
			m.mu.Unlock()
			return
		}
		next := m.pending[0]
		m.pending = m.pending[1:]
		m.mu.Unlock()
		next()
	}
}

func (m *manualScheduler) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending)
}

// fakeHosts is the HostDirectory: which hosts still exist.
type fakeHosts struct {
	mu     sync.Mutex
	live   map[string]bool
	remove func(hostID string)
}

func newFakeHosts(ids ...string) *fakeHosts {
	live := map[string]bool{}
	for _, id := range ids {
		live[id] = true
	}
	return &fakeHosts{live: live}
}

func (f *fakeHosts) Registered(hostID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live[hostID]
}

func (f *fakeHosts) kill(hostID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.live, hostID)
}

// fakeClock is a manual timer seam: the test decides when the owner-command timeout
// fires, so no test sleeps.
type fakeClock struct {
	mu      sync.Mutex
	pending map[int64]func()
	nextID  int64
	due     []timedEntry
}

type timedEntry struct {
	at int64
	id int64
}

func newFakeClock() *fakeClock {
	return &fakeClock{pending: map[int64]func(){}}
}

func (f *fakeClock) after(delay int64, fn func()) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.pending[f.nextID] = fn
	f.due = append(f.due, timedEntry{at: delay, id: f.nextID})
	return f.nextID
}

// fire runs every timer registered at or before now.
func (f *fakeClock) fire(now int64) {
	f.mu.Lock()
	var fire []func()
	kept := f.due[:0]
	for _, entry := range f.due {
		if entry.at <= now {
			fire = append(fire, f.pending[entry.id])
			delete(f.pending, entry.id)
			continue
		}
		kept = append(kept, entry)
	}
	f.due = kept
	f.mu.Unlock()
	for _, fn := range fire {
		fn()
	}
}

// newTestAdapter builds an adapter wired to manual drains.
func newTestAdapter(t *testing.T) (*Adapter, *fakeAgent, *manualScheduler) {
	t.Helper()
	agent := &fakeAgent{}
	scheduler := &manualScheduler{}
	adapter := NewAdapter(AdapterOptions{
		Agent:    agent,
		Schedule: scheduler.schedule,
		Now:      func() int64 { return 1 },
	})
	t.Cleanup(adapter.Dispose)
	return adapter, agent, scheduler
}

// testTarget is the fixture workspace/task used across the suite.
func testTarget() Target {
	return Target{TaskID: "task-1", WorkspacePath: "/repo"}
}

// remoteTarget is the phone leg: a remote workspace is identified, never path-matched.
func remoteTarget() Target {
	return Target{TaskID: "task-1", WorkspacePath: "/repo", WorkspaceIdentity: "remote-1"}
}

// asRejected unwraps a CommandRejectedError, the way a caller would with errors.As.
func asRejected(err error, target **CommandRejectedError) bool {
	return errors.As(err, target)
}

// newCommand is a queued input.
func newCommand(id, traceID, content string) Command {
	return Command{CommandID: id, TraceID: traceID, Content: content}
}

// ownerCommand is an owner-command request naming a run.
func ownerCommand(requestID, runID string) OwnerCommand {
	return OwnerCommand{
		CommandRequestID: requestID,
		WorkspacePath:    "/repo",
		WorkspaceKey:     "/repo",
		TaskID:           "task-1",
		RunID:            runID,
		Type:             "enqueue_task_command",
	}
}

// resultLog collects the results posted back to requesters.
type resultLog struct {
	mu      sync.Mutex
	results []OwnerCommandResult
}

func (l *resultLog) report(_ string, result OwnerCommandResult) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.results = append(l.results, result)
}

func (l *resultLog) all() []OwnerCommandResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]OwnerCommandResult, len(l.results))
	copy(out, l.results)
	return out
}

// newRouter wires a router over a real timer seam and returns the result log.
func newRouter(t *testing.T, leases *LeaseTable, hosts HostDirectory) (*CommandRouter, *resultLog) {
	t.Helper()
	return newRouterWithClock(t, leases, hosts, nil)
}

// newRouterWithClock replaces the timeout timer with the manual clock when non-nil.
func newRouterWithClock(t *testing.T, leases *LeaseTable, hosts HostDirectory, clock *fakeClock) (*CommandRouter, *resultLog) {
	t.Helper()
	log := &resultLog{}
	opts := RouterOptions{
		Leases:  leases,
		Hosts:   hosts,
		Report:  log.report,
		Timeout: 30 * time.Second,
	}
	if clock != nil {
		opts.AfterFunc = func(delay time.Duration, fn func()) *time.Timer {
			// The manual clock owns the decision, so there is no real timer to stop;
			// a nil handle is what the stop seam already tolerates.
			clock.after(int64(delay/time.Second), fn)
			return nil
		}
	}
	router := NewCommandRouter(opts)
	t.Cleanup(router.Dispose)
	return router, log
}
