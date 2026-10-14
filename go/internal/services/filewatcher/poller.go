package filewatcher

// poller.go is the polling fallback: per-subtree directory polling for
// recursive watches that exceed the OS watch limits (Linux inotify
// fs.inotify_max_user_watches / max_user_instances), plus the backend form
// used by NewPollingService in tests and degraded environments.

import (
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// dirPoller polls a set of directories and calls poll(dir) whenever the
// listing CHANGES (name set comparison, mtime-insensitive: the TS debounce
// consumes raw fs.watch events and the refresh is the caller's job).
type dirPoller struct {
	interval time.Duration
	poll     func(dir string)

	mu       sync.Mutex
	dirs     map[string]map[string]bool
	stopped  bool
	stopOnce sync.Once
	stopCh   chan struct{}
	started  bool
}

func (p *dirPoller) add(dir string) {
	p.mu.Lock()
	if p.dirs == nil {
		p.dirs = map[string]map[string]bool{}
	}
	if _, ok := p.dirs[dir]; !ok {
		p.dirs[dir] = nil // snapshot filled on the first tick
	}
	p.mu.Unlock()
}

func (p *dirPoller) start() {
	p.mu.Lock()
	if p.started || p.stopped {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.stopCh = make(chan struct{})
	interval := p.interval
	p.mu.Unlock()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-p.stopCh:
				return
			case <-ticker.C:
				p.tick()
			}
		}
	}()
}

func (p *dirPoller) tick() {
	p.mu.Lock()
	dirs := make([]string, 0, len(p.dirs))
	for d := range p.dirs {
		dirs = append(dirs, d)
	}
	stopped := p.stopped
	p.mu.Unlock()
	if stopped {
		return
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		current := map[string]bool{}
		for _, e := range entries {
			current[e.Name()] = true
		}
		p.mu.Lock()
		prev, ok := p.dirs[dir]
		if !ok {
			p.mu.Unlock()
			continue
		}
		if prev != nil && !sameNameSets(prev, current) {
			p.mu.Unlock()
			p.poll(dir)
			p.mu.Lock()
		}
		p.dirs[dir] = current
		p.mu.Unlock()
	}
}

func sameNameSets(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func (p *dirPoller) stop() {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		p.stopped = true
		ch := p.stopCh
		p.mu.Unlock()
		if ch != nil {
			close(ch)
		}
	})
}

// pollingBackend implements the backend interface by polling: it synthesizes
// fsnotify.Event values from listing diffs. Used by NewPollingService.
type pollingBackend struct {
	poller  *dirPoller
	eventsC chan fsnotify.Event
	errorsC chan error
	root    string
	mu      sync.Mutex
	closed  bool
}

func newPollingBackend(interval time.Duration) (backend, error) {
	return &pollingBackend{
		poller: &dirPoller{
			interval: interval,
		},
		eventsC: make(chan fsnotify.Event, 64),
		errorsC: make(chan error, 8),
	}, nil
}

func (b *pollingBackend) events() chan fsnotify.Event { return b.eventsC }
func (b *pollingBackend) errors() chan error          { return b.errorsC }

func (b *pollingBackend) add(path string) error {
	b.poller.poll = func(dir string) {
		b.mu.Lock()
		closed := b.closed
		b.mu.Unlock()
		if closed {
			return
		}
		select {
		case b.eventsC <- fsnotify.Event{Name: dir, Op: 0}:
		default:
		}
	}
	b.poller.add(path)
	b.poller.start()
	return nil
}

func (b *pollingBackend) stop() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.poller.stop()
}
