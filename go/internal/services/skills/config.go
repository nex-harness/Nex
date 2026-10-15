// config.go: the CLI-config readers shared by the skills service — the
// plugin block summary (dirs/enabled/enabledPlugins/storageDir/
// suppressedBuiltins) and the storage-root + official-cache resolvers.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tylinux/nex/go/internal/plugins"
)

// pluginConfigSummary ports PluginConfigSummary (the plugins block of the
// CLI config, shared with the commands service).
type pluginConfigSummary struct {
	Dirs               []string
	Enabled            bool
	EnabledPlugins     map[string]bool
	StorageDir         string
	SuppressedBuiltins []string
}

func (s *Service) readPluginConfig() pluginConfigSummary {
	config := s.readCliConfig()
	pluginsConfig, _ := config["plugins"].(map[string]any)
	summary := pluginConfigSummary{Enabled: true, EnabledPlugins: map[string]bool{}, StorageDir: "~/.nex"}
	if pluginsConfig != nil {
		summary.Dirs = readStringArray(pluginsConfig["dirs"])
		if enabled, ok := pluginsConfig["enabled"].(bool); ok {
			summary.Enabled = enabled
		}
		summary.EnabledPlugins = readBooleanRecord(pluginsConfig["enabledPlugins"])
		summary.SuppressedBuiltins = readStringArray(pluginsConfig["suppressedBuiltins"])
	}
	if storage, ok := config["storage"].(map[string]any); ok {
		if dir, ok := storage["dir"].(string); ok && strings.TrimSpace(dir) != "" {
			summary.StorageDir = dir
		}
	}
	return summary
}

func readStringArray(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func readBooleanRecord(value any) map[string]bool {
	record, ok := value.(map[string]any)
	if !ok {
		return map[string]bool{}
	}
	out := map[string]bool{}
	for key, enabled := range record {
		if b, ok := enabled.(bool); ok {
			out[key] = b
		}
	}
	return out
}

// resolveConfigPath expands ~ and resolves relative paths.
func (s *Service) resolveConfigPath(path string) string {
	expanded := path
	if strings.HasPrefix(path, "~/") {
		expanded = filepath.Join(s.homeDir, path[2:])
	}
	if filepath.IsAbs(expanded) {
		return filepath.Clean(expanded)
	}
	if absolute, err := filepath.Abs(expanded); err == nil {
		return absolute
	}
	return expanded
}

// resolvePluginStorageRoot ports resolvePluginStorageRoot: <storage>/cli/plugins.
func (s *Service) resolvePluginStorageRoot(storageDir string) string {
	storageRoot := s.resolveConfigPath(storageDir)
	if filepath.Base(storageRoot) == "cli" {
		return filepath.Join(storageRoot, "plugins")
	}
	return filepath.Join(storageRoot, "cli", "plugins")
}

// scanOfficialPluginCacheRoots ports scanOfficialPluginCacheRoots.
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

// CapabilityDesktopOnly is the userScopeReason value (resolveCapabilities).
const CapabilityDesktopOnly = "desktop_only"

// errPluginScopeRefusal is the exact plugin-scope delete refusal.
var errPluginScopeRefusal = fmt.Errorf("插件提供的技能不可单独删除，请卸载对应插件")
