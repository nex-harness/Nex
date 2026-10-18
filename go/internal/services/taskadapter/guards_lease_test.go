package taskadapter

import (
	"testing"
	"time"
)

// guards_lease_test.go — rows B1–B12 of phase-3-guards.md: the CROSS-HOST lease and
// the owner-command route.

func leaseTarget(runID string) LeaseTarget {
	return LeaseTarget{
		WorkspaceKey:     "/repo",
		WorkspacePath:    "/repo",
		TaskID:           "task-1",
		RunID:            runID,
		TraceID:          "trace-" + runID,
		OwnerClientID:    "client-1",
		OwnerDeviceLabel: "Pixel",
		LeaseRequestID:   "req-" + runID,
	}
}

// B1: another Host cannot take a lease the current owner holds.
func TestLease_HandoverRefusesOtherHost(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 100 })

	first := leases.Acquire("host-a", leaseTarget("run-A"))
	if !first.Acquired || first.OwnerHostID != "host-a" {
		t.Fatalf("the first acquire must succeed, got %+v", first)
	}

	// Two Desktop windows, one task: host B asks for the same task.
	second := leases.Acquire("host-b", leaseTarget("run-A"))
	if second.Acquired {
		t.Fatal("B1: a second host must not acquire a held lease")
	}
	if second.Reason != "owned_by_other_host" || second.OwnerHostID != "host-a" {
		t.Fatalf("the refusal must name the current owner, got %+v", second)
	}

	// And host B must not be able to publish into host A's run.
	if leases.OwnsPublish("host-b", "/repo", "task-1", "run-A") {
		t.Fatal("B11: a non-owner must not publish")
	}
}

// B1b: after host A releases, host B CAN take over — the refusal is about a held lease,
// not about the task itself.
func TestLease_HandoverSucceedsAfterRelease(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 100 })
	leases.Acquire("host-a", leaseTarget("run-A"))
	leases.Release("host-a", "/repo", "task-1", "run-A")

	taken := leases.Acquire("host-b", leaseTarget("run-B"))
	if !taken.Acquired || taken.OwnerHostID != "host-b" {
		t.Fatalf("the handover must succeed after a release, got %+v", taken)
	}
}

// B2: the same host + same run refreshes in place and keeps acquiredAt. A reconnect must
// not lose the lease or reset its identity timestamp.
func TestLease_ReacquireSameRunKeepsAcquiredAt(t *testing.T) {
	var clock int64 = 100
	leases := NewLeaseTable(func() int64 { return clock })
	leases.Acquire("host-a", leaseTarget("run-A"))
	clock = 500

	again := leases.Acquire("host-a", leaseTarget("run-A"))
	if !again.Acquired {
		t.Fatal("the owning host re-acquiring its own run must succeed")
	}
	lease, _ := leases.Lookup("/repo", "task-1")
	if lease.AcquiredAt != 100 {
		t.Fatalf("acquiredAt must be preserved across a re-acquire, got %d", lease.AcquiredAt)
	}
	if lease.UpdatedAt != 500 {
		t.Fatalf("updatedAt must advance, got %d", lease.UpdatedAt)
	}
	if got := leases.Diagnostics()["leases"]; got != 1 {
		t.Fatalf("a re-acquire must not add a second lease, got %d", got)
	}
}

// B3: the SAME host on a different run is refused — runId is part of ownership, not
// bookkeeping. Without this a second turn would take over the first turn's record.
func TestLease_NewRunOnSameHostRefused(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 100 })
	leases.Acquire("host-a", leaseTarget("run-A"))

	next := leases.Acquire("host-a", leaseTarget("run-B"))
	if next.Acquired {
		t.Fatal("B3: a new run must not take over the previous run's lease")
	}
	lease, _ := leases.Lookup("/repo", "task-1")
	if lease.RunID != "run-A" {
		t.Fatalf("the lease must still name the original run, got %q", lease.RunID)
	}
}

// B4: a release that does not match (hostId, runId) is ignored, so a late release from
// the previous run cannot free the CURRENT owner's lease.
func TestLease_ReleaseByNonOwnerIgnored(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 100 })
	leases.Acquire("host-a", leaseTarget("run-A"))

	// The previous run's late release.
	leases.Release("host-a", "/repo", "task-1", "run-OLD")
	// A host that never owned it.
	leases.Release("host-b", "/repo", "task-1", "run-A")

	lease, held := leases.Lookup("/repo", "task-1")
	if !held {
		t.Fatal("B4: a non-matching release must not free the lease")
	}
	if lease.RunID != "run-A" {
		t.Fatalf("the lease must survive, got run %q", lease.RunID)
	}
}

// B5: a window is gone, so every lease it held is dropped and no other host's lease is
// touched.
func TestLease_DisposeReleasesOnlyOwnLeases(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 100 })
	leases.Acquire("host-a", leaseTarget("run-A"))
	other := LeaseTarget{WorkspaceKey: "/repo2", TaskID: "task-2", RunID: "run-X", LeaseRequestID: "r"}
	leases.Acquire("host-b", other)
	leases.Acquire("host-a", LeaseTarget{WorkspaceKey: "/repo3", TaskID: "task-3", RunID: "run-Y", LeaseRequestID: "r3"})

	released := leases.ReleaseHost("host-a")
	if len(released) != 2 {
		t.Fatalf("want 2 released leases, got %d", len(released))
	}
	if _, held := leases.Lookup("/repo", "task-1"); held {
		t.Fatal("host-a's lease must be gone")
	}
	if _, held := leases.Lookup("/repo3", "task-3"); held {
		t.Fatal("host-a's second lease must be gone")
	}
	if _, held := leases.Lookup("/repo2", "task-2"); !held {
		t.Fatal("B5: host-b's lease must survive")
	}
}

// B5b: a window close FLUSHES the run's mirrored batch before dropping the lease, so
// buffered ops are not lost with the lease. The batch key names one run of one task.
func TestLease_ReleaseFlushesBatch(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 100 })
	var flushed []string
	leases.SetFlush(func(lease Lease) { flushed = append(flushed, lease.BatchKeyFor()) })
	leases.Acquire("host-a", leaseTarget("run-A"))
	other := LeaseTarget{WorkspaceKey: "/repo2", TaskID: "task-2", RunID: "run-X", LeaseRequestID: "r"}
	leases.Acquire("host-b", other)

	released := leases.ReleaseHost("host-a")
	if len(released) != 1 {
		t.Fatalf("want 1 released lease, got %d", len(released))
	}
	if len(flushed) != 1 {
		t.Fatalf("a released lease must flush its batch, flushed %v", flushed)
	}
	if flushed[0] != batchKey("/repo", "task-1", "run-A") {
		t.Fatalf("the flushed batch must be this run's, got %q", flushed[0])
	}
	// A non-matching release flushes nothing.
	leases.Release("host-b", "/repo", "task-1", "run-OTHER")
	if len(flushed) != 1 {
		t.Fatalf("a non-matching release must not flush, flushed %v", flushed)
	}
}

// B6: no lease at all → the requester is answered NO_ACTIVE_TASK_OWNER and nothing is
// delivered to any host.
func TestRoute_NoLeaseRefusesRequester(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 100 })
	hosts := newFakeHosts("host-a", "host-phone")
	router, results := newRouter(t, leases, hosts)

	err := router.Route("host-phone", ownerCommand("req-1", "run-A"))
	if got := OwnerErrorCode(err); got != CodeNoActiveTaskOwner {
		t.Fatalf("want %s, got %q", CodeNoActiveTaskOwner, got)
	}
	if got := router.PendingCount(); got != 0 {
		t.Fatalf("a refused command must not become pending, got %d", got)
	}
	if len(results.all()) != 1 || results.all()[0].Code != CodeNoActiveTaskOwner {
		t.Fatalf("the requester must be told, got %+v", results.all())
	}
}

// B7: the cross-host staleness boundary. A command naming a run the lease does not hold
// is refused and nothing is delivered to the owner host.
func TestRoute_StaleRunRefused(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 100 })
	leases.Acquire("host-a", leaseTarget("run-B"))
	hosts := newFakeHosts("host-a", "host-phone")
	router, results := newRouter(t, leases, hosts)

	err := router.Route("host-phone", ownerCommand("req-1", "run-A"))
	if got := OwnerErrorCode(err); got != CodeStaleTaskOwnerCommand {
		t.Fatalf("want %s, got %q (err=%v)", CodeStaleTaskOwnerCommand, got, err)
	}
	if got := router.PendingCount(); got != 0 {
		t.Fatalf("a stale command must not become pending, got %d", got)
	}
	if got := results.all(); len(got) != 1 || got[0].Code != CodeStaleTaskOwnerCommand {
		t.Fatalf("the requester must be told STALE, got %+v", got)
	}
}

// B7b: the matching run IS delivered to the lease owner, and to nobody else. Two hosts,
// one task, correct routing.
func TestRoute_OwnerRunReachesLeaseOwner(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 100 })
	leases.Acquire("host-a", leaseTarget("run-A"))
	hosts := newFakeHosts("host-a", "host-phone")
	router, _ := newRouter(t, leases, hosts)

	if err := router.Route("host-phone", ownerCommand("req-1", "run-A")); err != nil {
		t.Fatalf("the matching run must route: %v", err)
	}
	if got := router.PendingCount(); got != 1 {
		t.Fatalf("the routed command must be pending, got %d", got)
	}
	if !router.Complete("host-a", OwnerCommandResult{CommandRequestID: "req-1", Success: true}) {
		t.Fatal("the lease owner must be able to answer its own command")
	}
	if got := router.PendingCount(); got != 0 {
		t.Fatalf("the answered command must be cleared, got %d", got)
	}
}

// B8: the lease names a host that no longer exists. Reporting NO_ACTIVE_TASK_OWNER is
// the honest answer; waiting out the timeout would hide the crash behind a 30 s stall.
func TestRoute_DeadOwnerHostRefuses(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 100 })
	leases.Acquire("host-a", leaseTarget("run-A"))
	hosts := newFakeHosts("host-a", "host-phone")
	router, results := newRouter(t, leases, hosts)

	// The owning window crashed between acquire and command.
	hosts.kill("host-a")

	err := router.Route("host-phone", ownerCommand("req-1", "run-A"))
	if got := OwnerErrorCode(err); got != CodeNoActiveTaskOwner {
		t.Fatalf("want %s, got %q (err=%v)", CodeNoActiveTaskOwner, got, err)
	}
	if got := results.all(); len(got) != 1 || got[0].Code != CodeNoActiveTaskOwner {
		t.Fatalf("the requester must be told immediately, got %+v", got)
	}
}

// B9: an owner host that never answers must not leave the requester hanging.
func TestRoute_OwnerTimeoutFails(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 100 })
	leases.Acquire("host-a", leaseTarget("run-A"))
	hosts := newFakeHosts("host-a", "host-phone")
	clock := newFakeClock()
	router, results := newRouterWithClock(t, leases, hosts, clock)

	if err := router.Route("host-phone", ownerCommand("req-1", "run-A")); err != nil {
		t.Fatalf("route: %v", err)
	}
	clock.fire(10 * int64(time.Second))

	got := results.all()
	if len(got) != 1 || got[0].Code != CodeOwnerCommandFailed {
		t.Fatalf("the timeout must answer OWNER_COMMAND_FAILED, got %+v", got)
	}
	if router.PendingCount() != 0 {
		t.Fatal("a timed-out command must be cleared, so a late answer cannot resurrect it")
	}
	if router.Complete("host-a", OwnerCommandResult{CommandRequestID: "req-1", Success: true}) {
		t.Fatal("a late answer to a timed-out command must be dropped")
	}
}

// B10: only the pending OWNER's result is accepted. A result from any other host — or
// from the requester itself — is dropped, because a forged success would resolve the
// phone's command with an answer the owner never gave.
func TestRoute_ForeignResultDropped(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 100 })
	leases.Acquire("host-a", leaseTarget("run-A"))
	hosts := newFakeHosts("host-a", "host-b", "host-phone")
	router, _ := newRouter(t, leases, hosts)
	if err := router.Route("host-phone", ownerCommand("req-1", "run-A")); err != nil {
		t.Fatalf("route: %v", err)
	}

	for _, foreign := range []string{"host-b", "host-phone"} {
		if router.Complete(foreign, OwnerCommandResult{CommandRequestID: "req-1", Success: true}) {
			t.Fatalf("%s is not the pending owner; its result must be dropped", foreign)
		}
	}
	if got := router.PendingCount(); got != 1 {
		t.Fatalf("a dropped result must not clear the pending entry, got %d", got)
	}
	if !router.Complete("host-a", OwnerCommandResult{CommandRequestID: "req-1", Success: true}) {
		t.Fatal("the owner must still be able to answer")
	}
}

// B11: a mirrored stream op is accepted only from the lease owner, and only for the
// lease's run.
func TestStreamMirror_NonOwnerOpDropped(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 100 })
	leases.Acquire("host-a", leaseTarget("run-A"))

	if leases.OwnsPublish("host-a", "/repo", "task-1", "run-A") != true {
		t.Fatal("the owner must be allowed to publish its own run")
	}
	if leases.OwnsPublish("host-b", "/repo", "task-1", "run-A") {
		t.Fatal("B11: another host must not publish into this run")
	}
	if leases.OwnsPublish("host-a", "/repo", "task-1", "run-OTHER") {
		t.Fatal("B11: a different run of the same host must not publish")
	}
	if leases.OwnsPublish("host-a", "/repo", "no-lease", "run-A") {
		t.Fatal("B11: a task with no lease must not publish")
	}
}

// B12: a terminal stream event releases the lease — but only when it came from the
// owner. A non-owner's terminal event must not be able to free someone else's lease.
func TestStreamMirror_TerminalEventReleasesLease(t *testing.T) {
	leases := NewLeaseTable(func() int64 { return 100 })
	leases.Acquire("host-a", leaseTarget("run-A"))

	if leases.ReleaseOnTerminal("host-b", "/repo", "task-1", "run-A") {
		t.Fatal("a non-owner must not release the lease")
	}
	if _, held := leases.Lookup("/repo", "task-1"); !held {
		t.Fatal("the lease must survive a non-owner's terminal event")
	}
	if !leases.ReleaseOnTerminal("host-a", "/repo", "task-1", "run-A") {
		t.Fatal("the owner's terminal event must release the lease")
	}
	if _, held := leases.Lookup("/repo", "task-1"); held {
		t.Fatal("the lease must be gone after the owner's terminal event")
	}
	// And the next run can acquire immediately.
	if !leases.Acquire("host-b", leaseTarget("run-B")).Acquired {
		t.Fatal("the next run must be able to acquire the released task")
	}
}
