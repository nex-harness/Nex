// storage_test.go (step 3.8): the storage module suite — catalog
// classification vectors, the clean plan (protected paths, today's logs,
// active session windows), fold behavior, the scan accuracy against a
// synthetic tree with known sizes, clean integration on the synthetic dir,
// and the service's job lifecycle (throttle, cancel, latest-job ownership).
package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── domain: classification vectors ────────────────────────────────────────

func TestClassifyStoragePathVectors(t *testing.T) {
	ctxHome := CatalogContext{RootID: RootHome, HasCustomDataBaseDir: false}
	cases := []struct {
		path     string
		category string
		entryKey string
		context  CatalogContext
	}{
		{"v2/tasks-index.sqlite", "sessionStore", "v2/tasks-index.sqlite", ctxHome},
		{"v2/tasks-index.sqlite-wal", "sessionStore", "v2/tasks-index.sqlite-wal", ctxHome},
		{"cli/db/db.sqlite", "sessionStore", "cli/db/db.sqlite", ctxHome},
		{"cli/agents/sess_a/turn_1/transcript.jsonl", "subagentTranscripts", "cli/agents/sess_a", ctxHome},
		{"cli/agents/sess_a/turn_1/output.bin", "toolOutputs", "cli/agents/sess_a", ctxHome},
		{"v2/checkpoints/w1/pending/x.json", "toolOutputs", "v2/checkpoints/w1/pending/x.json", ctxHome},
		{"cli/db/db.sqlite.20260101", "backups", "cli/db/db.sqlite.20260101", ctxHome},
		{"v2/setting.json.corrupt-1", "backups", "v2/setting.json.corrupt-1", ctxHome},
		{"computer-use/run/op.log", "logs", "computer-use/run/op.log", ctxHome},
		{"v2/setting.json", "config", "v2/setting.json", ctxHome},
		{"cli/log/run.log", "logs", "cli/log/run.log", ctxHome},
		{"cli/debug/trace.jsonl", "modelTrajectory", "cli/debug/trace.jsonl", ctxHome},
		{"cli/rollout/x.bin", "modelTrajectory", "cli/rollout/x.bin", ctxHome},
		{"v2/dev/dump.txt", "devTraces", "v2/dev/dump.txt", ctxHome},
		{"export-log/exp.json", "exports", "export-log/exp.json", ctxHome},
		{"agents/bundled.md", "config", "agents/bundled.md", ctxHome},
		{"pets/dog/pet.json", "config", "pets/dog", ctxHome},
		{"pets", "config", "pets", ctxHome},
		{"cli/plugins/cache/x", "runtimes", "cli/plugins/cache", ctxHome},
		{"AGENTS.md", "config", "AGENTS.md", ctxHome},
		{"random/thing.txt", "other", "random", ctxHome},
		{"agent/legacy.bin", "other", "agent", ctxHome},
		// 自定义数据根：home 下的 v2 是旧副本 → other
		{"v2/setting.json", "other", "v2", CatalogContext{RootID: RootHome, HasCustomDataBaseDir: true}},
		{"v2/setting.json", "config", "v2/setting.json", CatalogContext{RootID: RootDataBaseDir, HasCustomDataBaseDir: true}},
		// Windows 分隔符规范化
		{`v2\sessions\w1\sess.json`, "sessionStore", "v2/sessions/w1", ctxHome},
	}
	for _, tc := range cases {
		got := ClassifyStoragePath(tc.path, tc.context)
		if got.CategoryID != tc.category || got.EntryKey != tc.entryKey {
			t.Errorf("ClassifyStoragePath(%q) = {%s %s}, want {%s %s}", tc.path, got.CategoryID, got.EntryKey, tc.category, tc.entryKey)
		}
	}
}

func TestProtectedPaths(t *testing.T) {
	for _, path := range []string{
		"v2/setting.json", "v2/setting.json.lock", "v2/credentials.json",
		"v2/.credentials.json", "v2/.tokens", "v2/nex-stdio-tap.json",
		"v2/crash/live/session.dmp", "v2/crash/live/deep/other.dmp",
	} {
		if !IsProtectedPath(path) {
			t.Errorf("IsProtectedPath(%q) = false, want true", path)
		}
	}
	for _, path := range []string{
		"v2/setting.json.bak", "v2/crash/old/session.dmp", "v2/other.json",
	} {
		if IsProtectedPath(path) {
			t.Errorf("IsProtectedPath(%q) = true, want false", path)
		}
	}
}

func TestCleanScopes(t *testing.T) {
	if scopes := CleanScopes("sessionStore"); len(scopes) != 0 {
		t.Fatalf("sessionStore (none-cleanable) must have no scopes, got %v", scopes)
	}
	scopes := CleanScopes("backups")
	recursive := map[string]bool{}
	shallow := map[string]bool{}
	for _, scope := range scopes {
		if scope.Recursive {
			recursive[scope.Prefix] = true
		} else {
			shallow[scope.Prefix] = true
		}
	}
	// PREFIX_RULES（backup、v2/backup、…）全部递归；FILE_RULE_SCOPES（cli/db、cli、v2）浅层。
	for _, prefix := range []string{"backup", "v2/backup", "v2/migrations", "cli/db/backup", "cli/db/backups"} {
		if !recursive[prefix] {
			t.Errorf("backups prefix %q must be recursive", prefix)
		}
	}
	for _, prefix := range []string{"cli/db", "cli", "v2"} {
		if !shallow[prefix] {
			t.Errorf("backups file-rule scope %q must be shallow", prefix)
		}
	}
	transcript := CleanScopes("subagentTranscripts")
	if len(transcript) != 1 || transcript[0].Prefix != "cli/agents" || !transcript[0].Recursive {
		t.Fatalf("subagentTranscripts scopes = %v, want recursive cli/agents", transcript)
	}
}

// ── domain: clean plan ────────────────────────────────────────────────────

func TestPlanStorageCleanProtectedAndClassification(t *testing.T) {
	context := CatalogContext{RootID: RootHome, HasCustomDataBaseDir: false}
	now := time.Now().UnixMilli()
	old := now - 48*3600*1000
	candidates := []CleanCandidate{
		{RelativePath: "cli/agents/sess_a/turn_1/transcript.jsonl", Bytes: 100, MtimeMs: old},
		{RelativePath: "v2/setting.json", Bytes: 50, MtimeMs: old}, // protected
		{RelativePath: "cli/agents/sess_b/turn_1/transcript.jsonl", Bytes: 200, MtimeMs: old},
	}
	plan := PlanStorageClean("subagentTranscripts", candidates, context, now)
	if len(plan.Targets) != 2 {
		t.Fatalf("targets = %v", plan.Targets)
	}
	if plan.SkippedCount != 1 {
		t.Fatalf("skippedCount = %d, want 1", plan.SkippedCount)
	}
}

func TestPlanStorageCleanLogsTodayKept(t *testing.T) {
	context := CatalogContext{RootID: RootHome, HasCustomDataBaseDir: false}
	now := time.Now().UnixMilli()
	yesterday := now - 30*3600*1000
	// 30 小时前可能是「昨天」，也可能是前天；构造明确的昨日同刻 - 1ms 不稳定，
	// 用两个候选：一个明确今天，一个明确远早于今天。
	today := now - 1000
	ancient := now - 30*24*3600*1000
	candidates := []CleanCandidate{
		{RelativePath: "cli/log/a.log", Bytes: 10, MtimeMs: today},
		{RelativePath: "cli/log/b.log", Bytes: 20, MtimeMs: ancient},
	}
	plan := PlanStorageClean("logs", candidates, context, now)
	if len(plan.Targets) != 1 || plan.Targets[0].RelativePath != "cli/log/b.log" {
		t.Fatalf("logs plan = %v (today's log must be kept)", plan.Targets)
	}
	_ = yesterday
}

func TestPlanStorageCleanSubagentActiveWindow(t *testing.T) {
	context := CatalogContext{RootID: RootHome, HasCustomDataBaseDir: false}
	now := time.Now().UnixMilli()
	old := now - 48*3600*1000
	recent := now - 3600*1000
	// sess_a 整组活跃（metadata 文件在窗口内），transcript 也不能删。
	candidates := []CleanCandidate{
		{RelativePath: "cli/agents/sess_a/turn_1/transcript.jsonl", Bytes: 100, MtimeMs: old},
		{RelativePath: "cli/agents/sess_a/turn_1/metadata.json", Bytes: 1, MtimeMs: recent},
		{RelativePath: "cli/agents/sess_b/turn_1/transcript.jsonl", Bytes: 200, MtimeMs: old},
	}
	plan := PlanStorageClean("subagentTranscripts", candidates, context, now)
	if len(plan.Targets) != 1 || plan.Targets[0].RelativePath != "cli/agents/sess_b/turn_1/transcript.jsonl" {
		t.Fatalf("session-window plan = %v", plan.Targets)
	}
}

// ── domain: usage accumulator fold ────────────────────────────────────────

func TestUsageAccumulatorFold(t *testing.T) {
	spec := RootSpec{ID: RootHome, Path: "/tmp/root"}
	accumulator := newUsageAccumulator(spec, 3)
	for i := 0; i < 5; i++ {
		accumulator.add(ScanEntry{RelativePath: fmt.Sprintf("cli/log/f%d.log", i), Bytes: int64(100 + i), MtimeMs: 1})
	}
	accumulator.add(ScanEntry{RelativePath: "v2/setting.json", Bytes: 10, MtimeMs: 1})
	usage := accumulator.snapshot(nil)
	if usage.Bytes != 100+101+102+103+104+10 {
		t.Fatalf("total bytes = %d", usage.Bytes)
	}
	if usage.FileCount != 6 {
		t.Fatalf("file count = %d", usage.FileCount)
	}
	var logs *CategoryUsage
	for i := range usage.Categories {
		if usage.Categories[i].ID == "logs" {
			logs = &usage.Categories[i]
		}
	}
	if logs == nil {
		t.Fatal("no logs category")
	}
	if len(logs.Entries) != 4 {
		t.Fatalf("logs entries = %v, want 3 kept + 1 fold", logs.Entries)
	}
	last := logs.Entries[len(logs.Entries)-1]
	if last.RelativePath != MoreEntriesPath {
		t.Fatalf("last entry = %q, want fold marker", last.RelativePath)
	}
	if last.Bytes != 100+101 || last.FileCount != 2 {
		t.Fatalf("fold entry = %+v", last)
	}
}

// ── scan accuracy on a synthetic dir with known sizes ────────────────────

// buildSyntheticRoot creates a data tree with KNOWN sizes:
//
//	.nex/v2/setting.json                10 bytes (config)
//	.nex/v2/logs/a.log                 1000 (logs)
//	.nex/v2/logs/b.log                 2000 (logs)
//	.nex/cli/agents/sess_x/t1/transcript.jsonl 500 (subagentTranscripts)
//	.nex/cli/agents/sess_x/t2/transcript.jsonl 250
//	.nex/cli/config.json               10 (config)
//	.nex/v2/crash/old/c.dmp            100 (logs)
//	symlink into /etc → NOT counted (walker must skip it)
func buildSyntheticRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	nex := filepath.Join(root, ".nex")
	write := func(rel string, size int) {
		t.Helper()
		path := filepath.Join(nex, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("v2/setting.json", 10)
	write("v2/logs/a.log", 1000)
	write("v2/logs/b.log", 2000)
	write("cli/agents/sess_x/t1/transcript.jsonl", 500)
	write("cli/agents/sess_x/t2/transcript.jsonl", 250)
	write("cli/config.json", 10)
	write("v2/crash/old/c.dmp", 100)
	if err := os.Symlink("/etc", filepath.Join(nex, "v2", "etc-link")); err == nil {
		// symlink dir: entries under it must be skipped
	}
	return root
}

func TestScanAccuracyAgainstKnownSizes(t *testing.T) {
	root := buildSyntheticRoot(t)
	spec := RootSpec{ID: RootHome, Path: filepath.Join(root, ".nex")}
	service := New(Options{
		Roots:      &staticRoots{roots: []RootSpec{spec}},
		ScanRunner: NewInProcessScanRunner(NewFsVolumeProbe(), 0, nil),
		Cleaner:    NewFsCleaner(),
		Now:        func() int64 { return time.Now().UnixMilli() },
	})
	if _, err := service.StartScan(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForSnapshot(t, service, "complete")

	snapshot := service.GetSnapshot(context.Background())
	if snapshot == nil || len(snapshot.Roots) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	usage := snapshot.Roots[0]
	// du-style expectation: sum of all regular files = 10+1000+2000+500+250+10+100
	if usage.Bytes != 3870 || usage.FileCount != 7 {
		t.Fatalf("scan totals = bytes %d files %d, want 3870/7", usage.Bytes, usage.FileCount)
	}
	byCategory := map[string]CategoryUsage{}
	for _, category := range usage.Categories {
		byCategory[category.ID] = category
	}
	if byCategory["logs"].Bytes != 3100 {
		t.Errorf("logs bytes = %d, want 3100", byCategory["logs"].Bytes)
	}
	if byCategory["subagentTranscripts"].Bytes != 750 {
		t.Errorf("subagentTranscripts bytes = %d, want 750", byCategory["subagentTranscripts"].Bytes)
	}
	if byCategory["config"].Bytes != 20 {
		t.Errorf("config bytes = %d, want 20 (setting.json + cli/config.json)", byCategory["config"].Bytes)
	}
	if byCategory["runtimes"].FileCount != 0 {
		t.Errorf("runtimes must be empty (no files there)")
	}
}

func TestScanSkipsSymlinkedDirectories(t *testing.T) {
	root := t.TempDir()
	nex := filepath.Join(root, ".nex")
	if err := os.MkdirAll(filepath.Join(nex, "v2", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nex, "v2", "logs", "real.log"), make([]byte, 5), 0o644); err != nil {
		t.Fatal(err)
	}
	// symlinked subdirectory pointing at a directory with many files
	target := t.TempDir()
	for i := 0; i < 10; i++ {
		os.WriteFile(filepath.Join(target, fmt.Sprintf("f%d.bin", i)), make([]byte, 50), 0o644)
	}
	if err := os.Symlink(target, filepath.Join(nex, "v2", "logs", "link")); err != nil {
		t.Fatal(err)
	}
	entries := []ScanEntry{}
	_, err := walkStorageRoot(context.Background(), walkOptions{
		rootPath: nex,
		onEntry:  func(entry ScanEntry) { entries = append(entries, entry) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %v, want only real.log (symlink dir skipped)", entries)
	}
}

// ── clean integration ─────────────────────────────────────────────────────

func TestCleanDeletesOldSubagentTranscripts(t *testing.T) {
	root := buildSyntheticRoot(t)
	spec := RootSpec{ID: RootHome, Path: filepath.Join(root, ".nex")}
	// rewrite transcripts to be old
	old := time.Now().Add(-48 * time.Hour).UnixNano()
	os.Chtimes(filepath.Join(spec.Path, "cli/agents/sess_x/t1/transcript.jsonl"), time.Unix(0, old), time.Unix(0, old))
	os.Chtimes(filepath.Join(spec.Path, "cli/agents/sess_x/t2/transcript.jsonl"), time.Unix(0, old), time.Unix(0, old))
	service := New(Options{
		Roots:      &staticRoots{roots: []RootSpec{spec}},
		ScanRunner: NewInProcessScanRunner(NewFsVolumeProbe(), 0, nil),
		Cleaner:    NewFsCleaner(),
		Now:        func() int64 { return time.Now().UnixMilli() },
	})
	result, err := service.Clean(context.Background(), CleanRequest{RootID: RootHome, CategoryID: "subagentTranscripts"})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedCount != 2 || result.FreedBytes != 750 {
		t.Fatalf("clean result = %+v, want 2 deleted / 750 bytes", result)
	}
	for _, rel := range []string{"cli/agents/sess_x/t1/transcript.jsonl", "cli/agents/sess_x/t2/transcript.jsonl"} {
		if _, err := os.Stat(filepath.Join(spec.Path, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s still exists", rel)
		}
	}
	// keepDirectories: cli/agents 顶层目录必须保留
	if info, err := os.Stat(filepath.Join(spec.Path, "cli", "agents")); err != nil || !info.IsDir() {
		t.Errorf("cli/agents must be kept: %v", err)
	}
}

func TestCleanRefusesNonCleanable(t *testing.T) {
	service := New(Options{Roots: &staticRoots{}, ScanRunner: nil, Cleaner: NewFsCleaner(), Now: nil})
	_, err := service.Clean(context.Background(), CleanRequest{RootID: RootHome, CategoryID: "sessionStore"})
	if err == nil || !strings.Contains(err.Error(), "storage category is not cleanable: sessionStore") {
		t.Fatalf("err = %v, want the exact refusal", err)
	}
}

func TestCleanRefusesOutsideRoot(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim.txt")
	os.WriteFile(victim, []byte("x"), 0o644)
	cleaner := NewFsCleaner()
	result, err := cleaner.DeleteFiles(context.Background(), root, []CleanCandidate{
		{RelativePath: "../victim.txt", Bytes: 1, MtimeMs: 0},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) != 1 || result.Failures[0].Code != "EOUTSIDE" {
		t.Fatalf("failures = %v, want EOUTSIDE", result.Failures)
	}
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Fatal("victim was deleted despite the confinement guard")
	}
}

// ── service lifecycle ─────────────────────────────────────────────────────

func TestScanProgressEventsAndTerminalSnapshot(t *testing.T) {
	root := buildSyntheticRoot(t)
	spec := RootSpec{ID: RootHome, Path: filepath.Join(root, ".nex")}
	service := New(Options{
		Roots:      &staticRoots{roots: []RootSpec{spec}},
		ScanRunner: NewInProcessScanRunner(NewFsVolumeProbe(), 0, nil),
		Cleaner:    NewFsCleaner(),
		Now:        func() int64 { return time.Now().UnixMilli() },
	})
	var mu sync.Mutex
	var statuses []string
	cancel := service.OnScanProgress(func(snapshot Snapshot) {
		mu.Lock()
		statuses = append(statuses, snapshot.Status)
		mu.Unlock()
	})
	defer cancel()
	if _, err := service.StartScan(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForSnapshot(t, service, "complete")
	mu.Lock()
	defer mu.Unlock()
	if len(statuses) == 0 || statuses[len(statuses)-1] != "complete" {
		t.Fatalf("event statuses = %v, want terminal complete", statuses)
	}
}

func TestCancelScanEmitsCancelledSnapshot(t *testing.T) {
	spec := RootSpec{ID: RootHome, Path: t.TempDir()}
	service := New(Options{
		Roots:      &staticRoots{roots: []RootSpec{spec}},
		ScanRunner: &blockingRunner{},
		Cleaner:    NewFsCleaner(),
		Now:        func() int64 { return time.Now().UnixMilli() },
	})
	var mu sync.Mutex
	terminal := ""
	done := make(chan struct{})
	cancel := service.OnScanProgress(func(snapshot Snapshot) {
		mu.Lock()
		terminal = snapshot.Status
		mu.Unlock()
		close(done)
	})
	defer cancel()
	jobID, err := service.StartScan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	service.CancelScan(context.Background(), jobID)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("no terminal event after cancel")
	}
	mu.Lock()
	defer mu.Unlock()
	if terminal != "cancelled" {
		t.Fatalf("terminal status = %q, want cancelled", terminal)
	}
}

func TestStartScanReplacesJobAndSnapshot(t *testing.T) {
	root := buildSyntheticRoot(t)
	spec := RootSpec{ID: RootHome, Path: filepath.Join(root, ".nex")}
	service := New(Options{
		Roots:      &staticRoots{roots: []RootSpec{spec}},
		ScanRunner: NewInProcessScanRunner(NewFsVolumeProbe(), 0, nil),
		Cleaner:    NewFsCleaner(),
		Now:        func() int64 { return time.Now().UnixMilli() },
	})
	firstJobID, err := service.StartScan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	waitForSnapshot(t, service, "complete")
	first := service.GetSnapshot(context.Background())
	if first == nil || first.JobID != firstJobID {
		t.Fatalf("snapshot after first scan = %+v (jobID %s)", first, firstJobID)
	}
	secondJobID, err := service.StartScan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if secondJobID == firstJobID {
		t.Fatalf("job ids must differ: %s", secondJobID)
	}
	waitForSnapshotJob(t, service, secondJobID, "complete")
	second := service.GetSnapshot(context.Background())
	if second == first {
		t.Fatal("second scan must produce a new snapshot object")
	}
	if second.Status != "complete" || second.JobID != secondJobID {
		t.Fatalf("second snapshot = %+v", second)
	}
}

func TestResolveStorageRoots(t *testing.T) {
	roots := ResolveStorageRoots("/home/u", "/home/u")
	if len(roots) != 1 || roots[0].ID != RootHome || roots[0].Path != filepath.Join("/home/u", ".nex") {
		t.Fatalf("roots = %v", roots)
	}
	roots = ResolveStorageRoots("/home/u", "/data/nex")
	if len(roots) != 2 {
		t.Fatalf("roots = %v, want home + dataBaseDir", roots)
	}
	if roots[0].HasCustomDataBaseDir != true || roots[1].ID != RootDataBaseDir {
		t.Fatalf("roots = %v", roots)
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

type staticRoots struct{ roots []RootSpec }

func (s *staticRoots) ResolveRoots(context.Context) ([]RootSpec, error) {
	return s.roots, nil
}

type blockingRunner struct{}

func (b *blockingRunner) Run(ctx context.Context, request ScanRunRequest) (ScanProgress, error) {
	<-request.Cancel
	return ScanProgress{}, ErrScanAborted
}

func waitForSnapshotJob(t *testing.T, service *Service, jobID, status string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if snapshot := service.GetSnapshot(context.Background()); snapshot != nil && snapshot.Status == status && snapshot.JobID == jobID {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("snapshot for job %s (%s) not reached", jobID, status)
}

func waitForSnapshot(t *testing.T, service *Service, status string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if snapshot := service.GetSnapshot(context.Background()); snapshot != nil && snapshot.Status == status {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot := service.GetSnapshot(context.Background())
	if snapshot == nil {
		t.Fatalf("no snapshot within timeout (want %s)", status)
	} else {
		t.Fatalf("snapshot status = %q, want %q", snapshot.Status, status)
	}
}
