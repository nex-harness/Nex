// pluginmgmt_test.go: the IPluginManagementService local read side —
// listing with enable state and offline marketplace metadata, the retired
// legacy channel stub, and the agent-required mutations' exact refusal.
package pluginmgmt

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tylinux/nex/go/internal/plugins"
)

func newService(t *testing.T) (*Service, string) {
	t.Helper()
	home := t.TempDir()
	return New(Options{HomeDir: home}), home
}

func writePluginAt(t *testing.T, root, name string) {
	t.Helper()
	os.MkdirAll(filepath.Join(root, ".nex-plugin"), 0o755)
	os.WriteFile(filepath.Join(root, ".nex-plugin", "plugin.json"),
		[]byte(`{"name":"`+name+`","version":"1.0.0"}`), 0o644)
}

func setConfig(t *testing.T, home string, configJSON string) {
	t.Helper()
	configPath := filepath.Join(home, ".nex", "cli", "config.json")
	os.MkdirAll(filepath.Dir(configPath), 0o755)
	os.WriteFile(configPath, []byte(configJSON), 0o644)
}

// TestListPluginsInlineDirs: config dirs listed with enable + version axis.
func TestListPluginsInlineDirs(t *testing.T) {
	service, home := newService(t)
	pluginA := filepath.Join(t.TempDir(), "alpha")
	writePluginAt(t, pluginA, "alpha")
	setConfig(t, home, `{"plugins":{"dirs":["`+pluginA+`"]}}`)

	result, err := service.ListPlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Plugins) != 1 {
		t.Fatalf("plugins = %+v", result.Plugins)
	}
	plugin := result.Plugins[0]
	if plugin.ID != "alpha@inline" || !plugin.Enabled || plugin.Version != "1.0.0" {
		t.Fatalf("plugin = %+v", plugin)
	}
	if plugin.UpdateStatus != string(plugins.UpdateNone) {
		t.Fatalf("updateStatus = %s (no latest metadata → none)", plugin.UpdateStatus)
	}
}

// TestListPluginsOfflineMarketplaceMetadata: the on-disk merged official
// marketplace.json drives update-available without any network.
func TestListPluginsOfflineMarketplaceMetadata(t *testing.T) {
	service, home := newService(t)
	// A plugin in the official cache at version 1.0.0.
	cacheDir := filepath.Join(home, ".nex", "cli", "plugins", "cache", plugins.OfficialMarketplaceID, "demo", "1.0.0")
	writePluginAt(t, cacheDir, "demo")
	// The merged official metadata says 2.0.0 is latest — cached on disk
	// (offline mode: no network → the cached metadata answers).
	marketplaceDir := plugins.MarketplacesDir(filepath.Join(home, ".nex", "cli", "plugins"), plugins.OfficialMarketplaceID)
	os.MkdirAll(marketplaceDir, 0o755)
	merged := map[string]any{
		"name":    plugins.OfficialMarketplaceID,
		"plugins": []any{map[string]any{"name": "demo", "version": "2.0.0"}},
	}
	data, _ := json.MarshalIndent(merged, "", "  ")
	os.WriteFile(filepath.Join(marketplaceDir, plugins.MergedFile), data, 0o644)

	result, err := service.ListPlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found *PluginSummary
	for index, plugin := range result.Plugins {
		if plugin.Name == "demo" {
			found = &result.Plugins[index]
		}
	}
	if found == nil {
		t.Fatalf("plugins = %+v", result.Plugins)
	}
	if found.UpdateStatus != string(plugins.UpdateAvailable) {
		t.Fatalf("updateStatus = %s want update-available (offline cached metadata)", found.UpdateStatus)
	}
	if found.Enabled {
		t.Fatal("official cache plugins are not default-enabled (unless on the default list)")
	}
}

// TestListPluginsDisabledAndSuppressed.
func TestListPluginsDisabledAndSuppressed(t *testing.T) {
	service, home := newService(t)
	pluginA := filepath.Join(t.TempDir(), "alpha")
	writePluginAt(t, pluginA, "alpha")
	setConfig(t, home, `{"plugins":{"enabled":false,"dirs":["`+pluginA+`"]}}`)
	result, err := service.ListPlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Plugins) != 0 {
		t.Fatalf("plugins globally disabled = %+v", result.Plugins)
	}

	// Suppressed builtin: official cache entry is filtered.
	cacheDir := filepath.Join(home, ".nex", "cli", "plugins", "cache", plugins.OfficialMarketplaceID, "demo", "1.0.0")
	writePluginAt(t, cacheDir, "demo")
	setConfig(t, home, `{"plugins":{"enabled":true,"suppressedBuiltins":["demo@nex-plugins-official"]}}`)
	result, err = service.ListPlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, plugin := range result.Plugins {
		if plugin.ID == "demo@nex-plugins-official" {
			t.Fatal("suppressed builtin must be filtered")
		}
	}
}

// TestDescribeAndValidate.
func TestDescribeAndValidate(t *testing.T) {
	service, home := newService(t)
	pluginA := filepath.Join(t.TempDir(), "alpha")
	writePluginAt(t, pluginA, "alpha")
	os.MkdirAll(filepath.Join(pluginA, "skills"), 0o755)
	setConfig(t, home, `{"plugins":{"dirs":["`+pluginA+`"]}}`)

	manifest, path, err := service.DescribePlugin(context.Background(), "alpha@inline")
	if err != nil || manifest == nil || path != pluginA {
		t.Fatalf("describe = %+v, %s, %v", manifest, path, err)
	}
	validation, err := service.ValidatePlugin(context.Background(), pluginA)
	if err != nil {
		t.Fatal(err)
	}
	if validation["valid"] != true {
		t.Fatalf("validation = %+v", validation)
	}
	if _, _, err := service.DescribePlugin(context.Background(), "missing@inline"); err == nil {
		t.Fatal("unknown plugin must error")
	}
	// No manifest → invalid.
	empty := t.TempDir()
	emptyValidation, err := service.ValidatePlugin(context.Background(), empty)
	if err != nil {
		t.Fatal(err)
	}
	if emptyValidation["valid"] != false {
		t.Fatalf("validation = %+v", emptyValidation)
	}
}

// TestAgentRequiredMutationsRefused with the exact error.
func TestAgentRequiredMutationsRefused(t *testing.T) {
	service, _ := newService(t)
	ctx := context.Background()
	for _, call := range []func() error{
		func() error { return service.InstallPlugin(ctx, "x", "m") },
		func() error { return service.UninstallPlugin(ctx, "x", "m") },
		func() error { return service.SetPluginEnabled(ctx, "x", "m", true) },
		func() error { return service.AddPluginMarketplace(ctx, "src") },
	} {
		if err := call(); err == nil || err.Error() != AgentUnavailableError {
			t.Fatalf("err = %v", err)
		}
	}
}
