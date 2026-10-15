package fsx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAtomicWriteBasic covers the happy path plus mode preservation.
func TestAtomicWriteBasic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "setting.json")
	ctx := context.Background()

	if err := AtomicWriteText(ctx, target, "first", nil); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "first" {
		t.Fatalf("after first write: %q, %v", got, err)
	}

	// Overwrite: new content, no temp siblings left behind.
	if err := AtomicWriteText(ctx, target, "second", nil); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "second" {
		t.Fatalf("after second write: %q", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file leaked: %s", e.Name())
		}
	}
}

// TestAtomicWritePreservesMode pins mode preservation: a 0600 file stays 0600
// after an atomic rewrite (the Phase-1 fsx behaviour; see atomic.go comment).
func TestAtomicWritePreservesMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "secret.json")
	ctx := context.Background()
	if err := AtomicWriteText(ctx, target, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteText(ctx, target, `{"a":1}`, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
}

// TestAtomicWriteCrashBetweenTempAndRename is the step-3.1 crash test: "kill
// between temp write and rename → either old or new content, never partial".
//
// The kill is simulated with the RunRename seam (the same role options.runRename
// plays in the TS test): the write completes its temp file, then the "process
// dies" — the callback returns WITHOUT renaming and without cleaning up, which
// is exactly the state a SIGKILL leaves behind. The target must still hold the
// OLD content byte-for-byte, and a follow-up writer must recover.
func TestAtomicWriteCrashBetweenTempAndRename(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "task.json")
	ctx := context.Background()

	oldContent := strings.Repeat("old-content\n", 500)
	if err := AtomicWriteText(ctx, target, oldContent, nil); err != nil {
		t.Fatal(err)
	}

	crashErr := &KillSimulationError{Err: errors.New("simulated kill between temp write and rename")}
	err := AtomicWriteText(ctx, target, strings.Repeat("new-content\n", 700), &AtomicWriteOptions{
		RunRename: func(renameFile func() error) error {
			// "Process killed" here: neither rename nor cleanup runs.
			return crashErr
		},
	})
	if !errors.Is(err, crashErr) {
		t.Fatalf("write should surface the crash error, got %v", err)
	}

	// Invariant 1: the target is the OLD content, complete — never a mix.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != oldContent {
		t.Fatalf("target corrupted after crash: %d bytes, want old %d bytes", len(got), len(oldContent))
	}

	// Invariant 2: the orphaned temp exists (the crash left it), and a
	// subsequent writer succeeds and cleans stale temps once they age out.
	var orphaned bool
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "task.json.") && strings.HasSuffix(e.Name(), ".tmp") {
			orphaned = true
		}
	}
	if !orphaned {
		t.Fatal("expected the crash to leave an orphaned temp file")
	}
	newContent := "recovered"
	if err := AtomicWriteText(ctx, target, newContent, &AtomicWriteOptions{
		// Pretend the orphan is old enough to be stale so this write cleans it.
		Now: func() time.Time { return time.Now().Add(2 * defaultTempFileStale) },
	}); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(target)
	if err != nil || string(got) != newContent {
		t.Fatalf("recovery write: %q, %v", got, err)
	}
	entries, _ = os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("stale temp not cleaned: %s", e.Name())
		}
	}
}

// TestAtomicWriteFaultInjectionAtRename uses the fault injector (the ported
// fsFaultInjection hook) to fail the rename itself: the write must fail, the
// temp must be cleaned, and the target must keep its old content.
func TestAtomicWriteFaultInjectionAtRename(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.json")
	ctx := context.Background()
	if err := AtomicWriteText(ctx, target, "before", nil); err != nil {
		t.Fatal(err)
	}

	inj := NewFaultInjector([]FsFaultRuleConfig{{
		ID: "fault-rename", Code: "EFAULTR",
		Operations:   []FsFaultOperation{FaultRename},
		PathEndsWith: "config.json",
	}})
	err := AtomicWriteText(ctx, target, "after", &AtomicWriteOptions{Injector: inj})
	if !IsInjectedFsFaultError(err) {
		t.Fatalf("expected injected fault, got %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "before" {
		t.Fatalf("target changed despite fault: %q", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp leaked after fault: %s", e.Name())
		}
	}
	hits := inj.GetHits()
	if len(hits) != 1 || hits[0].ID != "fault-rename" || hits[0].MatchIndex != 1 {
		t.Fatalf("hits = %+v", hits)
	}
}

// TestAtomicWriteFaultInjectionAtMkdirAndWrite covers the two earlier seams.
func TestAtomicWriteFaultInjectionAtMkdirAndWrite(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   FsFaultOperation
	}{
		{"mkdir", FaultMkdir},
		{"writeFile", FaultWriteFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "data.json")
			inj := NewFaultInjector([]FsFaultRuleConfig{{
				ID: "f", Code: "EFAULT", Operations: []FsFaultOperation{tc.op}, PathIncludes: dir,
			}})
			err := AtomicWriteText(context.Background(), target, "x", &AtomicWriteOptions{Injector: inj})
			if !IsInjectedFsFaultError(err) {
				t.Fatalf("expected injected fault at %s, got %v", tc.op, err)
			}
			if _, statErr := os.Stat(target); statErr == nil {
				t.Fatal("target must not exist after a fault before rename")
			}
		})
	}
}

// TestFaultInjectorRules pins the injector itself: matching (operation, path
// filters), maxMatches, hit records, reset.
func TestFaultInjectorRules(t *testing.T) {
	inj := NewFaultInjector([]FsFaultRuleConfig{
		{ID: "r1", Code: "E1", Operations: []FsFaultOperation{FaultRename}, PathIncludes: "tasks", MaxMatches: 2},
		{ID: "r2", Code: "E2", PathEndsWith: ".sqlite"}, // operations omitted → any
	})
	if !inj.IsEnabled() {
		t.Fatal("injector with rules must be enabled")
	}

	// r1: matches only rename on paths containing "tasks", exactly twice
	// (MaxMatches 2; default is 1). Note r2 (.sqlite, any op) matches the same
	// path, so the third call faults again through r2 — that is TS behaviour
	// too (first matching rule throws; each rule has its own budget), so the
	// "budget exhausted" assertion uses a path r2 does not match.
	for i := 0; i < 3; i++ {
		err := inj.MaybeThrow(FsFaultCheckInput{Operation: FaultRename, Path: "/data/tasks-index.json"})
		if i < 2 && !IsInjectedFsFaultError(err) {
			t.Fatalf("attempt %d: expected fault, got %v", i, err)
		}
		if i == 2 && err != nil {
			t.Fatalf("attempt %d: maxMatches exceeded, still faulted", i)
		}
	}
	// r1 must not match a non-rename operation on the same path (r2 only
	// matches .sqlite, so .json keeps the assertion about r1 alone).
	if err := inj.MaybeThrow(FsFaultCheckInput{Operation: FaultWriteFile, Path: "/data/tasks-index.json"}); IsInjectedFsFaultError(err) {
		t.Fatal("r1 must not match writeFile")
	}
	// r2 matches any operation ending with .sqlite.
	if err := inj.MaybeThrow(FsFaultCheckInput{Operation: FaultStat, Path: `C:\data\db.sqlite`}); !IsInjectedFsFaultError(err) {
		t.Fatalf("r2 should match (backslash path normalization), got %v", err)
	}
	hits := inj.GetHits()
	if len(hits) != 3 {
		t.Fatalf("hits = %d, want 3", len(hits))
	}

	inj.Reset()
	if len(inj.GetHits()) != 0 {
		t.Fatal("reset must clear hits")
	}
	if err := inj.MaybeThrow(FsFaultCheckInput{Operation: FaultRename, Path: "/data/tasks-index.sqlite"}); !IsInjectedFsFaultError(err) {
		t.Fatal("reset must restore match budgets")
	}
}

// TestFaultInjectorDisabled pins the empty injector.
func TestFaultInjectorDisabled(t *testing.T) {
	inj := NewFaultInjector(nil)
	if inj.IsEnabled() {
		t.Fatal("no rules must mean disabled")
	}
	if err := inj.MaybeThrow(FsFaultCheckInput{Operation: FaultAny, Path: "/x"}); err != nil {
		t.Fatalf("disabled injector must never throw, got %v", err)
	}
}

// TestAtomicWriteConcurrent is the two-writers race: with the lock file, both
// writes complete and the final content is exactly one of them — never a mix.
// TestAtomicWriteConcurrent is the two-writers race: with the lock file, both
// writes complete and the final content is exactly one of them — never a mix.
//
// 修复依据：并发写者的锁等待时间不是常数，gate 全量并跑几十个测试包时
// 临界区会被抢占拉长，8s 默认超时在高负载下不足。本测试显式给足余量，
// 不改变产品的默认锁等待语义。
func TestAtomicWriteConcurrent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "shared.json")
	ctx := context.Background()
	const writers = 8
	done := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			content := strings.Repeat(string(rune('a'+i)), 2048)
			done <- AtomicWriteText(ctx, target, content, &AtomicWriteOptions{LockMaxWait: 60 * time.Second})
		}(i)
	}
	for i := 0; i < writers; i++ {
		if err := <-done; err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2048 {
		t.Fatalf("final content has %d bytes, want exactly one writer's 2048 (mixed write)", len(got))
	}
	for _, b := range got {
		if b != got[0] {
			t.Fatal("content mixes two writers — write was not atomic")
		}
	}
}
