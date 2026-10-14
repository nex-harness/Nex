// Package filewatcher ports packages/services/src/fileWatcher/* (step 3.2):
// path-granularity watchers with a 150ms debounce, a changedPath that is only
// present when the debounce window saw exactly ONE known path, and a final
// event + cleanup when the watched directory errors out.
//
// Backend notes (fsnotify vs Node fs.watch):
//
//   - Linux: fsnotify uses inotify; recursive watching requires the kernel to
//     add a watch per directory, and the default fs.inotify_max_user_watches
//     (often 8192) caps deep trees — the TS's `recursive: true` on Linux hits
//     the same wall because Node's fs.watch(recursive) is also inotify-backed
//     there and returns EPERM/EMFILE past the cap.
//   - macOS: FSEvents make recursive watches cheap in both runtimes.
//   - Windows: ReadDirectoryChangesW is recursive in both runtimes.
//
// Because of the Linux watch-count wall and fsnotify's non-recursive core,
// the recursive mode walks the tree adding watches; when a watch cannot be
// added (EMFILE/ENOSPC) it falls back to polling that subtree. A polling
// fallback for the whole watcher is also available (used by tests and
// degraded environments) via NewPollingService.
package filewatcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ChannelName is ServiceChannels.FileWatcher.
const ChannelName = "file-watcher"

// debounceMs is DEBOUNCE_MS.
const debounceMs = 150 * time.Millisecond

// FileWatchEvent is FileWatchEvent from protocol.ts.
type FileWatchEvent struct {
	DirPath     string
	ChangedPath string // "" means omitted (multiple/unknown paths)
}

type watcherInstance struct {
	id             string
	path           string
	recursive      bool
	events         chan FileWatchEvent // buffered; emitters never block
	subscribers    map[chan FileWatchEvent]bool
	pendingPaths   map[string]bool
	hasUnknownPath bool
	debounceTimer  *time.Timer
	backend        interface{ stop() }
	poller         *dirPoller
	closed         bool
	mu             sync.Mutex
}

// Service is the IFileWatcherService implementation.
type Service struct {
	watchers map[string]*watcherInstance
	nextID   int64
	// newBackend creates the OS watcher; swappable for tests.
	newBackend func(paths ...string) (backend, error)
	mu         sync.Mutex
	now        func() time.Time
}

type backend interface {
	events() chan fsnotify.Event
	errors() chan error
	stop()
	add(path string) error
}

type fsnotifyBackend struct {
	w *fsnotify.Watcher
}

func (b *fsnotifyBackend) events() chan fsnotify.Event { return b.w.Events }
func (b *fsnotifyBackend) errors() chan error          { return b.w.Errors }
func (b *fsnotifyBackend) stop()                       { b.w.Close() }
func (b *fsnotifyBackend) add(path string) error       { return b.w.Add(path) }

// NewService builds the fsnotify-backed watcher service.
func NewService() *Service {
	return &Service{
		watchers:   map[string]*watcherInstance{},
		newBackend: func(paths ...string) (backend, error) { return newFSNotifyBackend() },
		now:        time.Now,
	}
}

// NewPollingService builds a polling-backed service (no inotify/FSEvents
// dependency — the fallback path). Polling has no OS event limits but adds
// pollInterval latency; it exists so the watcher keeps working in
// watch-exhausted or exotic environments.
func NewPollingService(pollInterval time.Duration) *Service {
	s := NewService()
	s.newBackend = func(paths ...string) (backend, error) { return newPollingBackend(pollInterval) }
	return s
}

func newFSNotifyBackend() (backend, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &fsnotifyBackend{w: w}, nil
}

// Watch ports watch(): returns a fresh watcher id.
func (s *Service) Watch(path string, recursive bool) (string, error) {
	s.mu.Lock()
	id := fmt.Sprintf("%d", s.nextID)
	s.nextID++
	s.mu.Unlock()

	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		// Node fs.watch rejects with "ENOENT: no such file or directory,
		// watch '<path>'" and the service wraps it with 无法监视目录.
		if err != nil {
			return "", fmt.Errorf("无法监视目录 '%s': ENOENT: no such file or directory, watch '%s'", path, path)
		}
		return "", fmt.Errorf("无法监视目录 '%s': ENOTDIR: not a directory, watch '%s'", path, path)
	}

	be, err := s.newBackend(path)
	if err != nil {
		return "", fmt.Errorf("无法监视目录 '%s': %s", path, err.Error())
	}

	inst := &watcherInstance{
		id:           id,
		path:         path,
		recursive:    recursive,
		events:       make(chan FileWatchEvent, 64),
		subscribers:  map[chan FileWatchEvent]bool{},
		pendingPaths: map[string]bool{},
	}
	// 添加根 watch；递归模式对整棵子树建 watch，单目录失败降级为轮询。
	if err := be.add(path); err != nil {
		be.stop()
		return "", fmt.Errorf("无法监视目录 '%s': %s", path, err.Error())
	}
	inst.backend = be
	if recursive {
		poller := &dirPoller{interval: 500 * time.Millisecond, poll: func(dir string) { inst.recordPath(dir, dir) }}
		inst.poller = poller
		addTreeWatch(be, path, poller)
		poller.start()
	}

	s.mu.Lock()
	if s.watchers == nil {
		s.watchers = map[string]*watcherInstance{}
	}
	s.watchers[id] = inst
	s.mu.Unlock()

	go inst.run(be, func() {
		s.mu.Lock()
		if cur, ok := s.watchers[id]; ok && cur == inst {
			delete(s.watchers, id)
		}
		s.mu.Unlock()
	})
	return id, nil
}

// addTreeWatch walks dir and adds a watch for every subdirectory; failures
// (Linux inotify limits: EMFILE/ENOSPC) degrade that subtree to polling.
func addTreeWatch(be backend, dir string, poller *dirPoller) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if err := be.add(sub); err != nil {
			poller.add(sub)
			continue
		}
		addTreeWatch(be, sub, poller)
	}
}

// run is the instance's event pump: backend events feed the debounce,
// backend errors fire the final event and clean up (the service map entry is
// dropped by onClosed).
func (w *watcherInstance) run(be backend, onClosed func()) {
	for {
		select {
		case ev, ok := <-be.events():
			if !ok {
				return
			}
			if ev.Op == 0 && ev.Name == "" {
				continue
			}
			w.recordPath(w.path, ev.Name)
		case err, ok := <-be.errors():
			if !ok {
				return
			}
			_ = err // the error text is not part of the FileWatchEvent contract
			// 监视目录被删除/重命名时，发送最终事件并清理。
			w.emit(FileWatchEvent{DirPath: w.path})
			w.cleanup()
			onClosed()
			return
		}
	}
}

// ownerCleanup is set by the service to remove the instance from its map.
var _ = struct{}{}

// recordPath applies the debounce rules from the TS:
//   - a known path accumulates in pendingChangedPaths;
//   - an unknown (empty) filename sets hasUnknownChangedPath;
//   - every event resets the timer; the window flushes ONE event whose
//     changedPath is present only when the window saw exactly one known path
//     and no unknown one.
func (w *watcherInstance) recordPath(watchedDir, fileName string) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	trimmed := strings.TrimSpace(fileName)
	if trimmed == "" {
		w.hasUnknownPath = true
	} else {
		w.pendingPaths[resolveClean(watchedDir, trimmed)] = true
	}
	if w.debounceTimer != nil {
		w.debounceTimer.Stop()
	}
	w.debounceTimer = time.AfterFunc(debounceMs, func() { w.flush() })
	w.mu.Unlock()
}

func resolveClean(dir, name string) string {
	if filepath.IsAbs(name) {
		return filepath.Clean(name)
	}
	return filepath.Clean(filepath.Join(dir, name))
}

func (w *watcherInstance) flush() {
	w.mu.Lock()
	w.debounceTimer = nil
	onlyChangedPath := ""
	if !w.hasUnknownPath && len(w.pendingPaths) == 1 {
		for p := range w.pendingPaths {
			onlyChangedPath = p
		}
	}
	w.pendingPaths = map[string]bool{}
	w.hasUnknownPath = false
	w.mu.Unlock()
	if onlyChangedPath != "" {
		w.emit(FileWatchEvent{DirPath: w.path, ChangedPath: onlyChangedPath})
	} else {
		w.emit(FileWatchEvent{DirPath: w.path})
	}
}

// emit fans the event out to subscribers without blocking: a slow
// subscriber's buffered channel drops the event (the RPC layer coalesces
// refreshes anyway; blocking would stall the fsnotify pump for ALL watchers).
func (w *watcherInstance) emit(ev FileWatchEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	for ch := range w.subscribers {
		select {
		case ch <- ev:
		default: // drop for slow subscribers
		}
	}
}

func (w *watcherInstance) subscribe() (<-chan FileWatchEvent, func()) {
	ch := make(chan FileWatchEvent, 64)
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		closed := make(chan FileWatchEvent)
		return closed, func() {}
	}
	w.subscribers[ch] = true
	w.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			w.mu.Lock()
			delete(w.subscribers, ch)
			w.mu.Unlock()
		})
	}
}

func (w *watcherInstance) cleanup() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	timer := w.debounceTimer
	subs := w.subscribers
	w.subscribers = map[chan FileWatchEvent]bool{}
	w.debounceTimer = nil
	w.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if w.backend != nil {
		w.backend.stop()
	}
	if w.poller != nil {
		w.poller.stop()
	}
	for ch := range subs {
		close(ch)
	}
}

// Unwatch ports unwatch(): cleanup by id; unknown ids are a no-op.
func (s *Service) Unwatch(id string) {
	s.mu.Lock()
	inst, ok := s.watchers[id]
	if ok {
		delete(s.watchers, id)
	}
	s.mu.Unlock()
	if ok {
		inst.cleanup()
	}
}

// DisposeAll ports disposeAll().
func (s *Service) DisposeAll() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.watchers))
	for id := range s.watchers {
		ids = append(ids, id)
	}
	instances := make([]*watcherInstance, 0, len(ids))
	for _, id := range ids {
		instances = append(instances, s.watchers[id])
		delete(s.watchers, id)
	}
	s.mu.Unlock()
	for _, inst := range instances {
		inst.cleanup()
	}
}

// Subscribe ports onDynamicChange(id): stale ids yield a closed channel
// (Event.None) instead of an error — the TS logs and returns Event.None so a
// removed watcher cannot crash the event subscription path.
func (s *Service) Subscribe(id string) (<-chan FileWatchEvent, func()) {
	s.mu.Lock()
	inst, ok := s.watchers[id]
	s.mu.Unlock()
	if !ok {
		// 忽略已失效的文件监听订阅：stale watcher id 是可恢复状态。
		closed := make(chan FileWatchEvent)
		return closed, func() {}
	}
	return inst.subscribe()
}

// WatchCount reports the open watcher count (memory diagnostics parity).
func (s *Service) WatchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.watchers)
}
