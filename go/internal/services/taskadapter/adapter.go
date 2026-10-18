package taskadapter

import (
	"context"
	"sync"
)

// adapter.go — the Adapter itself: the queue entry points the RPC channel calls, the
// send path that owns the active run, the terminal/ready edges, the event fan-out, and
// disposal.

// AgentService is the seam to the agent leg (step 3.10.9's bridge implements it). The
// adapter owns the local bookkeeping; the bridge owns the CLI.
type AgentService interface {
	// SendText submits one input. A stale baseRevision is refused by the CLI, so a
	// refused send returns an error and the command is marked failed (D5).
	SendText(ctx context.Context, target Target, command Command) error
}

// Adapter is the ported task service adapter.
type Adapter struct {
	agent   AgentService
	queue   *queue
	owner   *ownerState
	pending *pendingState
	drains  *drainer

	leases *LeaseTable
	router *CommandRouter

	mu sync.Mutex
	// emitters is taskKey → the per-task event subscribers; globalEmitters is keyed by
	// taskId alone. Every event fires BOTH (E4): the task view and the workspace list
	// subscribe through different keys, and dropping one loses events.
	emitters       map[string][]func(TaskEvent)
	globalEmitters map[string][]func(TaskEvent)
	// disposed is the A6/A7 guard: a terminal event queued as a microtask must not
	// touch released state, and a second dispose must not repeat the teardown.
	disposed bool
	// hostIDValue is this adapter's host identity in the lease table.
	hostIDValue string
	// disposeOnce makes Dispose idempotent.
	disposeOnce sync.Once
}

// TaskEvent is the adapter's stream event. Only the members this step's rules read are
// modelled; the projection itself lands with the agent bridge.
type TaskEvent struct {
	Type              string
	TaskID            string
	WorkspacePath     string
	WorkspaceIdentity string
	WorkspaceKey      string
	TraceID           string
	Reason            string
	PendingCommandIDs []string
}

// AdapterOptions configures NewAdapter.
type AdapterOptions struct {
	// Agent is the agent leg. Required.
	Agent AgentService
	// Leases and Router are the cross-host half. Leases is required when Router is set.
	Leases *LeaseTable
	Router *CommandRouter
	// Schedule defers drains off the caller's stack; tests replace it with a manual
	// trigger.
	Schedule func(fn func())
	// Now is the clock for command timestamps.
	Now func() int64
}

// NewAdapter builds the adapter.
func NewAdapter(opts AdapterOptions) *Adapter {
	if opts.Now == nil {
		opts.Now = func() int64 { return 0 }
	}
	a := &Adapter{
		agent:          opts.Agent,
		queue:          newQueue(),
		owner:          newOwnerState(),
		pending:        newPendingState(),
		leases:         opts.Leases,
		router:         opts.Router,
		emitters:       map[string][]func(TaskEvent){},
		globalEmitters: map[string][]func(TaskEvent){},
	}
	a.drains = newDrainer(a.queue, a.owner, a.sendPrompt, opts.Schedule, opts.Now)
	return a
}

// Queue exposes the host command queue for the snapshot projection (E2).
func (a *Adapter) Queue() *queue { return a.queue }

// Owner exposes the local owner-run table.
func (a *Adapter) Owner() *ownerState { return a.owner }

// Pending exposes the live pending buffers.
func (a *Adapter) Pending() *pendingState { return a.pending }

// Leases exposes the cross-host lease table.
func (a *Adapter) Leases() *LeaseTable { return a.leases }

// Router exposes the cross-host owner-command router.
func (a *Adapter) Router() *CommandRouter { return a.router }

// RememberTarget records a task target this host has loaded (A5).
func (a *Adapter) RememberTarget(target Target) { a.owner.RememberTarget(target) }

// TargetOf ports getTaskTarget (A4).
func (a *Adapter) TargetOf(taskID string) (Target, error) { return a.owner.Target(taskID) }

// SendPrompt ports the public sendPrompt entry: it resolves the stored target, merges
// an explicitly supplied remoteSessionId, and runs the send path.
func (a *Adapter) SendPrompt(ctx context.Context, taskID, remoteSessionID string, command Command) error {
	stored, err := a.owner.Target(taskID)
	if err != nil {
		return err
	}
	target := stored
	if remoteSessionID != "" {
		target.RemoteSessionID = remoteSessionID
	}
	return a.sendPrompt(ctx, target, command)
}

// sendPrompt ports sendPromptToAgent's state transitions (E1, D23).
//
// The ORDER is the whole point:
//
//  1. clear the previous run's live buffers (E1) — before anything else can observe
//     them, so no snapshot can mix two runs;
//  2. CLAIM the active run (A3's counterpart) — the claim is what makes every later
//     owner-checked command for this task be judged against THIS run;
//  3. send;
//  4. on failure, RELEASE the claim (D23). A phantom owner run would refuse every later
//     phone command for the task until the process restarted.
//
// On success the claim is NOT released: the run stays owned until the turn terminal
// (D22) or the ready boundary, which is exactly what makes an enqueue carrying a stale
// ownerRunId fail instead of writing into a live queue.
func (a *Adapter) sendPrompt(ctx context.Context, target Target, command Command) error {
	a.pending.ClearRun(target)
	a.owner.ClaimRun(target, command.TraceID)
	if err := a.agent.SendText(ctx, target, command); err != nil {
		a.owner.ReleaseRun(target)
		return err
	}
	return nil
}

// MemoryDiagnostics ports the memoryDiagnostics provider: the read-only per-table sizes
// a host reports. It is diagnostic only and must never change a decision.
func (a *Adapter) MemoryDiagnostics() map[string]int {
	a.mu.Lock()
	taskEmitters := len(a.emitters)
	a.mu.Unlock()
	stats := a.pending.Diagnostics()
	stats["runtimeCommands"] = a.queue.size()
	stats["taskEmitters"] = taskEmitters
	if a.leases != nil {
		for key, value := range a.leases.Diagnostics() {
			stats[key] = value
		}
	}
	if a.router != nil {
		stats["pendingOwnerCommands"] = a.router.PendingCount()
	}
	return stats
}

// Drain runs one drain pass for a task. Exposed because the RPC channel and the ready
// boundary both reach it, and because a test drives it manually instead of racing the
// default goroutine schedule.
func (a *Adapter) Drain(ctx context.Context, target Target) { a.drains.Drain(ctx, target) }

// EnqueueCommand ports enqueueTaskCommand (D13, D14, A1–A3).
//
// The owner check comes FIRST: a stale command must not even append, because appending
// is what would send the stale input on a shared host. The drain is scheduled after the
// append, because the drain reads the queue.
func (a *Adapter) EnqueueCommand(target Target, ownerRunID string, command Command) (Command, error) {
	if err := a.owner.AssertCurrentOwnerRun(target, ownerRunID); err != nil {
		return Command{}, err
	}
	command.WorkspaceKey = target.WorkspaceKey()
	command.TaskID = target.TaskID
	command.Type = CommandTypeSendPrompt
	command.Status = CommandAccepted
	now := a.drains.now()
	command.CreatedAt = now
	command.UpdatedAt = now
	stored := a.queue.Append(target, command)
	// 手机 replayable 的 accepted 命令必须由 host 自行消费：前端的本地 drain 会跳过
	// hostCommand，只等 host 的 snapshot。
	a.drains.Schedule(target)
	return stored, nil
}

// PromoteCommand ports promoteTaskCommand (D7–D9, A1–A3).
func (a *Adapter) PromoteCommand(target Target, ownerRunID, commandID string) (Command, error) {
	if err := a.owner.AssertCurrentOwnerRun(target, ownerRunID); err != nil {
		return Command{}, err
	}
	promoted, err := a.queue.Promote(target, commandID, a.drains.now())
	if err != nil {
		return Command{}, err
	}
	a.drains.Schedule(target)
	return promoted, nil
}

// CancelCommand ports cancelTaskCommand (D10–D12, E7, E8, A1–A3).
//
// The emission is conditional on the cancel actually removing something (E7 vs E8):
// a not_found or an already_running cancel changed nothing, so a snapshot update would
// be a lie the UI has to reconcile away.
func (a *Adapter) CancelCommand(target Target, ownerRunID, commandID string) (CancelOutcome, error) {
	if err := a.owner.AssertCurrentOwnerRun(target, ownerRunID); err != nil {
		return CancelOutcome{}, err
	}
	outcome, removed := a.queue.Cancel(target, commandID, a.drains.now())
	if removed {
		a.EmitTaskEvent(target, TaskEvent{
			Type:              "task_snapshot_updated",
			TaskID:            target.TaskID,
			WorkspacePath:     target.WorkspacePath,
			WorkspaceIdentity: target.WorkspaceIdentity,
			WorkspaceKey:      target.WorkspaceKey(),
			Reason:            "task_status_changed",
		})
	}
	return outcome, nil
}

// HandleTurnStarted ports the turn.started branch of mapServiceEvent (D21).
//
// A remote / replayable / fallback subscription is NOT established by sendPrompt, so
// there is no local claim yet. turn.started's payload.inputId is the protocol fact
// that says which run now owns the task, and the services layer must record it —
// otherwise every later owner-checked command for that task is judged against nothing.
func (a *Adapter) HandleTurnStarted(target Target, inputID string) {
	if inputID == "" {
		return
	}
	a.owner.ClaimRun(target, inputID)
}

// HandleTurnTerminal ports the turn.completed / turn.failed branch (D22) and the
// drain's "the send failed" edge.
func (a *Adapter) HandleTurnTerminal(target Target) {
	a.owner.ReleaseRun(target)
}

// HandleTerminalEvent ports handleTaskIndexTerminalEvent (D17, D20, D15, D16).
//
// Three rules in order:
//
//   - no local state at all → return (D17). A terminal event for a task this host does
//     not run must not CREATE state;
//   - close the queue entry whose traceId is the locally recorded active run (D15/D16).
//     The v4 sessions-index terminal event carries no inputId, so the LOCAL record is
//     the only thing that can close the queue;
//   - release the active run but DO NOT schedule a drain (D20). A turn terminal is not
//     the agent's active lock being released; the next command waits for the ready
//     boundary, and sending here would race the CLI's lock.
func (a *Adapter) HandleTerminalEvent(target Target, kind string) {
	if !a.owner.HasActiveRun(target) && !a.queue.present(target.TaskKey()) {
		return
	}
	terminalInputID, _ := a.owner.ActiveInput(target)
	a.queue.CompleteByInputID(target, terminalInputID)
	a.owner.ReleaseRun(target)
}

// HandleReadyEvent ports handleTaskIndexReadyEvent (D18, D19, D15).
//
// The ready boundary is `prompt_completed`/`prompt_failed`, emitted by the agent AFTER
// it released its active abort controller. It is the only signal that licenses the next
// send, which is why the drain is scheduled HERE and not on a timer: a fixed delay would
// either stall the queue or push a second input into a still-locked turn.
func (a *Adapter) HandleReadyEvent(target Target, reason string) {
	if !a.owner.HasActiveRun(target) && !a.queue.present(target.TaskKey()) {
		return
	}
	activeInputID, _ := a.owner.ActiveInput(target)
	a.queue.CompleteByInputID(target, activeInputID)
	a.owner.ReleaseRun(target)
	a.drains.Schedule(target)
}

// OnTaskEvent subscribes to one task's events (the task-keyed emitter). The returned
// function detaches the listener; the whole key's listener set is dropped, matching the
// TS Emitter's per-task disposal granularity.
func (a *Adapter) OnTaskEvent(target Target, fn func(TaskEvent)) (detach func()) {
	key := target.TaskKey()
	a.mu.Lock()
	a.emitters[key] = append(a.emitters[key], fn)
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.emitters, key)
	}
}

// OnGlobalTaskEvent subscribes to a taskId's global stream (the workspace list's view,
// which does not carry the workspace key).
func (a *Adapter) OnGlobalTaskEvent(taskID string, fn func(TaskEvent)) (detach func()) {
	a.mu.Lock()
	a.globalEmitters[taskID] = append(a.globalEmitters[taskID], fn)
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.globalEmitters, taskID)
	}
}

// EmitTaskEvent ports emitTaskEvent (E4): the event fires BOTH the task-keyed and the
// taskId-global emitter. The task view and the workspace list subscribe through
// different keys, so delivering to one and not the other loses events for half the UI.
func (a *Adapter) EmitTaskEvent(target Target, event TaskEvent) {
	event.TaskID = target.TaskID
	event.WorkspacePath = target.WorkspacePath
	event.WorkspaceIdentity = target.WorkspaceIdentity
	event.WorkspaceKey = target.WorkspaceKey()
	key := target.TaskKey()
	a.mu.Lock()
	taskListeners := append([]func(TaskEvent){}, a.emitters[key]...)
	globalListeners := append([]func(TaskEvent){}, a.globalEmitters[target.TaskID]...)
	a.mu.Unlock()
	for _, listener := range taskListeners {
		listener(event)
	}
	for _, listener := range globalListeners {
		listener(event)
	}
}

// Dispose ports disposeAllAndWait (A7, A6, B5).
//
// Order matters and matches the TS comment: the cross-host leases are released and the
// router's timers stopped BEFORE the agent legs are torn down, so a command still in
// flight cannot be routed to a host whose runtime just died. It is idempotent because
// both the host-exit path and the window-teardown path call it.
func (a *Adapter) Dispose() {
	a.disposeOnce.Do(func() {
		a.mu.Lock()
		a.disposed = true
		a.emitters = map[string][]func(TaskEvent){}
		a.globalEmitters = map[string][]func(TaskEvent){}
		hostID := a.hostIDValue
		a.mu.Unlock()
		a.drains.Dispose()
		if a.router != nil {
			a.router.Dispose()
		}
		if a.leases != nil {
			// B5: only THIS host's leases are released. Another window's leases are not
			// ours to drop — it is still running that task's runtime.
			a.leases.ReleaseHost(hostID)
		}
	})
}

// IsDisposed reports the A6/A7 state.
func (a *Adapter) IsDisposed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.disposed
}

// SetHostID names this adapter's host identity in the lease table. It is set before the
// adapter takes part in lease routing (a single-host deployment never does).
func (a *Adapter) SetHostID(hostID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hostIDValue = hostID
}

// HostID reports this adapter's host identity.
func (a *Adapter) HostID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hostIDValue
}
