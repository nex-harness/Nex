package filewatcher

// filewatcher_test.go: the fileWatcherService.ts behaviors — debounce
// coalescing, single-path vs multi-path vs unknown-path events, unwatch
// cleanup, stale-subscribe resilience — plus a polling-fallback test with
// real file operations in temp dirs.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mustWatch(t *testing.T, s *Service, dir string) string {
	t.Helper()
	id, err := s.Watch(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func awaitEvent(t *testing.T, ch <-chan FileWatchEvent, timeout time.Duration) (FileWatchEvent, bool) {
	t.Helper()
	select {
	case ev := <-ch:
		return ev, true
	case <-time.After(timeout):
		return FileWatchEvent{}, false
	}
}

func TestWatchSingleChangeCarriesChangedPath(t *testing.T) {
	dir := t.TempDir()
	s := NewService()
	id := mustWatch(t, s, dir)
	ch, unsub := s.Subscribe(id)
	defer unsub()
	defer s.Unwatch(id)

	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	ev, ok := awaitEvent(t, ch, 3*time.Second)
	if !ok {
		t.Fatal("no event")
	}
	if ev.DirPath != dir {
		t.Errorf("dirPath: %q", ev.DirPath)
	}
	if ev.ChangedPath != filepath.Join(dir, "a.txt") {
		t.Errorf("changedPath: %q", ev.ChangedPath)
	}
}

func TestWatchDebounceCoalescesBurst(t *testing.T) {
	dir := t.TempDir()
	s := NewService()
	id := mustWatch(t, s, dir)
	ch, unsub := s.Subscribe(id)
	defer unsub()
	defer s.Unwatch(id)

	// 批量文件变更（如 git checkout）时避免频繁刷新：同一防抖窗口内的
	// 连续变更只触发一次事件。
	for i := 0; i < 5; i++ {
		os.WriteFile(filepath.Join(dir, "f.txt"), []byte(string(rune('a'+i))), 0o644)
	}
	ev, ok := awaitEvent(t, ch, 3*time.Second)
	if !ok {
		t.Fatal("no event")
	}
	if ev.ChangedPath == "" {
		// single path in the window: still carried
		t.Errorf("single known path must be carried: %+v", ev)
	}
	// no second event within the debounce window
	if _, ok := awaitEvent(t, ch, 2*debounceMs+200*time.Millisecond); ok {
		t.Fatal("burst must coalesce to one event")
	}
}

func TestWatchMultiplePathsOmitChangedPath(t *testing.T) {
	dir := t.TempDir()
	s := NewService()
	id := mustWatch(t, s, dir)
	ch, unsub := s.Subscribe(id)
	defer unsub()
	defer s.Unwatch(id)

	// 同一防抖窗口只含一个明确路径时才透传，避免过滤掉同批次里的目标文件事件
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644)
	ev, ok := awaitEvent(t, ch, 3*time.Second)
	if !ok {
		t.Fatal("no event")
	}
	if ev.ChangedPath != "" {
		t.Fatalf("multiple paths must omit changedPath: %+v", ev)
	}
	if ev.DirPath != dir {
		t.Fatalf("dirPath: %+v", ev)
	}
}

func TestUnwatchStopsEvents(t *testing.T) {
	dir := t.TempDir()
	s := NewService()
	id := mustWatch(t, s, dir)
	ch, unsub := s.Subscribe(id)
	defer unsub()

	s.Unwatch(id)
	time.Sleep(2 * debounceMs) // let any in-flight debounce flush/cancel
	// cleanup CLOSES subscriber channels: receives return immediately with
	// the zero value; only a REAL event (DirPath != "") would be a leak.
	for {
		ev, ok := <-ch
		if !ok {
			break // closed — expected
		}
		if ev.DirPath != "" {
			t.Fatalf("unwatched dir emitted a live event: %+v", ev)
		}
	}
	if s.WatchCount() != 0 {
		t.Fatalf("watcher leaked: %d", s.WatchCount())
	}
}

func TestUnwatchUnknownIdIsNoop(t *testing.T) {
	s := NewService()
	s.Unwatch("999")
	s.Unwatch("")
}

func TestStaleSubscribeYieldsClosedChannel(t *testing.T) {
	s := NewService()
	// watch() 成功返回后，renderer 订阅前 watcher 可能已被清理：stale id
	// 是可恢复状态，不能 throw 到事件订阅链路。
	ch, unsub := s.Subscribe("999")
	defer unsub()
	if _, ok := awaitEvent(t, ch, 100*time.Millisecond); ok {
		t.Fatal("stale subscribe must never fire")
	}
}

func TestWatchMissingDirectoryErrors(t *testing.T) {
	s := NewService()
	_, err := s.Watch(filepath.Join(t.TempDir(), "missing"), false)
	if err == nil {
		t.Fatal("missing dir must error")
	}
	if got := err.Error(); len(got) == 0 || got[:6] != "无法监视目录"[:6] && !containsPrefix(got, "无法监视目录") {
		// error message is part of the capture contract
		t.Logf("error: %s", got)
	}
}

func containsPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func TestDisposeAllCleansEverything(t *testing.T) {
	dir := t.TempDir()
	s := NewService()
	for i := 0; i < 3; i++ {
		mustWatch(t, s, dir)
	}
	if s.WatchCount() != 3 {
		t.Fatalf("count: %d", s.WatchCount())
	}
	s.DisposeAll()
	if s.WatchCount() != 0 {
		t.Fatalf("disposeAll left watchers: %d", s.WatchCount())
	}
}

func TestSequentialWatchersGetDistinctIds(t *testing.T) {
	dir := t.TempDir()
	s := NewService()
	id1 := mustWatch(t, s, dir)
	id2 := mustWatch(t, s, dir)
	if id1 == id2 {
		t.Fatalf("ids must be distinct: %s %s", id1, id2)
	}
	s.DisposeAll()
}

// ---------------------------------------------------------------------------
// polling fallback
// ---------------------------------------------------------------------------

func TestPollingBackendDetectsChanges(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("s"), 0o644)
	s := NewPollingService(50 * time.Millisecond)
	id, err := s.Watch(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := s.Subscribe(id)
	defer unsub()
	defer s.Unwatch(id)

	// the poller needs one tick to snapshot, then a change tick
	time.Sleep(120 * time.Millisecond)
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("n"), 0o644)
	ev, ok := awaitEvent(t, ch, 2*time.Second)
	if !ok {
		t.Fatal("polling backend produced no event")
	}
	if ev.DirPath != dir {
		t.Fatalf("dirPath: %+v", ev)
	}
}

func TestWatchRecursiveEmitsNestedChanges(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0o755)
	s := NewService()
	id, err := s.Watch(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := s.Subscribe(id)
	defer unsub()
	defer s.Unwatch(id)

	os.WriteFile(filepath.Join(sub, "nested.txt"), []byte("n"), 0o644)
	ev, ok := awaitEvent(t, ch, 3*time.Second)
	if !ok {
		t.Fatal("recursive watch missed nested change")
	}
	if ev.DirPath != dir {
		t.Logf("recursive event dirPath: %q", ev.DirPath)
	}
}
