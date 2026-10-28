package local

// Import from a runner: `config.exportLocal` (its local ~/.pi/agent providers,
// keys, settings) stored as that runner's scope + secrets (docs/next/
// config-mcp-skills.md decision 2, "Import from <runner>"). Split out of
// config.go for the 400-line file budget.
//
// docs/next/model-onboarding.md decision 3 adds the shared (global) scope: the
// UI's "Import from machine" writes `scopes.global` so the provider is editable
// in Settings and reaches every runner. The HTTP default stays `runner` for
// compatibility.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nex-harness/nex/server/internal/configstore"
	"github.com/nex-harness/nex/server/internal/runners"
)

// Import scopes accepted by ImportFromRunner.
const (
	// ImportScopeRunner writes the source runner's scope (the historical
	// behaviour and the HTTP default).
	ImportScopeRunner = "runner"
	// ImportScopeGlobal writes the shared global layer every runner sees.
	ImportScopeGlobal = "global"
)

// ExportLocal calls `config.exportLocal` on one runner: its local ~/.pi/agent
// providers, keys (values), mcp.json and settings defaults.
func (m *Manager) ExportLocal(ctx context.Context, runnerID string) (ExportResult, error) {
	if m.reg == nil {
		return ExportResult{}, runners.ErrNoRunnerConnected
	}
	link, ok := m.reg.Runner(runnerID)
	if !ok {
		return ExportResult{}, runners.OfflineError{}
	}
	callCtx, cancel := context.WithTimeout(ctx, configExportTimeout)
	defer cancel()
	arg, _ := json.Marshal(map[string]any{"runnerId": runnerID})
	value, err := link.Call(callCtx, "config", "exportLocal", arg)
	if err != nil {
		return ExportResult{}, err
	}
	var result ExportResult
	if err := json.Unmarshal(value, &result); err != nil {
		return ExportResult{}, fmt.Errorf("decoding exportLocal result: %w", err)
	}
	return result, nil
}

// ExportResult is the runner's answer to config.exportLocal. Keys travel as
// values only over the runner link and are stored as secrets; they never reach
// the browser.
type ExportResult struct {
	RunnerID   string                          `json:"runnerId,omitempty"`
	Providers  map[string]configstore.Provider `json:"providers"`
	Keys       map[string]string               `json:"keys,omitempty"`
	Settings   map[string]json.RawMessage      `json:"settings,omitempty"`
	Mcp        json.RawMessage                 `json:"mcp,omitempty"`
	AuthPath   string                          `json:"authPath,omitempty"`
	ModelsPath string                          `json:"modelsPath,omitempty"`
	Errors     []map[string]any                `json:"errors,omitempty"`
}

// AdminImportFromRunner is ImportFromRunner behind the HTTP layer's `any`-typed
// interface (httpapi.ConfigAdmin).
func (m *Manager) AdminImportFromRunner(ctx context.Context, runnerID, scope string) (any, error) {
	summary, err := m.ImportFromRunner(ctx, runnerID, scope)
	if err != nil {
		return nil, err
	}
	return summary, nil
}

// ImportFromRunner imports one runner's local providers into the requested scope
// and stores their keys as secrets. The result reports provider ids and whether
// each got a key — never a key value.
//
// An empty scope means "runner" (the HTTP default). A global import pushes to
// every connected runner: the shared layer changes what all of them apply.
func (m *Manager) ImportFromRunner(ctx context.Context, runnerID, scope string) (ImportSummary, error) {
	if scope == "" {
		scope = ImportScopeRunner
	}
	if scope != ImportScopeRunner && scope != ImportScopeGlobal {
		return ImportSummary{}, fmt.Errorf("scope must be global or runner")
	}
	exported, err := m.ExportLocal(ctx, runnerID)
	if err != nil {
		return ImportSummary{}, err
	}
	layer := configstore.Layer{Providers: map[string]*configstore.Provider{}}
	for id, provider := range exported.Providers {
		copied := provider
		copied.ID = id
		if _, hasKey := exported.Keys[id]; hasKey {
			copied.APIKey = configstore.SecretID(id)
		}
		layer.Providers[id] = &copied
	}
	if len(exported.Settings) > 0 {
		layer.Settings = exported.Settings
	}
	var (
		doc      configstore.ConfigDoc
		imported []string
	)
	if scope == ImportScopeGlobal {
		doc, imported, err = m.store.ImportGlobalLayer(layer)
	} else {
		doc, imported, err = m.store.ImportLayer(runnerID, layer)
	}
	if err != nil {
		return ImportSummary{}, err
	}
	summary := ImportSummary{
		RunnerID:   runnerID,
		Scope:      scope,
		Version:    doc.Version,
		Providers:  imported,
		KeysStored: []string{},
		Errors:     exported.Errors,
	}
	for id, key := range exported.Keys {
		if key == "" {
			continue
		}
		if _, isImported := exported.Providers[id]; !isImported {
			continue
		}
		if err := m.store.Secrets().Put(configstore.SecretID(id), key); err != nil {
			return summary, fmt.Errorf("storing key for %s: %w", id, err)
		}
		summary.KeysStored = append(summary.KeysStored, id)
	}
	// The import changed provider secrets without bumping a doc version for each:
	// re-push so the runner sees the secrets it is entitled to. A global import
	// is entitled to every runner, so it pushes all of them.
	if scope == ImportScopeGlobal {
		m.SyncPush()
	} else {
		m.push(runnerID)
	}
	m.notifyChange()
	return summary, nil
}

// ImportSummary is the browser-facing answer of the import endpoint.
type ImportSummary struct {
	RunnerID   string           `json:"runnerId"`
	Scope      string           `json:"scope"`
	Version    uint64           `json:"version"`
	Providers  []string         `json:"providers"`
	KeysStored []string         `json:"keysStored"`
	Errors     []map[string]any `json:"errors,omitempty"`
}
