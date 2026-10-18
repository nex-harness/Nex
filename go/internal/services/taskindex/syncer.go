package taskindex

// syncer.go — the sessions-index → task index mirror.
//
// Port of packages/services/src/nex-agent/nexTaskIndexSyncer.ts, restricted to
// the sessions-index topic family (the workspace-config half lands with the
// config service in 3.10.5). The recovery/generation rules are the substance of
// the port and are preserved 1:1; see syncer_state.go for the ordering
// invariants they protect.

import (
	"context"
	"reflect"
	"sync"
	"time"
)

// Syncer mirrors agent sessions into the task index and fans the resulting
// changes out to workspace listeners.
type Syncer struct {
	agent Agent
	repo  *Repo

	mu      sync.Mutex
	ingests map[string]*ingestState
	// runtimeGenerations tracks which workspaces have an attached runtime.
	runtimeGenerations map[string]int64

	emitters map[string]*workspaceEmitter

	terminalListeners []func(TerminalEvent)
	readyListeners    []func(ReadyEvent)

	unsubscribeRuntime func()
	disposed           bool

	// schedule runs the retry timers; tests replace it with a manual trigger.
	schedule func(delay time.Duration, fire func())
	now      func() int64
}

// SyncerOptions configures NewSyncer.
type SyncerOptions struct {
	Agent Agent
	Repo  *Repo
	// Schedule defaults to a real timer; tests drive retries manually.
	Schedule func(delay time.Duration, fire func())
	Now      func() int64
}

// NewSyncer builds the syncer and starts listening for runtime lifecycle events.
func NewSyncer(opts SyncerOptions) *Syncer {
	if opts.Schedule == nil {
		opts.Schedule = func(delay time.Duration, fire func()) {
			time.AfterFunc(delay, fire)
		}
	}
	if opts.Now == nil {
		opts.Now = func() int64 { return time.Now().UnixMilli() }
	}
	s := &Syncer{
		agent:              opts.Agent,
		repo:               opts.Repo,
		ingests:            map[string]*ingestState{},
		runtimeGenerations: map[string]int64{},
		emitters:           map[string]*workspaceEmitter{},
		schedule:           opts.Schedule,
		now:                opts.Now,
	}
	if opts.Agent != nil {
		s.unsubscribeRuntime = opts.Agent.OnRuntimeLifecycle(s.onRuntimeLifecycle)
	}
	return s
}

// TerminalEvent is NexTaskIndexTerminalEvent. Order matters: the turn terminal
// closes the current input, then the ready boundary releases the next one.
type TerminalEvent struct {
	Target SessionTarget
	// Kind is "turn.completed" | "turn.failed".
	Kind string
}

// ReadyEvent is NexTaskIndexReadyEvent: the boundary at which the next input may
// be sent (the phone command queue depends on exactly this event).
type ReadyEvent struct {
	Target SessionTarget
	// Reason is "prompt_completed" | "prompt_failed".
	Reason string
}

// workspaceEmitter is the per-workspace event fan-out the UI listens on. The
// adapter path and the desktop-continuous path share one emitter, so a
// workspace_task_list_changed is delivered once no matter which path wrote it.
type workspaceEmitter struct {
	mu        sync.Mutex
	listeners []func(WorkspaceEvent)
	closed    bool
}

// WorkspaceEvent is NexWorkspaceEvent. Only the events this step produces are
// modelled.
type WorkspaceEvent struct {
	Type              string    `json:"type"`
	WorkspacePath     string    `json:"workspacePath"`
	WorkspaceIdentity string    `json:"workspaceIdentity,omitempty"`
	TaskID            string    `json:"taskId,omitempty"`
	Reason            string    `json:"reason,omitempty"`
	UnreadSignal      string    `json:"unreadSignal,omitempty"`
	TaskMeta          *TaskMeta `json:"taskMeta,omitempty"`
}

// OnTerminalEvent registers a listener for turn terminal transitions.
func (s *Syncer) OnTerminalEvent(fn func(TerminalEvent)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.terminalListeners = append(s.terminalListeners, fn)
}

// OnReadyEvent registers a listener for the post-terminal ready boundary.
func (s *Syncer) OnReadyEvent(fn func(ReadyEvent)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readyListeners = append(s.readyListeners, fn)
}

// OnWorkspaceEvent subscribes to a workspace's event stream. Attaching a
// listener never activates a subscription and never starts an agent: the task
// list mounts for every restored workspace.
func (s *Syncer) OnWorkspaceEvent(workspace WorkspaceTarget, fn func(WorkspaceEvent)) (detach func()) {
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		return func() {}
	}
	key := workspace.Key()
	emitter := s.emitters[key]
	if emitter == nil {
		emitter = &workspaceEmitter{}
		s.emitters[key] = emitter
	}
	s.mu.Unlock()
	return emitter.add(fn)
}

func (e *workspaceEmitter) add(fn func(WorkspaceEvent)) func() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return func() {}
	}
	e.listeners = append(e.listeners, fn)
	return func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		for i, l := range e.listeners {
			if sameFunc(l, fn) {
				e.listeners = append(e.listeners[:i], e.listeners[i+1:]...)
				return
			}
		}
	}
}

func (e *workspaceEmitter) fire(event WorkspaceEvent) {
	e.mu.Lock()
	listeners := make([]func(WorkspaceEvent), len(e.listeners))
	copy(listeners, e.listeners)
	e.mu.Unlock()
	for _, fn := range listeners {
		fn(event)
	}
}

// fireWorkspaceEvent emits on the workspace emitter (emitWorkspaceTaskListChanged).
func (s *Syncer) fireWorkspaceEvent(target WorkspaceTarget, taskID string, meta *TaskMeta, reason, unreadSignal string) {
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		return
	}
	emitter := s.emitters[target.Key()]
	s.mu.Unlock()
	if emitter == nil {
		return
	}
	emitter.fire(WorkspaceEvent{
		Type:              "workspace_task_list_changed",
		WorkspacePath:     target.WorkspacePath,
		WorkspaceIdentity: target.WorkspaceIdentity,
		TaskID:            taskID,
		Reason:            reason,
		UnreadSignal:      unreadSignal,
		TaskMeta:          meta,
	})
}

// EnsureWorkspaceSubscription ports ensureWorkspaceSubscription: idempotent per
// workspace key, and a placeholder is written first so concurrent callers do not
// subscribe twice.
func (s *Syncer) EnsureWorkspaceSubscription(ctx context.Context, target WorkspaceTarget) {
	s.mu.Lock()
	if s.disposed || target.WorkspacePath == "" {
		s.mu.Unlock()
		return
	}
	key := target.Key()
	if _, exists := s.ingests[key]; exists {
		s.mu.Unlock()
		return
	}
	state := &ingestState{
		target:            target,
		runtimeGeneration: -1,
		summaries:         map[string]SessionSummary{},
		schedule:          s.schedule,
	}
	state.runtimeGeneration = s.runtimeGenerations[key] - 1
	if gen, ok := s.runtimeGenerations[key]; ok {
		state.runtimeGeneration = gen
	}
	s.ingests[key] = state
	hasRuntime := s.runtimeGenerations != nil
	if _, ok := s.runtimeGenerations[key]; !ok {
		// A workspace with no lifecycle signal yet stays dormant: a passive
		// observer must not start a CLI.
		state.runtimeGeneration = -1
	}
	s.mu.Unlock()

	// Frame listeners are installed before any subscribe: the response promise
	// resolves before the initial notification is fired on the same read, so the
	// handler must already be able to stage frames.
	s.agent.Frames(target, func(event SessionFrameEvent) { s.onFrame(ctx, state, event) })
	if hasRuntime {
		s.mu.Lock()
		dormant := state.runtimeGeneration < 0
		s.mu.Unlock()
		if dormant {
			return
		}
	}
	go s.subscribeIndexTopic(ctx, state, "initial", false)
}

// sameFunc compares two function values by their code pointer: listeners are
// registered by identity, not by behavioural equality.
func sameFunc(a, b any) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}
