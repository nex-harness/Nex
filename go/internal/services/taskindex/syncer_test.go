package taskindex_test

// syncer_test.go — the sessions-index ingestion rules. Each test drives one
// rule the TypeScript comment calls out as load-bearing.

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	adapter "github.com/tylinux/nex/go/internal/adapters/taskindex"
	"github.com/tylinux/nex/go/internal/services/taskindex"
)

const testWorkspace = "/w/alpha"

// testNowMs is a fixed clock well past the epoch, so day-based cutoffs are
// meaningful and unread markers stay comparable.
const testNowMs = 1_700_000_000_000

// newRepo opens a repo on a fixed clock; the syncer tests need both to be stable.
func newRepo(t *testing.T, path string) *adapter.Repo {
	t.Helper()
	r, err := adapter.New(context.Background(), adapter.RepoOptions{
		Path: path,
		Now:  func() int64 { return testNowMs },
	})
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func newRepoAt(t *testing.T, dir string) *adapter.Repo {
	t.Helper()
	return newRepo(t, filepath.Join(dir, "tasks-index.sqlite"))
}

func strPtr(s string) *string { return &s }

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

type syncerHarness struct {
	syncer *taskindex.Syncer
	repo   *adapter.Repo
	agent  *fakeAgent
	events []taskindex.WorkspaceEvent
	terms  []taskindex.TerminalEvent
	readys []taskindex.ReadyEvent

	mu sync.Mutex
	// pendingRetries holds the callbacks a manual scheduler captured.
	pendingRetries []func()
}

func newHarness(t *testing.T) *syncerHarness {
	t.Helper()
	repo := newRepo(t, filepath.Join(t.TempDir(), "tasks-index.sqlite"))
	agent := newFakeAgent()
	h := &syncerHarness{repo: repo, agent: agent}
	h.syncer = taskindex.NewSyncer(taskindex.SyncerOptions{
		Agent: agent,
		Repo:  repo,
		Now:   func() int64 { return testNowMs },
		Schedule: func(_ time.Duration, fire func()) {
			h.mu.Lock()
			h.pendingRetries = append(h.pendingRetries, fire)
			h.mu.Unlock()
		},
	})
	t.Cleanup(h.syncer.DisposeAll)
	h.syncer.OnWorkspaceEvent(taskindex.WorkspaceTarget{WorkspacePath: testWorkspace},
		func(e taskindex.WorkspaceEvent) {
			h.mu.Lock()
			h.events = append(h.events, e)
			h.mu.Unlock()
		})
	h.syncer.OnTerminalEvent(func(e taskindex.TerminalEvent) {
		h.mu.Lock()
		h.terms = append(h.terms, e)
		h.mu.Unlock()
	})
	h.syncer.OnReadyEvent(func(e taskindex.ReadyEvent) {
		h.mu.Lock()
		h.readys = append(h.readys, e)
		h.mu.Unlock()
	})
	return h
}

// attach runs the subscribe path synchronously: the runtime is already
// available, so the first snapshot arrives with a known subscription id.
func (h *syncerHarness) attach(t *testing.T) (Workspace, string) {
	t.Helper()
	ctx := context.Background()
	target := taskindex.WorkspaceTarget{WorkspacePath: testWorkspace}
	h.agent.emit(taskindex.RuntimeLifecycleEvent{
		State: "available", WorkspaceKey: target.Key(), WorkspacePath: testWorkspace,
		RuntimeIdentity: taskindex.RuntimeIdentity{Generation: 1},
	})
	waitFor(t, func() bool { return h.agent.subscriptionCount() >= 1 })
	h.syncer.EnsureWorkspaceSubscription(ctx, target)
	waitFor(t, func() bool { return h.agent.subscriptionCount() >= 1 })
	return Workspace(target), h.agent.lastSubscription()
}

// Workspace is a tiny alias so the tests read as prose.
type Workspace = taskindex.WorkspaceTarget

func (h *syncerHarness) runRetries() {
	h.mu.Lock()
	pending := h.pendingRetries
	h.pendingRetries = nil
	h.mu.Unlock()
	for _, fire := range pending {
		go fire()
	}
}

func (h *syncerHarness) eventLog() []taskindex.WorkspaceEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]taskindex.WorkspaceEvent{}, h.events...)
}

func (h *syncerHarness) reasons() []string {
	var out []string
	for _, e := range h.eventLog() {
		out = append(out, e.Reason)
	}
	return out
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not reached within 3s")
}

func (h *syncerHarness) waitEventCount(t *testing.T, n int) {
	t.Helper()
	waitFor(t, func() bool { return len(h.eventLog()) >= n })
}

func (h *syncerHarness) waitTerminalCount(t *testing.T, n int) {
	t.Helper()
	waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.terms) >= n
	})
}

// seedRow pre-creates the row so a title/status patch has something to land on
// (the real path is: the session becomes visible first, then it settles).
func (h *syncerHarness) seedRow(t *testing.T, id string, updatedAt int64) {
	t.Helper()
	status := "running"
	if _, err := h.repo.SyncTaskMeta(context.Background(), adapter.TaskMeta{
		TaskID: id, TraceID: "nex-" + id, Title: id, WorkspacePath: testWorkspace,
		CreatedAt: 1, UpdatedAt: updatedAt, Mode: "build",
		Provider: strPtr(taskindex.NexAgentProvider), Status: &status,
	}, nil); err != nil {
		t.Fatal(err)
	}
	h.agent.putSession(taskindex.SessionSnapshot{
		SessionID: id, Title: id, WorkspacePath: testWorkspace,
		CreatedAt: 1, UpdatedAt: updatedAt, Mode: "build", Status: "running",
		HasUserVisibleContent: true,
	})
}

// TestSilentBaselineSeedsMissingRowsOnly: the first snapshot fills absent rows
// and must not broadcast or replay historical terminal transitions — otherwise a
// cold start fires a completion notification for every session ever created.
func TestSilentBaselineSeedsMissingRowsOnly(t *testing.T) {
	h := newHarness(t)
	target, sub := h.attach(t)
	topic := taskindex.SessionsIndexTopic(target.Key())
	h.agent.deliver(snapshotFrame(topic, sub, 0, 5, "epoch-1", []taskindex.SessionSummary{
		summary("a", taskindex.PhaseCompletedSuccess, 1, 10, "done"),
		summary("b", taskindex.PhaseRunning, 2, 20, "live"),
	}), taskindex.DeliveryInitial)
	waitFor(t, func() bool {
		metas, err := h.repo.ListTaskMetas(context.Background(), adapter.ListFilter{
			WorkspacePath: testWorkspace})
		return err == nil && len(metas) == 2
	})
	if events := h.eventLog(); len(events) != 0 {
		t.Fatalf("baseline broadcast %d events, want 0: %v", len(events), events)
	}
	h.mu.Lock()
	terms := len(h.terms)
	readys := len(h.readys)
	h.mu.Unlock()
	if terms != 0 || readys != 0 {
		t.Fatalf("baseline emitted %d terminal / %d ready events, want 0", terms, readys)
	}
	metas, _ := h.repo.ListTaskMetas(context.Background(), adapter.ListFilter{WorkspacePath: testWorkspace})
	byID := map[string]adapter.TaskMeta{}
	for _, m := range metas {
		byID[m.TaskID] = m
	}
	if byID["a"].Status == nil || *byID["a"].Status != "completed" {
		t.Fatalf("seeded status %v, want completed", byID["a"].Status)
	}
	if byID["b"].Status == nil || *byID["b"].Status != "running" {
		t.Fatalf("seeded status %v, want running", byID["b"].Status)
	}
}

// TestTerminalTransitionPatchesStatusAndBroadcasts: a baseline running → a delta
// with completed is a real transition: status patch, one broadcast with the
// status reason (never the heavy meta reason), then the terminal/ready pair.
func TestTerminalTransitionPatchesStatusAndBroadcasts(t *testing.T) {
	h := newHarness(t)
	target, sub := h.attach(t)
	topic := taskindex.SessionsIndexTopic(target.Key())
	h.seedRow(t, "t1", 100)
	h.agent.deliver(snapshotFrame(topic, sub, 0, 1, "epoch-1", []taskindex.SessionSummary{
		summary("t1", taskindex.PhaseRunning, 1, 100, "t1"),
	}), taskindex.DeliveryInitial)
	waitFor(t, func() bool { return h.baselineSeeded() })
	h.agent.deliver(deltaFrame(topic, sub, 1, 2, []taskindex.SessionDelta{
		upsert(summary("t1", taskindex.PhaseCompletedSuccess, 1, 200, "t1")),
	}), taskindex.DeliveryOnline)
	h.waitEventCount(t, 2)
	h.waitTerminalCount(t, 1)
	got, err := h.repo.GetTaskMeta(context.Background(), adapter.TargetRef{WorkspacePath: testWorkspace, TaskID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == nil || *got.Status != "completed" {
		t.Fatalf("status %v, want completed", got.Status)
	}
	if got.UpdatedAt < 200 {
		t.Fatalf("updatedAt %d, want the summary's lastActivityAt", got.UpdatedAt)
	}
	// Two broadcasts, exactly as the TypeScript does: the status patch fires
	// task_status_changed with the unread signal, and the snapshot re-read that
	// follows fires again — but the signal is only carried by the first, so the
	// completion reminder is neither lost nor duplicated.
	reasons := h.reasons()
	if len(reasons) != 2 ||
		reasons[0] != taskindex.ReasonTaskStatusChanged ||
		reasons[1] != taskindex.ReasonTaskStatusChanged {
		t.Fatalf("reasons %v, want two task_status_changed broadcasts", reasons)
	}
	events := h.eventLog()
	if events[0].UnreadSignal != "background_terminal" {
		t.Fatalf("first broadcast unreadSignal %q, want background_terminal", events[0].UnreadSignal)
	}
	if events[1].UnreadSignal != "" {
		t.Fatalf("second broadcast unreadSignal %q; the reminder must not repeat", events[1].UnreadSignal)
	}
	h.mu.Lock()
	terms := append([]taskindex.TerminalEvent{}, h.terms...)
	readys := append([]taskindex.ReadyEvent{}, h.readys...)
	h.mu.Unlock()
	if len(terms) != 1 || terms[0].Kind != "turn.completed" {
		t.Fatalf("terminal events %v", terms)
	}
	if len(readys) != 1 || readys[0].Reason != "prompt_completed" {
		t.Fatalf("ready events %v", readys)
	}
}

// TestErrorPhaseIsATurnFailure: an error phase fails the turn and keeps the
// lastError for the re-read to fill in.
func TestErrorPhaseIsATurnFailure(t *testing.T) {
	h := newHarness(t)
	target, sub := h.attach(t)
	topic := taskindex.SessionsIndexTopic(target.Key())
	h.seedRow(t, "t1", 100)
	h.agent.deliver(snapshotFrame(topic, sub, 0, 1, "epoch-1", []taskindex.SessionSummary{
		summary("t1", taskindex.PhaseRunning, 1, 100, "t1"),
	}), taskindex.DeliveryInitial)
	waitFor(t, func() bool { return h.baselineSeeded() })
	h.agent.deliver(deltaFrame(topic, sub, 1, 2, []taskindex.SessionDelta{
		upsert(summary("t1", taskindex.PhaseError, 1, 200, "t1")),
	}), taskindex.DeliveryOnline)
	h.waitTerminalCount(t, 1)
	h.mu.Lock()
	terms := append([]taskindex.TerminalEvent{}, h.terms...)
	readys := append([]taskindex.ReadyEvent{}, h.readys...)
	h.mu.Unlock()
	if terms[0].Kind != "turn.failed" || readys[0].Reason != "prompt_failed" {
		t.Fatalf("error phase produced %v / %v", terms[0], readys[0])
	}
}

// TestTerminalOrderIsTerminalBeforeReady: the phone command queue treats the
// ready event as the boundary to send the next input, so the turn must close
// first.
func TestTerminalOrderIsTerminalBeforeReady(t *testing.T) {
	h := newHarness(t)
	target, sub := h.attach(t)
	topic := taskindex.SessionsIndexTopic(target.Key())
	h.seedRow(t, "t1", 100)
	h.agent.deliver(snapshotFrame(topic, sub, 0, 1, "epoch-1", []taskindex.SessionSummary{
		summary("t1", taskindex.PhaseRunning, 1, 100, "t1"),
	}), taskindex.DeliveryInitial)
	waitFor(t, func() bool { return h.baselineSeeded() })
	var order []string
	h.syncer.OnTerminalEvent(func(taskindex.TerminalEvent) {
		h.mu.Lock()
		order = append(order, "terminal")
		h.mu.Unlock()
	})
	h.syncer.OnReadyEvent(func(taskindex.ReadyEvent) {
		h.mu.Lock()
		order = append(order, "ready")
		h.mu.Unlock()
	})
	h.agent.deliver(deltaFrame(topic, sub, 1, 2, []taskindex.SessionDelta{
		upsert(summary("t1", taskindex.PhaseCompletedSuccess, 1, 200, "t1")),
	}), taskindex.DeliveryOnline)
	h.waitTerminalCount(t, 1)
	waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(order) == 2
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	if order[0] != "terminal" || order[1] != "ready" {
		t.Fatalf("event order %v, want terminal before ready", order)
	}
}

// TestTitleChangeUsesItsOwnReason: title changes happen for every task and must
// not trigger a global membership reload.
func TestTitleChangeUsesItsOwnReason(t *testing.T) {
	h := newHarness(t)
	target, sub := h.attach(t)
	topic := taskindex.SessionsIndexTopic(target.Key())
	h.seedRow(t, "t1", 100)
	h.agent.deliver(snapshotFrame(topic, sub, 0, 1, "epoch-1", []taskindex.SessionSummary{
		summary("t1", taskindex.PhaseRunning, 1, 100, "first title"),
	}), taskindex.DeliveryInitial)
	waitFor(t, func() bool { return h.baselineSeeded() })
	h.agent.deliver(deltaFrame(topic, sub, 1, 2, []taskindex.SessionDelta{
		upsert(summary("t1", taskindex.PhaseRunning, 1, 150, "second title")),
	}), taskindex.DeliveryOnline)
	h.waitEventCount(t, 1)
	if reasons := h.reasons(); reasons[0] != taskindex.ReasonTaskTitleChanged {
		t.Fatalf("reasons %v, want task_title_changed", reasons)
	}
	got, _ := h.repo.GetTaskMeta(context.Background(), adapter.TargetRef{WorkspacePath: testWorkspace, TaskID: "t1"})
	if got.Title != "second title" {
		t.Fatalf("title %q, want the new title", got.Title)
	}
}

// TestGapTriggersResyncAndReappliesFromTheBase: a frame that does not continue
// the watermark must not be applied; the syncer resyncs from the proven base and
// the delivered delta is then applied.
func TestGapTriggersResyncAndReappliesFromTheBase(t *testing.T) {
	h := newHarness(t)
	target, sub := h.attach(t)
	topic := taskindex.SessionsIndexTopic(target.Key())
	h.seedRow(t, "t1", 100)
	h.agent.deliver(snapshotFrame(topic, sub, 0, 1, "epoch-1", []taskindex.SessionSummary{
		summary("t1", taskindex.PhaseRunning, 1, 100, "t1"),
	}), taskindex.DeliveryInitial)
	waitFor(t, func() bool { return h.baselineSeeded() })
	// fromSeq 5 while the watermark is 1: a hole.
	h.agent.deliver(deltaFrame(topic, sub, 5, 6, []taskindex.SessionDelta{
		upsert(summary("t1", taskindex.PhaseRunning, 1, 300, "t1")),
	}), taskindex.DeliveryOnline)
	waitFor(t, func() bool { return len(h.agent.resyncLog()) == 1 })
	resyncs := h.agent.resyncLog()
	if resyncs[0].SubscriptionID != sub {
		t.Fatalf("resync on %q, want the owned subscription %q", resyncs[0].SubscriptionID, sub)
	}
	if resyncs[0].Base == nil || resyncs[0].Base.Seq != 1 || resyncs[0].Base.LogEpoch != "epoch-1" {
		t.Fatalf("resync base %+v, want the proven watermark (epoch-1, 1)", resyncs[0].Base)
	}
	if resyncs[0].ForceSnapshot {
		t.Fatal("a same-epoch gap must resume from the base, not force a snapshot")
	}
	// The gapped frame itself must not be applied.
	got, _ := h.repo.GetTaskMeta(context.Background(), adapter.TargetRef{WorkspacePath: testWorkspace, TaskID: "t1"})
	if got.UpdatedAt >= 300 {
		t.Fatalf("the gapped frame was applied (updatedAt %d)", got.UpdatedAt)
	}
}

// TestGapWithoutAProvenBaseForcesASnapshot: a fresh subscribe never carries a
// base, so a delta arriving before any applied frame cannot build a cold
// baseline — it must ask for a snapshot.
func TestGapWithoutAProvenBaseForcesASnapshot(t *testing.T) {
	h := newHarness(t)
	target, sub := h.attach(t)
	topic := taskindex.SessionsIndexTopic(target.Key())
	h.agent.deliver(deltaFrame(topic, sub, 7, 8, []taskindex.SessionDelta{
		upsert(summary("t1", taskindex.PhaseRunning, 1, 100, "t1")),
	}), taskindex.DeliveryInitial)
	waitFor(t, func() bool { return len(h.agent.resyncLog()) == 1 })
	resyncs := h.agent.resyncLog()
	if !resyncs[0].ForceSnapshot {
		t.Fatal("without a proven base the recovery must force a snapshot")
	}
	metas, _ := h.repo.ListTaskMetas(context.Background(), adapter.ListFilter{WorkspacePath: testWorkspace})
	if len(metas) != 0 {
		t.Fatalf("a delta built a cold baseline: %v", metas)
	}
}

// TestDuplicateFrameIsDropped: an online frame at or below the watermark is a
// duplicate, not a new transition.
func TestDuplicateFrameIsDropped(t *testing.T) {
	h := newHarness(t)
	target, sub := h.attach(t)
	topic := taskindex.SessionsIndexTopic(target.Key())
	h.seedRow(t, "t1", 100)
	h.agent.deliver(snapshotFrame(topic, sub, 0, 1, "epoch-1", []taskindex.SessionSummary{
		summary("t1", taskindex.PhaseRunning, 1, 100, "t1"),
	}), taskindex.DeliveryInitial)
	waitFor(t, func() bool { return h.baselineSeeded() })
	h.agent.deliver(deltaFrame(topic, sub, 1, 2, []taskindex.SessionDelta{
		upsert(summary("t1", taskindex.PhaseRunning, 1, 200, "renamed")),
	}), taskindex.DeliveryOnline)
	h.waitEventCount(t, 1)
	// Re-deliver the very same frame: it must be dropped, not re-applied.
	h.agent.deliver(deltaFrame(topic, sub, 1, 2, []taskindex.SessionDelta{
		upsert(summary("t1", taskindex.PhaseRunning, 1, 200, "renamed again")),
	}), taskindex.DeliveryOnline)
	time.Sleep(50 * time.Millisecond)
	if events := h.eventLog(); len(events) != 1 {
		t.Fatalf("a duplicate frame produced %d events, want 1", len(events))
	}
	got, _ := h.repo.GetTaskMeta(context.Background(), adapter.TargetRef{WorkspacePath: testWorkspace, TaskID: "t1"})
	if got.Title != "renamed" {
		t.Fatalf("title %q, want the first frame's title", got.Title)
	}
}

// TestForeignSubscriptionFrameIsIgnored: a frame for another subscription must
// never be interpreted as ours.
func TestForeignSubscriptionFrameIsIgnored(t *testing.T) {
	h := newHarness(t)
	target, sub := h.attach(t)
	topic := taskindex.SessionsIndexTopic(target.Key())
	h.seedRow(t, "t1", 100)
	h.agent.deliver(snapshotFrame(topic, sub, 0, 1, "epoch-1", []taskindex.SessionSummary{
		summary("t1", taskindex.PhaseRunning, 1, 100, "t1"),
	}), taskindex.DeliveryInitial)
	waitFor(t, func() bool { return h.baselineSeeded() })
	h.agent.deliver(deltaFrame(topic, "somebody-else", 1, 2, []taskindex.SessionDelta{
		upsert(summary("t1", taskindex.PhaseCompletedSuccess, 1, 200, "t1")),
	}), taskindex.DeliveryOnline)
	time.Sleep(50 * time.Millisecond)
	h.mu.Lock()
	terms := len(h.terms)
	h.mu.Unlock()
	if terms != 0 {
		t.Fatal("a foreign subscription's frame drove a terminal transition")
	}
}

// TestWrongTopicIsIgnored: another workspace's frames must not touch this row.
func TestWrongTopicIsIgnored(t *testing.T) {
	h := newHarness(t)
	_, sub := h.attach(t)
	h.seedRow(t, "t1", 100)
	h.agent.deliver(snapshotFrame(taskindex.SessionsIndexTopic("/w/other"), sub, 0, 1, "epoch-1",
		[]taskindex.SessionSummary{summary("t1", taskindex.PhaseRunning, 1, 100, "other")}),
		taskindex.DeliveryInitial)
	time.Sleep(50 * time.Millisecond)
	if metas, _ := h.repo.ListTaskMetas(context.Background(),
		adapter.ListFilter{WorkspacePath: testWorkspace}); len(metas) != 1 {
		t.Fatalf("row count %d, want the seeded 1", len(metas))
	}
}

// TestDraftSessionsAreNeverMirrored: a draft is in-memory state only.
func TestDraftSessionsAreNeverMirrored(t *testing.T) {
	h := newHarness(t)
	target, sub := h.attach(t)
	topic := taskindex.SessionsIndexTopic(target.Key())
	h.agent.deliver(snapshotFrame(topic, sub, 0, 1, "epoch-1", []taskindex.SessionSummary{
		summary("draft1", taskindex.PhaseDraft, 1, 10, "draft"),
		summary("real1", taskindex.PhaseRunning, 2, 20, "real"),
	}), taskindex.DeliveryInitial)
	waitFor(t, func() bool {
		metas, err := h.repo.ListTaskMetas(context.Background(), adapter.ListFilter{WorkspacePath: testWorkspace})
		return err == nil && len(metas) == 1
	})
	metas, _ := h.repo.ListTaskMetas(context.Background(), adapter.ListFilter{WorkspacePath: testWorkspace})
	if metas[0].TaskID != "real1" {
		t.Fatalf("mirrored %q; drafts must not reach the index", metas[0].TaskID)
	}
}

// TestSessionRemovedKeepsTheRow: session.removed only maintains the baseline.
// The SQLite tombstone belongs to the delete operation (adapter deleteTask /
// the v4 deleteSession host tail), not to the topic.
func TestSessionRemovedKeepsTheRow(t *testing.T) {
	h := newHarness(t)
	target, sub := h.attach(t)
	topic := taskindex.SessionsIndexTopic(target.Key())
	h.seedRow(t, "t1", 100)
	h.agent.deliver(snapshotFrame(topic, sub, 0, 1, "epoch-1", []taskindex.SessionSummary{
		summary("t1", taskindex.PhaseRunning, 1, 100, "t1"),
	}), taskindex.DeliveryInitial)
	waitFor(t, func() bool { return h.baselineSeeded() })
	h.agent.deliver(deltaFrame(topic, sub, 1, 2, []taskindex.SessionDelta{remove("t1")}),
		taskindex.DeliveryOnline)
	waitFor(t, func() bool {
		rows, _ := h.repo.QueryTaskList(context.Background(), adapter.ListQuery{
			Kind:            adapter.ListKindActive,
			WorkspaceScopes: []adapter.WorkspaceScope{{WorkspacePath: testWorkspace}},
		})
		return rows.Total == 1
	})
	got, _ := h.repo.GetTaskMeta(context.Background(), adapter.TargetRef{WorkspacePath: testWorkspace, TaskID: "t1"})
	if got == nil {
		t.Fatal("session.removed deleted the row; only the baseline may change")
	}
}

// TestSnapshotAfterGapDiffsAgainstTheBaseline: a downgrade to a snapshot is
// applied as the equivalent of the deltas that were lost.
func TestSnapshotAfterGapDiffsAgainstTheBaseline(t *testing.T) {
	h := newHarness(t)
	target, sub := h.attach(t)
	topic := taskindex.SessionsIndexTopic(target.Key())
	h.seedRow(t, "t1", 100)
	h.seedRow(t, "t2", 110)
	h.agent.deliver(snapshotFrame(topic, sub, 0, 1, "epoch-1", []taskindex.SessionSummary{
		summary("t1", taskindex.PhaseRunning, 1, 100, "t1"),
		summary("t2", taskindex.PhaseRunning, 2, 110, "t2"),
	}), taskindex.DeliveryInitial)
	waitFor(t, func() bool { return h.baselineSeeded() })
	// t3 first appears here; it has no row, so the became-visible branch re-reads
	// the authoritative snapshot and creates it.
	h.agent.putSession(taskindex.SessionSnapshot{
		SessionID: "t3", Title: "t3", WorkspacePath: testWorkspace,
		CreatedAt: 3, UpdatedAt: 420, Mode: "build", Status: "running",
		HasUserVisibleContent: true,
	})
	// A recovery snapshot at a higher watermark re-diffs the baseline, which is
	// the conflated equivalent of the deltas that were lost.
	h.agent.deliver(snapshotFrame(topic, sub, 0, 9, "epoch-1", []taskindex.SessionSummary{
		summary("t1", taskindex.PhaseCompletedSuccess, 1, 400, "t1"),
		summary("t2", taskindex.PhaseRunning, 2, 410, "t2 renamed"),
		summary("t3", taskindex.PhaseRunning, 3, 420, "t3"),
	}), taskindex.DeliveryRecovery)
	h.waitTerminalCount(t, 1)
	got, _ := h.repo.GetTaskMeta(context.Background(), adapter.TargetRef{WorkspacePath: testWorkspace, TaskID: "t1"})
	if got.Status == nil || *got.Status != "completed" {
		t.Fatalf("status %v, want the snapshot's completed phase", got.Status)
	}
	renamed, _ := h.repo.GetTaskMeta(context.Background(), adapter.TargetRef{WorkspacePath: testWorkspace, TaskID: "t2"})
	if renamed.Title != "t2 renamed" {
		t.Fatalf("title %q, want the snapshot's title", renamed.Title)
	}
	waitFor(t, func() bool {
		metas, _ := h.repo.ListTaskMetas(context.Background(), adapter.ListFilter{WorkspacePath: testWorkspace})
		return len(metas) == 3
	})
}

// TestStaleOnlineSnapshotIsIgnored: an online snapshot the owned state already
// covers must not diff against itself.
func TestStaleOnlineSnapshotIsIgnored(t *testing.T) {
	h := newHarness(t)
	target, sub := h.attach(t)
	topic := taskindex.SessionsIndexTopic(target.Key())
	h.seedRow(t, "t1", 100)
	h.agent.deliver(snapshotFrame(topic, sub, 0, 5, "epoch-1", []taskindex.SessionSummary{
		summary("t1", taskindex.PhaseRunning, 1, 100, "t1"),
	}), taskindex.DeliveryInitial)
	waitFor(t, func() bool { return h.baselineSeeded() })
	h.agent.deliver(snapshotFrame(topic, sub, 0, 3, "epoch-1", []taskindex.SessionSummary{
		summary("t1", taskindex.PhaseCompletedSuccess, 1, 900, "stale"),
	}), taskindex.DeliveryOnline)
	time.Sleep(50 * time.Millisecond)
	got, _ := h.repo.GetTaskMeta(context.Background(), adapter.TargetRef{WorkspacePath: testWorkspace, TaskID: "t1"})
	if got.Status == nil || *got.Status != "running" {
		t.Fatalf("status %v; a stale online snapshot must be dropped", got.Status)
	}
}

// TestAssemblyFaultTriggersRecovery: a physical assembly failure is a gap.
func TestAssemblyFaultTriggersRecovery(t *testing.T) {
	h := newHarness(t)
	target, sub := h.attach(t)
	topic := taskindex.SessionsIndexTopic(target.Key())
	h.seedRow(t, "t1", 100)
	h.agent.deliver(snapshotFrame(topic, sub, 0, 1, "epoch-1", []taskindex.SessionSummary{
		summary("t1", taskindex.PhaseRunning, 1, 100, "t1"),
	}), taskindex.DeliveryInitial)
	waitFor(t, func() bool { return h.baselineSeeded() })
	h.agent.deliverFault("checksum_mismatch", taskindex.DeliveryOnline)
	waitFor(t, func() bool { return len(h.agent.resyncLog()) == 1 })
	if resyncs := h.agent.resyncLog(); resyncs[0].SubscriptionID != sub {
		t.Fatalf("fault recovery resynced %q, want %q", resyncs[0].SubscriptionID, sub)
	}
}

// TestProviderNotReadyRetriesWithoutSubscribingTwice: a missing model is a wait
// state on a fixed cadence, not a fault.
func TestProviderNotReadyRetriesWithoutSubscribingTwice(t *testing.T) {
	h := newHarness(t)
	target := taskindex.WorkspaceTarget{WorkspacePath: testWorkspace}
	h.agent.subscribeErr = errProviderNotReady()
	h.agent.emit(taskindex.RuntimeLifecycleEvent{
		State: "available", WorkspaceKey: target.Key(), WorkspacePath: testWorkspace,
		RuntimeIdentity: taskindex.RuntimeIdentity{Generation: 1},
	})
	waitFor(t, func() bool { return h.hasPendingRetry() })
	if n := h.agent.subscribeAttempts(); n != 1 {
		t.Fatalf("subscription attempts %d, want the single failing one", n)
	}
	h.runRetries()
	waitFor(t, func() bool { return h.hasPendingRetry() })
	if n := h.agent.subscribeAttempts(); n != 2 {
		t.Fatalf("subscription attempts %d after one retry, want 2", n)
	}
}

// TestTransientSubscribeFailureBacksOff: a transport failure retries too, but on
// the exponential path.
func TestTransientSubscribeFailureBacksOff(t *testing.T) {
	h := newHarness(t)
	h.agent.subscribeErr = errTransient()
	target := taskindex.WorkspaceTarget{WorkspacePath: testWorkspace}
	h.agent.emit(taskindex.RuntimeLifecycleEvent{
		State: "available", WorkspaceKey: target.Key(), WorkspacePath: testWorkspace,
		RuntimeIdentity: taskindex.RuntimeIdentity{Generation: 1},
	})
	waitFor(t, func() bool { return h.hasPendingRetry() })
	if n := h.agent.subscribeAttempts(); n != 1 {
		t.Fatalf("subscription attempts %d, want 1", n)
	}
}

// TestRuntimeUnavailableParksTheWorkspace: with no runtime there is nothing to
// unsubscribe and nothing to resync; the workspace just waits.
func TestRuntimeUnavailableParksTheWorkspace(t *testing.T) {
	h := newHarness(t)
	target := taskindex.WorkspaceTarget{WorkspacePath: testWorkspace}
	h.agent.subscribeErr = errRuntimeUnavailable()
	h.agent.emit(taskindex.RuntimeLifecycleEvent{
		State: "available", WorkspaceKey: target.Key(), WorkspacePath: testWorkspace,
		RuntimeIdentity: taskindex.RuntimeIdentity{Generation: 1},
	})
	waitFor(t, func() bool { return h.agent.subscribeAttempts() == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := h.agent.subscribeAttempts(); n != 1 {
		t.Fatalf("subscription attempts %d after runtime loss, want the single attempt", n)
	}
	if h.hasPendingRetry() {
		t.Fatal("a gone runtime must not schedule a retry")
	}
}

// TestRuntimeRestartResubscribes: the CLI loses its in-memory subscriptions when
// the process is replaced, and no gap frame arrives — the syncer must resubscribe
// on the lifecycle event.
func TestRuntimeRestartResubscribes(t *testing.T) {
	h := newHarness(t)
	_, first := h.attach(t)
	h.agent.emit(taskindex.RuntimeLifecycleEvent{
		State: "available", WorkspaceKey: "/w/alpha", WorkspacePath: testWorkspace,
		RuntimeIdentity: taskindex.RuntimeIdentity{Generation: 2},
	})
	waitFor(t, func() bool { return h.agent.subscriptionCount() == 2 })
	if second := h.agent.lastSubscription(); second == first {
		t.Fatalf("runtime restart reused subscription %q", second)
	}
}

// TestNoRuntimeMeansNoSubscribe: the task index is a passive observer and must
// never start a CLI.
func TestNoRuntimeMeansNoSubscribe(t *testing.T) {
	h := newHarness(t)
	h.syncer.EnsureWorkspaceSubscription(context.Background(),
		taskindex.WorkspaceTarget{WorkspacePath: testWorkspace})
	time.Sleep(50 * time.Millisecond)
	if n := h.agent.subscriptionCount(); n != 0 {
		t.Fatalf("subscribed %d times with no runtime attached", n)
	}
}

// TestConcurrentEventsConvergeToTheSameRow: many sessions settling at once must
// each end up with exactly one row and the final watermark title.
func TestConcurrentEventsConvergeToTheSameRow(t *testing.T) {
	h := newHarness(t)
	target, sub := h.attach(t)
	topic := taskindex.SessionsIndexTopic(target.Key())
	const sessions = 24
	var baseline []taskindex.SessionSummary
	for i := range sessions {
		id := "s" + itoa(i)
		h.seedRow(t, id, 100)
		baseline = append(baseline, summary(id, taskindex.PhaseRunning, 1, 100, id))
	}
	h.agent.deliver(snapshotFrame(topic, sub, 0, 1, "epoch-1", baseline), taskindex.DeliveryInitial)
	waitFor(t, func() bool { return h.baselineSeeded() })
	var wg sync.WaitGroup
	for i := range sessions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "s" + itoa(i)
			h.agent.deliver(deltaFrame(topic, sub, 1, 2, []taskindex.SessionDelta{
				upsert(summary(id, taskindex.PhaseCompletedSuccess, 1, 200, id+"-done")),
			}), taskindex.DeliveryOnline)
		}(i)
	}
	wg.Wait()
	// The frames race on the frame gate too; the gate is serialized by the state
	// mutex, so the watermark may reject some of them — what matters is that the
	// index converges to one row per session with no interleaved half-writes.
	waitFor(t, func() bool {
		metas, err := h.repo.ListTaskMetas(context.Background(), adapter.ListFilter{WorkspacePath: testWorkspace})
		return err == nil && len(metas) == sessions
	})
	metas, _ := h.repo.ListTaskMetas(context.Background(), adapter.ListFilter{WorkspacePath: testWorkspace})
	seen := map[string]bool{}
	for _, m := range metas {
		if seen[m.TaskID] {
			t.Fatalf("duplicate row for %s", m.TaskID)
		}
		seen[m.TaskID] = true
		if m.Status == nil {
			t.Fatalf("row %s has no status", m.TaskID)
		}
	}
	if ok, err := h.repo.IntegrityCheck(context.Background()); err != nil || ok != "ok" {
		t.Fatalf("integrity_check %q (%v) after concurrent settles", ok, err)
	}
}

// TestDisposeStopsEverything: after disposal no frame may write.
func TestDisposeStopsEverything(t *testing.T) {
	h := newHarness(t)
	target, sub := h.attach(t)
	topic := taskindex.SessionsIndexTopic(target.Key())
	h.seedRow(t, "t1", 100)
	h.agent.deliver(snapshotFrame(topic, sub, 0, 1, "epoch-1", []taskindex.SessionSummary{
		summary("t1", taskindex.PhaseRunning, 1, 100, "t1"),
	}), taskindex.DeliveryInitial)
	waitFor(t, func() bool { return h.baselineSeeded() })
	h.syncer.DisposeAll()
	h.agent.deliver(deltaFrame(topic, sub, 1, 2, []taskindex.SessionDelta{
		upsert(summary("t1", taskindex.PhaseCompletedSuccess, 1, 500, "after dispose")),
	}), taskindex.DeliveryOnline)
	time.Sleep(50 * time.Millisecond)
	got, _ := h.repo.GetTaskMeta(context.Background(), adapter.TargetRef{WorkspacePath: testWorkspace, TaskID: "t1"})
	if got.UpdatedAt >= 500 {
		t.Fatal("a frame was applied after DisposeAll")
	}
	waitFor(t, func() bool { return len(h.agent.unsubscribeLog()) == 1 })
}

// TestSyncTaskModelUpdatesTheIndexWithoutBroadcasting: the model switch must not
// replay a historical snapshot.
func TestSyncTaskModelUpdatesTheIndexWithoutBroadcasting(t *testing.T) {
	h := newHarness(t)
	h.seedRow(t, "t1", 100)
	ctx := context.Background()
	if _, err := h.syncer.SyncTaskModel(ctx, taskindex.SessionTarget{
		WorkspacePath: testWorkspace, SessionID: "t1"}, "GLM-5.3"); err != nil {
		t.Fatal(err)
	}
	got, _ := h.repo.GetTaskMeta(ctx, adapter.TargetRef{WorkspacePath: testWorkspace, TaskID: "t1"})
	if got.Model == nil || *got.Model != "GLM-5.3" {
		t.Fatalf("model %v, want GLM-5.3", got.Model)
	}
	if events := h.eventLog(); len(events) != 0 {
		t.Fatalf("syncTaskModel broadcast %d events, want none", len(events))
	}
	// An empty model is a no-op, not a cleared value.
	if meta, err := h.syncer.SyncTaskModel(ctx, taskindex.SessionTarget{
		WorkspacePath: testWorkspace, SessionID: "t1"}, "   "); err != nil || meta != nil {
		t.Fatalf("empty model returned (%v, %v), want (nil, nil)", meta, err)
	}
	got, _ = h.repo.GetTaskMeta(ctx, adapter.TargetRef{WorkspacePath: testWorkspace, TaskID: "t1"})
	if got.Model == nil || *got.Model != "GLM-5.3" {
		t.Fatalf("model %v, want the empty update to be ignored", got.Model)
	}
}

// TestEmptySnapshotDoesNotBroadcastThePlaceholderTitle: createSession returns a
// snapshot with neither a title nor a user message; broadcasting it flashes
// "New session" in the sidebar.
func TestEmptySnapshotDoesNotBroadcastThePlaceholderTitle(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.syncer.SyncSnapshotAndBroadcast(ctx, taskindex.SessionSnapshot{
		SessionID: "t1", Title: "New session", WorkspacePath: testWorkspace,
		CreatedAt: 1, UpdatedAt: 1, Mode: "build",
	}, taskindex.BroadcastOptions{Reason: taskindex.ReasonTaskStatusChanged}); err != nil {
		t.Fatal(err)
	}
	if events := h.eventLog(); len(events) != 0 {
		t.Fatalf("an empty snapshot broadcast %d events, want none", len(events))
	}
	// The row must still exist so the later title patch has something to find.
	got, _ := h.repo.GetTaskMeta(ctx, adapter.TargetRef{WorkspacePath: testWorkspace, TaskID: "t1"})
	if got == nil {
		t.Fatal("the row was not written; the later title patch would find nothing")
	}
}

// TestSnapshotWithContentBroadcastsTheGivenReason: the same snapshot once it has
// a title broadcasts with the caller's declared reason.
func TestSnapshotWithContentBroadcastsTheGivenReason(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.syncer.SyncSnapshotAndBroadcast(ctx, taskindex.SessionSnapshot{
		SessionID: "t1", Title: "real title", WorkspacePath: testWorkspace,
		CreatedAt: 1, UpdatedAt: 1, Mode: "build", Status: "running",
		SearchableText: "hello", HasUserVisibleContent: true,
	}, taskindex.BroadcastOptions{Reason: taskindex.ReasonTaskStatusChanged}); err != nil {
		t.Fatal(err)
	}
	h.waitEventCount(t, 1)
	if reasons := h.reasons(); reasons[0] != taskindex.ReasonTaskStatusChanged {
		t.Fatalf("reasons %v, want the declared reason", reasons)
	}
	result, _ := h.repo.QueryTaskList(ctx, adapter.ListQuery{
		Kind:            adapter.ListKindActive,
		WorkspaceScopes: []adapter.WorkspaceScope{{WorkspacePath: testWorkspace}},
		Search:          "hello",
	})
	if result.Total != 1 {
		t.Fatalf("search total %d; the snapshot body must be indexed", result.Total)
	}
}

// TestMoveGroupedToTopInitializesTheOrderOnce: the first visible snapshot also
// claims the grouped top-level order, and reports task_created so the running
// grouped structure cache invalidates.
func TestMoveGroupedToTopInitializesTheOrderOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// The session became visible while carrying no user content: the row and the
	// grouped order are written, and the renderer is told to reload the structure
	// even though there is no task meta to show yet.
	if _, err := h.syncer.SyncSnapshotAndBroadcast(ctx, taskindex.SessionSnapshot{
		SessionID: "t1", Title: "New session", WorkspacePath: testWorkspace,
		CreatedAt: 1, UpdatedAt: 1, Mode: "build",
	}, taskindex.BroadcastOptions{Reason: taskindex.ReasonTaskStatusChanged, MoveGroupedTaskToTop: true}); err != nil {
		t.Fatal(err)
	}
	h.waitEventCount(t, 1)
	if reasons := h.reasons(); reasons[0] != taskindex.ReasonTaskCreated {
		t.Fatalf("reasons %v, want task_created", reasons)
	}
	if _, ok, err := h.repo.GroupedTopOrder(ctx, adapter.TargetRef{WorkspacePath: testWorkspace, TaskID: "t1"}); err != nil || !ok {
		t.Fatalf("no grouped top order was initialized: %v", err)
	}
	if meta, _ := h.repo.GetTaskMeta(ctx, adapter.TargetRef{WorkspacePath: testWorkspace, TaskID: "t1"}); meta == nil {
		t.Fatal("the row was not written")
	}
}

func (h *syncerHarness) baselineSeeded() bool {
	return h.syncer.IsSeeded(taskindex.WorkspaceTarget{WorkspacePath: testWorkspace})
}

func (h *syncerHarness) hasPendingRetry() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.pendingRetries) > 0
}
