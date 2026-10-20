package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/tylinux/nex-next/server/internal/rpcwire"
)

// Settings implements the `setting` channel from
// packages/services/src/setting/setting.ts.
//
// The RPC contract is get/update/updateDataBaseDir/ensureDefaultProject; there
// is deliberately no onDidChange event on the channel (the host-internal
// observable wrapper is not part of RPC). UI refresh comes from the broadcast
// channel `settings:app-runtime-preferences` instead.
type Settings struct {
	path     string
	defaults map[string]json.RawMessage
	routes   func() []string

	mu      sync.Mutex
	current map[string]json.RawMessage
	loaded  bool
}

// NewSettings creates the setting channel. routes supplies the routed
// workspace paths injected into recentProjects on every read; it may be nil.
func NewSettings(dataDir string, routes func() []string) *Settings {
	var defaults map[string]json.RawMessage
	if err := json.Unmarshal([]byte(defaultSettingsJSON), &defaults); err != nil {
		// The default document is a compile-time constant; a failure here is a
		// programming error and must be loud during tests.
		panic(fmt.Sprintf("local: default settings are not valid JSON: %v", err))
	}
	return &Settings{
		path:     filepath.Join(dataDir, "settings.json"),
		defaults: defaults,
		routes:   routes,
	}
}

// loadLocked reads the persisted top-level keys once.
func (s *Settings) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.current = make(map[string]json.RawMessage)
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			// A corrupt or unreadable file falls back to defaults, matching the
			// TypeScript service's fail-open read behavior.
			return
		}
		return
	}
	var persisted map[string]json.RawMessage
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return
	}
	for key, value := range persisted {
		s.current[key] = append(json.RawMessage(nil), value...)
	}
}

// snapshotLocked builds the effective settings object: defaults, persisted
// values, then the routed workspace paths at the front of recentProjects.
func (s *Settings) snapshotLocked() map[string]json.RawMessage {
	effective := make(map[string]json.RawMessage, len(s.defaults)+len(s.current))
	for key, value := range s.defaults {
		effective[key] = append(json.RawMessage(nil), value...)
	}
	for key, value := range s.current {
		effective[key] = append(json.RawMessage(nil), value...)
	}

	routed := []string{}
	if s.routes != nil {
		routed = s.routes()
	}
	persisted := []string{}
	if raw, ok := effective["recentProjects"]; ok {
		_ = json.Unmarshal(raw, &persisted)
	}
	merged := make([]string, 0, len(routed)+len(persisted))
	seen := make(map[string]struct{}, len(routed)+len(persisted))
	for _, path := range append(append([]string{}, routed...), persisted...) {
		if path == "" {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		merged = append(merged, path)
	}
	encoded, _ := json.Marshal(merged)
	effective["recentProjects"] = encoded
	return effective
}

// persistLocked atomically replaces settings.json (tmp + rename).
func (s *Settings) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}
	encoded, err := json.MarshalIndent(s.current, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding settings: %w", err)
	}
	encoded = append(encoded, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "settings-*.tmp")
	if err != nil {
		return fmt.Errorf("creating settings temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting settings file mode: %w", err)
	}
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing settings temp file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replacing settings: %w", err)
	}
	return nil
}

// Call implements Channel.
func (s *Settings) Call(_ context.Context, env *Env, method string, arg rpcwire.Value) (rpcwire.Value, error) {
	switch method {
	case "get":
		s.mu.Lock()
		defer s.mu.Unlock()
		s.loadLocked()
		encoded, err := json.Marshal(s.snapshotLocked())
		if err != nil {
			return nil, fmt.Errorf("encoding settings: %w", err)
		}
		return json.RawMessage(encoded), nil

	case "update":
		patch, err := decodeObject(arg)
		if err != nil {
			return nil, err
		}
		// expectedAccountSettings (second argument) is not used by the MVP UI.
		s.mu.Lock()
		defer s.mu.Unlock()
		s.loadLocked()
		if patch == nil {
			return rpcwire.Undefined{}, nil
		}
		if raw, ok := patch["recentProjects"]; ok {
			var incoming []string
			if err := json.Unmarshal(raw, &incoming); err != nil {
				return nil, errors.New("recentProjects must be an array of strings")
			}
		}
		for key, value := range patch {
			s.current[key] = append(json.RawMessage(nil), value...)
		}
		if err := s.persistLocked(); err != nil {
			return nil, err
		}
		return rpcwire.Undefined{}, nil

	case "updateDataBaseDir":
		// The MVP server cannot relocate its own data directory while running;
		// expose the same failure shape so the UI surfaces an error instead of
		// silently pretending success.
		return nil, errors.New("updateDataBaseDir is not supported by nex-next-server")

	case "ensureDefaultProject":
		// Declarative signature: ensureDefaultProject(homedir: string), so the
		// body is `["/home/user"]`; a named object is also accepted.
		home := stringArgument(arg)
		if home == "" {
			home = stringField(arg, "homedir")
		}
		if home == "" {
			return nil, errors.New("homedir is required")
		}
		path := filepath.Join(home, "NexProject")
		_, statErr := os.Stat(path)
		created := errors.Is(statErr, os.ErrNotExist)
		if err := os.MkdirAll(path, 0o755); err != nil {
			return nil, fmt.Errorf("creating default project: %w", err)
		}
		encoded, _ := json.Marshal(map[string]any{"path": path, "created": created})
		return json.RawMessage(encoded), nil
	}
	return nil, &MethodNotFoundError{Method: method}
}

// Listen implements Channel. `setting` exposes no events.
func (s *Settings) Listen(_ context.Context, _ *Env, event string, _ rpcwire.Value, _ func(rpcwire.Value)) (func(), error) {
	return nil, nil
}

// SortedKeys exposes the persisted top-level keys for tests and diagnostics.
func (s *Settings) SortedKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	keys := make([]string, 0, len(s.current))
	for key := range s.current {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
