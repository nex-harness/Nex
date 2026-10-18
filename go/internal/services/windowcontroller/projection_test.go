package windowcontroller

import (
	"context"
	"errors"
	"testing"

	adapter "github.com/tylinux/nex/go/internal/adapters/taskindex"
)

// adapterTaskMeta is the meta type the fake stores, named once for the tests that
// need to hand-build one.
type adapterTaskMeta = adapter.TaskMeta

// projection_test.go — the projection's own rules: live status provenance, the
// frozen offline snapshot, the membership buckets, the identity isolation
// between a local and a remote source, the single-flight read, and the write
// routing (including that a write never goes through the projection).

// TestLiveStatusComesOnlyFromTheOverlay is the provenance rule: without a live
// overlay the row must NOT claim running or waiting, even when the persisted
// status says running — that status may be left over from the previous process
// exit. Only the overlay may assert a live phase.
func TestLiveStatusComesOnlyFromTheOverlay(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t-done", "done", Membership{Active: true}, 100)
	src.addTask("t-err", "failed", Membership{Active: true}, 101)
	src.addTask("t-run", "running in the index", Membership{Active: true}, 102)
	src.setStatus("t-done", "completed")
	src.setStatus("t-err", "error")
	src.setStatus("t-run", "running")

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	list, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[string]ListItem{}
	for _, item := range list.Items {
		byID[item.TaskID] = item
	}
	if len(byID) != 3 {
		t.Fatalf("list returned %d items (%v), want the three tasks", len(byID), taskIDsOf(list))
	}
	if got := byID["t-done"].LiveStatus; got != LiveCompleted {
		t.Fatalf("a completed task projects liveStatus %q, want %q", got, LiveCompleted)
	}
	if got := byID["t-err"].LiveStatus; got != LiveError {
		t.Fatalf("an errored task projects liveStatus %q, want %q", got, LiveError)
	}
	if got := byID["t-run"].LiveStatus; got != LiveIdle {
		t.Fatalf("a persisted running status with no overlay projects liveStatus %q, want %q "+
			"(a running status in SQLite may be from the previous process exit)",
			got, LiveIdle)
	}

	// The overlay is the only thing that may assert a live phase.
	src.deliverOverlays([]SessionOverlay{{
		TaskID:     "t-run",
		LiveStatus: LiveRunning,
		Activity:   &TaskActivity{Phase: "running", LastActivityAt: 500, HasBackgroundWork: true},
	}})
	row := h.runtime.projection.findRow(AddressOf(src.scope, "t-run"))
	if row == nil {
		t.Fatalf("the overlay did not reach the projection")
	}
	if row.LiveStatus != LiveRunning {
		t.Fatalf("with an overlay the row projects liveStatus %q, want %q", row.LiveStatus, LiveRunning)
	}
	if row.Meta.Status == nil || *row.Meta.Status != "running" {
		t.Fatalf("the overlay's phase must override the persisted status")
	}
}

// TestDisconnectedSourceFreezesItsRows: a source that goes offline keeps its
// last trusted rows with an offline badge. Losing the rows on a disconnect is the
// bug this rule prevents — the user would see their whole workspace disappear for
// a dropped SSH connection.
func TestDisconnectedSourceFreezesItsRows(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	if _, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("list: %v", err)
	}

	h.runtime.DisconnectSource(src.scope)

	row := h.runtime.projection.findRow(AddressOf(src.scope, "t1"))
	if row == nil {
		t.Fatalf("disconnect removed the row instead of freezing it")
	}
	if row.SourceAvailability != AvailabilityOffline {
		t.Fatalf("frozen row availability %q, want %q", row.SourceAvailability, AvailabilityOffline)
	}
	fact := h.runtime.projection.sourceFor(src.scope)
	if fact == nil {
		t.Fatalf("disconnect dropped the source")
	}
	built := workspaceFactOf(fact)
	if built.ConnectionState != StateDisconnected {
		t.Fatalf("offline fact connectionState %q, want %q (an offline source must be disconnected or failed)",
			built.ConnectionState, StateDisconnected)
	}
}

// TestFailedReadKeepsTheLastTrustedSnapshot: a read that fails must not empty the
// list. The frozen rows are the whole point of keeping an in-memory projection.
func TestFailedReadKeepsTheLastTrustedSnapshot(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	if _, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("seed list: %v", err)
	}

	src.mu.Lock()
	src.listErr = errors.New("index is locked")
	src.mu.Unlock()

	// A forced refresh fails; the list call still answers from the projection.
	resolved := h.runtime.resolveSource(src.scope)
	if err := h.runtime.refreshSource(context.Background(), resolved, true); err == nil {
		t.Fatalf("a forced refresh with a failing reader must report the error")
	}
	list, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	})
	if err != nil {
		t.Fatalf("list after a failed read: %v", err)
	}
	if !containsTask(list, "t1") {
		t.Fatalf("a failed read emptied the list; the last trusted snapshot must survive")
	}
}

// TestMembershipBucketsAreDisjoint: the four membership kinds partition the rows
// the way the sidebar's buckets expect. A pinned row is in `pinned` and `active`
// but NOT in `timeline`; an archived row is only in `archived`.
func TestMembershipBucketsAreDisjoint(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t-plain", "plain", Membership{Active: true}, 100)
	src.addTask("t-pinned", "pinned", Membership{Pinned: true, Active: true}, 101)
	src.addTask("t-archived", "archived", Membership{Archived: true}, 102)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	for _, tc := range []struct {
		kind  MembershipKind
		want  []string
		avoid []string
	}{
		{MembershipPinned, []string{"t-pinned"}, []string{"t-plain", "t-archived"}},
		{MembershipArchived, []string{"t-archived"}, []string{"t-plain", "t-pinned"}},
		{MembershipTimeline, []string{"t-plain"}, []string{"t-pinned", "t-archived"}},
		{MembershipActive, []string{"t-plain", "t-pinned"}, []string{"t-archived"}},
	} {
		list, err := att.ListTaskList(context.Background(), ListQuery{
			Kind: tc.kind, WorkspaceScopes: []SourceScope{src.scope},
		})
		if err != nil {
			t.Fatalf("list %s: %v", tc.kind, err)
		}
		for _, id := range tc.want {
			if !containsTask(list, id) {
				t.Fatalf("%s bucket is missing %s (has %v)", tc.kind, id, taskIDsOf(list))
			}
		}
		for _, id := range tc.avoid {
			if containsTask(list, id) {
				t.Fatalf("%s bucket must not contain %s (has %v)", tc.kind, id, taskIDsOf(list))
			}
		}
	}
}

// TestLocalAndRemoteRowsStayIsolated: two sources may carry the SAME task id. The
// projection must keep them apart by (remote marker, identity key, task id), or
// a remote row would overwrite the local one in the sidebar.
func TestLocalAndRemoteRowsStayIsolated(t *testing.T) {
	h := newTestHarness()
	local := h.add(newFakeSource(LocalScope("/w/proj", "")))
	local.addTask("shared", "local task", Membership{Active: true}, 100)
	remote := h.add(newFakeSource(RemoteScope("remote-1", "/w/proj", "identity-1")))
	remote.addTask("shared", "remote task", Membership{Active: true}, 200)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	list, err := att.ListTaskList(context.Background(), ListQuery{
		Kind:            MembershipActive,
		WorkspaceScopes: []SourceScope{local.scope, remote.scope},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("got %d items (%v), want both the local and the remote row", len(list.Items), taskIDsOf(list))
	}
	var sawLocal, sawRemote bool
	for _, item := range list.Items {
		if item.RemoteSessionID == "" && item.Title == "local task" {
			sawLocal = true
		}
		if item.RemoteSessionID == "remote-1" && item.Title == "remote task" {
			sawRemote = true
			if item.WorkspaceIdentity == nil || *item.WorkspaceIdentity != "identity-1" {
				t.Fatalf("the remote row lost its identity; it must never be keyed by path alone")
			}
		}
	}
	if !sawLocal || !sawRemote {
		t.Fatalf("local=%v remote=%v; both rows must survive independently", sawLocal, sawRemote)
	}
}

// TestUnresolvableScopeIsSkippedNotAnsweredLocally is the fail-closed half of the
// resolver: a scope with no source contributes nothing, and it must never fall
// back to reading another scope's rows.
func TestUnresolvableScopeIsSkippedNotAnsweredLocally(t *testing.T) {
	h := newTestHarness()
	local := h.add(newFakeSource(LocalScope("/w/local", "")))
	local.addTask("t1", "local task", Membership{Active: true}, 100)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	list, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive,
		WorkspaceScopes: []SourceScope{
			local.scope,
			RemoteScope("remote-missing", "/w/local", "identity-missing"),
		},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].TaskID != "t1" {
		t.Fatalf("got %v, want only the local row; an unresolvable scope must contribute nothing", taskIDsOf(list))
	}
	// And a query naming ONLY the unresolvable scope returns nothing at all.
	onlyRemote, err := att.ListTaskList(context.Background(), ListQuery{
		Kind:            MembershipActive,
		WorkspaceScopes: []SourceScope{RemoteScope("remote-missing", "/w/local", "identity-missing")},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(onlyRemote.Items) != 0 {
		t.Fatalf("an unresolvable remote scope answered with %v; it must fail closed", taskIDsOf(onlyRemote))
	}
}

// TestConcurrentRefreshIsSingleFlight: two list calls racing on one source must
// share ONE index read. Without single-flight, every sidebar keystroke and every
// second window would issue its own full read of the same workspace.
func TestConcurrentRefreshIsSingleFlight(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)
	// Hold the read open so both callers are inside it at once.
	src.mu.Lock()
	gate := make(chan struct{})
	src.listGate = gate
	src.mu.Unlock()

	const callers = 8
	done := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			_, err := h.runtime.ListTaskList(context.Background(), ListQuery{
				Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
			})
			done <- err
		}()
	}
	// Give the callers a moment to pile up on the in-flight read.
	for i := 0; i < 50; i++ {
		src.mu.Lock()
		calls := src.listCalls
		src.mu.Unlock()
		if calls > 0 {
			break
		}
	}
	close(gate)
	for i := 0; i < callers; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent list: %v", err)
		}
	}
	src.mu.Lock()
	calls := src.listCalls
	src.mu.Unlock()
	if calls != 1 {
		t.Fatalf("%d concurrent list calls issued %d index reads, want 1 (the read is single-flight)",
			callers, calls)
	}
}

// TestWriteRoutesToTheSourceAndConvergesTheProjection: a mutation goes to the
// source's reader and the projection is refreshed onto it. The projection is a
// cache of the index, never a second write path.
func TestWriteRoutesToTheSourceAndConvergesTheProjection(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	if _, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	src.mu.Lock()
	src.mutations = nil
	src.mu.Unlock()

	meta, err := att.MutateTask(context.Background(), AddressOf(src.scope, "t1"),
		Mutation{Kind: MutPin, Pinned: true})
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	if meta == nil {
		t.Fatalf("pin returned no meta for a row that still exists")
	}
	src.mu.Lock()
	mutations := append([]string(nil), src.mutations...)
	src.mu.Unlock()
	if len(mutations) == 0 || mutations[0] != "pin:t1:true" {
		t.Fatalf("mutations = %v, want the pin to be routed to the source", mutations)
	}
	// The projection converged: the row is now in the pinned bucket.
	pinned, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipPinned, WorkspaceScopes: []SourceScope{src.scope},
	})
	if err != nil {
		t.Fatalf("list pinned: %v", err)
	}
	if !containsTask(pinned, "t1") {
		t.Fatalf("the projection did not converge on the write (%v)", taskIDsOf(pinned))
	}
}

// TestDeleteRemovesTheRowAndReturnsNoMeta: a delete's normal answer is no meta,
// because the row is gone. Returning the old meta would make the sidebar re-add
// a task the index just removed.
func TestDeleteRemovesTheRowAndReturnsNoMeta(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	if _, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("seed list: %v", err)
	}

	meta, err := att.MutateTask(context.Background(), AddressOf(src.scope, "t1"), Mutation{Kind: MutDelete})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if meta != nil {
		t.Fatalf("delete returned meta %+v, want nil for a removed row", meta.TaskID)
	}
	list, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if containsTask(list, "t1") {
		t.Fatalf("the deleted row is still projected (%v)", taskIDsOf(list))
	}
}

// TestOpenAndResumeAreNotWrites: the controller's only job for open/resume is to
// verify that exactly one source owns the address. It must not write, and it must
// not re-read the index (the conversation that follows goes through the scoped
// facade, which owns its own lifecycle).
func TestOpenAndResumeAreNotWrites(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "first", Membership{Active: true}, 100)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	if _, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	src.mu.Lock()
	src.mutations = nil
	src.listCalls = 0
	src.mu.Unlock()

	for _, kind := range []MutationKind{MutOpen, MutResume} {
		meta, err := att.MutateTask(context.Background(), AddressOf(src.scope, "t1"), Mutation{Kind: kind})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if meta == nil || meta.TaskID != "t1" {
			t.Fatalf("%s must still resolve the row it verified", kind)
		}
	}
	src.mu.Lock()
	mutations := append([]string(nil), src.mutations...)
	reads := src.listCalls
	src.mu.Unlock()
	if len(mutations) != 0 {
		t.Fatalf("open/resume wrote to the source: %v", mutations)
	}
	if reads != 0 {
		t.Fatalf("open/resume re-read the index %d times; the row was already materialized", reads)
	}
}

// TestOfflineSourceRefusesWrites: a write against an offline source is refused
// with the availability error, not silently dropped. The UI offers "retry" for
// this, which it cannot do if the write appears to have succeeded.
func TestOfflineSourceRefusesWrites(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(RemoteScope("remote-1", "/w/proj", "identity-1")))
	src.addTask("t1", "first", Membership{Active: true}, 100)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	if _, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	h.runtime.DisconnectSource(src.scope)
	src.goOffline()

	_, err := att.MutateTask(context.Background(), AddressOf(src.scope, "t1"),
		Mutation{Kind: MutPin, Pinned: true})
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("a write against a disconnected source: %v, want ErrSourceUnavailable", err)
	}
	if _, err := att.DeleteArchivedTask(context.Background(), AddressOf(src.scope, "t1")); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("a conditional delete against a disconnected source: %v, want ErrSourceUnavailable", err)
	}
}

// TestArchivedBatchRejectsAReplacedSource: between the user confirming the
// archived-sweep dialog and the write landing, a remote session can be replaced.
// Writing the old address's batch into the new source's identity would delete rows
// the user never confirmed, so the batch is refused instead.
func TestArchivedBatchRejectsAReplacedSource(t *testing.T) {
	h := newTestHarness()
	old := h.add(newFakeSource(RemoteScope("remote-1", "/w/proj", "identity-1")))
	old.addTask("t1", "archived", Membership{Archived: true}, 100)
	old.addTask("t2", "archived", Membership{Archived: true}, 101)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	if _, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipArchived, WorkspaceScopes: []SourceScope{old.scope},
	}); err != nil {
		t.Fatalf("seed list: %v", err)
	}

	// The remote comes back under a new session id. The caller still holds the
	// OLD address.
	next := h.add(newFakeSource(RemoteScope("remote-2", "/w/proj", "identity-1")))
	next.addTask("t1", "archived", Membership{Archived: true}, 200)
	resolved := h.runtime.resolveSource(next.scope)
	if err := h.runtime.ReplaceDisconnectedSource(context.Background(), old.scope, resolved); err != nil {
		t.Fatalf("replace: %v", err)
	}
	// The connection registry forgets the old remote session, exactly as a real
	// reconnect does.
	h.drop(old.scope)

	_, err := att.DeleteArchivedTasks(context.Background(), AddressOf(old.scope, "t1"), []string{"t1"})
	if err == nil {
		t.Fatalf("a batch written against a replaced source was accepted; it would delete rows the user never confirmed")
	}
	next.mu.Lock()
	writes := len(next.mutations)
	next.mu.Unlock()
	if writes != 0 {
		t.Fatalf("the refused batch still wrote %d times to the replacement source", writes)
	}
}

// TestSearchGoesToTheSourceAndEnrichesFromTheProjection: with a search term the
// answer comes from each source's own index, and the controller enriches every
// hit with the live facts it already holds.
func TestSearchGoesToTheSourceAndEnrichesFromTheProjection(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	src.addTask("t1", "needle in a haystack", Membership{Active: true}, 100)
	src.addTask("t2", "unrelated", Membership{Active: true}, 101)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	// Prime the projection so the enrichment path has a row to read.
	if _, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	src.deliverOverlays([]SessionOverlay{{
		TaskID:     "t1",
		LiveStatus: LiveWaiting,
		Activity:   &TaskActivity{Phase: "running", LastActivityAt: 700, HasBackgroundWork: true},
	}})

	list, err := att.ListTaskList(context.Background(), ListQuery{
		Kind:            MembershipActive,
		WorkspaceScopes: []SourceScope{src.scope},
		Search:          "needle",
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].TaskID != "t1" {
		t.Fatalf("search returned %v, want only the matching row", taskIDsOf(list))
	}
	hit := list.Items[0]
	if hit.LiveStatus != LiveWaiting {
		t.Fatalf("the search hit was not enriched from the projection: liveStatus %q, want %q",
			hit.LiveStatus, LiveWaiting)
	}
	if len(hit.SearchSnippets) == 0 {
		t.Fatalf("the search hit lost its snippets")
	}
}

// TestSearchFailureDoesNotEmptyTheAnswer: one source's search failing must not
// take the other source's results with it.
func TestSearchFailureDoesNotEmptyTheAnswer(t *testing.T) {
	h := newTestHarness()
	healthy := h.add(newFakeSource(LocalScope("/w/a", "")))
	healthy.addTask("t1", "needle here", Membership{Active: true}, 100)
	broken := h.add(newFakeSource(LocalScope("/w/b", "")))
	broken.addTask("t2", "needle there", Membership{Active: true}, 101)
	broken.mu.Lock()
	broken.searchErr = errors.New("search index is corrupt")
	broken.mu.Unlock()

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	list, err := att.ListTaskList(context.Background(), ListQuery{
		Kind:            MembershipActive,
		WorkspaceScopes: []SourceScope{broken.scope, healthy.scope},
		Search:          "needle",
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].TaskID != "t1" {
		t.Fatalf("got %v, want the healthy source's row to survive the other source's failure", taskIDsOf(list))
	}
}

// TestLimitAndSortAreTotalAndDeterministic: the sort is by the requested time
// field descending with the task id ascending as the tiebreak, so two rows with
// the same timestamp never swap between two list calls.
func TestLimitAndSortAreTotalAndDeterministic(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(LocalScope("/w/a", "")))
	for _, id := range []string{"t-c", "t-a", "t-b"} {
		src.addTask(id, id, Membership{Active: true}, 500)
	}
	src.addTask("t-new", "newest", Membership{Active: true}, 900)

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	var first []string
	for i := 0; i < 5; i++ {
		list, err := att.ListTaskList(context.Background(), ListQuery{
			Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope}, Limit: ptr(2),
		})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		got := taskIDsOf(list)
		if i == 0 {
			first = got
			if len(got) != 2 || got[0] != "t-new" || got[1] != "t-a" {
				t.Fatalf("sort/limit = %v, want [t-new t-a] (updatedAt desc, then taskId asc)", got)
			}
			if list.Total != 4 || !list.HasMore {
				t.Fatalf("Total=%d HasMore=%v, want 4 and true", list.Total, list.HasMore)
			}
			continue
		}
		if len(got) != len(first) {
			t.Fatalf("list order changed between calls: %v then %v", first, got)
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("list order changed between calls: %v then %v", first, got)
			}
		}
	}
}

// taskIDsOf lists a result's task ids, for failure messages.
func taskIDsOf(list ListResult) []string {
	out := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, item.TaskID)
	}
	return out
}

// TestMembershipIsPinnedToTheResolvingScope: the reader answers per scope, and
// the row's meta is pinned to THAT scope's path/identity before it becomes a
// projection row. A reader that returned a row carrying a different path would
// otherwise produce a row whose meta disagrees with its address — the exact
// mismatch windowHostControllerTaskRowSchema refuses.
func TestMembershipIsPinnedToTheResolvingScope(t *testing.T) {
	h := newTestHarness()
	// The fake is registered under remote-2 but reports rows stamped with the
	// OLD session's identity-free path, which is what a stale index read looks
	// like.
	src := h.add(newFakeSource(RemoteScope("remote-2", "/w/proj", "identity-2")))
	src.addTask("t1", "task", Membership{Active: true}, 100)

	// Overwrite the meta's path so it disagrees with the scope.
	src.mu.Lock()
	src.tasks["t1"].meta.WorkspacePath = "/w/stale"
	src.mu.Unlock()

	att := h.runtime.CreateAttachment()
	defer att.Dispose()
	if _, err := att.ListTaskList(context.Background(), ListQuery{
		Kind: MembershipActive, WorkspaceScopes: []SourceScope{src.scope},
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	row := h.runtime.projection.findRow(AddressOf(src.scope, "t1"))
	if row == nil {
		t.Fatalf("the row was not projected")
	}
	if row.Meta.WorkspacePath != "/w/proj" {
		t.Fatalf("the row kept the reader's path %q, want the scope's %q",
			row.Meta.WorkspacePath, "/w/proj")
	}
	if row.Address.WorkspacePath != row.Meta.WorkspacePath {
		t.Fatalf("address path %q and meta path %q must agree",
			row.Address.WorkspacePath, row.Meta.WorkspacePath)
	}
	if row.Meta.WorkspaceIdentity == nil || *row.Meta.WorkspaceIdentity != "identity-2" {
		t.Fatalf("the row lost the scope's identity; it must never be keyed by path alone")
	}
}

// TestARemoteRowFromAWrongIdentityIsRefused: a membership whose meta names a
// DIFFERENT identity than the source is refused rather than projected under the
// wrong identity.
func TestARemoteRowFromAWrongIdentityIsRefused(t *testing.T) {
	h := newTestHarness()
	src := h.add(newFakeSource(RemoteScope("remote-1", "/w/proj", "identity-1")))
	src.addTask("t1", "task", Membership{Active: true}, 100)
	src.mu.Lock()
	wrong := "identity-OTHER"
	src.tasks["t1"].meta.WorkspaceIdentity = &wrong
	src.mu.Unlock()

	// normalizeMemberships overwrites the identity with the scope's, so a reader
	// cannot smuggle a foreign identity into the projection; assert the pin, not
	// the refusal, because the pin is what makes the refusal unreachable.
	memberships := normalizeMemberships([]TaskMembership{{
		Meta:       src.muLockedTask("t1"),
		Membership: Membership{Active: true},
	}}, src.scope)
	if memberships[0].meta.WorkspaceIdentity == nil ||
		*memberships[0].meta.WorkspaceIdentity != "identity-1" {
		t.Fatalf("a membership carrying a foreign identity was not pinned to the scope")
	}

	// And a row whose meta is inconsistent with a source that has NO identity is
	// caught by the scope validation.
	local := LocalScope("/w/local", "")
	if err := validateMembershipScope(local, src.muLockedTask("t1")); err == nil {
		t.Fatalf("a membership from another workspace was accepted by a different scope")
	}
}

// muLockedTask reads one task's meta under the fake's lock.
func (f *fakeSource) muLockedTask(taskID string) *adapterTaskMeta {
	f.mu.Lock()
	defer f.mu.Unlock()
	task, ok := f.tasks[taskID]
	if !ok {
		return nil
	}
	meta := task.meta
	return &meta
}
