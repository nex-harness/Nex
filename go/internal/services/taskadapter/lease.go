package taskadapter

import (
	"sort"
	"sync"
)

// lease.go — the task run lease table. Ports taskRealtimeBus's TaskRunLease /
// acquireLease / releaseLease / releaseLeasesForHost (guards B1–B5, B12).
//
// A lease names the Host that owns a task run: it may publish that run's mirrored
// stream, and it is the Host an owner command is routed to. Two Desktop windows can
// both have the same task open, and a phone can attach to either — without the lease,
// both would publish into the same stream and either could answer an owner command.

// Lease is TaskRunLease.
type Lease struct {
	Key               string
	WorkspaceKey      string
	WorkspacePath     string
	WorkspaceIdentity string
	TaskID            string
	RunID             string
	TraceID           string
	OwnerClientID     string
	OwnerDeviceLabel  string
	OwnerHostID       string
	AcquiredAt        int64
	UpdatedAt         int64
}

// LeaseTarget is the (workspace, task, run) a lease request names.
type LeaseTarget struct {
	WorkspaceKey      string
	WorkspacePath     string
	WorkspaceIdentity string
	TaskID            string
	RunID             string
	TraceID           string
	OwnerClientID     string
	OwnerDeviceLabel  string
	// LeaseRequestID is the correlation id echoed back in the result.
	LeaseRequestID string
}

// LeaseResult is TaskRunLeaseResult: the discriminated union of acquired / refused.
type LeaseResult struct {
	LeaseRequestID string `json:"leaseRequestId"`
	Acquired       bool   `json:"acquired"`
	OwnerHostID    string `json:"ownerHostId"`
	// Reason is "owned_by_other_host" and is set only on a refusal.
	Reason string `json:"reason,omitempty"`
}

// LeaseTable is the lease store.
type LeaseTable struct {
	mu sync.Mutex
	// leases is leaseKey(workspaceKey, taskId) → Lease. The task, not the run, is the
	// key: a run is the lease's CONTENT, and a new run must be able to take the task's
	// lease over once the old run released it.
	leases map[string]*Lease
	// flush releases a run's mirrored stream batches. The TS flushes the batch before
	// dropping the lease so buffered ops are not lost; the stream mirror itself lands
	// with the agent bridge, so this is an injected seam.
	flush func(Lease)
	now   func() int64
}

// NewLeaseTable builds an empty lease table.
func NewLeaseTable(now func() int64) *LeaseTable {
	if now == nil {
		now = func() int64 { return 0 }
	}
	return &LeaseTable{leases: map[string]*Lease{}, now: now}
}

// SetFlush installs the batch-flush seam called before a lease is dropped.
func (t *LeaseTable) SetFlush(fn func(Lease)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.flush = fn
}

// LeaseKey ports getLeaseKey.
func LeaseKey(workspaceKey, taskID string) string { return workspaceKey + "\x00" + taskID }

// batchKey ports getBatchKey: a batch belongs to one run of one task.
func batchKey(workspaceKey, taskID, runID string) string {
	return LeaseKey(workspaceKey, taskID) + "\x00" + runID
}

// BatchKey is getBatchKey, exported for the stream-mirror half.
func BatchKey(workspaceKey, taskID, runID string) string {
	return batchKey(workspaceKey, taskID, runID)
}

// Acquire ports acquireLease (B1–B3).
//
// The rule has three cases, and the third is the subtle one:
//
//   - no lease: acquire.
//   - same host AND same run: refresh in place, keeping acquiredAt (B2). A reconnect or
//     a retry must not lose the lease or reset its identity timestamp.
//   - anything else — another host, or the SAME host on a different run (B1/B3): refuse.
//
// The same-host-different-run case is why runId is part of the comparison and not just
// bookkeeping: without it a second turn on the same host would silently take over the
// first turn's lease, and the first turn's owner commands would then be routed against
// the wrong run.
func (t *LeaseTable) Acquire(hostID string, target LeaseTarget) LeaseResult {
	key := LeaseKey(target.WorkspaceKey, target.TaskID)
	t.mu.Lock()
	defer t.mu.Unlock()
	existing, held := t.leases[key]
	if held && (existing.OwnerHostID != hostID || existing.RunID != target.RunID) {
		return LeaseResult{
			LeaseRequestID: target.LeaseRequestID,
			Acquired:       false,
			OwnerHostID:    existing.OwnerHostID,
			Reason:         "owned_by_other_host",
		}
	}
	now := t.now()
	acquiredAt := now
	if held {
		acquiredAt = existing.AcquiredAt
	}
	t.leases[key] = &Lease{
		Key:               key,
		WorkspaceKey:      target.WorkspaceKey,
		WorkspacePath:     target.WorkspacePath,
		WorkspaceIdentity: target.WorkspaceIdentity,
		TaskID:            target.TaskID,
		RunID:             target.RunID,
		TraceID:           target.TraceID,
		OwnerClientID:     target.OwnerClientID,
		OwnerDeviceLabel:  target.OwnerDeviceLabel,
		OwnerHostID:       hostID,
		AcquiredAt:        acquiredAt,
		UpdatedAt:         now,
	}
	return LeaseResult{LeaseRequestID: target.LeaseRequestID, Acquired: true, OwnerHostID: hostID}
}

// Release ports releaseLease (B4). A release that does not match (hostId, runId) is
// IGNORED rather than applied: a late release from the previous run, or from a host
// that already lost the lease, must not free the current owner's record.
func (t *LeaseTable) Release(hostID string, workspaceKey, taskID, runID string) {
	key := LeaseKey(workspaceKey, taskID)
	t.mu.Lock()
	existing, held := t.leases[key]
	if !held || existing.OwnerHostID != hostID || existing.RunID != runID {
		t.mu.Unlock()
		return
	}
	delete(t.leases, key)
	flush := t.flush
	t.mu.Unlock()
	if flush != nil {
		flush(*existing)
	}
}

// ReleaseHost ports releaseLeasesForHost (B5): a window is gone, so every lease it held
// is flushed and dropped, and no other host's lease is touched. The returned leases are
// sorted by key so the caller can report them deterministically.
func (t *LeaseTable) ReleaseHost(hostID string) []Lease {
	t.mu.Lock()
	released := make([]Lease, 0)
	keys := make([]string, 0, len(t.leases))
	for key := range t.leases {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if t.leases[key].OwnerHostID != hostID {
			continue
		}
		released = append(released, *t.leases[key])
		delete(t.leases, key)
	}
	flush := t.flush
	t.mu.Unlock()
	if flush != nil {
		for _, lease := range released {
			flush(lease)
		}
	}
	return released
}

// ReleaseOnTerminal ports the terminal-event branch of handleStreamOpPublish (B12): a
// terminal stream event ends the run, so the lease is dropped without waiting for an
// explicit release. It is still ownership-checked — a non-owner publishing a terminal
// event must not be able to free someone else's lease.
func (t *LeaseTable) ReleaseOnTerminal(hostID, workspaceKey, taskID, runID string) bool {
	key := LeaseKey(workspaceKey, taskID)
	t.mu.Lock()
	existing, held := t.leases[key]
	if !held || existing.OwnerHostID != hostID || existing.RunID != runID {
		t.mu.Unlock()
		return false
	}
	delete(t.leases, key)
	t.mu.Unlock()
	return true
}

// Lookup returns the lease for a task, if any.
func (t *LeaseTable) Lookup(workspaceKey, taskID string) (Lease, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	lease, ok := t.leases[LeaseKey(workspaceKey, taskID)]
	if !ok {
		return Lease{}, false
	}
	return *lease, true
}

// BatchKeyFor is the mirrored-batch key of a lease's run: a batch belongs to one run of
// one task, and it is what Release/ReleaseHost flush before dropping the lease.
func (l Lease) BatchKeyFor() string { return batchKey(l.WorkspaceKey, l.TaskID, l.RunID) }

// OwnsPublish ports the guard in handleStreamOpPublish (B11): a mirrored op is accepted
// only from the lease's host AND only for the lease's run. Two windows both mirroring a
// run would otherwise interleave into the shared stream.
func (t *LeaseTable) OwnsPublish(hostID, workspaceKey, taskID, runID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	lease, ok := t.leases[LeaseKey(workspaceKey, taskID)]
	if !ok {
		return false
	}
	return lease.OwnerHostID == hostID && lease.RunID == runID
}

// Diagnostics ports collectMemoryDiagnostics: read-only sizes.
func (t *LeaseTable) Diagnostics() map[string]int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return map[string]int{"leases": len(t.leases)}
}
