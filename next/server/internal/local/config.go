package local

// Runner push (spec docs/next/config-mcp-skills.md decisions 1-5, research
// §C3/C5); the browser-facing `config` channel is in configchannel.go and the
// one-shot import in configimport.go.
//
// Ownership: ConfigManager owns the desired doc (via configstore), the push
// schedule per connected runner and the facts each runner reported in
// `configApplied`. The runner owns its effective RuntimeConfig; the manager
// never rewrites runner state.
//
// Event order for one edit:
//
//	store.Update → listener → Manager.afterChange
//	  → push(version) for each connected runner (async)
//	  → runner applies → configApplied{version, ok, errors, facts}
//	  → Manager.recordApplied → changeSink (provider-settings/model-selection views)
//
// A reconnect with hello.lastAppliedVersion < doc.Version pushes immediately.

import (
	"log/slog"
	"sync"
	"time"

	"github.com/tylinux/nex-next/server/internal/configstore"
	"github.com/tylinux/nex-next/server/internal/runners"
)

// configPushTimeout bounds one push write (it is a single frame write).
const configPushTimeout = 15 * time.Second

// configExportTimeout bounds a server-initiated `config.exportLocal` call.
const configExportTimeout = 30 * time.Second

// RunnerFacts is what one runner reported for one applied version.
type RunnerFacts struct {
	Version uint64
	OK      bool
	Errors  []map[string]any
	Facts   map[string]any
}

// AppliedState is the manager's view of one runner's config.
type AppliedState struct {
	Connected      bool
	LastApplied    uint64
	OK             bool
	Errors         []map[string]any
	Facts          map[string]any
	PendingVersion uint64
}

// Manager is the server side of the config push. It implements
// runnerws.ConfigHandler.
type Manager struct {
	logger     *slog.Logger
	store      *configstore.Store
	reg        *runners.Registry
	changeSink func()

	mu      sync.Mutex
	links   map[string]func(version uint64, payload any) error
	applied map[string]*RunnerFacts
	pending map[string]uint64
	wg      sync.WaitGroup
	closed  bool
}

// NewManager returns a manager for store. reg resolves runner links for export;
// changeSink is notified after any doc change or fact update (it re-broadcasts
// the provider-settings/model-selection views).
func NewManager(store *configstore.Store, reg *runners.Registry, logger *slog.Logger, changeSink func()) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	manager := &Manager{
		logger:     logger,
		store:      store,
		reg:        reg,
		changeSink: changeSink,
		links:      map[string]func(version uint64, payload any) error{},
		applied:    map[string]*RunnerFacts{},
		pending:    map[string]uint64{},
	}
	store.Subscribe(func(change configstore.Change) {
		manager.afterChange(change)
	})
	return manager
}

// OwnsProviders reports whether the desired doc defines at least one enabled
// provider (server-owned mode). Until then runners serve their local catalog.
func (m *Manager) OwnsProviders() bool {
	doc, err := m.store.Snapshot()
	if err != nil || doc.Version == 0 {
		return false
	}
	for _, provider := range doc.Scopes.Global.Providers {
		if provider != nil && provider.Enabled {
			return true
		}
	}
	for _, layer := range doc.Scopes.Runner {
		for _, provider := range layer.Providers {
			if provider != nil && provider.Enabled {
				return true
			}
		}
	}
	return false
}

// Store exposes the config store (provider-settings writes go through it).
func (m *Manager) Store() *configstore.Store { return m.store }

// afterChange pushes the new version to every connected runner.
func (m *Manager) afterChange(change configstore.Change) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	targets := make([]string, 0, len(m.links))
	for runnerID := range m.links {
		targets = append(targets, runnerID)
	}
	m.mu.Unlock()
	for _, runnerID := range targets {
		m.push(runnerID)
	}
	m.notifyChange()
	var _ = change
}

// push sends the current snapshot to one runner. The write is synchronous
// (bounded by writeTimeout) so frames reach the runner in doc order and a burst
// of edits arrives as a sequence of versions instead of racing goroutines. The
// store listener runs on the caller's goroutine, so a slow runner delays the
// caller's own write response rather than another caller's.
func (m *Manager) push(runnerID string) {
	if err := m.pushNow(runnerID); err != nil {
		m.logger.Warn("config push failed", "runnerId", runnerID, "error", err)
	}
}

func (m *Manager) pushNow(runnerID string) error {
	m.mu.Lock()
	send, ok := m.links[runnerID]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	payload, _, err := m.store.ProjectPush(runnerID)
	if err != nil {
		return err
	}

	version := payload.Version
	m.mu.Lock()
	m.pending[runnerID] = version
	m.mu.Unlock()
	return send(version, payload)
}

// RunnerConnected implements runnerws.ConfigHandler: it records the link's push
// function (OnLinkReady did that) and pushes when the runner is stale.
func (m *Manager) RunnerConnected(runnerID string, lastAppliedVersion uint64) {
	m.mu.Lock()
	if facts := m.applied[runnerID]; facts != nil && facts.Version > lastAppliedVersion {
		lastAppliedVersion = facts.Version
	}
	_, connected := m.links[runnerID]
	m.mu.Unlock()
	if !connected {
		return
	}
	doc, err := m.store.Snapshot()
	if err != nil {
		m.logger.Warn("config push skipped: doc unreadable", "runnerId", runnerID, "error", err)
		return
	}
	// A runner that never applied anything gets the initial snapshot even when
	// the doc is empty: in fallback mode its facts are the only catalog source.
	neverApplied := lastAppliedVersion == 0 && (m.applied[runnerID] == nil)
	if !neverApplied && (doc.Version == 0 || lastAppliedVersion == doc.Version) {
		return
	}
	m.logger.Info("runner config stale, pushing",
		"runnerId", runnerID, "applied", lastAppliedVersion, "version", doc.Version)
	m.push(runnerID)
}

// RunnerDisconnected implements runnerws.ConfigHandler.
func (m *Manager) RunnerDisconnected(runnerID string) {
	m.mu.Lock()
	delete(m.links, runnerID)
	delete(m.pending, runnerID)
	m.mu.Unlock()
}

// OnLinkReady implements runnerws.ConfigHandler.
func (m *Manager) OnLinkReady(runnerID string, push func(version uint64, payload any) error) {
	m.mu.Lock()
	m.links[runnerID] = push
	m.mu.Unlock()
}

// ConfigApplied implements runnerws.ConfigHandler. A structurally invalid doc
// (ok:false) keeps the previous applied facts, so the UI keeps showing the last
// known good catalog together with the runner's errors.
func (m *Manager) ConfigApplied(runnerID string, version uint64, ok bool, errors []map[string]any, facts map[string]any) {
	m.mu.Lock()
	pending := m.pending[runnerID]
	delete(m.pending, runnerID)
	previous := m.applied[runnerID]
	if ok || previous == nil {
		m.applied[runnerID] = &RunnerFacts{Version: version, OK: ok, Errors: errors, Facts: facts}
	} else {
		copied := *previous
		copied.Errors = errors
		copied.OK = false
		m.applied[runnerID] = &copied
	}
	m.mu.Unlock()
	if !ok {
		m.logger.Warn("runner rejected config", "runnerId", runnerID, "version", version, "errors", len(errors), "pending", pending)
	} else if len(errors) > 0 {
		m.logger.Warn("runner applied config with errors", "runnerId", runnerID, "version", version, "errors", len(errors))
	} else {
		m.logger.Info("runner applied config", "runnerId", runnerID, "version", version)
	}
	m.notifyChange()
}

// Applied returns a copy of one runner's facts.
func (m *Manager) Applied(runnerID string) (AppliedState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := AppliedState{}
	if facts, ok := m.applied[runnerID]; ok {
		state.LastApplied = facts.Version
		state.OK = facts.OK
		state.Errors = append([]map[string]any(nil), facts.Errors...)
		state.Facts = facts.Facts
	}
	state.PendingVersion = m.pending[runnerID]
	_, state.Connected = m.links[runnerID]
	return state, state.Connected || state.Facts != nil
}

// KnownRunners lists runner ids with live links or recorded facts.
func (m *Manager) KnownRunners() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]struct{}{}
	for id := range m.links {
		seen[id] = struct{}{}
	}
	for id := range m.applied {
		seen[id] = struct{}{}
	}
	return configstore.SortedKeys(seen)
}

// Close waits for in-flight pushes (used by tests).
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) notifyChange() {
	if m.changeSink != nil {
		m.changeSink()
	}
}

// SyncPush pushes every connected runner now (tests + the HTTP import path).
func (m *Manager) SyncPush() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.links))
	for id := range m.links {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		if err := m.pushNow(id); err != nil {
			m.logger.Warn("config push failed", "runnerId", id, "error", err)
		}
	}
}
