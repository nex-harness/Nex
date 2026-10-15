// config.go: the CLI config plugin-block readers shared by the commands and
// skills services (readPluginConfigFromConfig + the storage-root resolvers).
package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tylinux/nex/go/internal/plugins"
)

type cliPluginConfig struct {
	dirs               []string
	enabled            bool
	enabledPlugins     map[string]bool
	storageDir         string
	suppressedBuiltins []string
}

func (s *Service) readCliPluginConfig() cliPluginConfig {
	config := s.readUserCliConfig()
	pluginsConfig, _ := config["plugins"].(map[string]any)
	out := cliPluginConfig{enabled: true, enabledPlugins: map[string]bool{}, storageDir: "~/.nex"}
	if pluginsConfig != nil {
		if list, ok := pluginsConfig["dirs"].([]any); ok {
			for _, item := range list {
				if str, ok := item.(string); ok {
					out.dirs = append(out.dirs, str)
				}
			}
		}
		if enabled, ok := pluginsConfig["enabled"].(bool); ok {
			out.enabled = enabled
		}
		if record, ok := pluginsConfig["enabledPlugins"].(map[string]any); ok {
			for key, value := range record {
				if b, ok := value.(bool); ok {
					out.enabledPlugins[key] = b
				}
			}
		}
		if list, ok := pluginsConfig["suppressedBuiltins"].([]any); ok {
			for _, item := range list {
				if str, ok := item.(string); ok {
					out.suppressedBuiltins = append(out.suppressedBuiltins, str)
				}
			}
		}
	}
	if storage, ok := config["storage"].(map[string]any); ok {
		if dir, ok := storage["dir"].(string); ok && strings.TrimSpace(dir) != "" {
			out.storageDir = dir
		}
	}
	return out
}

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

func (s *Service) resolvePluginStorageRoot(storageDir string) string {
	storageRoot := s.resolveConfigPath(storageDir)
	if filepath.Base(storageRoot) == "cli" {
		return filepath.Join(storageRoot, "plugins")
	}
	return filepath.Join(storageRoot, "cli", "plugins")
}

func (s *Service) scanOfficialPluginCacheRoots(pluginStorageRoot string) []string {
	cacheRoot := filepath.Join(pluginStorageRoot, "cache", plugins.OfficialMarketplaceID)
	pluginEntries, err := os.ReadDir(cacheRoot)
	if err != nil {
		return nil
	}
	var roots []string
	for _, pluginEntry := range pluginEntries {
		pluginDir := filepath.Join(cacheRoot, pluginEntry.Name())
		versionEntries, err := os.ReadDir(pluginDir)
		if err != nil {
			continue
		}
		for _, versionEntry := range versionEntries {
			rootPath := filepath.Join(pluginDir, versionEntry.Name())
			if info, err := os.Lstat(rootPath); err == nil && info.IsDir() {
				roots = append(roots, rootPath)
			}
		}
	}
	sort.Strings(roots)
	return roots
}

var _ = json.Marshal
