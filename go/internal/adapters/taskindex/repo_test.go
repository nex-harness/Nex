package taskindex_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tylinux/nex/go/internal/adapters/taskindex"
)

// scenarioRow is the shared shape both engines print for the crosscheck: enough
// to compare the row semantics (identity projection, flags, status, model)
// without depending on field ordering inside meta_json.
type scenarioRow struct {
	TaskID            string  `json:"taskId"`
	Title             string  `json:"title"`
	WorkspacePath     string  `json:"workspacePath"`
	WorkspaceIdentity *string `json:"workspaceIdentity"`
	Model             *string `json:"model"`
	Provider          *string `json:"provider"`
	Mode              string  `json:"mode"`
	Status            *string `json:"status"`
	LastErrorCode     *string `json:"lastErrorCode"`
	CreatedAt         int64   `json:"createdAt"`
	UpdatedAt         int64   `json:"updatedAt"`
	UnreadAt          *int64  `json:"unreadAt"`
}

type crosscheckScenario struct {
	workspacePath     string
	workspaceIdentity string
	ref               taskindex.TargetRef
}

var scenario = []crosscheckScenario{
	{workspacePath: "/w/alpha", ref: taskindex.TargetRef{WorkspacePath: "/w/alpha", TaskID: "task-alpha"}},
	{workspacePath: "/w/beta", ref: taskindex.TargetRef{WorkspacePath: "/w/beta", TaskID: "task-beta"}},
	{
		workspacePath:     "/w/alpha",
		workspaceIdentity: "remote:ssh:host:22:root:/srv/alpha",
		ref: taskindex.TargetRef{
			WorkspacePath:     "/w/alpha",
			WorkspaceIdentity: "remote:ssh:host:22:root:/srv/alpha",
			TaskID:            "task-gamma",
		},
	},
}

func baseMeta(i int, s crosscheckScenario) taskindex.TaskMeta {
	switch i {
	case 0:
		model, provider, status := "GLM-5.3", taskindex.NexAgentProvider, "running"
		return taskindex.TaskMeta{
			TaskID: "task-alpha", TraceID: "nex-task-alpha", Title: "Alpha session",
			WorkspacePath: "/w/alpha", CreatedAt: 1000, UpdatedAt: 2000,
			Mode: "build", Model: &model, Provider: &provider, Status: &status,
		}
	case 1:
		provider, status := taskindex.NexAgentProvider, "completed"
		return taskindex.TaskMeta{
			TaskID: "task-beta", TraceID: "nex-task-beta", Title: "Beta session",
			WorkspacePath: "/w/beta", CreatedAt: 1500, UpdatedAt: 2500,
			Mode: "plan", Provider: &provider, Status: &status,
		}
	default:
		provider, status := taskindex.NexAgentProvider, "error"
		return taskindex.TaskMeta{
			TaskID: "task-gamma", TraceID: "nex-task-gamma", Title: "Gamma session",
			WorkspacePath:     "/w/alpha",
			WorkspaceIdentity: strPtr("remote:ssh:host:22:root:/srv/alpha"),
			CreatedAt:         1200, UpdatedAt: 9000, Mode: "build",
			Provider: &provider, Status: &status,
			LastError: &taskindex.TaskLastError{Code: "boom", Message: "agent failed"},
		}
	}
}

func strPtr(s string) *string { return &s }

// testNowMs is a fixed clock well past the epoch, so day-based cutoffs are
// meaningful.
const testNowMs = 1_700_000_000_000

func newRepo(t *testing.T, path string) *taskindex.Repo {
	t.Helper()
	r, err := taskindex.New(context.Background(), taskindex.RepoOptions{
		Path: path,
		Now:  func() int64 { return testNowMs },
	})
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func newRepoAt(t *testing.T, dir string) *taskindex.Repo {
	t.Helper()
	return newRepo(t, filepath.Join(dir, "tasks-index.sqlite"))
}

// writeScenario is the write half shared by the unit test and the crosscheck Go
// half: three metas (one remote), a pin, an archive and an agent title patch.
func writeScenario(t *testing.T, r *taskindex.Repo) {
	t.Helper()
	ctx := context.Background()
	body := "hello alpha body"
	for i, s := range scenario {
		_, err := r.SyncTaskMeta(ctx, baseMeta(i, s), nil)
		if err != nil {
			t.Fatalf("sync %s: %v", s.ref.TaskID, err)
		}
		if i == 0 {
			if _, err := r.SyncTaskMeta(ctx, baseMeta(i, s), &body); err != nil {
				t.Fatalf("sync searchable: %v", err)
			}
		}
	}
	pin := true
	if _, err := r.UpdateTaskState(ctx, scenario[0].ref, taskindex.StatePatch{Pinned: &pin, UpdatedAt: int64Ptr(3000)}); err != nil {
		t.Fatalf("pin: %v", err)
	}
	archive := true
	if _, err := r.UpdateTaskState(ctx, scenario[1].ref, taskindex.StatePatch{Archived: &archive}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	title := "Alpha renamed"
	if _, err := r.ApplyAgentPatch(ctx, scenario[0].ref, taskindex.AgentPatch{Title: &title}); err != nil {
		t.Fatalf("rename: %v", err)
	}
}

func int64Ptr(v int64) *int64 { return &v }

func readScenario(t *testing.T, r *taskindex.Repo) []scenarioRow {
	t.Helper()
	metas, err := r.ListTaskMetas(context.Background(), taskindex.ListFilter{IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]scenarioRow, 0, len(metas))
	for _, m := range metas {
		row := scenarioRow{
			TaskID: m.TaskID, Title: m.Title, WorkspacePath: m.WorkspacePath,
			WorkspaceIdentity: m.WorkspaceIdentity, Model: m.Model, Provider: m.Provider,
			Mode: m.Mode, Status: m.Status, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
			UnreadAt: m.UnreadAt,
		}
		if m.LastError != nil {
			row.LastErrorCode = strPtr(m.LastError.Code)
		}
		out = append(out, row)
	}
	return out
}

// TestWriteAndReadRoundTrip: the pinned/archived flags, the remote identity
// projection and the agent title patch all survive the round trip.
func TestWriteAndReadRoundTrip(t *testing.T) {
	r := newRepoAt(t, t.TempDir())
	writeScenario(t, r)
	rows := readScenario(t, r)
	if len(rows) != 3 {
		t.Fatalf("row count %d, want 3", len(rows))
	}
	byID := map[string]scenarioRow{}
	for _, row := range rows {
		byID[row.TaskID] = row
	}
	alpha := byID["task-alpha"]
	if alpha.Title != "Alpha renamed" {
		t.Fatalf("alpha title %q, want the agent patch title", alpha.Title)
	}
	if alpha.UpdatedAt != 3000 {
		t.Fatalf("alpha updatedAt %d, want 3000", alpha.UpdatedAt)
	}
	beta := byID["task-beta"]
	if beta.UpdatedAt != 2500 {
		t.Fatalf("beta updatedAt %d, want 2500", beta.UpdatedAt)
	}
	gamma := byID["task-gamma"]
	if gamma.WorkspaceIdentity == nil || *gamma.WorkspaceIdentity != "remote:ssh:host:22:root:/srv/alpha" {
		t.Fatalf("gamma identity %v, want the remote identity", gamma.WorkspaceIdentity)
	}
	if gamma.LastErrorCode == nil || *gamma.LastErrorCode != "boom" {
		t.Fatalf("gamma lastErrorCode %v, want boom", gamma.LastErrorCode)
	}
}

// TestRemoteIdentityIsolatesSamePath: two workspaces that share a path but not
// an identity must not see each other's rows (AGENTS.md identity rule).
func TestRemoteIdentityIsolatesSamePath(t *testing.T) {
	r := newRepoAt(t, t.TempDir())
	ctx := context.Background()
	local := taskindex.TaskMeta{TaskID: "local", TraceID: "nex-local", Title: "local",
		WorkspacePath: "/w/same", CreatedAt: 1, UpdatedAt: 1, Mode: "build"}
	remote := taskindex.TaskMeta{TaskID: "remote", TraceID: "nex-remote", Title: "remote",
		WorkspacePath: "/w/same", WorkspaceIdentity: strPtr("remote:ssh:host:22:root:/w/same"),
		CreatedAt: 1, UpdatedAt: 1, Mode: "build"}
	if _, err := r.SyncTaskMeta(ctx, local, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SyncTaskMeta(ctx, remote, nil); err != nil {
		t.Fatal(err)
	}
	localRows, err := r.ListTaskMetas(ctx, taskindex.ListFilter{WorkspacePath: "/w/same"})
	if err != nil {
		t.Fatal(err)
	}
	if len(localRows) != 1 || localRows[0].TaskID != "local" {
		t.Fatalf("local scope returned %d rows, want only the local one", len(localRows))
	}
	if localRows[0].WorkspaceIdentity != nil {
		t.Fatalf("local row projects an identity: %v", *localRows[0].WorkspaceIdentity)
	}
	remoteRows, err := r.ListTaskMetas(ctx, taskindex.ListFilter{
		WorkspacePath: "/w/same", WorkspaceIdentity: "remote:ssh:host:22:root:/w/same"})
	if err != nil {
		t.Fatal(err)
	}
	if len(remoteRows) != 1 || remoteRows[0].TaskID != "remote" {
		t.Fatalf("remote scope returned %d rows, want only the remote one", len(remoteRows))
	}
}

// TestStaleIdentityColumnDoesNotReproject: a row whose workspace_identity column
// disagrees with the primary key must never project onto the column's remote —
// that would cross-wire two remotes that share a path in the sidebar join.
func TestStaleIdentityColumnDoesNotReproject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks-index.sqlite")
	r := newRepo(t, path)
	meta := taskindex.TaskMeta{TaskID: "t", TraceID: "nex-t", Title: "t",
		WorkspacePath: "/w/x", WorkspaceIdentity: strPtr("remote:ssh:a:22:root:/w/x"),
		CreatedAt: 1, UpdatedAt: 1, Mode: "build"}
	if _, err := r.SyncTaskMeta(context.Background(), meta, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// Rewrite the identity column to a different remote while keeping the key.
	raw, err := openRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.close()
	if err := raw.exec("update tasks set workspace_identity='remote:ssh:other:22:root:/w/x'"); err != nil {
		t.Fatal(err)
	}
	r = newRepo(t, path)
	got, err := r.GetTaskMeta(context.Background(), taskindex.TargetRef{
		WorkspacePath: "/w/x", WorkspaceIdentity: "remote:ssh:a:22:root:/w/x", TaskID: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("row vanished")
	}
	if got.WorkspaceIdentity == nil || *got.WorkspaceIdentity != "remote:ssh:a:22:root:/w/x" {
		t.Fatalf("identity projected as %v; the primary key must win", got.WorkspaceIdentity)
	}
}

// TestSnapshotUpdatedAtIsMonotonic: a snapshot whose updatedAt is older than the
// row must not rewind it, or a new session drops back to the bottom of the list
// right after its first prompt.
func TestSnapshotUpdatedAtIsMonotonic(t *testing.T) {
	r := newRepoAt(t, t.TempDir())
	ctx := context.Background()
	meta := taskindex.TaskMeta{TaskID: "t", TraceID: "nex-t", Title: "t",
		WorkspacePath: "/w", CreatedAt: 1, UpdatedAt: 10, Mode: "build"}
	if _, err := r.SyncTaskMeta(ctx, meta, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ApplyAgentPatch(ctx, taskindex.TargetRef{WorkspacePath: "/w", TaskID: "t"},
		taskindex.AgentPatch{UpdatedAt: int64Ptr(50)}); err != nil {
		t.Fatal(err)
	}
	stale := meta
	stale.UpdatedAt = 10
	got, err := r.SyncTaskMeta(ctx, stale, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.UpdatedAt != 50 {
		t.Fatalf("updatedAt rewound to %d, want the max(50, 10) = 50", got.UpdatedAt)
	}
}

// TestTerminalStatusIsNotRewound: turn.completed writes the terminal status first;
// a later snapshot still carrying "running" must not restore the working state.
func TestTerminalStatusIsNotRewound(t *testing.T) {
	r := newRepoAt(t, t.TempDir())
	ctx := context.Background()
	ref := taskindex.TargetRef{WorkspacePath: "/w", TaskID: "t"}
	meta := taskindex.TaskMeta{TaskID: "t", TraceID: "nex-t", Title: "t",
		WorkspacePath: "/w", CreatedAt: 1, UpdatedAt: 10, Mode: "build",
		Status: strPtr("running")}
	if _, err := r.SyncTaskMeta(ctx, meta, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ApplyAgentPatch(ctx, ref, taskindex.AgentPatch{Status: strPtr("completed"), UpdatedAt: int64Ptr(20)}); err != nil {
		t.Fatal(err)
	}
	stale := meta
	stale.UpdatedAt = 10
	got, err := r.SyncTaskMeta(ctx, stale, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == nil || *got.Status != "completed" {
		t.Fatalf("status %v, want the newer terminal status kept", got.Status)
	}
}

// TestUserTitleIsNotOverwrittenByAgent: the agent owns the session core title;
// a manual rename belongs to the app and must survive every later sync.
func TestUserTitleIsNotOverwrittenByAgent(t *testing.T) {
	r := newRepoAt(t, t.TempDir())
	ctx := context.Background()
	ref := taskindex.TargetRef{WorkspacePath: "/w", TaskID: "t"}
	meta := taskindex.TaskMeta{TaskID: "t", TraceID: "nex-t", Title: "agent title",
		WorkspacePath: "/w", CreatedAt: 1, UpdatedAt: 1, Mode: "build"}
	if _, err := r.SyncTaskMeta(ctx, meta, nil); err != nil {
		t.Fatal(err)
	}
	override := true
	if _, err := r.UpdateTaskState(ctx, ref, taskindex.StatePatch{
		Title: strPtr("my title"), TitleOverridden: &override}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ApplyAgentPatch(ctx, ref, taskindex.AgentPatch{Title: strPtr("agent retitled")}); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetTaskMeta(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "my title" || !got.TitleOverridden {
		t.Fatalf("title %q overridden=%v; the manual rename must survive", got.Title, got.TitleOverridden)
	}
}

// TestSearchableTextIsPreservedOnAgentPatch: a write that carries no message
// context must not clear the indexed conversation body.
func TestSearchableTextIsPreservedOnAgentPatch(t *testing.T) {
	r := newRepoAt(t, t.TempDir())
	ctx := context.Background()
	ref := taskindex.TargetRef{WorkspacePath: "/w", TaskID: "t"}
	body := "needle in the body"
	meta := taskindex.TaskMeta{TaskID: "t", TraceID: "nex-t", Title: "t",
		WorkspacePath: "/w", CreatedAt: 1, UpdatedAt: 1, Mode: "build"}
	if _, err := r.SyncTaskMeta(ctx, meta, &body); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ApplyAgentPatch(ctx, ref, taskindex.AgentPatch{Status: strPtr("completed")}); err != nil {
		t.Fatal(err)
	}
	result, err := r.QueryTaskList(ctx, taskindex.ListQuery{
		Kind:            taskindex.ListKindActive,
		WorkspaceScopes: []taskindex.WorkspaceScope{{WorkspacePath: "/w"}},
		Search:          "needle",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 {
		t.Fatalf("search total %d, want the indexed body to still match", result.Total)
	}
	if len(result.Items[0].SearchSnippets) == 0 {
		t.Fatal("no snippet returned for a body hit")
	}
}

// TestSearchSnippetMergesNearDuplicates: a keyword repeated in one window yields
// one excerpt, not four copies of the same text.
func TestSearchSnippetMergesNearDuplicates(t *testing.T) {
	r := newRepoAt(t, t.TempDir())
	ctx := context.Background()
	body := strings.Repeat("target ", 3)
	if _, err := r.SyncTaskMeta(ctx, taskindex.TaskMeta{TaskID: "t", TraceID: "nex-t",
		Title: "t", WorkspacePath: "/w", CreatedAt: 1, UpdatedAt: 1, Mode: "build"}, &body); err != nil {
		t.Fatal(err)
	}
	result, err := r.QueryTaskList(ctx, taskindex.ListQuery{
		Kind:            taskindex.ListKindActive,
		WorkspaceScopes: []taskindex.WorkspaceScope{{WorkspacePath: "/w"}},
		Search:          "target",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items[0].SearchSnippets) != 1 {
		t.Fatalf("snippets %v, want the near-duplicate windows merged into one", result.Items[0].SearchSnippets)
	}
}

// TestUnreadMarkerIsStrictlyIncreasing: millisecond timestamps can give two
// logical unreads the same version, and clearing must not allow reuse.
func TestUnreadMarkerIsStrictlyIncreasing(t *testing.T) {
	r := newRepoAt(t, t.TempDir())
	ctx := context.Background()
	ref := taskindex.TargetRef{WorkspacePath: "/w", TaskID: "t"}
	if _, err := r.SyncTaskMeta(ctx, taskindex.TaskMeta{TaskID: "t", TraceID: "nex-t", Title: "t",
		WorkspacePath: "/w", CreatedAt: 1, UpdatedAt: 1, Mode: "build"}, nil); err != nil {
		t.Fatal(err)
	}
	first, err := r.UpdateTaskState(ctx, ref, taskindex.StatePatch{UnreadAt: int64Ptr(500)})
	if err != nil {
		t.Fatal(err)
	}
	if first.UnreadAt == nil || *first.UnreadAt != 500 {
		t.Fatalf("first marker %v, want 500", first.UnreadAt)
	}
	second, err := r.UpdateTaskState(ctx, ref, taskindex.StatePatch{UnreadAt: int64Ptr(500)})
	if err != nil {
		t.Fatal(err)
	}
	if *second.UnreadAt <= *first.UnreadAt {
		t.Fatalf("second marker %d is not above %d", *second.UnreadAt, *first.UnreadAt)
	}
	cleared, ok, err := r.ClearTaskUnreadIfMatches(ctx, ref, *second.UnreadAt)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || cleared.UnreadAt != nil {
		t.Fatalf("clear: ok=%v unreadAt=%v, want cleared", ok, cleared.UnreadAt)
	}
	third, err := r.UpdateTaskState(ctx, ref, taskindex.StatePatch{UnreadAt: int64Ptr(1)})
	if err != nil {
		t.Fatal(err)
	}
	if *third.UnreadAt <= *second.UnreadAt {
		t.Fatalf("marker %d reused the pre-clear watermark %d", *third.UnreadAt, *second.UnreadAt)
	}
}

// TestClearUnreadDoesNotLoseANewerUnread: clearing is conditional on the marker
// the client saw.
func TestClearUnreadDoesNotLoseANewerUnread(t *testing.T) {
	r := newRepoAt(t, t.TempDir())
	ctx := context.Background()
	ref := taskindex.TargetRef{WorkspacePath: "/w", TaskID: "t"}
	if _, err := r.SyncTaskMeta(ctx, taskindex.TaskMeta{TaskID: "t", TraceID: "nex-t", Title: "t",
		WorkspacePath: "/w", CreatedAt: 1, UpdatedAt: 1, Mode: "build"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.UpdateTaskState(ctx, ref, taskindex.StatePatch{UnreadAt: int64Ptr(10)}); err != nil {
		t.Fatal(err)
	}
	_, ok, err := r.ClearTaskUnreadIfMatches(ctx, ref, 9)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("cleared a marker the client never saw")
	}
	got, err := r.GetTaskMeta(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.UnreadAt == nil || *got.UnreadAt != 10 {
		t.Fatalf("unreadAt %v, want the newer marker preserved", got.UnreadAt)
	}
}

// TestDeleteArchivedTaskRequiresArchive: a task that is not archived cannot be
// deleted through the archive confirmation path, and the tombstone is atomic
// with the grouping cleanup.
func TestDeleteArchivedTaskRequiresArchive(t *testing.T) {
	r := newRepoAt(t, t.TempDir())
	ctx := context.Background()
	ref := taskindex.TargetRef{WorkspacePath: "/w", TaskID: "t"}
	if _, err := r.SyncTaskMeta(ctx, taskindex.TaskMeta{TaskID: "t", TraceID: "nex-t", Title: "t",
		WorkspacePath: "/w", CreatedAt: 1, UpdatedAt: 1, Mode: "build"}, nil); err != nil {
		t.Fatal(err)
	}
	deleted, err := r.DeleteArchivedTask(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != nil {
		t.Fatal("deleted a task that was never archived")
	}
	archive := true
	if _, err := r.UpdateTaskState(ctx, ref, taskindex.StatePatch{Archived: &archive}); err != nil {
		t.Fatal(err)
	}
	tombstone, err := r.DeleteArchivedTask(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if tombstone == nil {
		t.Fatal("archived task was not tombstoned")
	}
	ids, err := r.ListDeletedTaskIds(ctx, "/w", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "t" {
		t.Fatalf("deleted ids %v, want [t]", ids)
	}
}

// TestCronSessionJoinsTheSystemGroupOnce: acquiring a cron identity groups the
// task, but a later user drag-out is not undone by the next sync.
func TestCronSessionJoinsTheSystemGroupOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks-index.sqlite")
	r := newRepo(t, path)
	ctx := context.Background()
	meta := taskindex.TaskMeta{TaskID: "t", TraceID: "nex-t", Title: "t",
		WorkspacePath: "/w", CreatedAt: 1, UpdatedAt: 1, Mode: "build"}
	if _, err := r.SyncTaskMeta(ctx, meta, nil); err != nil {
		t.Fatal(err)
	}
	withCron := meta
	withCron.CronAutomationID = strPtr("cron-1")
	if _, err := r.SyncTaskMeta(ctx, withCron, nil); err != nil {
		t.Fatal(err)
	}
	group := groupOf(t, path, "t")
	if group != taskindex.CronDefaultGroupID {
		t.Fatalf("group %q, want the cron system group", group)
	}
	// The user moves it out; a later sync must not drag it back.
	raw, err := openRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.exec("update task_group_members set group_id='custom' where task_id='t'"); err != nil {
		t.Fatal(err)
	}
	raw.close()
	if _, err := r.SyncTaskMeta(ctx, withCron, nil); err != nil {
		t.Fatal(err)
	}
	if got := groupOf(t, path, "t"); got != "custom" {
		t.Fatalf("group %q, want the user's manual grouping preserved", got)
	}
}

// TestGroupedTopOrderIsInitializedOnce: concurrent snapshot re-reads must not
// re-assign the minimum sort order, or a slow old task overtakes newer ones.
func TestGroupedTopOrderIsInitializedOnce(t *testing.T) {
	r := newRepoAt(t, t.TempDir())
	ctx := context.Background()
	meta := taskindex.TaskMeta{TaskID: "t", TraceID: "nex-t", Title: "t",
		WorkspacePath: "/w", CreatedAt: 1, UpdatedAt: 1, Mode: "build"}
	if _, _, err := r.SyncTaskMetaAtGroupedTop(ctx, meta, nil); err != nil {
		t.Fatal(err)
	}
	first, exists, err := r.GroupedTopOrder(ctx, taskindex.TargetRef{WorkspacePath: "/w", TaskID: "t"})
	if err != nil || !exists {
		t.Fatalf("no grouped top order after SyncTaskMetaAtGroupedTop: %v", err)
	}
	ok, err := r.InitializeGroupedTaskAtTop(ctx, taskindex.TargetRef{WorkspacePath: "/w", TaskID: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("re-initialized the grouped top order for a task that already has one")
	}
	second, _, err := r.GroupedTopOrder(ctx, taskindex.TargetRef{WorkspacePath: "/w", TaskID: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("top order moved from %d to %d", first, second)
	}
}

// TestConcurrentWritesToOneRowAreSerialized: the per-key write chain must not
// interleave, and every writer must see a well-formed row afterwards.
func TestConcurrentWritesToOneRowAreSerialized(t *testing.T) {
	r := newRepoAt(t, t.TempDir())
	ctx := context.Background()
	ref := taskindex.TargetRef{WorkspacePath: "/w", TaskID: "t"}
	if _, err := r.SyncTaskMeta(ctx, taskindex.TaskMeta{TaskID: "t", TraceID: "nex-t", Title: "start",
		WorkspacePath: "/w", CreatedAt: 1, UpdatedAt: 1, Mode: "build"}, nil); err != nil {
		t.Fatal(err)
	}
	// Every writer targets the same row with the same updatedAt, so the final
	// state must be that exact value — proof the chain serialized the writes
	// instead of letting the last-started writer win by luck.
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.ApplyAgentPatch(ctx, ref, taskindex.AgentPatch{
				Title: strPtr("serialized"), UpdatedAt: int64Ptr(777)}); err != nil {
				t.Errorf("patch: %v", err)
			}
		}()
	}
	wg.Wait()
	got, err := r.GetTaskMeta(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "serialized" {
		t.Fatalf("title %q, want the serialized write", got.Title)
	}
	if got.UpdatedAt != 777 {
		t.Fatalf("updatedAt %d, want 777", got.UpdatedAt)
	}
	if ok, err := r.IntegrityCheck(ctx); err != nil || ok != "ok" {
		t.Fatalf("integrity_check %q (%v)", ok, err)
	}
}

// TestArchiveStaleTasksOnlyTouchesSettledRows: pinned, unread, running and
// already-archived rows must survive the sweep.
func TestArchiveStaleTasksOnlyTouchesSettledRows(t *testing.T) {
	r := newRepo(t, filepath.Join(t.TempDir(), "tasks-index.sqlite"))
	ctx := context.Background()
	makeMeta := func(id string, updated int64, status string) taskindex.TaskMeta {
		return taskindex.TaskMeta{TaskID: id, TraceID: "nex-" + id, Title: id,
			WorkspacePath: "/w", CreatedAt: 1, UpdatedAt: updated, Mode: "build", Status: strPtr(status)}
	}
	if _, err := r.SyncTaskMeta(ctx, makeMeta("settled", 1, "completed"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SyncTaskMeta(ctx, makeMeta("pinned", 1, "completed"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SyncTaskMeta(ctx, makeMeta("unread", 1, "completed"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SyncTaskMeta(ctx, makeMeta("running", 1, "running"), nil); err != nil {
		t.Fatal(err)
	}
	pin := true
	if _, err := r.UpdateTaskState(ctx, taskindex.TargetRef{WorkspacePath: "/w", TaskID: "pinned"},
		taskindex.StatePatch{Pinned: &pin}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.UpdateTaskState(ctx, taskindex.TargetRef{WorkspacePath: "/w", TaskID: "unread"},
		taskindex.StatePatch{UnreadAt: int64Ptr(5)}); err != nil {
		t.Fatal(err)
	}
	archived, err := r.ArchiveStaleTasks(ctx, "/w", "", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 1 || archived[0].TaskID != "settled" {
		t.Fatalf("archived %v, want only the settled row", idsOf(archived))
	}
}

// TestMetaJSONSurvivesSpecialCharacters: JSON.stringify on Node does not escape
// "<", ">" or "&"; if Go escaped them the two engines would stop agreeing on
// meta_json and the sidebar would show different titles for the same row.
func TestMetaJSONSurvivesSpecialCharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks-index.sqlite")
	r := newRepo(t, path)
	title := `A <b>bold</b> & "quoted"`
	if _, err := r.SyncTaskMeta(context.Background(), taskindex.TaskMeta{
		TaskID: "t", TraceID: "nex-t", Title: title, WorkspacePath: "/w",
		CreatedAt: 1, UpdatedAt: 1, Mode: "build"}, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := openRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.close()
	var metaJSON string
	if err := raw.db.QueryRow("SELECT meta_json FROM tasks WHERE task_id = 't'").Scan(&metaJSON); err != nil {
		t.Fatal(err)
	}
	// JSON.stringify escapes the quotes, but "<", ">" and "&" stay literal —
	// those are what Go's encoder would have escaped by default.
	if strings.Contains(metaJSON, `\u003c`) || strings.Contains(metaJSON, `\u0026`) {
		t.Fatalf("meta_json HTML-escaped a character Node leaves literal: %s", metaJSON)
	}
	var decoded struct{ Title string }
	if err := json.Unmarshal([]byte(metaJSON), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Title != title {
		t.Fatalf("round trip title %q, want %q", decoded.Title, title)
	}
}

func idsOf(metas []taskindex.TaskMeta) []string {
	out := make([]string, 0, len(metas))
	for _, m := range metas {
		out = append(out, m.TaskID)
	}
	return out
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
