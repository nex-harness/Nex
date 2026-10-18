package windowcontroller

import (
	"context"
	"sync"

	adapter "github.com/tylinux/nex/go/internal/adapters/taskindex"
)

// fakes_test.go — the in-memory source the tests drive. It is deliberately a
// faithful stand-in for the task-index reader rather than a mock with recorded
// expectations: the controller's rules are about what the projection does with a
// SEQUENCE of reads and writes, so the fake has to hold real state and count real
// calls (including concurrent ones, so the single-flight rule can be measured).

// fakeSource is one source's task index, its live-session sink and its mutation
// log.
type fakeSource struct {
	scope SourceScope
	// offline marks the source as disconnected from the resolver's point of
	// view, so a write against it is refused exactly as it would be for a remote
	// whose transport dropped.
	offline bool

	mu sync.Mutex
	// tasks is taskId → the row the reader will report.
	tasks map[string]*fakeTask
	// sink is the live-observer callback, nil when no observer is running.
	sink func([]SessionOverlay)
	// observerStarts counts Observe calls; observerStops counts stop calls. A
	// disposed runtime must leave them equal.
	observerStarts int
	observerStops  int
	// mutations is the ordered log of applied writes.
	mutations []string

	// listCalls counts ListTaskIndex calls, so single-flight can be asserted.
	listCalls int
	// listGate, when non-nil, blocks every ListTaskIndex until it is closed, so a
	// test can hold two refreshes in flight at once.
	listGate chan struct{}
	// listErr, when non-nil, fails every ListTaskIndex.
	listErr error
	// searchErr, when non-nil, fails every SearchTasks.
	searchErr error
	// searchCalls counts SearchTasks calls.
	searchCalls int
}

type fakeTask struct {
	meta       adapter.TaskMeta
	membership Membership
	// gone is set by a delete and applied under the mutate lock; the row is
	// dropped from the map in the same critical section.
	gone bool
}

func newFakeSource(scope SourceScope) *fakeSource {
	return &fakeSource{scope: scope, tasks: map[string]*fakeTask{}}
}

// addTask registers one row in the given bucket and returns its meta.
func (f *fakeSource) addTask(taskID, title string, membership Membership, updatedAt int64) adapter.TaskMeta {
	f.mu.Lock()
	defer f.mu.Unlock()
	var identity *string
	if f.scope.WorkspaceIdentity != "" {
		id := f.scope.WorkspaceIdentity
		identity = &id
	}
	meta := adapter.TaskMeta{
		TaskID:            taskID,
		TraceID:           "trace-" + taskID,
		Title:             title,
		WorkspacePath:     f.scope.WorkspacePath,
		WorkspaceIdentity: identity,
		CreatedAt:         updatedAt,
		UpdatedAt:         updatedAt,
		Mode:              "chat",
	}
	f.tasks[taskID] = &fakeTask{meta: meta, membership: membership}
	return meta
}

// goOffline marks the source disconnected at the resolver.
func (f *fakeSource) goOffline() {
	f.mu.Lock()
	f.offline = true
	f.mu.Unlock()
}

func (f *fakeSource) setStatus(taskID, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if task, ok := f.tasks[taskID]; ok {
		value := status
		task.meta.Status = &value
	}
}

func (f *fakeSource) setTitle(taskID, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if task, ok := f.tasks[taskID]; ok {
		task.meta.Title = title
	}
}

func (f *fakeSource) removeTask(taskID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.tasks, taskID)
}

// read is the SourceReader view.
func (f *fakeSource) read() SourceReader { return (*fakeSourceReader)(f) }

// observe is the SessionObserver view.
func (f *fakeSource) observe() SessionObserver { return (*fakeObserver)(f) }

// deliverOverlays pushes a live-fact set to the running observer. It is a no-op
// when no observer is attached, which is how a test asserts that a stopped
// observer can no longer drive the projection.
func (f *fakeSource) deliverOverlays(overlays []SessionOverlay) {
	f.mu.Lock()
	sink := f.sink
	f.mu.Unlock()
	if sink != nil {
		sink(overlays)
	}
}

// fakeSourceReader is the SourceReader view of a fakeSource.
type fakeSourceReader fakeSource

// ListTaskIndex implements SourceReader. It is the three-bucket read.
func (f *fakeSourceReader) ListTaskIndex(ctx context.Context, scope SourceScope) ([]TaskMembership, error) {
	f.mu.Lock()
	f.listCalls++
	gate := f.listGate
	listErr := f.listErr
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if listErr != nil {
		return nil, listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]TaskMembership, 0, len(f.tasks))
	for _, task := range f.tasks {
		meta := task.meta
		out = append(out, TaskMembership{Meta: &meta, Membership: task.membership})
	}
	return out, nil
}

// SearchTasks implements SourceReader.
func (f *fakeSourceReader) SearchTasks(ctx context.Context, query ListQuery, scope SourceScope) (SearchResult, error) {
	f.mu.Lock()
	f.searchCalls++
	searchErr := f.searchErr
	f.mu.Unlock()
	if searchErr != nil {
		return SearchResult{}, searchErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	needle := query.Search
	out := SearchResult{}
	for _, task := range f.tasks {
		if needle != "" && !containsFold(task.meta.Title, needle) {
			continue
		}
		meta := task.meta
		out.Items = append(out.Items, SearchItem{
			Meta:           &meta,
			SearchSnippets: []string{"…" + task.meta.Title + "…"},
		})
	}
	out.Total = len(out.Items)
	return out, nil
}

func (f *fakeSourceReader) SetPinned(ctx context.Context, addr TaskAddress, pinned bool) error {
	return f.mutate("pin:"+addr.TaskID+":"+boolText(pinned), func(task *fakeTask) {
		task.membership.Pinned = pinned
	})
}

func (f *fakeSourceReader) SetArchived(ctx context.Context, addr TaskAddress, archived bool) error {
	return f.mutate("archive:"+addr.TaskID+":"+boolText(archived), func(task *fakeTask) {
		task.membership.Archived = archived
	})
}

func (f *fakeSourceReader) DeleteTask(ctx context.Context, addr TaskAddress) error {
	return f.mutate("delete:"+addr.TaskID, func(task *fakeTask) {
		task.gone = true
	})
}

func (f *fakeSourceReader) DeleteArchivedTask(ctx context.Context, addr TaskAddress) (bool, error) {
	found := false
	err := f.mutate("delete-archived:"+addr.TaskID, func(task *fakeTask) {
		found = true
		task.gone = true
	})
	return found, err
}

func (f *fakeSourceReader) DeleteArchivedTasks(ctx context.Context, addr TaskAddress, taskIDs []string) (ArchivedDeletionResult, error) {
	result := ArchivedDeletionResult{
		DeletedTaskIDs: []string{},
		SkippedTaskIDs: []string{},
		FailedTaskIDs:  []string{},
	}
	for _, id := range taskIDs {
		if err := f.mutate("delete-archived-batch:"+id, func(task *fakeTask) {
			task.gone = true
		}); err != nil {
			result.FailedTaskIDs = append(result.FailedTaskIDs, id)
			continue
		}
		result.DeletedTaskIDs = append(result.DeletedTaskIDs, id)
	}
	return result, nil
}

func (f *fakeSourceReader) SetUnread(ctx context.Context, addr TaskAddress, unread bool, expectedUnreadAt *int64) error {
	marker := "-"
	if expectedUnreadAt != nil {
		marker = "cas"
	}
	return f.mutate("unread:"+addr.TaskID+":"+boolText(unread)+":"+marker, func(task *fakeTask) {})
}

// mutate applies one write and records it. A write for a row that is not present
// is still recorded, so a test can tell "the controller routed it" from "the
// controller refused it".
func (f *fakeSourceReader) mutate(entry string, apply func(*fakeTask)) error {
	holder := (*fakeSource)(f)
	holder.mu.Lock()
	defer holder.mu.Unlock()
	holder.mutations = append(holder.mutations, entry)
	taskID := entryTaskID(entry)
	if task, ok := holder.tasks[taskID]; ok {
		apply(task)
		// A delete marks the row gone; it is dropped here, under the same lock
		// the read side takes, rather than through removeTask (which would
		// re-lock and deadlock the caller that already holds it).
		if task.gone {
			delete(holder.tasks, taskID)
		}
	}
	return nil
}

// fakeObserver is the SessionObserver view of a fakeSource.
type fakeObserver fakeSource

// Observe implements SessionObserver. The stop func clears the sink and counts
// itself, so "an observer that outlived its source" is observable.
func (f *fakeObserver) Observe(ctx context.Context, scope SourceScope, onChange func([]SessionOverlay)) (func(), error) {
	holder := (*fakeSource)(f)
	holder.mu.Lock()
	holder.observerStarts++
	holder.sink = onChange
	holder.mu.Unlock()
	return func() {
		holder.mu.Lock()
		holder.sink = nil
		holder.observerStops++
		holder.mu.Unlock()
	}, nil
}

// entryTaskID extracts the task id from a mutation-log entry. Every entry is
// "verb:taskId[:extra]", so the second field is always the id.
func entryTaskID(entry string) string {
	rest := entry
	for i := 0; i < len(entry); i++ {
		if entry[i] == ':' {
			rest = entry[i+1:]
			break
		}
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] == ':' {
			return rest[:i]
		}
	}
	return rest
}

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func containsFold(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	h, n := lowerASCII(haystack), lowerASCII(needle)
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}

func lowerASCII(s string) string {
	out := []byte(s)
	for i := range out {
		if out[i] >= 'A' && out[i] <= 'Z' {
			out[i] += 32
		}
	}
	return string(out)
}

// containsTask reports whether a list result carries the given task id.
func containsTask(list ListResult, taskID string) bool {
	for _, item := range list.Items {
		if item.TaskID == taskID {
			return true
		}
	}
	return false
}

// testHarness is the runtime under test plus the sources it resolves.
type testHarness struct {
	runtime *Runtime
	sources map[string]*fakeSource
	// ids makes subscription ids deterministic and ordered. It is guarded
	// because CreateID is called from every connection's goroutine.
	idsMu sync.Mutex
	ids   int
}

// nextID mints the next deterministic id.
func (h *testHarness) nextID() string {
	h.idsMu.Lock()
	defer h.idsMu.Unlock()
	h.ids++
	return "sub-" + itoaTest(h.ids)
}

func newTestHarness() *testHarness {
	h := &testHarness{sources: map[string]*fakeSource{}}
	h.runtime = NewRuntime(RuntimeOptions{
		Resolve: func(scope SourceScope) *ResolvedSource {
			src := h.sources[SourceKey(scope)]
			if src == nil {
				// A remote identity that has no source is the fail-closed case: the
				// server variant returns nil so a remote never reads the local index.
				return nil
			}
			// A read error alone does not make a source offline: it keeps its last
			// trusted snapshot and the next call retries. Only an explicit
			// disconnect makes it offline, which is what refuses writes.
			availability := AvailabilityOnline
			if src.offline {
				availability = AvailabilityOffline
			}
			return &ResolvedSource{
				Scope:        src.scope,
				Reader:       src.read(),
				Observer:     src.observe(),
				Availability: availability,
			}
		},
		OnError:  func(scope SourceScope, operation string, err error) {},
		CreateID: h.nextID,
		Now:      func() int64 { return 1000 },
	})
	return h
}

func (h *testHarness) add(source *fakeSource) *fakeSource {
	h.sources[SourceKey(source.scope)] = source
	return source
}

// changed models the source's workspace event reaching the controller: the index
// moved underneath it, so the next refresh must re-read. Without this the
// controller correctly answers from its existing snapshot (a list call is not a
// poll), which is why a test that edits the fake and then lists must say so.
func (h *testHarness) changed(t interface{ Fatalf(string, ...any) }, scope SourceScope) {
	resolved := h.runtime.resolveSource(scope)
	if resolved == nil {
		t.Fatalf("changed: scope %s does not resolve", SourceKey(scope))
	}
	if err := h.runtime.refreshSource(context.Background(), resolved, true); err != nil {
		t.Fatalf("changed: refresh: %v", err)
	}
}

// drop removes a scope from the resolver. It models the connection registry
// forgetting a remote session: after a reconnect the OLD remoteSessionId no
// longer resolves, so a call still holding the old address fails closed instead
// of reaching the replacement source's rows.
func (h *testHarness) drop(scope SourceScope) {
	delete(h.sources, SourceKey(scope))
}

func itoaTest(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

// ptr is the test-local pointer helper, matching the convention in the other
// service packages.
func ptr[T any](v T) *T { return &v }
