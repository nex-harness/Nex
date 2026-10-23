package configstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
)

// Store owns the persisted ConfigDoc and the secret box.
//
// Locking: mu serializes every read-modify-write. The secret box has its own
// lock; the order is always Store.mu → SecretBox.mu.
type Store struct {
	mu      sync.Mutex
	path    string
	secrets *SecretBox
	logger  *slog.Logger
	doc     ConfigDoc
	loaded  bool
	// listeners are notified (outside mu) after a committed write.
	listeners map[uint64]func(Change)
	nextSub   uint64
}

// Change describes one committed doc change.
type Change struct {
	Version  uint64
	Previous uint64
	Reason   string
}

// NewStore returns a store rooted at dataDir (config/config.json and
// config/secrets.enc).
func NewStore(dataDir string, logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{
		path:    filepath.Join(dataDir, "config.json"),
		secrets: NewSecretBox(dataDir),
		logger:  logger,
	}
}

// ConfigDir is the directory holding config.json (diagnostics and tests).
func (s *Store) ConfigDir() string { return filepath.Dir(s.path) }

// Secrets exposes the secret box (the provider-settings channel stores keys
// through it and the push path reads them).
func (s *Store) Secrets() *SecretBox { return s.secrets }

// Subscribe registers a change callback. It runs outside the store lock and must
// not call back into the store synchronously.
func (s *Store) Subscribe(fn func(Change)) func() {
	if fn == nil {
		return func() {}
	}
	s.mu.Lock()
	if s.listeners == nil {
		s.listeners = map[uint64]func(Change){}
	}
	s.nextSub++
	id := s.nextSub
	s.listeners[id] = fn
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}
}

// Snapshot returns a deep copy of the current doc.
func (s *Store) Snapshot() (ConfigDoc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return ConfigDoc{}, err
	}
	return cloneDoc(s.doc), nil
}

// Update applies mutate to a copy of the doc, validates it, persists it with a
// bumped version and notifies listeners. A validation failure leaves the stored
// doc untouched.
func (s *Store) Update(reason string, mutate func(*ConfigDoc) error) (ConfigDoc, error) {
	s.mu.Lock()
	if err := s.loadLocked(); err != nil {
		s.mu.Unlock()
		return ConfigDoc{}, err
	}
	next := cloneDoc(s.doc)
	if err := mutate(&next); err != nil {
		s.mu.Unlock()
		return ConfigDoc{}, err
	}
	next.Schema = SchemaVersion
	if issues := ValidateDoc(next); len(issues) > 0 {
		s.mu.Unlock()
		return ConfigDoc{}, &IssuesError{Issues: issues}
	}
	previous := s.doc.Version
	next.Version = previous + 1
	next.Scopes.Runner = nonEmptyLayers(next.Scopes.Runner)
	next.Scopes.Workspace = nonEmptyLayers(next.Scopes.Workspace)
	next.Scopes.Session = nonEmptyLayers(next.Scopes.Session)
	if err := s.persistLocked(next); err != nil {
		s.mu.Unlock()
		return ConfigDoc{}, err
	}
	s.doc = next
	listeners := make([]func(Change), 0, len(s.listeners))
	for _, listener := range s.listeners {
		listeners = append(listeners, listener)
	}
	committed := cloneDoc(next)
	s.mu.Unlock()

	s.logger.Info("config doc updated", "version", committed.Version, "reason", reason)
	for _, listener := range listeners {
		listener(Change{Version: committed.Version, Previous: previous, Reason: reason})
	}
	return committed, nil
}

// UpsertProvider writes a provider into the global scope (P0's only write scope
// for UI edits).
func (s *Store) UpsertProvider(provider Provider) (ConfigDoc, error) {
	if strings.TrimSpace(provider.ID) == "" {
		return ConfigDoc{}, errors.New("providerId is required")
	}
	return s.Update("provider:"+provider.ID, func(doc *ConfigDoc) error {
		if doc.Scopes.Global.Providers == nil {
			doc.Scopes.Global.Providers = map[string]*Provider{}
		}
		stored := cloneProvider(provider)
		stored.ID = provider.ID
		doc.Scopes.Global.Providers[provider.ID] = &stored
		return nil
	})
}

// DeleteProvider removes a provider (and its API-key secret) from the global
// scope.
func (s *Store) DeleteProvider(providerID string) (ConfigDoc, error) {
	doc, err := s.Update("provider-delete:"+providerID, func(doc *ConfigDoc) error {
		provider, ok := doc.Scopes.Global.Providers[providerID]
		if !ok || provider == nil {
			return fmt.Errorf("provider %s: %w", providerID, ErrNotFound)
		}
		delete(doc.Scopes.Global.Providers, providerID)
		order := doc.Scopes.Global.ProviderOrder[:0]
		for _, id := range doc.Scopes.Global.ProviderOrder {
			if id != providerID {
				order = append(order, id)
			}
		}
		doc.Scopes.Global.ProviderOrder = order
		return nil
	})
	if err != nil {
		return ConfigDoc{}, err
	}
	if err := s.secrets.Delete(SecretID(providerID)); err != nil {
		// The provider is gone; a leftover secret is dead data, not a failure the
		// user must see. Report it in the log only.
		s.logger.Warn("deleting provider secret failed", "providerId", providerID, "error", err)
	}
	return doc, nil
}

// UpdateProvider applies mutate to one global provider (model edits).
func (s *Store) UpdateProvider(providerID, reason string, mutate func(*Provider) error) (ConfigDoc, error) {
	return s.Update(reason, func(doc *ConfigDoc) error {
		provider, ok := doc.Scopes.Global.Providers[providerID]
		if !ok || provider == nil {
			return fmt.Errorf("provider %s: %w", providerID, ErrNotFound)
		}
		copied := cloneProvider(*provider)
		if err := mutate(&copied); err != nil {
			return err
		}
		copied.ID = providerID
		doc.Scopes.Global.Providers[providerID] = &copied
		return nil
	})
}

// SetProviderOrder replaces the global display order.
func (s *Store) SetProviderOrder(ids []string) (ConfigDoc, error) {
	return s.Update("provider-order", func(doc *ConfigDoc) error {
		doc.Scopes.Global.ProviderOrder = append([]string(nil), ids...)
		return nil
	})
}

func (s *Store) loadLocked() error {
	if s.loaded {
		return nil
	}
	raw, present, err := readFileOrMissing(s.path)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	s.doc = EmptyDoc()
	if present && len(raw) > 0 {
		var persisted ConfigDoc
		if err := json.Unmarshal(raw, &persisted); err != nil {
			// A corrupt file must not be silently replaced: the operator needs to see
			// it. Runners keep their last applied config meanwhile.
			return fmt.Errorf("decoding config %s: %w", s.path, err)
		}
		if persisted.Schema == 0 {
			persisted.Schema = SchemaVersion
		}
		s.doc = persisted
		s.doc.Scopes = normalizeScopes(s.doc.Scopes)
	}
	s.loaded = true
	return nil
}

func (s *Store) persistLocked(doc ConfigDoc) error {
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	encoded = append(encoded, '\n')
	return writeFileAtomic(s.path, encoded, 0o600)
}

func normalizeScopes(scopes DocScopes) DocScopes {
	if scopes.Runner == nil {
		scopes.Runner = map[string]Layer{}
	}
	if scopes.Workspace == nil {
		scopes.Workspace = map[string]Layer{}
	}
	if scopes.Session == nil {
		scopes.Session = map[string]Layer{}
	}
	if scopes.Global.Providers == nil && !scopes.Global.Empty() {
		scopes.Global.Providers = map[string]*Provider{}
	}
	return scopes
}

func nonEmptyLayers(layers map[string]Layer) map[string]Layer {
	if len(layers) == 0 {
		return map[string]Layer{}
	}
	for key, layer := range layers {
		if layer.Empty() {
			delete(layers, key)
		}
	}
	return layers
}

func cloneDoc(doc ConfigDoc) ConfigDoc {
	copied := doc
	copied.Scopes = DocScopes{
		Global:    cloneLayer(doc.Scopes.Global),
		Runner:    cloneLayers(doc.Scopes.Runner),
		Workspace: cloneLayers(doc.Scopes.Workspace),
		Session:   cloneLayers(doc.Scopes.Session),
	}
	return copied
}

func cloneLayers(layers map[string]Layer) map[string]Layer {
	out := make(map[string]Layer, len(layers))
	for key, layer := range layers {
		out[key] = cloneLayer(layer)
	}
	return out
}

func cloneLayer(layer Layer) Layer {
	out := Layer{}
	if layer.Providers != nil {
		out.Providers = make(map[string]*Provider, len(layer.Providers))
		for id, provider := range layer.Providers {
			if provider == nil {
				out.Providers[id] = nil
				continue
			}
			copied := cloneProvider(*provider)
			out.Providers[id] = &copied
		}
	}
	if layer.Settings != nil {
		out.Settings = make(map[string]json.RawMessage, len(layer.Settings))
		for key, value := range layer.Settings {
			out.Settings[key] = append(json.RawMessage(nil), value...)
		}
	}
	if layer.ProviderOrder != nil {
		out.ProviderOrder = append([]string(nil), layer.ProviderOrder...)
	}
	if layer.ModelOrder != nil {
		out.ModelOrder = make(map[string][]string, len(layer.ModelOrder))
		for key, order := range layer.ModelOrder {
			out.ModelOrder[key] = append([]string(nil), order...)
		}
	}
	return out
}

// ImportLayer merges exported runner config into the runner scope. Providers are
// upserted (existing ids from the import win); the returned map tells the caller
// which provider ids are new so it can store their keys as secrets.
func (s *Store) ImportLayer(runnerID string, layer Layer) (ConfigDoc, []string, error) {
	var imported []string
	doc, err := s.Update("import:"+runnerID, func(doc *ConfigDoc) error {
		if doc.Scopes.Runner == nil {
			doc.Scopes.Runner = map[string]Layer{}
		}
		current := doc.Scopes.Runner[runnerID]
		if current.Providers == nil {
			current.Providers = map[string]*Provider{}
		}
		for id, provider := range layer.Providers {
			if provider == nil {
				continue
			}
			if _, exists := current.Providers[id]; !exists {
				imported = append(imported, id)
			}
			copied := cloneProvider(*provider)
			current.Providers[id] = &copied
		}
		if current.Settings == nil {
			current.Settings = map[string]json.RawMessage{}
		}
		for key, value := range layer.Settings {
			current.Settings[key] = append(json.RawMessage(nil), value...)
		}
		doc.Scopes.Runner[runnerID] = current
		return nil
	})
	return doc, imported, err
}
