package local

// Import from a runner: `config.exportLocal` (its local ~/.pi/agent providers,
// keys, settings) stored as that runner's scope + secrets (docs/next/
// config-mcp-skills.md decision 2, "Import from <runner>"). Split out of
// config.go for the 400-line file budget.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tylinux/nex-next/server/internal/configstore"
	"github.com/tylinux/nex-next/server/internal/runners"
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
func (m *Manager) AdminImportFromRunner(ctx context.Context, runnerID string) (any, error) {
	summary, err := m.ImportFromRunner(ctx, runnerID)
	if err != nil {
		return nil, err
	}
	return summary, nil
}

// ImportFromRunner imports one runner's local providers into its runner scope and
// stores their keys as secrets. The result reports provider ids and whether each
// got a key — never a key value.
func (m *Manager) ImportFromRunner(ctx context.Context, runnerID string) (ImportSummary, error) {
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
	doc, imported, err := m.store.ImportLayer(runnerID, layer)
	if err != nil {
		return ImportSummary{}, err
	}
	summary := ImportSummary{
		RunnerID:   runnerID,
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
	// re-push so the runner sees the secrets it is entitled to.
	m.push(runnerID)
	m.notifyChange()
	return summary, nil
}

// ImportSummary is the browser-facing answer of the import endpoint.
type ImportSummary struct {
	RunnerID   string           `json:"runnerId"`
	Version    uint64           `json:"version"`
	Providers  []string         `json:"providers"`
	KeysStored []string         `json:"keysStored"`
	Errors     []map[string]any `json:"errors,omitempty"`
}
