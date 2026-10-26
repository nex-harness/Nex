package browserws

// Server-global id → runner bindings for the batch-1 workspace tool channels
// (docs/next/workspace-tools.md decision 1):
//
//   - terminal write/resize/dispose and the data/exit listens carry only the
//     terminal id; the binding is recorded when `terminal.create` returns the
//     id to the browser.
//   - file-watcher unwatch and the change listen carry only the watcher id;
//     the binding is recorded when `file-watcher.watch` returns it.
//
// The maps are server-global (not per connection) because a page reload opens
// a new browser connection while the terminal/watcher on the runner survives,
// and the reattach must resolve to the same machine. Entries live until the
// owning runner disconnects (the ids die with it).

import (
	"strings"
	"sync"
)

// IDBindings is the concurrency-safe id→runner-id store for tool ids.
type IDBindings struct {
	mu        sync.Mutex
	terminals map[string]string
	watchers  map[string]string
}

// NewIDBindings builds an empty store.
func NewIDBindings() *IDBindings {
	return &IDBindings{
		terminals: map[string]string{},
		watchers:  map[string]string{},
	}
}

// RecordTerminal stores (or replaces) one terminal binding.
func (b *IDBindings) RecordTerminal(id, runnerID string) {
	id = strings.TrimSpace(id)
	runnerID = strings.TrimSpace(runnerID)
	if id == "" || runnerID == "" {
		return
	}
	b.mu.Lock()
	b.terminals[id] = runnerID
	b.mu.Unlock()
}

// Terminal resolves one terminal binding to its runner id.
func (b *IDBindings) Terminal(id string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	runnerID, ok := b.terminals[strings.TrimSpace(id)]
	return runnerID, ok
}

// RecordWatcher stores (or replaces) one watcher binding.
func (b *IDBindings) RecordWatcher(id, runnerID string) {
	id = strings.TrimSpace(id)
	runnerID = strings.TrimSpace(runnerID)
	if id == "" || runnerID == "" {
		return
	}
	b.mu.Lock()
	b.watchers[id] = runnerID
	b.mu.Unlock()
}

// Watcher resolves one watcher binding to its runner id.
func (b *IDBindings) Watcher(id string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	runnerID, ok := b.watchers[strings.TrimSpace(id)]
	return runnerID, ok
}

// DropRunner clears every binding of a runner that went offline: the ids die
// with the process (terminals are SIGKILLed; the runner's watchers close).
func (b *IDBindings) DropRunner(runnerID string) {
	runnerID = strings.TrimSpace(runnerID)
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, owner := range b.terminals {
		if owner == runnerID {
			delete(b.terminals, id)
		}
	}
	for id, owner := range b.watchers {
		if owner == runnerID {
			delete(b.watchers, id)
		}
	}
}
