// Package pluginmgmt is the IPluginManagementService port (step 3.7): the
// settings-page thin surface over the plugins/* agent protocol words.
//
// Source: packages/services/src/plugins/pluginManagementService.ts. The TS
// implementation forwards every method to the agent process (the source of
// truth for installs lives in nex-cli); in the Go phase split the same shape
// holds — the real agent round-trip arrives with the Phase 4 agent bridge
// (step 4.7), so this port implements the LOCAL read side (list/overview/
// describe/validate over the on-disk ~/.nex plugin state through the shared
// internal/plugins library) and rejects the mutations that require the
// agent's runtime coordination with the exact TS retirement-style errors.
// Plugin RUNTIME effects (MCP servers started by plugins) are Phase 4.
package pluginmgmt

import (
	"path/filepath"
	"strings"
)

// ChannelName is the rpc channel name (ServiceChannels.PluginManagement).
const ChannelName = "plugin-management"

// AgentUnavailableError is the exact message class for operations that need
// the agent process.
const AgentUnavailableError = "Plugin management requires the agent runtime in this build"

// PluginSummary is one installed plugin list entry.
type PluginSummary struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Marketplace    string   `json:"marketplace"`
	Description    string   `json:"description,omitempty"`
	Version        string   `json:"version,omitempty"`
	Enabled        bool     `json:"enabled"`
	InstallPath    string   `json:"installPath,omitempty"`
	ComponentTypes []string `json:"componentTypes,omitempty"`
	// UpdateStatus from the version-compare axis: none | update-available | version-changed.
	UpdateStatus string `json:"updateStatus,omitempty"`
}

// ListResult ports NexPluginsListResult.
type ListResult struct {
	Plugins []PluginSummary `json:"plugins"`
}

// OverviewResult ports NexPluginsOverviewResult (the read side).
type OverviewResult struct {
	Plugins []PluginSummary `json:"plugins"`
}

// Options carry the seams.
type Options struct {
	HomeDir string
}

// Service is the local read-side implementation.
type Service struct {
	homeDir string
}

// New builds the service.
func New(options Options) *Service { return &Service{homeDir: options.HomeDir} }

func (s *Service) cliConfigPath() string {
	return filepath.Join(s.homeDir, ".nex", "cli", "config.json")
}

func (s *Service) pluginStorageRoot() string {
	config := readJSONObjectOrEmpty(s.cliConfigPath())
	storageDir := "~/.nex"
	if storage, ok := config["storage"].(map[string]any); ok {
		if dir, ok := storage["dir"].(string); ok && strings.TrimSpace(dir) != "" {
			storageDir = dir
		}
	}
	expanded := storageDir
	if strings.HasPrefix(storageDir, "~/") {
		expanded = filepath.Join(s.homeDir, storageDir[2:])
	}
	if !filepath.IsAbs(expanded) {
		if absolute, err := filepath.Abs(expanded); err == nil {
			expanded = absolute
		}
	}
	if filepath.Base(expanded) == "cli" {
		return filepath.Join(expanded, "plugins")
	}
	return filepath.Join(expanded, "cli", "plugins")
}
