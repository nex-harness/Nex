// config.go: the CLI-config plugin-block readers and the plugin storage
// root resolution shared by the management service.
package pluginmgmt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tylinux/nex/go/internal/plugins"
)

// readCliPluginConfig reads the plugin enable state from the CLI config.
func (s *Service) readCliPluginConfig() (dirs []string, enabled bool, enabledPlugins map[string]bool, suppressedBuiltins []string) {
	config := readJSONObjectOrEmpty(s.cliConfigPath())
	enabled = true
	enabledPlugins = map[string]bool{}
	pluginsConfig, _ := config["plugins"].(map[string]any)
	if pluginsConfig == nil {
		return nil, enabled, enabledPlugins, nil
	}
	if list, ok := pluginsConfig["dirs"].([]any); ok {
		for _, item := range list {
			if str, ok := item.(string); ok {
				dirs = append(dirs, str)
			}
		}
	}
	if value, ok := pluginsConfig["enabled"].(bool); ok {
		enabled = value
	}
	if record, ok := pluginsConfig["enabledPlugins"].(map[string]any); ok {
		for key, value := range record {
			if b, ok := value.(bool); ok {
				enabledPlugins[key] = b
			}
		}
	}
	if list, ok := pluginsConfig["suppressedBuiltins"].([]any); ok {
		for _, item := range list {
			if str, ok := item.(string); ok {
				suppressedBuiltins = append(suppressedBuiltins, str)
			}
		}
	}
	return dirs, enabled, enabledPlugins, suppressedBuiltins
}

// ListPlugins ports listPlugins over the local state: inline dirs + official
// cache + installed records, deduped by plugin id, with update status
// computed against the merged official marketplace metadata.
func (s *Service) ListPlugins(ctx context.Context) (*ListResult, error) {
	dirs, enabled, enabledPlugins, suppressedBuiltins := s.readCliPluginConfig()
	if !enabled {
		return &ListResult{Plugins: []PluginSummary{}}, nil
	}
	storageRoot := s.pluginStorageRoot()

	type candidate struct {
		defaultEnabled bool
		marketplace    string
		rootPath       string
	}
	var candidates []candidate
	for _, dir := range dirs {
		expanded := dir
		if strings.HasPrefix(dir, "~/") {
			expanded = filepath.Join(s.homeDir, dir[2:])
		}
		if !filepath.IsAbs(expanded) {
			if absolute, err := filepath.Abs(expanded); err == nil {
				expanded = absolute
			}
		}
		candidates = append(candidates, candidate{true, plugins.InlineMarketplace, expanded})
	}
	if cacheRoots := s.scanOfficialPluginCacheRoots(storageRoot); cacheRoots != nil {
		for _, rootPath := range cacheRoots {
			candidates = append(candidates, candidate{false, plugins.OfficialMarketplaceID, rootPath})
		}
	}
	for _, record := range plugins.ReadInstalledPluginRoots(storageRoot) {
		candidates = append(candidates, candidate{false, record.Marketplace, record.InstallPath})
	}

	// The merged official metadata drives the version axis for update checks
	// (offline mode: the on-disk merged file; absent → no version axis).
	latestVersionByName := map[string]string{}
	if merged, err := plugins.ReadOfficialMergedMarketplaceStrict(storageRoot); err == nil {
		if entries, ok := merged["plugins"].([]any); ok {
			for _, entry := range entries {
				if record, ok := entry.(map[string]any); ok {
					if name, ok := record["name"].(string); ok {
						if version, ok := record["version"].(string); ok {
							latestVersionByName[name] = version
						}
					}
				}
			}
		}
	}

	result := &ListResult{Plugins: []PluginSummary{}}
	seenPluginIDs := map[string]bool{}
	for _, item := range candidates {
		manifest, err := plugins.ReadManifest(item.rootPath)
		if err != nil || manifest == nil {
			continue
		}
		pluginID := manifest.Name + "@" + item.marketplace
		if item.marketplace == plugins.OfficialMarketplaceID && containsString(suppressedBuiltins, pluginID) {
			continue
		}
		if seenPluginIDs[pluginID] {
			continue
		}
		seenPluginIDs[pluginID] = true
		pluginEnabled := item.defaultEnabled || plugins.DefaultEnabledOfficialPluginIDs[pluginID]
		if override, ok := enabledPlugins[pluginID]; ok {
			pluginEnabled = override
		}
		summary := PluginSummary{
			ID:             pluginID,
			Name:           manifest.Name,
			Marketplace:    item.marketplace,
			Description:    manifest.Description,
			Version:        manifest.Version,
			Enabled:        pluginEnabled,
			InstallPath:    item.rootPath,
			ComponentTypes: resolveComponentTypes(item.rootPath, manifest),
			UpdateStatus:   string(plugins.ComparePluginVersions(manifest.Version, latestVersionByName[manifest.Name])),
		}
		result.Plugins = append(result.Plugins, summary)
	}
	sort.Slice(result.Plugins, func(i, j int) bool { return result.Plugins[i].ID < result.Plugins[j].ID })
	return result, nil
}

// resolveComponentTypes ports resolvePluginComponentTypes.
func resolveComponentTypes(pluginRoot string, manifest *plugins.Manifest) []string {
	var componentTypes []string
	manifestHas := func(key string) bool {
		if manifest == nil {
			return false
		}
		raw := map[string]any{}
		if json.Unmarshal(manifest.Raw, &raw) == nil {
			_, ok := raw[key]
			return ok
		}
		return false
	}
	if manifestHas("skills") || pathExists(filepath.Join(pluginRoot, "skills")) {
		componentTypes = append(componentTypes, "skills")
	}
	if manifestHas("commands") || pathExists(filepath.Join(pluginRoot, "commands")) {
		componentTypes = append(componentTypes, "commands")
	}
	if manifestHas("hooks") || pathExists(filepath.Join(pluginRoot, "hooks", "hooks.json")) {
		componentTypes = append(componentTypes, "hooks")
	}
	if manifestHas("mcpServers") || pathExists(filepath.Join(pluginRoot, ".mcp.json")) {
		componentTypes = append(componentTypes, "mcp")
	}
	return componentTypes
}

func (s *Service) scanOfficialPluginCacheRoots(pluginStorageRoot string) []string {
	cacheRoot := filepath.Join(pluginStorageRoot, "cache", plugins.OfficialMarketplaceID)
	pluginEntries, err := os.ReadDir(cacheRoot)
	if err != nil {
		return nil
	}
	var roots []string
	for _, pluginEntry := range pluginEntries {
		if !pluginEntry.IsDir() {
			continue
		}
		pluginDir := filepath.Join(cacheRoot, pluginEntry.Name())
		versionEntries, err := os.ReadDir(pluginDir)
		if err != nil {
			continue
		}
		for _, versionEntry := range versionEntries {
			if versionEntry.IsDir() {
				roots = append(roots, filepath.Join(pluginDir, versionEntry.Name()))
			}
		}
	}
	sort.Strings(roots)
	return roots
}

// GetPluginsOverview ports getPluginsOverview (the local read side).
func (s *Service) GetPluginsOverview(ctx context.Context) (*OverviewResult, error) {
	list, err := s.ListPlugins(ctx)
	if err != nil {
		return nil, err
	}
	return &OverviewResult{Plugins: list.Plugins}, nil
}

// DescribePlugin ports describePlugin (manifest fields only — the runtime
// surface is Phase 4).
func (s *Service) DescribePlugin(ctx context.Context, pluginID string) (*plugins.Manifest, string, error) {
	_, _, _, suppressedBuiltins := s.readCliPluginConfig()
	storageRoot := s.pluginStorageRoot()
	candidates := s.collectRootsForPluginID(storageRoot, pluginID)
	for _, rootPath := range candidates {
		manifest, err := plugins.ReadManifest(rootPath)
		if err == nil && manifest != nil && manifest.Name+"@"+marketplaceForRoot(rootPath, storageRoot) == pluginID {
			if containsString(suppressedBuiltins, pluginID) {
				return nil, rootPath, fmt.Errorf("plugin is uninstalled (restorable builtin): %s", pluginID)
			}
			return manifest, rootPath, nil
		}
	}
	return nil, "", fmt.Errorf("Plugin not found: %s", pluginID)
}

func marketplaceForRoot(rootPath, storageRoot string) string {
	cacheRoot := filepath.Join(storageRoot, "cache", plugins.OfficialMarketplaceID)
	if strings.HasPrefix(rootPath, cacheRoot+string(filepath.Separator)) {
		return plugins.OfficialMarketplaceID
	}
	return plugins.InlineMarketplace
}

func (s *Service) collectRootsForPluginID(storageRoot, pluginID string) []string {
	name := pluginID
	if index := strings.Index(pluginID, "@"); index >= 0 {
		name = pluginID[:index]
	}
	dirs, _, _, _ := s.readCliPluginConfig()
	var roots []string
	for _, dir := range dirs {
		if filepath.Base(dir) == name {
			roots = append(roots, dir)
		}
	}
	roots = append(roots, s.scanOfficialPluginCacheRoots(storageRoot)...)
	for _, record := range plugins.ReadInstalledPluginRoots(storageRoot) {
		roots = append(roots, record.InstallPath)
	}
	return roots
}

// ValidatePlugin ports validatePlugin: manifest parse + component roots.
func (s *Service) ValidatePlugin(ctx context.Context, pluginPath string) (map[string]any, error) {
	manifest, err := plugins.ReadManifest(pluginPath)
	if err != nil {
		return nil, err
	}
	if manifest == nil {
		return map[string]any{
			"valid":  false,
			"errors": []string{"plugin manifest not found or invalid: " + pluginPath},
		}, nil
	}
	var errors []string
	if manifest.Name == "" {
		errors = append(errors, "plugin manifest name is empty")
	}
	skillRoots := plugins.ResolveComponentRoots(pluginPath, manifest.Skills, "skills")
	commandRoots := plugins.ResolveComponentRoots(pluginPath, manifest.Commands, "commands")
	return map[string]any{
		"valid":          len(errors) == 0,
		"errors":         errors,
		"name":           manifest.Name,
		"componentTypes": resolveComponentTypes(pluginPath, manifest),
		"skillRoots":     skillRoots,
		"commandRoots":   commandRoots,
	}, nil
}

// InstallPlugin / UninstallPlugin / SetPluginEnabled / marketplace mutations
// need the agent runtime's hot-reload coordination; they are rejected until
// the Phase 4 bridge with a single exact error.

// InstallPlugin rejects with AgentUnavailableError.
func (s *Service) InstallPlugin(ctx context.Context, pluginName, marketplace string) error {
	return fmt.Errorf("%s", AgentUnavailableError)
}

// UninstallPlugin rejects with AgentUnavailableError.
func (s *Service) UninstallPlugin(ctx context.Context, pluginName, marketplace string) error {
	return fmt.Errorf("%s", AgentUnavailableError)
}

// SetPluginEnabled rejects with AgentUnavailableError.
func (s *Service) SetPluginEnabled(ctx context.Context, pluginName, marketplace string, enabled bool) error {
	return fmt.Errorf("%s", AgentUnavailableError)
}

// AddPluginMarketplace rejects with AgentUnavailableError.
func (s *Service) AddPluginMarketplace(ctx context.Context, source string) error {
	return fmt.Errorf("%s", AgentUnavailableError)
}

func readJSONObjectOrEmpty(path string) map[string]any {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed == nil {
		return map[string]any{}
	}
	return parsed
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
