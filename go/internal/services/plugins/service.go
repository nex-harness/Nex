// Package plugins is the IPluginsService port (step 3.7) — the RETIRED
// legacy marketplace channel. In the TS source every method of this service
// is either the retired overview stub or throws
// "Legacy plugin management has been retired in Nex Agent mode"; the
// replacement surface is IPluginManagementService (pluginmgmt package).
//
// Source: packages/services/src/plugins/pluginsService.ts. The port keeps
// the exact stub and error so the UI branches stay identical.
package plugins

import (
	"context"
	"fmt"
)

// ChannelName is the rpc channel name (ServiceChannels.Plugins).
const ChannelName = "plugins"

// RetiredError is the exact retirement message (throwRetiredPluginManagement).
const RetiredError = "Legacy plugin management has been retired in Nex Agent mode"

// Capability is the retired overview capability.
type Capability struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
}

// OverviewResult ports PluginsOverviewResult (the retired shape).
type OverviewResult struct {
	Marketplaces     []any      `json:"marketplaces"`
	AvailablePlugins []any      `json:"availablePlugins"`
	InstalledPlugins []any      `json:"installedPlugins"`
	Capability       Capability `json:"capability"`
}

// Service is the retired service.
type Service struct{}

// New builds the retired service.
func New() *Service { return &Service{} }

// GetOverview returns the retired overview (empty lists, unsupported).
func (s *Service) GetOverview(ctx context.Context, workspacePath, workspaceIdentity string) (*OverviewResult, error) {
	return &OverviewResult{
		Marketplaces:     []any{},
		AvailablePlugins: []any{},
		InstalledPlugins: []any{},
		Capability:       Capability{Supported: false},
	}, nil
}

// AddMarketplace rejects with the retirement error.
func (s *Service) AddMarketplace(ctx context.Context, workspacePath, workspaceIdentity, source string) error {
	return fmt.Errorf("%s", RetiredError)
}

// RemoveMarketplace rejects with the retirement error.
func (s *Service) RemoveMarketplace(ctx context.Context, workspacePath, workspaceIdentity, marketplace string) error {
	return fmt.Errorf("%s", RetiredError)
}

// UpdateMarketplace rejects with the retirement error.
func (s *Service) UpdateMarketplace(ctx context.Context, workspacePath, workspaceIdentity, marketplace string) error {
	return fmt.Errorf("%s", RetiredError)
}

// InstallPlugin rejects with the retirement error.
func (s *Service) InstallPlugin(ctx context.Context, workspacePath, workspaceIdentity, pluginName, marketplace string) error {
	return fmt.Errorf("%s", RetiredError)
}

// UninstallPlugin rejects with the retirement error.
func (s *Service) UninstallPlugin(ctx context.Context, workspacePath, workspaceIdentity, pluginName, marketplace string) error {
	return fmt.Errorf("%s", RetiredError)
}

// SetPluginEnabled rejects with the retirement error.
func (s *Service) SetPluginEnabled(ctx context.Context, workspacePath, workspaceIdentity, pluginName, marketplace string, enabled bool) error {
	return fmt.Errorf("%s", RetiredError)
}
