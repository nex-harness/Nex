package setting

// updatebasedir_test.go: the updateDataBaseDir filesystem-fault suite (the
// step-3.5 "Special" requirement) plus crash-safety and the two-writer
// interop test against the shared lock file.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/fsx"
	"github.com/tylinux/nex/go/internal/services/paths"
)

func TestUpdateDataBaseDirCopiesAndPersists(t *testing.T) {
	oldBase := t.TempDir()
	newBase := t.TempDir()
	// Seed the old data dir.
	oldV2 := filepath.Join(oldBase, ".nex", "v2")
	if err := os.MkdirAll(filepath.Join(oldV2, "sessions", "abc"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldV2, "sessions", "abc", "task.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// setting.json must NOT be copied (bootstrap intermediate).
	if err := os.WriteFile(filepath.Join(oldV2, "setting.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := paths.New("", oldBase)
	svc := NewService(ServiceOptions{
		SettingsFile: filepath.Join(oldV2, "setting.json"),
		Paths:        p,
		EnvSnapshot:  func() map[string]string { return map[string]string{} },
	})
	newDir := newBase
	if err := svc.UpdateDataBaseDir(context.Background(), &newDir); err != nil {
		t.Fatal(err)
	}

	// Data copied to the new location, setting.json NOT copied.
	copied, err := os.ReadFile(filepath.Join(newBase, ".nex", "v2", "sessions", "abc", "task.json"))
	if err != nil || string(copied) != "{}" {
		t.Fatalf("task.json not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newBase, ".nex", "v2", "setting.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("setting.json must not be copied (TS filter)")
	}
	// The dataBaseDir setting was persisted.
	data, err := os.ReadFile(filepath.Join(oldV2, "setting.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"dataBaseDir"`) {
		t.Fatalf("dataBaseDir not persisted: %s", data)
	}
}

func TestUpdateDataBaseDirSameDirSkipsCopy(t *testing.T) {
	base := t.TempDir()
	p := paths.New("", base)
	svc := NewService(ServiceOptions{
		SettingsFile: filepath.Join(base, ".nex", "v2", "setting.json"),
		Paths:        p,
		EnvSnapshot:  func() map[string]string { return map[string]string{} },
	})
	same := base
	if err := svc.UpdateDataBaseDir(context.Background(), &same); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateDataBaseDirForbiddenWindowsInstallDir(t *testing.T) {
	// The Windows forbidden-install-dir guard is validated through the seam
	// (the pure Windows table lives in the paths package, tested there).
	svc := NewService(ServiceOptions{
		ValidateTarget: func(string) paths.ValidateDataBaseDirTargetResult {
			return paths.ValidateDataBaseDirTargetResult{
				OK:           false,
				Code:         paths.DataBaseDirForbiddenWindowsInstallDirErrorCode,
				ForbiddenDir: `C:\Users\u\AppData\Local\Programs\nex`,
			}
		},
		EnvSnapshot: func() map[string]string { return map[string]string{} },
	})
	target := "/new"
	err := svc.UpdateDataBaseDir(context.Background(), &target)
	if err == nil {
		t.Fatal("forbidden target must be rejected")
	}
	fsErr := &fsx.Error{}
	if !errors.As(err, &fsErr) || fsErr.Code != paths.DataBaseDirForbiddenWindowsInstallDirErrorCode {
		t.Fatalf("wrong error: %v", err)
	}
	if !strings.Contains(err.Error(), "DATA_BASE_DIR_FORBIDDEN_WINDOWS_INSTALL_DIR") {
		t.Fatalf("error message must carry the code: %v", err)
	}
}

func TestUpdateDataBaseDirDiskFull(t *testing.T) {
	// Disk full: ENOSPC on the copy seam propagates; the dataBaseDir setting
	// is NOT persisted (the update never runs).
	oldBase := t.TempDir()
	p := paths.New("", oldBase)
	diskFull := errors.New("ENOSPC: no space left on device, write '/new/.nex/v2/x'")
	svc := NewService(ServiceOptions{
		Paths:       p,
		EnvSnapshot: func() map[string]string { return map[string]string{} },
		CopyDataDir: func(ctx context.Context, from, to string) error { return diskFull },
	})
	target := "/new-base"
	err := svc.UpdateDataBaseDir(context.Background(), &target)
	if !errors.Is(err, diskFull) {
		t.Fatalf("disk full must propagate, got %v", err)
	}
}

func TestUpdateDataBaseDirPartialCopy(t *testing.T) {
	// Partial copy: some files land, then the copy fails. The failure
	// propagates and the setting is not flipped (the next attempt re-copies;
	// force:false semantics make existing files an error, matching TS fs.cp).
	oldBase := t.TempDir()
	p := paths.New("", oldBase)
	var copiedOnce bool
	svc := NewService(ServiceOptions{
		Paths:       p,
		EnvSnapshot: func() map[string]string { return map[string]string{} },
		CopyDataDir: func(ctx context.Context, from, to string) error {
			if !copiedOnce {
				copiedOnce = true
				// Simulate a partial landing: one file written, then crash.
				_ = os.MkdirAll(filepath.Join(to, ".nex", "v2"), 0o777)
				_ = os.WriteFile(filepath.Join(to, ".nex", "v2", "partial.txt"), []byte("x"), 0o644)
				return errors.New("partial copy aborted")
			}
			// A re-run hits the existing partial file: EEXIST-family error.
			return &CopyExistsError{Path: filepath.Join(to, ".nex", "v2", "partial.txt")}
		},
	})
	target := t.TempDir()
	firstErr := svc.UpdateDataBaseDir(context.Background(), &target)
	if firstErr == nil || !strings.Contains(firstErr.Error(), "partial copy aborted") {
		t.Fatalf("first attempt must fail with the partial abort, got %v", firstErr)
	}
	secondErr := svc.UpdateDataBaseDir(context.Background(), &target)
	if secondErr == nil || !strings.Contains(secondErr.Error(), "Target already exists") {
		t.Fatalf("partial-copy rerun must surface EEXIST, got %v", secondErr)
	}
}

func TestCopyDataDirectorySkipsSymlinks(t *testing.T) {
	oldBase := t.TempDir()
	newBase := t.TempDir()
	oldV2 := filepath.Join(oldBase, ".nex", "v2")
	if err := os.MkdirAll(filepath.Join(oldV2, "real"), 0o777); err != nil {
		t.Fatal(err)
	}
	// Symlinked dir + symlinked file must be skipped (Windows EPERM filter).
	if err := os.Symlink(filepath.Join(oldV2, "real"), filepath.Join(oldV2, "link")); err != nil {
		t.Fatal(err)
	}
	if err := CopyDataDirectory(context.Background(), oldBase, newBase); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(newBase, ".nex", "v2", "real")); err != nil {
		t.Fatal("real dir not copied")
	}
	if _, err := os.Lstat(filepath.Join(newBase, ".nex", "v2", "link")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("symlink must be skipped")
	}
}

func TestUpdateDataBaseDirConcurrentWriter(t *testing.T) {
	// Concurrent writer on the settings file while the data dir copy runs:
	// the copy skips setting.json* entirely, so the writer's rename cannot
	// race the copy (this is WHY the TS filter exists — a vanished lock file
	// made fs.cp fail with ENOENT).
	oldBase := t.TempDir()
	newBase := t.TempDir()
	oldV2 := filepath.Join(oldBase, ".nex", "v2")
	settingsFile := filepath.Join(oldV2, "setting.json")
	if err := os.MkdirAll(oldV2, 0o777); err != nil {
		t.Fatal(err)
	}
	svc := NewService(ServiceOptions{
		SettingsFile: settingsFile,
		Paths:        paths.New("", oldBase),
		EnvSnapshot:  func() map[string]string { return map[string]string{} },
	})
	// A concurrent update WHILE the copy runs.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := svc.Update(context.Background(), map[string]any{"locale": "en-US"}, nil); err != nil {
			t.Errorf("concurrent update: %v", err)
		}
	}()
	// Copy concurrently.
	if err := CopyDataDirectory(context.Background(), oldBase, newBase); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}

func TestCrashSafetyKillBetweenTempAndRename(t *testing.T) {
	// Kill between temp write and rename: the file keeps the OLD content and
	// the orphan temp file is left behind (recovered by stale-temp cleanup on
	// the next write) — never a partial settings file.
	dir := t.TempDir()
	file := filepath.Join(dir, "setting.json")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"locale":"zh-CN"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &Store{SettingsFile: file}
	sanitized, err := ValidateSettings(map[string]any{"locale": "en-US"})
	if err != nil {
		t.Fatal(err)
	}
	crashErr := &fsx.KillSimulationError{Err: errors.New("simulated SIGKILL")}
	err = store.WriteSettings(context.Background(), NormalizeSettings(sanitized), WriteSettingsOptions{
		RunExclusiveCommit: func(commit func() error) error {
			// The crash happens inside the rename critical section.
			return crashErr
		},
	})
	if err == nil {
		t.Fatal("write must fail with the kill error")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"zh-CN"`) {
		t.Fatalf("old content must survive the crash, got: %s", data)
	}
	// The orphan temp survives (a killed process runs no cleanup).
	foundTemp := false
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "setting.json.") && strings.HasSuffix(e.Name(), ".tmp") {
			foundTemp = true
		}
	}
	if !foundTemp {
		t.Fatal("crash simulation must leave the orphan temp file")
	}
	// The next successful write recovers (stale-temp cleanup) and the reader
	// still gets valid settings.
	store2 := &Store{SettingsFile: file, Now: func() time.Time { return time.Now().Add(time.Hour) }}
	result := store2.ReadSettingsWithMeta(context.Background())
	if locale, _ := result.Settings.Get("locale"); locale != "zh-CN" {
		t.Fatalf("reader after crash: %v", locale)
	}
}

func TestTwoServicesSameFileInteroperate(t *testing.T) {
	// Two service instances (Node and Go during the transition, modelled as
	// two Services) write the same setting.json. Each instance's OWN queue
	// serializes its updates; the shared <file>.lock (fsx, same convention
	// as the TS atomicFileUtils) serializes the atomic renames ACROSS
	// instances. Two SEPARATE queues can interleave read-modify-write cycles
	// — exactly like two Node processes in the TS — so the assertion is the
	// one the TS contract makes: the file is always VALID settings (either
	// writer's merge), never corrupt, and sequential updates from both
	// sides accumulate.
	dir := t.TempDir()
	file := filepath.Join(dir, "setting.json")
	a := NewService(ServiceOptions{SettingsFile: file, RetrySleep: func(time.Duration) {}})
	b := NewService(ServiceOptions{SettingsFile: file, RetrySleep: func(time.Duration) {}})

	// Sequential interleave: A then B then A — all fields accumulate.
	for _, step := range []struct {
		svc   *Service
		patch map[string]any
	}{
		{a, map[string]any{"locale": "en-US"}},
		{b, map[string]any{"lastActiveTabIndex": int64(2)}},
		{a, map[string]any{"taskAutoArchiveEnabled": true}},
	} {
		if err := step.svc.Update(context.Background(), step.patch, nil); err != nil {
			t.Fatalf("update: %v", err)
		}
	}
	settings, err := a.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if locale, _ := settings.Get("locale"); locale != "en-US" {
		t.Fatal("A's locale lost")
	}
	if tab, _ := settings.Get("lastActiveTabIndex"); toInt64(tab) != 2 {
		t.Fatal("B's tabIndex lost")
	}
	if enabled, _ := settings.Get("taskAutoArchiveEnabled"); enabled != true {
		t.Fatal("A's second update lost")
	}

	// Concurrent renames from both instances: the file must never be corrupt
	// (the lock serializes the renames; any final state is a valid merge).
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			svc := a
			if i%2 == 1 {
				svc = b
			}
			_ = svc.Update(context.Background(), map[string]any{"lastActiveTaskByWorkspace": map[string]any{string(rune('a' + i)): "t"}}, nil)
		}(i)
	}
	wg.Wait()
	final := a.store.ReadSettingsWithMeta(context.Background())
	if locale, _ := final.Settings.Get("locale"); locale != "en-US" {
		t.Fatalf("file corrupt after concurrent writers: locale=%v", locale)
	}
}
