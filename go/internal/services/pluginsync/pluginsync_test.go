// pluginsync_test.go: the IPluginSyncService behavior suite — candidates
// from config dirs, remote statuses, the plugin archive export/import
// round-trip with config write-back and rollback, and the marketplace
// source mirror (dependency closure, cross-marketplace refusal, id
// mismatch).
package pluginsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func newService(t *testing.T) (*Service, string) {
	t.Helper()
	home := t.TempDir()
	return New(Options{HomeDir: home, TempDir: t.TempDir()}), home
}

func writePlugin(t *testing.T, root, name string) {
	t.Helper()
	os.MkdirAll(filepath.Join(root, ".nex-plugin"), 0o755)
	os.WriteFile(filepath.Join(root, ".nex-plugin", "plugin.json"),
		[]byte(`{"name":"`+name+`","description":"d","version":"1.0.0"}`), 0o644)
	os.MkdirAll(filepath.Join(root, "skills"), 0o755)
	os.WriteFile(filepath.Join(root, "skills", "SKILL.md"), []byte("x"), 0o644)
}

func setConfigDirs(t *testing.T, home string, dirs ...string) {
	t.Helper()
	configPath := filepath.Join(home, ".nex", "cli", "config.json")
	os.MkdirAll(filepath.Dir(configPath), 0o755)
	config := map[string]any{}
	if raw, err := os.ReadFile(configPath); err == nil {
		json.Unmarshal(raw, &config)
	}
	pluginsConfig, _ := config["plugins"].(map[string]any)
	if pluginsConfig == nil {
		pluginsConfig = map[string]any{}
	}
	pluginsConfig["dirs"] = dirs
	config["plugins"] = pluginsConfig
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(configPath, data, 0o644)
}

// TestListLocalUserPluginCandidates: config dirs with manifests, dedupe by
// realpath, enabled from overrides.
func TestListLocalUserPluginCandidates(t *testing.T) {
	service, home := newService(t)
	pluginsParent := t.TempDir()
	pluginA := filepath.Join(pluginsParent, "alpha")
	pluginB := filepath.Join(pluginsParent, "beta")
	writePlugin(t, pluginA, "alpha")
	writePlugin(t, pluginB, "beta")
	setConfigDirs(t, home, pluginA, pluginB, pluginA /* dupe */)

	result, err := service.ListLocalUserPluginCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("candidates = %+v", result.Candidates)
	}
	if result.Candidates[0].Name != "alpha" || result.Candidates[0].PluginID != "alpha@inline" {
		t.Fatalf("candidate = %+v", result.Candidates[0])
	}
	if !result.Candidates[0].Enabled {
		t.Fatal("inline plugins default enabled")
	}
	if len(result.Candidates[0].ComponentTypes) != 1 || result.Candidates[0].ComponentTypes[0] != "skills" {
		t.Fatalf("componentTypes = %v", result.Candidates[0].ComponentTypes)
	}
}

// TestListRemoteUserPluginStatuses: targetExists vs samePluginId vs absent.
func TestListRemoteUserPluginStatuses(t *testing.T) {
	service, home := newService(t)
	pluginA := t.TempDir()
	writePlugin(t, pluginA, "alpha")
	setConfigDirs(t, home, pluginA)
	// A directory already at the target path.
	os.MkdirAll(filepath.Join(home, ".nex", "plugins", "alpha"), 0o755)

	statuses, err := service.ListRemoteUserPluginStatuses(context.Background(), []struct{ PluginID, DirectoryName string }{
		{"alpha@inline", "alpha"},
		{"beta@inline", "beta"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses.Statuses) != 2 {
		t.Fatalf("statuses = %+v", statuses.Statuses)
	}
	if !statuses.Statuses[0].Exists || statuses.Statuses[0].Reason != "targetExists" {
		t.Fatalf("targetExists: %+v", statuses.Statuses[0])
	}
	if statuses.Statuses[1].Exists {
		t.Fatal("absent plugin must not exist")
	}
	// Without the on-disk dir, the same-plugin-id config entry counts.
	os.RemoveAll(filepath.Join(home, ".nex", "plugins", "alpha"))
	statuses, err = service.ListRemoteUserPluginStatuses(context.Background(), []struct{ PluginID, DirectoryName string }{
		{"alpha@inline", "alpha"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !statuses.Statuses[0].Exists || statuses.Statuses[0].Reason != "samePluginId" {
		t.Fatalf("samePluginId: %+v", statuses.Statuses[0])
	}
}

// TestExportImportPluginArchive: export → import into a fresh home syncs
// the dir AND registers it in the remote config; re-import skips.
func TestExportImportPluginArchive(t *testing.T) {
	exportService, exportHome := newService(t)
	pluginA := filepath.Join(t.TempDir(), "alpha")
	writePlugin(t, pluginA, "alpha")
	setConfigDirs(t, exportHome, pluginA)
	candidates, err := exportService.ListLocalUserPluginCandidates(context.Background())
	if err != nil || len(candidates.Candidates) != 1 {
		t.Fatalf("candidates = %+v, %v", candidates, err)
	}

	exported, err := exportService.ExportPluginsArchive(context.Background(), []string{candidates.Candidates[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Archive) == 0 || exported.Plugins[0].PluginID != "alpha@inline" {
		t.Fatalf("export = %+v", exported)
	}

	importService, importHome := newService(t)
	imported, err := importService.ImportPluginsArchive(context.Background(), exported.Archive, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(imported.Results) != 1 || imported.Results[0].Status != "synced" {
		t.Fatalf("import = %+v", imported.Results)
	}
	targetPath := filepath.Join(importHome, ".nex", "plugins", "alpha")
	if _, err := os.Stat(filepath.Join(targetPath, ".nex-plugin", "plugin.json")); err != nil {
		t.Fatal("plugin dir missing on remote")
	}
	// The remote config lists the absolute path in plugins.dirs.
	raw, _ := os.ReadFile(filepath.Join(importHome, ".nex", "cli", "config.json"))
	var config map[string]any
	json.Unmarshal(raw, &config)
	pluginsConfig := config["plugins"].(map[string]any)
	dirs := pluginsConfig["dirs"].([]any)
	if len(dirs) != 1 || dirs[0] != targetPath {
		t.Fatalf("dirs = %v", dirs)
	}

	// Re-import skips.
	imported, err = importService.ImportPluginsArchive(context.Background(), exported.Archive, false)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Results[0].Status != "skipped" {
		t.Fatalf("re-import = %+v", imported.Results[0])
	}

	// Overwrite refused.
	if _, err := importService.ImportPluginsArchive(context.Background(), exported.Archive, true); err == nil {
		t.Fatal("overwrite must be refused")
	}
}

// TestCheckRemoteUserPluginWriteAccess.
func TestCheckRemoteUserPluginWriteAccess(t *testing.T) {
	service, _ := newService(t)
	result, err := service.CheckRemoteUserPluginWriteAccess(context.Background())
	if err != nil || !result.OK {
		t.Fatalf("result = %+v, %v", result, err)
	}
}

// TestExportMarketplaceSourceArchive: local directory sources are mirrored
// under plugins/, dependencies included, manifest rewritten.
func TestExportMarketplaceSourceArchive(t *testing.T) {
	service, _ := newService(t)
	marketRoot := t.TempDir()
	os.MkdirAll(marketRoot, 0o755)
	os.MkdirAll(filepath.Join(marketRoot, "plugins", "hello", ".nex-plugin"), 0o755)
	os.WriteFile(filepath.Join(marketRoot, "plugins", "hello", ".nex-plugin", "plugin.json"), []byte(`{"name":"hello"}`), 0o644)
	os.MkdirAll(filepath.Join(marketRoot, "plugins", "dep", ".nex-plugin"), 0o755)
	os.WriteFile(filepath.Join(marketRoot, "plugins", "dep", ".nex-plugin", "plugin.json"), []byte(`{"name":"dep"}`), 0o644)
	// Entry sources are relative to metadata.pluginRoot (the TS resolves
	// them against the plugin base dir).
	os.WriteFile(filepath.Join(marketRoot, "marketplace.json"), []byte(`{
	  "name": "my-market",
	  "metadata": {"pluginRoot": "./plugins"},
	  "plugins": [
	    {"name": "hello", "source": "./hello", "dependencies": ["dep@my-market"]},
	    {"name": "dep", "source": "./dep"}
	  ]
	}`), 0o644)

	result, err := service.ExportMarketplaceSourceArchive(context.Background(), "my-market",
		[]string{"hello"}, map[string]any{"source": "directory", "path": marketRoot})
	if err != nil {
		t.Fatal(err)
	}
	if result.MarketplaceID != "my-market" {
		t.Fatalf("marketplaceId = %s", result.MarketplaceID)
	}
	if len(result.PluginNames) != 2 {
		t.Fatalf("dependency closure = %v (dep must follow hello)", result.PluginNames)
	}

	// Import into a fresh home and inspect the mirror layout.
	importService, importHome := newService(t)
	imported, err := importService.ImportMarketplaceSourceArchive(context.Background(), result.Archive, false)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Status != "synced" || imported.MarketplaceID != "my-market" {
		t.Fatalf("import = %+v", imported)
	}
	manifest, err := os.ReadFile(filepath.Join(imported.Path, "marketplace.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	json.Unmarshal(manifest, &parsed)
	metadata := parsed["metadata"].(map[string]any)
	if metadata["pluginRoot"] != "plugins" {
		t.Fatalf("mirrored pluginRoot = %v", metadata["pluginRoot"])
	}
	pluginsList := parsed["plugins"].([]any)
	hello := pluginsList[0].(map[string]any)
	if hello["source"] != "./hello" {
		t.Fatalf("mirrored source = %v", hello["source"])
	}
	if _, err := os.Stat(filepath.Join(imported.Path, "plugins", "hello", ".nex-plugin", "plugin.json")); err != nil {
		t.Fatal("mirrored plugin dir missing")
	}
	// Re-import skips.
	imported, err = importService.ImportMarketplaceSourceArchive(context.Background(), result.Archive, false)
	if err != nil || imported.Status != "skipped" {
		t.Fatalf("re-import = %+v, %v", imported, err)
	}
	_ = importHome
}

// TestExportMarketplaceSourceCrossMarketplaceDependency: cross-marketplace
// dependencies are refused.
func TestExportMarketplaceSourceCrossMarketplaceDependency(t *testing.T) {
	service, _ := newService(t)
	marketRoot := t.TempDir()
	os.MkdirAll(marketRoot, 0o755)
	os.WriteFile(filepath.Join(marketRoot, "marketplace.json"), []byte(`{
	  "name": "my-market",
	  "plugins": [{"name": "hello", "dependencies": ["other@other-market"]}]
	}`), 0o644)
	_, err := service.ExportMarketplaceSourceArchive(context.Background(), "my-market",
		[]string{"hello"}, map[string]any{"source": "directory", "path": marketRoot})
	if err == nil {
		t.Fatal("cross-marketplace dependency must be refused")
	}
}

// TestExportMarketplaceSourceIdMismatch: manifest name must equal the id.
func TestExportMarketplaceSourceIdMismatch(t *testing.T) {
	service, _ := newService(t)
	marketRoot := t.TempDir()
	os.MkdirAll(marketRoot, 0o755)
	os.WriteFile(filepath.Join(marketRoot, "marketplace.json"), []byte(`{"name":"other","plugins":[]}`), 0o644)
	_, err := service.ExportMarketplaceSourceArchive(context.Background(), "my-market",
		[]string{"hello"}, map[string]any{"source": "directory", "path": marketRoot})
	if err == nil {
		t.Fatal("id mismatch must be refused")
	}
}
