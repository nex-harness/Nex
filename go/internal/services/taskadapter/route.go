package taskadapter

import (
	"sync"
	"time"
)

// route.go — the cross-host owner-command route. Ports
// taskRealtimeBus's handleOwnerCommandRequest / handleOwnerCommandResult (guards
// B6–B10).
//
// An owner command is a phone's intent that must be executed by the Host that owns the
// task run — the host holding the live CLI process. This table decides three things
// and refuses three others:
//
//	accept  → the lease's host, with a timeout so the requester is never left hanging;
//	refuse  → no lease (NO_ACTIVE_TASK_OWNER), a foreign run
//	           (STALE_TASK_OWNER_COMMAND), or a lease whose host is gone (also
//	           NO_ACTIVE_TASK_OWNER);
//	drop    → a result from a host that is not the pending owner.

// OwnerCommand is TaskOwnerCommandRequest: the (workspace, task, run) plus the kind of
// command. The payload is opaque here — the owner host re-validates it.
type OwnerCommand struct {
	CommandRequestID string
	WorkspacePath    string
	// WorkspaceIdentity is optional on a LOCAL task and REQUIRED on a remote one: the
	// identity is what distinguishes two workspaces that share a path.
	WorkspaceIdentity string
	WorkspaceKey      string
	TaskID            string
	// RunID is the staleness boundary of the whole owner-command path.
	RunID string
	Type  string
	// Payload carries the type-specific members verbatim (taskCommand, commandId,
	// optionId, …). It travels unchanged; the bus never interprets it.
	Payload map[string]any
}

// OwnerCommandResult is TaskOwnerCommandResult.
type OwnerCommandResult struct {
	CommandRequestID string
	Success          bool
	Error            string
	Code             string
	Result           map[string]any
}

// OwnerCommandDelivery is TaskOwnerCommandDelivery: what the owner host receives.
type OwnerCommandDelivery struct {
	Command       OwnerCommand
	RequesterHost string
}

// HostDirectory is the bus's host registry seam. `Registered` answers whether a host
// still exists, which is what makes B8 (a lease whose owner crashed) detectable.
type HostDirectory interface {
	Registered(hostID string) bool
}

// OwnerCommandTimeout is OWNER_COMMAND_TIMEOUT_MS. It exists so a requester whose owner
// host never answers gets OWNER_COMMAND_FAILED instead of hanging forever.
const OwnerCommandTimeout = 30 * time.Second

// pendingOwnerCommand is one in-flight routed command.
type pendingOwnerCommand struct {
	CommandRequestID string
	RequesterHostID  string
	OwnerHostID      string
	CreatedAt        int64
	timer            *time.Timer
	stop             func()
}

// CommandRouter routes owner commands to the lease owner and correlates their results.
type CommandRouter struct {
	leases  *LeaseTable
	hosts   HostDirectory
	now     func() int64
	timeout time.Duration

	mu sync.Mutex
	// pending is commandRequestId → the in-flight command.
	pending map[string]*pendingOwnerCommand
	// deliver sends the command to the owner host. It is the cross-host transport
	// (a stdio/MessagePort hop); without one the router still decides, it just has
	// nowhere to hand the command to.
	deliver func(OwnerCommandDelivery) error
	// reportFn posts a result back to the requester host. Every non-success outcome
	// and every completed command goes through it, so the requester has exactly one
	// channel to listen on.
	reportFn func(requesterHostID string, result OwnerCommandResult)

	stopTimer func(*time.Timer)
	afterFunc func(time.Duration, func()) *time.Timer
}

// RouterOptions configures NewCommandRouter.
type RouterOptions struct {
	Leases *LeaseTable
	Hosts  HostDirectory
	// Now is the clock.
	Now func() int64
	// Timeout defaults to OwnerCommandTimeout.
	Timeout time.Duration
	// Deliver hands the command to the owner host.
	Deliver func(OwnerCommandDelivery) error
	// Report posts a result to the requester host.
	Report func(requesterHostID string, result OwnerCommandResult)
	// AfterFunc is the timer seam, replaced by tests. Defaults to time.AfterFunc.
	AfterFunc func(time.Duration, func()) *time.Timer
}

// NewCommandRouter builds the router.
func NewCommandRouter(opts RouterOptions) *CommandRouter {
	if opts.Now == nil {
		opts.Now = func() int64 { return 0 }
	}
	if opts.Timeout == 0 {
		opts.Timeout = OwnerCommandTimeout
	}
	if opts.AfterFunc == nil {
		opts.AfterFunc = time.AfterFunc
	}
	stop := func(timer *time.Timer) {
		if timer != nil {
			timer.Stop()
		}
	}
	return &CommandRouter{
		leases:    opts.Leases,
		hosts:     opts.Hosts,
		now:       opts.Now,
		timeout:   opts.Timeout,
		pending:   map[string]*pendingOwnerCommand{},
		deliver:   opts.Deliver,
		reportFn:  opts.Report,
		afterFunc: opts.AfterFunc,
		stopTimer: stop,
	}
}

// Route ports handleOwnerCommandRequest (B6–B9). It never answers the command locally:
// every outcome — the three refusals and the timeout — is reported to the REQUESTER,
// and only a live lease owner host receives the command.
func (r *CommandRouter) Route(requesterHostID string, command OwnerCommand) error {
	lease, held := r.leases.Lookup(command.WorkspaceKey, command.TaskID)
	if !held {
		return r.refuse(requesterHostID, command, CodeNoActiveTaskOwner, "No active task owner.")
	}
	if lease.RunID != command.RunID {
		return r.refuse(requesterHostID, command, CodeStaleTaskOwnerCommand, "Stale task owner command.")
	}
	if r.hosts == nil || !r.hosts.Registered(lease.OwnerHostID) {
		// B8: the lease is a fact about a Host that no longer exists. Reporting
		// NO_ACTIVE_TASK_OWNER is the honest answer; waiting for the timeout would
		// hide the crash behind a 30 s stall.
		return r.refuse(requesterHostID, command, CodeNoActiveTaskOwner, "No active task owner.")
	}

	entry := &pendingOwnerCommand{
		CommandRequestID: command.CommandRequestID,
		RequesterHostID:  requesterHostID,
		OwnerHostID:      lease.OwnerHostID,
		CreatedAt:        r.now(),
	}
	entry.stop = func() {}
	entry.timer = r.afterFunc(r.timeout, func() { r.onTimeout(command) })
	entry.stop = func() { r.stopTimer(entry.timer) }

	r.mu.Lock()
	r.pending[command.CommandRequestID] = entry
	r.mu.Unlock()

	if r.deliver != nil {
		if err := r.deliver(OwnerCommandDelivery{Command: command, RequesterHost: requesterHostID}); err != nil {
			r.forget(command.CommandRequestID)
			return r.refuse(requesterHostID, command, CodeOwnerCommandFailed, err.Error())
		}
	}
	return nil
}

// onTimeout answers OWNER_COMMAND_FAILED and forgets the entry, so a late answer cannot
// resurrect a request the requester already saw fail (B9).
func (r *CommandRouter) onTimeout(command OwnerCommand) {
	r.mu.Lock()
	entry, ok := r.pending[command.CommandRequestID]
	if ok {
		delete(r.pending, command.CommandRequestID)
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	r.report(entry.RequesterHostID, OwnerCommandResult{
		CommandRequestID: command.CommandRequestID,
		Success:          false,
		Code:             CodeOwnerCommandFailed,
		Error:            "Owner command timed out.",
	})
}

// Complete ports handleOwnerCommandResult (B10).
//
// A result is accepted only from the host the pending entry named as its owner. A
// different host — or the requester itself — posting a result for the same
// commandRequestId is DROPPED silently: replying with an error would let any attached
// host probe the pending table, and a forged success would resolve the phone's command
// with an answer the owner never gave.
func (r *CommandRouter) Complete(ownerHostID string, result OwnerCommandResult) bool {
	r.mu.Lock()
	entry, ok := r.pending[result.CommandRequestID]
	if !ok || entry.OwnerHostID != ownerHostID {
		r.mu.Unlock()
		return false
	}
	delete(r.pending, result.CommandRequestID)
	stop := entry.stop
	r.mu.Unlock()
	stop()
	return true
}

// report is postOwnerCommandResult: the single sink every non-success outcome goes
// through, so a refusal and a timeout reach the requester by the same path.
func (r *CommandRouter) report(requesterHostID string, result OwnerCommandResult) {
	if r.reportFn != nil {
		r.reportFn(requesterHostID, result)
	}
}

// refuse reports an immediate refusal to the requester.
func (r *CommandRouter) refuse(requesterHostID string, command OwnerCommand, code, message string) error {
	r.report(requesterHostID, OwnerCommandResult{
		CommandRequestID: command.CommandRequestID,
		Success:          false,
		Code:             code,
		Error:            message,
	})
	return &OwnerError{Code: code, Message: message}
}

// forget drops a pending entry and stops its timer.
func (r *CommandRouter) forget(commandRequestID string) {
	r.mu.Lock()
	entry, ok := r.pending[commandRequestID]
	if ok {
		delete(r.pending, commandRequestID)
	}
	r.mu.Unlock()
	if ok && entry.stop != nil {
		entry.stop()
	}
}

// PendingCount is the diagnostic size of the in-flight table.
func (r *CommandRouter) PendingCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}

// Dispose stops every timer and clears the table, so a shutting-down bus leaves no
// timer that could fire into freed state.
func (r *CommandRouter) Dispose() {
	r.mu.Lock()
	entries := make([]*pendingOwnerCommand, 0, len(r.pending))
	for _, entry := range r.pending {
		entries = append(entries, entry)
	}
	r.pending = map[string]*pendingOwnerCommand{}
	r.mu.Unlock()
	for _, entry := range entries {
		if entry.stop != nil {
			entry.stop()
		}
	}
}
